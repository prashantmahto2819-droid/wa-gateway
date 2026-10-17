package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// ======================= 1. Per-number proxy =======================

// validateProxy accepts http://, https:// and socks5:// (user:pass@ allowed).
func validateProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Port() == "" {
		return "", errors.New("proxy must look like socks5://user:pass@host:port or http://host:port")
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return "", errors.New("proxy scheme must be http, https or socks5")
	}
	return u.String(), nil
}

// maskProxy hides the password when showing a proxy.
func maskProxy(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return ""
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	return u.Redacted()
}

type proxyInfo struct {
	IP      string `json:"ip"`
	Country string `json:"country,omitempty"`
	Region  string `json:"region,omitempty"`
	City    string `json:"city,omitempty"`
	Org     string `json:"org,omitempty"`
	Millis  int64  `json:"latency_ms"`
}

// testProxy checks that the proxy works and shows which IP / country WhatsApp will see.
func testProxy(ctx context.Context, raw string) (*proxyInfo, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
	start := time.Now()
	for _, endpoint := range []string{"https://ipinfo.io/json", "https://api.ipify.org?format=json"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		var info proxyInfo
		if json.Unmarshal(body, &info) == nil && info.IP != "" {
			info.Millis = time.Since(start).Milliseconds()
			return &info, nil
		}
	}
	return nil, errors.New("proxy is not working (could not reach the internet through it)")
}

// ======================= 2. Number pool =======================

// poolCandidate is a session that can take campaign messages right now.
type poolCandidate struct {
	name  string
	load  float64 // queued messages / daily limit
	limit int
}

// poolCandidates returns usable sessions from the pool (linked, not paused, not banned).
func (m *Manager) poolCandidates(ctx context.Context, pool []string) []*poolCandidate {
	var out []*poolCandidate
	for _, name := range pool {
		s, err := m.get(name)
		if err != nil {
			continue
		}
		r := s.row(ctx)
		s.mu.Lock()
		bad := s.status == StLoggedOut || s.status == StReplaced || s.banUntil.After(time.Now())
		s.mu.Unlock()
		if r.jid == "" || r.paused || bad {
			continue
		}
		var queued int
		_ = m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gw_outbox WHERE session=? AND status='queued'`, name).Scan(&queued)
		limit := max(s.dailyLimit(r.pairedAt), 1)
		out = append(out, &poolCandidate{name: name, limit: limit, load: float64(queued) / float64(limit)})
	}
	return out
}

// pickSession chooses who sends to this person: the number they already
// talk to, else the number that messaged them before, else the least busy.
func (m *Manager) pickSession(ctx context.Context, cands []*poolCandidate, to string) string {
	if len(cands) == 0 {
		return ""
	}
	names := make([]any, 0, len(cands)+1)
	ph := make([]string, 0, len(cands))
	names = append(names, to)
	for _, c := range cands {
		names = append(names, c.name)
		ph = append(ph, "?")
	}
	in := strings.Join(ph, ",")
	var name string
	if m.db.QueryRowContext(ctx, `SELECT session FROM gw_contacts WHERE jid=? AND session IN (`+in+`) ORDER BY last_in DESC LIMIT 1`, names...).Scan(&name) == nil {
		return name
	}
	if m.db.QueryRowContext(ctx, `SELECT session FROM gw_outbox WHERE to_addr=? AND status='sent' AND session IN (`+in+`) ORDER BY sent_at DESC LIMIT 1`, names...).Scan(&name) == nil {
		return name
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.load < best.load {
			best = c
		}
	}
	best.load += 1 / float64(best.limit)
	return best.name
}

// rebalance moves pooled, still-queued messages away from a number that
// can't send any more (logged out / banned / replaced).
func (m *Manager) rebalance(from string) {
	ctx := m.ctx
	rows, err := m.db.QueryContext(ctx, `SELECT id, to_addr, pool FROM gw_outbox WHERE session=? AND status='queued' AND pool<>''`, from)
	if err != nil {
		return
	}
	type item struct {
		id       int64
		to, pool string
	}
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.to, &it.pool) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	cache := map[string][]*poolCandidate{}
	moved := 0
	for _, it := range items {
		cands, ok := cache[it.pool]
		if !ok {
			others := slices.DeleteFunc(strings.Split(it.pool, ","), func(n string) bool { return n == from })
			cands = m.poolCandidates(ctx, others)
			cache[it.pool] = cands
		}
		if target := m.pickSession(ctx, cands, it.to); target != "" {
			if _, err := m.db.ExecContext(ctx, `UPDATE gw_outbox SET session=? WHERE id=? AND status='queued'`, target, it.id); err == nil {
				moved++
			}
		}
	}
	if moved > 0 {
		m.log.Info("moved queued messages to other numbers", "from", from, "count", moved)
		for _, s := range m.list() {
			s.poke()
		}
	}
}

// ======================= 3. STOP / START =======================

// handleKeyword records opt-outs ("STOP") and opt-ins ("START") from 1:1 chats.
func (s *Session) handleKeyword(ctx context.Context, chat types.JID, text string) {
	kw := normKeyword(text)
	if kw == "" || len(kw) > 40 {
		return
	}
	cfg := s.m.cfg
	phone := digitsOf(chat.String())
	if phone == "" {
		return
	}
	var reply, event string
	switch {
	case slices.Contains(cfg.OptoutKeywords, kw):
		res, err := s.m.db.ExecContext(ctx, `INSERT OR IGNORE INTO gw_optouts(phone, keyword, session, at) VALUES(?,?,?,?)`, phone, kw, s.name, time.Now().Unix())
		if err != nil {
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return // already opted out (e.g. replayed after reconnect): don't reply again
		}
		// Cancel anything still waiting for this person.
		_, _ = s.m.db.ExecContext(ctx, `UPDATE gw_outbox SET status='skipped', error='opted_out' WHERE status='queued' AND to_addr=?`, chat.String())
		reply, event = cfg.OptoutReply, "contact.optout"
	case slices.Contains(cfg.OptinKeywords, kw):
		res, err := s.m.db.ExecContext(ctx, `DELETE FROM gw_optouts WHERE phone=?`, phone)
		if err != nil {
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return // wasn't opted out; "start" may just be a normal word
		}
		reply, event = cfg.OptinReply, "contact.optin"
	default:
		return
	}
	s.hook(event, map[string]any{"phone": phone, "chat": chat.String(), "keyword": kw})
	if reply != "" {
		go func() {
			ctx, cancel := context.WithTimeout(s.m.ctx, time.Minute)
			defer cancel()
			req := &SendRequest{To: chat.String(), Text: reply}
			res, err := s.send(ctx, req, sendOpts{direct: true})
			s.recordDirect(ctx, req, res, err)
		}()
	}
}

// ======================= 4 & 5. Per-person cap and priority =======================

// weeklyCount counts campaign messages this person got in the last 7 days (all numbers).
func weeklyCount(ctx context.Context, db *sql.DB, to string) int {
	var n int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gw_outbox WHERE to_addr=? AND direct=0 AND status='sent' AND sent_at>?`,
		to, time.Now().AddDate(0, 0, -7).Unix()).Scan(&n)
	return n
}

// priorityFor: 2 = wrote to us recently, 1 = wrote to us before, 0 = never.
func (m *Manager) priorityFor(ctx context.Context, sessions []string, to string) int {
	args := []any{to}
	ph := []string{}
	for _, s := range sessions {
		args = append(args, s)
		ph = append(ph, "?")
	}
	var last int64
	if m.db.QueryRowContext(ctx, `SELECT MAX(last_in) FROM gw_contacts WHERE jid=? AND session IN (`+strings.Join(ph, ",")+`)`, args...).Scan(&last) != nil || last == 0 {
		return 0
	}
	if time.Since(time.Unix(last, 0)) < time.Duration(m.cfg.RecentReplyDays)*24*time.Hour {
		return 2
	}
	return 1
}

// ======================= 6. Health guard =======================

type Health struct {
	State     string  `json:"state"` // ok | slow | paused
	Sent      int     `json:"campaign_sent"`
	Failed    int     `json:"campaign_failed"`
	FailRate  float64 `json:"fail_rate"`
	NewSent   int     `json:"new_contact_sent"`
	Replied   int     `json:"new_contact_replied"`
	ReplyRate float64 `json:"reply_rate"`
	Reason    string  `json:"reason,omitempty"`
}

// health looks at the last 3 days of campaign sends (since the last manual resume).
func (s *Session) health(ctx context.Context, r sessionRow) Health {
	cfg := s.m.cfg
	since := max(time.Now().Add(-72*time.Hour).Unix(), r.healthSince)
	h := Health{State: "ok"}
	_ = s.m.db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(status='sent'),0), COALESCE(SUM(status='failed' AND wa_error=1),0),
			COALESCE(SUM(status='sent' AND new_contact=1),0),
			COALESCE(SUM(status='sent' AND new_contact=1 AND EXISTS(
				SELECT 1 FROM gw_contacts c WHERE c.session=o.session AND c.jid=o.to_addr AND c.last_in>o.sent_at)),0)
		FROM gw_outbox o WHERE session=? AND direct=0 AND status IN ('sent','failed') AND finished_at>=?`,
		s.name, since).Scan(&h.Sent, &h.Failed, &h.NewSent, &h.Replied)
	if total := h.Sent + h.Failed; total > 0 {
		h.FailRate = float64(h.Failed) / float64(total)
		if total >= cfg.HealthMinSample && h.FailRate > cfg.HealthMaxFail {
			h.State, h.Reason = "paused", fmt.Sprintf("%.0f%% of recent campaign messages failed", h.FailRate*100)
			return h
		}
	}
	if h.NewSent > 0 {
		h.ReplyRate = float64(h.Replied) / float64(h.NewSent)
		if h.NewSent >= cfg.HealthMinSample && h.ReplyRate < cfg.HealthMinReply {
			h.State, h.Reason = "slow", fmt.Sprintf("only %.1f%% of new contacts replied; sending slower", h.ReplyRate*100)
		}
	}
	return h
}

// applyHealth pauses or slows the queue and tells the website once per change.
func (s *Session) applyHealth(ctx context.Context, h Health) {
	s.mu.Lock()
	changed := s.lastHealth != h.State
	s.lastHealth = h.State
	s.mu.Unlock()
	if h.State == "paused" {
		_, _ = s.m.db.ExecContext(ctx, `UPDATE gw_sessions SET paused=1, pause_reason=? WHERE name=?`, "health: "+h.Reason, s.name)
		if changed {
			go s.m.rebalance(s.name) // pooled messages continue on the other numbers
		}
	}
	if changed {
		s.m.log.Warn("number health changed", "session", s.name, "state", h.State, "reason", h.Reason)
		s.hook("session.health", h)
	}
}
