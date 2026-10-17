package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"
)

// Webhooks tell websites about incoming messages, receipts and session
// changes. Each request is signed:
//   X-Signature: sha256=<hex HMAC(secret, timestamp + "." + body)>
// and retried up to 4 times if the site is down.

type webhookJob struct {
	url        string
	secret     string
	body       []byte
	evt        string
	endpointID int64 // user endpoint (0 = global / per-session URL)
	guarded    bool  // user-supplied URL: never call private addresses
}

type Webhooks struct {
	secret  string // global WEBHOOK_SECRET
	queue   chan webhookJob
	client  *http.Client
	guarded *http.Client
	log     *slog.Logger
	db      *sql.DB
}

func newWebhooks(secret string, log *slog.Logger) *Webhooks {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, _ := net.SplitHostPort(address)
		if !publicIP(net.ParseIP(host)) {
			return fmt.Errorf("blocked address %s", host)
		}
		return nil
	}}
	return &Webhooks{
		secret:  secret,
		queue:   make(chan webhookJob, 5000),
		client:  &http.Client{Timeout: 15 * time.Second},
		guarded: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second}},
		log:     log,
	}
}

// start runs a few delivery workers until ctx ends.
func (w *Webhooks) start(ctx context.Context, workers int) {
	for range workers {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-w.queue:
					w.deliver(ctx, job)
				}
			}
		}()
	}
}

func payload(session string, ownerID int64, event string, data any) []byte {
	body, _ := json.Marshal(map[string]any{
		"event":     event,
		"session":   session,
		"owner_id":  ownerID,
		"timestamp": time.Now().Unix(),
		"data":      data,
	})
	return body
}

// send queues one delivery; it never blocks the WhatsApp event loop.
func (w *Webhooks) send(url, session, event string, data any) {
	w.enqueue(webhookJob{url: url, secret: w.secret, body: payload(session, 0, event, data), evt: event})
}

func (w *Webhooks) enqueue(job webhookJob) {
	if job.url == "" {
		return
	}
	select {
	case w.queue <- job:
	default:
		w.log.Warn("webhook buffer full, event dropped", "event", job.evt)
	}
}

func sign(secret string, body []byte, ts string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts))
	m.Write([]byte("."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (w *Webhooks) sign(body []byte, ts string) string { return sign(w.secret, body, ts) }

func (w *Webhooks) deliver(ctx context.Context, job webhookJob) {
	backoff := []time.Duration{0, 3 * time.Second, 15 * time.Second, time.Minute, 5 * time.Minute}
	for i, wait := range backoff {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		status, err := w.post(ctx, job)
		if job.endpointID > 0 && w.db != nil {
			_, _ = w.db.ExecContext(context.WithoutCancel(ctx), `UPDATE webhook_endpoints SET last_status=?, last_at=? WHERE id=?`, status, time.Now().Unix(), job.endpointID)
		}
		if err == nil {
			return
		}
		if i == len(backoff)-1 {
			w.log.Warn("webhook failed, giving up", "event", job.evt, "url", job.url, "err", err)
		}
	}
}

func (w *Webhooks) post(ctx context.Context, job webhookJob) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.url, bytes.NewReader(job.body))
	if err != nil {
		return 0, err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "vivid-relay/1.0")
	req.Header.Set("X-Event", job.evt)
	req.Header.Set("X-Timestamp", ts)
	if job.secret != "" {
		req.Header.Set("X-Signature", sign(job.secret, job.body, ts))
	}
	client := w.client
	if job.guarded {
		client = w.guarded
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// dispatch sends an event to every place that wants it: the global
// WEBHOOK_URL, the session's own URL, and the owner's webhook endpoints.
func (m *Manager) dispatch(s *Session, event string, data any) {
	owner := s.owner()
	body := payload(s.name, owner, event, data)
	h := m.hooks
	h.enqueue(webhookJob{url: m.cfg.WebhookURL, secret: h.secret, body: body, evt: event})
	var sessURL string
	_ = m.db.QueryRow(`SELECT webhook_url FROM gw_sessions WHERE name=?`, s.name).Scan(&sessURL)
	if sessURL != "" && sessURL != m.cfg.WebhookURL {
		h.enqueue(webhookJob{url: sessURL, secret: h.secret, body: body, evt: event, guarded: !m.cfg.AllowPrivateURLs})
	}
	if owner == 0 {
		return
	}
	rows, err := m.db.Query(`SELECT id, url, secret, events FROM webhook_endpoints WHERE user_id=? AND active=1`, owner)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var url, secret, evts string
		if rows.Scan(&id, &url, &secret, &evts) != nil {
			continue
		}
		if !eventWanted(fromJSON[[]string](evts), event) {
			continue
		}
		h.enqueue(webhookJob{url: url, secret: secret, body: body, evt: event, endpointID: id, guarded: !m.cfg.AllowPrivateURLs})
	}
}

func eventWanted(list []string, event string) bool {
	if len(list) == 0 {
		return true
	}
	for _, e := range list {
		if e == "*" || e == event {
			return true
		}
	}
	return false
}
