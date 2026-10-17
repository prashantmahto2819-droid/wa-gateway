package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type API struct {
	m       *Manager
	cfg     *Config
	started time.Time
}

func (a *API) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", serveDashboard)
	mux.HandleFunc("GET /health", a.health)

	auth := func(h http.HandlerFunc) http.HandlerFunc { return a.auth(h) }
	mux.HandleFunc("GET /api/sessions", auth(a.listSessions))
	mux.HandleFunc("POST /api/sessions", auth(a.createSession))
	mux.HandleFunc("GET /api/sessions/{name}", auth(a.getSession))
	mux.HandleFunc("PATCH /api/sessions/{name}", auth(a.updateSession))
	mux.HandleFunc("DELETE /api/sessions/{name}", auth(a.deleteSession))
	mux.HandleFunc("POST /api/sessions/{name}/qr", auth(a.startQR))
	mux.HandleFunc("GET /api/sessions/{name}/qr", auth(a.getQR))
	mux.HandleFunc("POST /api/sessions/{name}/pair-code", auth(a.pairCode))
	mux.HandleFunc("POST /api/sessions/{name}/logout", auth(a.logout))
	mux.HandleFunc("POST /api/sessions/{name}/reconnect", auth(a.reconnect))

	mux.HandleFunc("POST /api/sessions/{name}/send", auth(a.send))
	mux.HandleFunc("POST /api/sessions/{name}/queue", auth(a.enqueue))
	mux.HandleFunc("GET /api/sessions/{name}/queue", auth(a.queueStats))
	mux.HandleFunc("POST /api/sessions/{name}/read", auth(a.markRead))
	mux.HandleFunc("POST /api/sessions/{name}/typing", auth(a.typing))
	mux.HandleFunc("POST /api/sessions/{name}/check", auth(a.check))
	mux.HandleFunc("GET /api/sessions/{name}/groups", auth(a.groups))
	mux.HandleFunc("GET /api/sessions/{name}/channels", auth(a.channels))
	mux.HandleFunc("GET /api/sessions/{name}/channels/resolve", auth(a.resolveChannel))

	mux.HandleFunc("POST /api/queue", auth(a.enqueuePool))
	mux.HandleFunc("GET /api/queue/{id}", auth(a.queueItem))
	mux.HandleFunc("POST /api/sessions/{name}/proxy-test", auth(a.proxyTest))
	mux.HandleFunc("GET /api/optouts", auth(a.listOptouts))
	mux.HandleFunc("POST /api/optouts", auth(a.addOptouts))
	mux.HandleFunc("DELETE /api/optouts/{phone}", auth(a.removeOptout))
	mux.HandleFunc("GET /api/campaigns/{campaign}", auth(a.campaignStats))
	mux.HandleFunc("POST /api/campaigns/{campaign}/cancel", auth(a.cancelCampaign))
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

func (a *API) auth(h http.HandlerFunc) http.HandlerFunc {
	want := []byte(a.cfg.APIKey)
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			key = b
		}
		if subtle.ConstantTimeCompare([]byte(key), want) != 1 {
			time.Sleep(300 * time.Millisecond) // slow down guessing
			writeErr(w, http.StatusUnauthorized, "unauthorized", errors.New("missing or wrong API key"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
		h(w, r)
	}
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, kind string, err error) {
	writeJSON(w, code, map[string]any{"ok": false, "code": kind, "error": err.Error()})
}

// errStatus maps internal errors to HTTP codes and stable error codes for the website.
func errStatus(err error) (int, string) {
	switch {
	case errors.Is(err, errNoSession):
		return http.StatusNotFound, "session_not_found"
	case errors.Is(err, errSessionExists):
		return http.StatusConflict, "session_exists"
	case errors.Is(err, errBadName):
		return http.StatusBadRequest, "bad_session_name"
	case errors.Is(err, errNotConnected):
		return http.StatusServiceUnavailable, "not_connected"
	case errors.Is(err, errAlreadyLinked):
		return http.StatusConflict, "already_linked"
	case errors.Is(err, errNotOnWA):
		return http.StatusUnprocessableEntity, "not_on_whatsapp"
	case errors.Is(err, errTimelocked):
		return http.StatusTooManyRequests, "timelocked"
	case errors.Is(err, errDailyLimit):
		return http.StatusTooManyRequests, "daily_limit"
	case errors.Is(err, errNewLimit):
		return http.StatusTooManyRequests, "new_contact_limit"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout"
	case isPermanent(err):
		return http.StatusBadRequest, "bad_request"
	}
	return http.StatusBadGateway, "whatsapp_error"
}

func (a *API) fail(w http.ResponseWriter, err error) {
	code, kind := errStatus(err)
	writeErr(w, code, kind, err)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

func (a *API) session(w http.ResponseWriter, r *http.Request) *Session {
	s, err := a.m.get(r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return nil
	}
	return s
}

func (a *API) client(w http.ResponseWriter, r *http.Request) (*Session, *whatsmeow.Client, bool) {
	s := a.session(w, r)
	if s == nil {
		return nil, nil, false
	}
	cli, err := s.ready()
	if err != nil {
		a.fail(w, err)
		return nil, nil, false
	}
	return s, cli, true
}

// ---------- sessions ----------

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	connected := 0
	for _, s := range a.m.list() {
		if _, err := s.ready(); err == nil {
			connected++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version, "uptime_sec": int(time.Since(a.started).Seconds()),
		"sessions": len(a.m.list()), "connected": connected})
}

func (a *API) sessionInfo(ctx context.Context, s *Session) map[string]any {
	r := s.row(ctx)
	s.mu.Lock()
	st, lastErr, push := s.status, s.lastErr, s.pushName
	banUntil := s.banUntil
	s.mu.Unlock()
	now := time.Now()
	sent, _ := countSentSince(ctx, a.m.db, s.name, dayStart(now), false)
	sentNew, _ := countSentSince(ctx, a.m.db, s.name, dayStart(now), true)
	info := map[string]any{
		"name": s.name, "status": st, "error": lastErr, "jid": r.jid, "phone": strings.SplitN(strings.SplitN(r.jid, "@", 2)[0], ":", 2)[0],
		"push_name": push, "paused": r.paused,
		"today": map[string]any{"sent": sent, "limit": s.dailyLimit(r.pairedAt), "new_contacts": sentNew, "new_contact_limit": a.cfg.NewContactLimit},
	}
	if r.timelockUntil > now.Unix() {
		info["timelock_until"] = r.timelockUntil
	}
	if banUntil.After(now) {
		info["banned_until"] = banUntil.Unix()
	}
	if r.pairedAt > 0 {
		info["linked_at"] = r.pairedAt
	}
	if r.proxy != "" {
		info["proxy"] = maskProxy(r.proxy)
	}
	if r.pauseReason != "" && r.paused {
		info["pause_reason"] = r.pauseReason
	}
	info["health"] = s.health(ctx, r)
	return info
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, s := range a.m.list() {
		out = append(out, a.sessionInfo(r.Context(), s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": out})
}

func (a *API) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		WebhookURL string `json:"webhook_url"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	s, err := a.m.create(strings.ToLower(strings.TrimSpace(body.Name)), strings.TrimSpace(body.WebhookURL))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "session": a.sessionInfo(r.Context(), s)})
}

func (a *API) getSession(w http.ResponseWriter, r *http.Request) {
	if s := a.session(w, r); s != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": a.sessionInfo(r.Context(), s)})
	}
}

func (a *API) updateSession(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	var body struct {
		WebhookURL *string `json:"webhook_url"`
		Paused     *bool   `json:"paused"`
		Proxy      *string `json:"proxy"` // "" removes it
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx := r.Context()
	if body.WebhookURL != nil {
		_, _ = a.m.db.ExecContext(ctx, `UPDATE gw_sessions SET webhook_url=? WHERE name=?`, strings.TrimSpace(*body.WebhookURL), s.name)
	}
	if body.Paused != nil {
		if *body.Paused {
			_, _ = a.m.db.ExecContext(ctx, `UPDATE gw_sessions SET paused=1, pause_reason='manual' WHERE name=?`, s.name)
		} else {
			// Resuming also restarts the health window so old failures don't pause it again.
			_, _ = a.m.db.ExecContext(ctx, `UPDATE gw_sessions SET paused=0, pause_reason='', health_since=? WHERE name=?`, time.Now().Unix(), s.name)
		}
		s.poke()
	}
	if body.Proxy != nil {
		p, err := validateProxy(*body.Proxy)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_proxy", err)
			return
		}
		_, _ = a.m.db.ExecContext(ctx, `UPDATE gw_sessions SET proxy=? WHERE name=?`, p, s.name)
		// Reconnect a linked number so the new proxy takes effect now.
		if row := s.row(ctx); row.jid != "" {
			if jid, err := types.ParseJID(row.jid); err == nil {
				go func() { _ = s.connectExisting(a.m.ctx, jid) }()
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": a.sessionInfo(r.Context(), s)})
}

func (a *API) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := a.m.remove(r.Context(), r.PathValue("name")); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) startQR(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	if err := s.startPairing(); err != nil {
		a.fail(w, err)
		return
	}
	// Give WhatsApp a moment to send the first code.
	for i := 0; i < 30; i++ {
		s.mu.Lock()
		has := s.qrCode != "" || s.status != StQR
		s.mu.Unlock()
		if has {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	a.getQR(w, r)
}

func (a *API) getQR(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	s.mu.Lock()
	code, st, lastErr := s.qrCode, s.status, s.lastErr
	s.mu.Unlock()
	out := map[string]any{"ok": true, "status": st, "error": lastErr}
	if code != "" && st == StQR {
		out["qr"] = code
		if png, err := qrcode.Encode(code, qrcode.Medium, 320); err == nil {
			out["qr_image"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) pairCode(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	var body struct {
		Phone string `json:"phone"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	code, err := s.pairCode(ctx, body.Phone)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": code,
		"how": "Phone: WhatsApp > Linked devices > Link a device > Link with phone number instead, then type this code"})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if s := a.session(w, r); s != nil {
		s.logout(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (a *API) reconnect(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	row := s.row(r.Context())
	jid, err := types.ParseJID(row.jid)
	if row.jid == "" || err != nil {
		writeErr(w, http.StatusConflict, "not_linked", errors.New("session is not linked; scan a QR first"))
		return
	}
	if err := s.connectExisting(r.Context(), jid); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- sending ----------

func (a *API) send(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	var req SendRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, err := s.send(ctx, &req, sendOpts{direct: true})
	if err == nil || !errors.Is(err, errNotConnected) {
		s.recordDirect(context.Background(), &req, res, err)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": res})
}

type queueBody struct {
	Campaign   string        `json:"campaign"`
	Messages   []SendRequest `json:"messages"` // full messages, or...
	Template   *SendRequest  `json:"template"` // ...one template plus a recipient list
	Recipients []struct {
		To   string            `json:"to"`
		Vars map[string]string `json:"vars"`
	} `json:"recipients"`
	SendAfter int64    `json:"send_after"` // optional unix time: don't start before this
	Pool      []string `json:"pool"`       // optional: spread over these sessions
}

func (a *API) enqueue(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	var body queueBody
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	pool := []string{s.name}
	if len(body.Pool) > 0 {
		pool = body.Pool
	}
	a.doEnqueue(w, r, pool, &body)
}

// enqueuePool: POST /api/queue with "pool": ["main","second",...] spreads a
// campaign over several numbers.
func (a *API) enqueuePool(w http.ResponseWriter, r *http.Request) {
	var body queueBody
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if len(body.Pool) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("pool (list of session names) is required"))
		return
	}
	a.doEnqueue(w, r, body.Pool, &body)
}

func (a *API) doEnqueue(w http.ResponseWriter, r *http.Request, pool []string, body *queueBody) {
	ctx := r.Context()
	seen := map[string]bool{}
	var clean []string
	for _, n := range pool {
		n = strings.TrimSpace(n)
		if seen[n] {
			continue
		}
		if _, err := a.m.get(n); err != nil {
			writeErr(w, http.StatusNotFound, "session_not_found", errors.New("unknown session in pool: "+n))
			return
		}
		seen[n] = true
		clean = append(clean, n)
	}
	pool = clean
	poolCol := ""
	if len(pool) > 1 {
		poolCol = strings.Join(pool, ",")
	}

	msgs := body.Messages
	if body.Template != nil {
		for _, rc := range body.Recipients {
			m := *body.Template
			m.To, m.Vars = rc.To, rc.Vars
			msgs = append(msgs, m)
		}
	}
	if len(msgs) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("no messages"))
		return
	}
	if len(msgs) > 10000 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("at most 10000 messages per request"))
		return
	}
	type row struct {
		session, to, payload string
		priority             int
	}
	rows := make([]row, 0, len(msgs))
	cands := a.m.poolCandidates(ctx, pool)
	skipped, dupes := 0, 0
	inBatch := map[string]bool{}
	for i := range msgs {
		if err := msgs[i].validate(); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("message "+itoa(i)+": "+err.Error()))
			return
		}
		to, err := parseTarget(msgs[i].To, a.cfg.DefaultCountryCode)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("message "+itoa(i)+": "+err.Error()))
			return
		}
		if msgs[i].Media != nil && msgs[i].Media.Base64 != "" {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("message "+itoa(i)+": queued messages must use media.url, not base64"))
			return
		}
		key := to.String()
		if isOptedOut(ctx, a.m.db, key) {
			skipped++
			continue
		}
		if inBatch[key] && to.Server != types.GroupServer && to.Server != types.NewsletterServer {
			dupes++ // same person twice in one campaign
			continue
		}
		inBatch[key] = true
		session := pool[len(rows)%len(pool)] // nobody usable yet: spread evenly
		if len(pool) > 1 {
			if pick := a.m.pickSession(ctx, cands, key); pick != "" {
				session = pick
			}
		}
		p, _ := json.Marshal(msgs[i])
		rows = append(rows, row{session: session, to: key, payload: string(p), priority: a.m.priorityFor(ctx, pool, key)})
	}

	tx, err := a.m.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO gw_outbox(session,campaign,to_addr,payload,status,not_before,priority,pool,created_at) VALUES(?,?,?,?,'queued',?,?,?,?)`)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer stmt.Close()
	now := time.Now().Unix()
	ids := make([]int64, 0, len(rows))
	perSession := map[string]int{}
	for _, rw := range rows {
		res, err := stmt.ExecContext(ctx, rw.session, body.Campaign, rw.to, rw.payload, body.SendAfter, rw.priority, poolCol, now)
		if err != nil {
			a.fail(w, err)
			return
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
		perSession[rw.session]++
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	for name := range perSession {
		if s, err := a.m.get(name); err == nil {
			s.poke()
		}
	}
	out := map[string]any{"ok": true, "queued": len(ids), "skipped_opted_out": skipped, "skipped_duplicates": dupes,
		"per_session": perSession, "campaign": body.Campaign}
	if len(ids) > 0 {
		out["first_id"], out["last_id"] = ids[0], ids[len(ids)-1]
	}
	if len(ids) <= 100 {
		out["ids"] = ids
	}
	writeJSON(w, http.StatusAccepted, out)
}

func itoa(i int) string { return strconv.Itoa(i) }

func statusCounts(ctx context.Context, db *sql.DB, where string, arg any) map[string]int {
	out := map[string]int{"queued": 0, "sending": 0, "sent": 0, "failed": 0, "skipped": 0, "cancelled": 0}
	rows, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM gw_outbox WHERE `+where+` AND direct=0 GROUP BY status`, arg)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if rows.Scan(&st, &n) == nil {
			out[st] = n
		}
	}
	return out
}

func (a *API) queueStats(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "counts": statusCounts(r.Context(), a.m.db, "session=?", s.name),
		"session": a.sessionInfo(r.Context(), s), "sending_hours": []int{a.cfg.SendHourFrom, a.cfg.SendHourTo}})
}

func (a *API) queueItem(w http.ResponseWriter, r *http.Request) {
	var it struct {
		ID                                         int64
		Session, Campaign, To, Status, Error, WaID string
		Attempts                                   int
		CreatedAt, SentAt                          int64
	}
	err := a.m.db.QueryRowContext(r.Context(), `SELECT id, session, campaign, to_addr, status, error, wa_id, attempts, created_at, sent_at FROM gw_outbox WHERE id=?`,
		r.PathValue("id")).Scan(&it.ID, &it.Session, &it.Campaign, &it.To, &it.Status, &it.Error, &it.WaID, &it.Attempts, &it.CreatedAt, &it.SentAt)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("queue item not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": map[string]any{"id": it.ID, "session": it.Session, "campaign": it.Campaign,
		"to": it.To, "status": it.Status, "error": it.Error, "wa_id": it.WaID, "attempts": it.Attempts, "created_at": it.CreatedAt, "sent_at": it.SentAt}})
}

func (a *API) campaignStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "campaign": r.PathValue("campaign"),
		"counts": statusCounts(r.Context(), a.m.db, "campaign=?", r.PathValue("campaign"))})
}

func (a *API) cancelCampaign(w http.ResponseWriter, r *http.Request) {
	res, err := a.m.db.ExecContext(r.Context(), `UPDATE gw_outbox SET status='cancelled' WHERE campaign=? AND status='queued'`, r.PathValue("campaign"))
	if err != nil {
		a.fail(w, err)
		return
	}
	n, _ := res.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": n})
}

// ---------- chat helpers ----------

func (a *API) markRead(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	var body struct {
		Chat   string   `json:"chat"`
		Sender string   `json:"sender"`
		IDs    []string `json:"ids"`
	}
	if err := decode(r, &body); err != nil || len(body.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("chat and ids are required"))
		return
	}
	chat, err := parseTarget(body.Chat, a.cfg.DefaultCountryCode)
	if err != nil {
		a.fail(w, err)
		return
	}
	sender := chat
	if body.Sender != "" {
		if sender, err = parseTarget(body.Sender, a.cfg.DefaultCountryCode); err != nil {
			a.fail(w, err)
			return
		}
	}
	if err := cli.MarkRead(r.Context(), body.IDs, time.Now(), chat, sender); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) typing(w http.ResponseWriter, r *http.Request) {
	s, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	var body struct {
		To    string `json:"to"`
		State string `json:"state"` // composing | paused
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	to, err := parseTarget(body.To, a.cfg.DefaultCountryCode)
	if err != nil {
		a.fail(w, err)
		return
	}
	state := types.ChatPresenceComposing
	if body.State == "paused" {
		state = types.ChatPresencePaused
	}
	s.goOnline(r.Context(), cli)
	if err := cli.SendChatPresence(r.Context(), to, state, types.ChatPresenceMediaText); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) check(w http.ResponseWriter, r *http.Request) {
	s, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	var body struct {
		Numbers []string `json:"numbers"`
	}
	if err := decode(r, &body); err != nil || len(body.Numbers) == 0 || len(body.Numbers) > 50 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("give 1-50 numbers"))
		return
	}
	// Mass number checking is itself a ban signal, so it is rate limited.
	s.checkMu.Lock()
	if time.Since(s.checkWindow) > time.Hour {
		s.checkWindow, s.checkCount = time.Now(), 0
	}
	if s.checkCount+len(body.Numbers) > a.cfg.CheckHourly {
		s.checkMu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "check_limit", errors.New("hourly number-check limit reached"))
		return
	}
	s.checkCount += len(body.Numbers)
	s.checkMu.Unlock()

	var phones []string
	for _, n := range body.Numbers {
		j, err := parseTarget(n, a.cfg.DefaultCountryCode)
		if err != nil || j.Server != types.DefaultUserServer {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("invalid number: "+n))
			return
		}
		phones = append(phones, "+"+j.User)
	}
	res, err := cli.IsOnWhatsApp(r.Context(), phones)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []map[string]any{}
	for _, x := range res {
		jid := ""
		if x.IsIn {
			jid = x.JID.ToNonAD().String()
		}
		_, _ = a.m.db.ExecContext(r.Context(), `INSERT OR REPLACE INTO gw_numbers(phone,jid,is_in,checked_at) VALUES(?,?,?,?)`,
			strings.TrimPrefix(x.Query, "+"), jid, b2i(x.IsIn), time.Now().Unix())
		out = append(out, map[string]any{"number": strings.TrimPrefix(x.Query, "+"), "exists": x.IsIn, "jid": jid})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": out})
}

func (a *API) groups(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	gs, err := cli.GetJoinedGroups(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	me := cli.Store.GetJID().ToNonAD()
	myLID := cli.Store.GetLID().ToNonAD()
	out := []map[string]any{}
	for _, g := range gs {
		admin := false
		for _, p := range g.Participants {
			pj := p.JID.ToNonAD()
			if (pj == me || pj == myLID || p.PhoneNumber.ToNonAD() == me) && (p.IsAdmin || p.IsSuperAdmin) {
				admin = true
			}
		}
		kind := "group"
		switch {
		case g.IsParent:
			kind = "community"
		case g.IsDefaultSubGroup:
			kind = "community_announcements" // post here to reach the whole community
		}
		item := map[string]any{"jid": g.JID.String(), "name": g.Name, "type": kind, "participants": len(g.Participants),
			"announce_only": g.IsAnnounce, "i_am_admin": admin}
		if !g.LinkedParentJID.IsEmpty() {
			item["community"] = g.LinkedParentJID.String()
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": out})
}

func (a *API) channels(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	ns, err := cli.GetSubscribedNewsletters(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []map[string]any{}
	for _, n := range ns {
		role := ""
		if n.ViewerMeta != nil {
			role = string(n.ViewerMeta.Role)
		}
		out = append(out, map[string]any{"jid": n.ID.String(), "name": n.ThreadMeta.Name.Text, "role": role,
			"can_post": role == "owner" || role == "admin", "subscribers": n.ThreadMeta.SubscriberCount, "invite": n.ThreadMeta.InviteCode})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": out})
}

func (a *API) resolveChannel(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	invite := r.URL.Query().Get("invite")
	if i := strings.LastIndex(invite, "/channel/"); i >= 0 {
		invite = invite[i+len("/channel/"):]
	}
	if invite == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("invite (channel link) is required"))
		return
	}
	n, err := cli.GetNewsletterInfoWithInvite(r.Context(), invite)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "jid": n.ID.String(), "name": n.ThreadMeta.Name.Text})
}

// ---------- proxy and opt-outs ----------

func (a *API) proxyTest(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	var body struct {
		Proxy string `json:"proxy"` // empty = test the saved one
	}
	_ = decode(r, &body)
	raw := body.Proxy
	if raw == "" {
		raw = s.row(r.Context()).proxy
	}
	p, err := validateProxy(raw)
	if err != nil || p == "" {
		writeErr(w, http.StatusBadRequest, "bad_proxy", errors.New("no valid proxy to test"))
		return
	}
	info, err := testProxy(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "proxy_failed", err)
		return
	}
	out := map[string]any{"ok": true, "proxy": maskProxy(p), "result": info}
	if info.Country != "" && !strings.EqualFold(info.Country, "IN") && a.cfg.DefaultCountryCode == "91" {
		out["warning"] = "proxy IP is not in India; for Indian numbers an Indian IP looks more natural"
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) listOptouts(w http.ResponseWriter, r *http.Request) {
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT phone, keyword, session, at FROM gw_optouts ORDER BY at DESC LIMIT 5000`)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var phone, kw, sess string
		var at int64
		if rows.Scan(&phone, &kw, &sess, &at) == nil {
			out = append(out, map[string]any{"phone": phone, "keyword": kw, "session": sess, "at": at})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "optouts": out})
}

func (a *API) addOptouts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Numbers []string `json:"numbers"`
	}
	if err := decode(r, &body); err != nil || len(body.Numbers) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("numbers is required"))
		return
	}
	added := 0
	for _, n := range body.Numbers {
		j, err := parseTarget(n, a.cfg.DefaultCountryCode)
		if err != nil {
			continue
		}
		if _, err := a.m.db.ExecContext(r.Context(), `INSERT OR REPLACE INTO gw_optouts(phone, keyword, session, at) VALUES(?, 'manual', '', ?)`,
			digitsOf(j.String()), time.Now().Unix()); err == nil {
			added++
			_, _ = a.m.db.ExecContext(r.Context(), `UPDATE gw_outbox SET status='skipped', error='opted_out' WHERE status='queued' AND to_addr=?`, j.String())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added})
}

func (a *API) removeOptout(w http.ResponseWriter, r *http.Request) {
	j, err := parseTarget(r.PathValue("phone"), a.cfg.DefaultCountryCode)
	if err != nil {
		a.fail(w, err)
		return
	}
	res, _ := a.m.db.ExecContext(r.Context(), `DELETE FROM gw_optouts WHERE phone=?`, digitsOf(j.String()))
	n, _ := res.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": n})
}
