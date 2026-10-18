package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// ======================= sending pace (anti-ban) =======================
//
// Three layers decide how fast a number sends:
//   1. server defaults from .env (MIN_DELAY, MAX_DELAY, BATCH_*, DIRECT_GAP, TYPING)
//   2. the super admin's "Sending safety" settings, including hard floors
//      nobody can go below, and whether users may change their own pace
//   3. each user's own settings (only slower/within the floors)
// A campaign may also set its own gap, and "take turns" across its numbers.

type Pacing struct {
	GapMin        int   `json:"gap_min"`                // seconds between queued (campaign/bulk) messages, per number
	GapMax        int   `json:"gap_max"`                //
	BatchSize     int   `json:"batch_size"`             // after this many queued messages take a break (0 = never)
	BatchPauseMin int   `json:"batch_pause_min"`        // break length in minutes
	APIGapMin     int   `json:"api_gap_min"`            // seconds between API / webhook / auto-reply messages, per number
	APIGapMax     int   `json:"api_gap_max"`            //
	Typing        *bool `json:"typing,omitempty"`       // show "typing…" before API and campaign texts
	ReplyTyping   *bool `json:"reply_typing,omitempty"` // show "typing…" before auto-replies
	ReplyDelayMin int   `json:"reply_delay_min"`        // seconds an auto-reply waits ("reading") before typing
	ReplyDelayMax int   `json:"reply_delay_max"`        //
}

type AdminPacing struct {
	Pacing
	FloorGap       int  `json:"floor_gap"`        // nobody's campaign gap may be lower (seconds)
	FloorAPIGap    int  `json:"floor_api_gap"`    // nobody's API gap may be lower (seconds)
	UsersCanChange bool `json:"users_can_change"` // users may set their own (slower) pace
}

func boolp(b bool) *bool { return &b }

// envPacing is layer 1.
func envPacing(cfg *Config) AdminPacing {
	return AdminPacing{
		Pacing: Pacing{
			GapMin: int(cfg.MinDelay / time.Second), GapMax: int(cfg.MaxDelay / time.Second),
			BatchSize: cfg.BatchSize, BatchPauseMin: max(1, int(cfg.BatchPause/time.Minute)),
			APIGapMin: max(1, int(cfg.DirectGap/time.Second)), APIGapMax: max(1, int(cfg.DirectGap/time.Second)) + 3,
			Typing: boolp(cfg.Typing), ReplyTyping: boolp(true), ReplyDelayMin: 2, ReplyDelayMax: 6,
		},
		FloorGap: 8, FloorAPIGap: 1, UsersCanChange: true,
	}
}

// overlay copies the fields that are set in o over p.
func (p Pacing) overlay(o Pacing) Pacing {
	setI := func(dst *int, v int) {
		if v > 0 {
			*dst = v
		}
	}
	setI(&p.GapMin, o.GapMin)
	setI(&p.GapMax, o.GapMax)
	if o.BatchSize != 0 {
		p.BatchSize = max(o.BatchSize, 0)
	}
	setI(&p.BatchPauseMin, o.BatchPauseMin)
	setI(&p.APIGapMin, o.APIGapMin)
	setI(&p.APIGapMax, o.APIGapMax)
	if o.Typing != nil {
		p.Typing = o.Typing
	}
	if o.ReplyTyping != nil {
		p.ReplyTyping = o.ReplyTyping
	}
	setI(&p.ReplyDelayMin, o.ReplyDelayMin)
	setI(&p.ReplyDelayMax, o.ReplyDelayMax)
	return p
}

// clamp enforces the admin floors and sane ranges.
func (p Pacing) clamp(a AdminPacing) Pacing {
	p.GapMin = max(p.GapMin, a.FloorGap, 1)
	p.GapMax = min(max(p.GapMax, p.GapMin), 3600)
	p.APIGapMin = max(p.APIGapMin, a.FloorAPIGap, 0)
	p.APIGapMax = min(max(p.APIGapMax, p.APIGapMin), 600)
	p.BatchSize = min(max(p.BatchSize, 0), 1000)
	p.BatchPauseMin = min(max(p.BatchPauseMin, 1), 240)
	p.ReplyDelayMin = min(max(p.ReplyDelayMin, 0), 120)
	p.ReplyDelayMax = min(max(p.ReplyDelayMax, p.ReplyDelayMin), 180)
	if p.Typing == nil {
		p.Typing = boolp(true)
	}
	if p.ReplyTyping == nil {
		p.ReplyTyping = boolp(true)
	}
	return p
}

type pacingCache struct {
	mu    sync.Mutex
	admin *AdminPacing
	at    time.Time
	users map[int64]pacingEntry
}

type pacingEntry struct {
	p  Pacing
	at time.Time
}

func (m *Manager) adminPacing(ctx context.Context) AdminPacing {
	m.pace.mu.Lock()
	defer m.pace.mu.Unlock()
	if m.pace.admin != nil && time.Since(m.pace.at) < 30*time.Second {
		return *m.pace.admin
	}
	a := envPacing(m.cfg)
	if raw := getSetting(ctx, m.db, "pacing"); raw != "" {
		var saved AdminPacing
		if json.Unmarshal([]byte(raw), &saved) == nil {
			a.Pacing = a.Pacing.overlay(saved.Pacing)
			a.FloorGap, a.FloorAPIGap, a.UsersCanChange = saved.FloorGap, saved.FloorAPIGap, saved.UsersCanChange
		}
	}
	a.Pacing = a.Pacing.clamp(a)
	m.pace.admin, m.pace.at = &a, time.Now()
	return a
}

// pacing is the effective pace for one owner (all layers, floors applied).
func (m *Manager) pacing(ctx context.Context, owner int64) Pacing {
	a := m.adminPacing(ctx)
	m.pace.mu.Lock()
	if e, ok := m.pace.users[owner]; ok && time.Since(e.at) < 30*time.Second {
		m.pace.mu.Unlock()
		return e.p
	}
	m.pace.mu.Unlock()
	p := a.Pacing
	if a.UsersCanChange && owner > 0 {
		var raw string
		if m.db.QueryRowContext(ctx, `SELECT data FROM user_pacing WHERE user_id=?`, owner).Scan(&raw) == nil {
			var mine Pacing
			if json.Unmarshal([]byte(raw), &mine) == nil {
				p = p.overlay(mine)
			}
		}
	}
	p = p.clamp(a)
	m.pace.mu.Lock()
	if m.pace.users == nil {
		m.pace.users = map[int64]pacingEntry{}
	}
	m.pace.users[owner] = pacingEntry{p: p, at: time.Now()}
	m.pace.mu.Unlock()
	return p
}

func (m *Manager) forgetPacing() {
	m.pace.mu.Lock()
	m.pace.admin, m.pace.users = nil, nil
	m.pace.mu.Unlock()
}

func secs(n int) time.Duration { return time.Duration(n) * time.Second }

// ---------- API ----------

func (a *API) getAdminPacing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pacing": a.m.adminPacing(r.Context()), "server_defaults": envPacing(a.cfg)})
}

func (a *API) saveAdminPacing(w http.ResponseWriter, r *http.Request) {
	var body AdminPacing
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if body.FloorGap < 1 || body.FloorAPIGap < 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("floor_gap must be at least 1 second"))
		return
	}
	body.Pacing = envPacing(a.cfg).Pacing.overlay(body.Pacing).clamp(body)
	stored := body
	if stored.BatchSize == 0 {
		stored.BatchSize = -1 // "no breaks" (0 would mean "not set")
	}
	if err := setSetting(r.Context(), a.m.db, "pacing", jsonText(stored)); err != nil {
		a.fail(w, err)
		return
	}
	a.m.forgetPacing()
	audit(r.Context(), a.m.db, principalOf(r).UserID, clientIP(r), "pacing.update", jsonText(body))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pacing": a.m.adminPacing(r.Context())})
}

func (a *API) getMyPacing(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	adm := a.m.adminPacing(r.Context())
	var mine Pacing
	var raw string
	if a.m.db.QueryRowContext(r.Context(), `SELECT data FROM user_pacing WHERE user_id=?`, p.UserID).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &mine)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "effective": a.m.pacing(r.Context(), p.UserID), "mine": mine,
		"defaults": adm.Pacing, "floor_gap": adm.FloorGap, "floor_api_gap": adm.FloorAPIGap, "can_change": adm.UsersCanChange || p.Admin()})
}

func (a *API) saveMyPacing(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	adm := a.m.adminPacing(r.Context())
	if !adm.UsersCanChange && !p.Admin() {
		writeErr(w, http.StatusForbidden, "forbidden", errors.New("the administrator has fixed the sending pace for all accounts"))
		return
	}
	var mine Pacing
	if err := decode(r, &mine); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if mine.GapMin > 0 && mine.GapMin < adm.FloorGap {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("gap_min is below the minimum the administrator allows"))
		return
	}
	if mine.APIGapMin > 0 && mine.APIGapMin < adm.FloorAPIGap {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("api_gap_min is below the minimum the administrator allows"))
		return
	}
	if _, err := a.m.db.ExecContext(r.Context(), `INSERT INTO user_pacing(user_id, data) VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET data=excluded.data`,
		p.UserID, jsonText(mine)); err != nil {
		a.fail(w, err)
		return
	}
	a.m.forgetPacing()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "effective": a.m.pacing(r.Context(), p.UserID)})
}

// ======================= campaigns that take turns =======================
//
// rotation "turns": one message at a time across the campaign's numbers, in
// order (1 → 2 → 3 → 1 …), with the campaign's gap between messages. A
// number that doesn't take its turn within 45 s (offline, daily limit,
// quiet hours) is skipped for 10 minutes so the others keep going.

type campaignPace struct {
	rotation       string
	gapMin, gapMax int
	pool           []string
	at             time.Time
}

type turnState struct {
	last      string
	gateUntil time.Time
	sent      int
	skip      map[string]time.Time
}

type turnBook struct {
	mu    sync.Mutex
	camps map[int64]campaignPace
	turns map[int64]*turnState
}

func (m *Manager) campaignPace(ctx context.Context, id int64) campaignPace {
	if id <= 0 {
		return campaignPace{}
	}
	m.turnsBook.mu.Lock()
	c, ok := m.turnsBook.camps[id]
	m.turnsBook.mu.Unlock()
	if ok && time.Since(c.at) < 30*time.Second {
		return c
	}
	var rot, sessions string
	var gmin, gmax int
	if m.db.QueryRowContext(ctx, `SELECT rotation, gap_min, gap_max, sessions FROM campaigns WHERE id=?`, id).Scan(&rot, &gmin, &gmax, &sessions) != nil {
		return campaignPace{}
	}
	c = campaignPace{rotation: rot, gapMin: gmin, gapMax: gmax, pool: fromJSON[[]string](sessions), at: time.Now()}
	m.turnsBook.mu.Lock()
	if m.turnsBook.camps == nil {
		m.turnsBook.camps = map[int64]campaignPace{}
	}
	m.turnsBook.camps[id] = c
	m.turnsBook.mu.Unlock()
	return c
}

// myTurn reports whether session may send the next message of a "turns" campaign now.
func (m *Manager) myTurn(id int64, c campaignPace, session string, now time.Time) bool {
	if c.rotation != "turns" || len(c.pool) == 0 {
		return true
	}
	m.turnsBook.mu.Lock()
	defer m.turnsBook.mu.Unlock()
	if m.turnsBook.turns == nil {
		m.turnsBook.turns = map[int64]*turnState{}
	}
	t := m.turnsBook.turns[id]
	if t == nil {
		t = &turnState{skip: map[string]time.Time{}}
		m.turnsBook.turns[id] = t
	}
	if now.Before(t.gateUntil) {
		return false
	}
	expected := nextInPool(c.pool, t.last, t.skip, now)
	if expected == "" || expected == session {
		return true
	}
	// The expected number hasn't taken its turn: skip it for a while.
	if !t.gateUntil.IsZero() && now.Sub(t.gateUntil) > 45*time.Second {
		t.skip[expected] = now.Add(10 * time.Minute)
		return nextInPool(c.pool, t.last, t.skip, now) == session || len(c.pool) == 1
	}
	return false
}

func nextInPool(pool []string, last string, skip map[string]time.Time, now time.Time) string {
	start := 0
	for i, n := range pool {
		if n == last {
			start = i + 1
			break
		}
	}
	for k := 0; k < len(pool); k++ {
		n := pool[(start+k)%len(pool)]
		if until, ok := skip[n]; ok && now.Before(until) {
			continue
		}
		return n
	}
	return ""
}

// tookTurn records a send and closes the gate for gap; the other numbers are woken when it opens.
func (m *Manager) tookTurn(id int64, c campaignPace, session string, gap time.Duration, batchSize int, batchPause time.Duration) {
	m.turnsBook.mu.Lock()
	if m.turnsBook.turns == nil {
		m.turnsBook.turns = map[int64]*turnState{}
	}
	t := m.turnsBook.turns[id]
	if t == nil {
		t = &turnState{skip: map[string]time.Time{}}
		m.turnsBook.turns[id] = t
	}
	t.last, t.sent = session, t.sent+1
	delete(t.skip, session)
	if batchSize > 0 && t.sent%batchSize == 0 {
		gap += batchPause
	}
	t.gateUntil = time.Now().Add(gap)
	m.turnsBook.mu.Unlock()
	pool := c.pool
	time.AfterFunc(gap, func() {
		for _, n := range pool {
			if s, err := m.get(n); err == nil {
				s.poke()
			}
		}
	})
}
