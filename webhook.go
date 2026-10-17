package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// Webhooks tell the website about incoming messages, delivery/read
// receipts and session changes. Each request is signed:
//   X-Signature: sha256=<hex HMAC of the raw body using WEBHOOK_SECRET>
// and retried up to 4 times if the site is down.

type webhookJob struct {
	url  string
	body []byte
	evt  string
}

type Webhooks struct {
	secret string
	queue  chan webhookJob
	client *http.Client
	log    *slog.Logger
}

func newWebhooks(secret string, log *slog.Logger) *Webhooks {
	return &Webhooks{
		secret: secret,
		queue:  make(chan webhookJob, 2000),
		client: &http.Client{Timeout: 15 * time.Second},
		log:    log,
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

// send never blocks the WhatsApp event loop; if the buffer is full the
// event is dropped and logged.
func (w *Webhooks) send(url, session, event string, data any) {
	if url == "" {
		return
	}
	body, err := json.Marshal(map[string]any{
		"event":     event,
		"session":   session,
		"timestamp": time.Now().Unix(),
		"data":      data,
	})
	if err != nil {
		return
	}
	select {
	case w.queue <- webhookJob{url: url, body: body, evt: event}:
	default:
		w.log.Warn("webhook buffer full, event dropped", "event", event, "session", session)
	}
}

func (w *Webhooks) sign(body []byte, ts string) string {
	m := hmac.New(sha256.New, []byte(w.secret))
	m.Write([]byte(ts))
	m.Write([]byte("."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

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
		err := w.post(ctx, job)
		if err == nil {
			return
		}
		if i == len(backoff)-1 {
			w.log.Warn("webhook failed, giving up", "event", job.evt, "err", err)
		}
	}
}

func (w *Webhooks) post(ctx context.Context, job webhookJob) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.url, bytes.NewReader(job.body))
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wa-gateway/1.0")
	req.Header.Set("X-Event", job.evt)
	req.Header.Set("X-Timestamp", ts)
	if w.secret != "" {
		req.Header.Set("X-Signature", w.sign(job.body, ts))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
