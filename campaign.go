package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// ======================= templates =======================

type Template struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	Message   SendRequest `json:"message"`
	CreatedAt int64       `json:"created_at"`
	UpdatedAt int64       `json:"updated_at"`
}

func (a *API) listTemplates(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT id, name, message, created_at, updated_at FROM templates WHERE owner_id=? ORDER BY updated_at DESC`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		var t Template
		var msg string
		if rows.Scan(&t.ID, &t.Name, &msg, &t.CreatedAt, &t.UpdatedAt) == nil {
			t.Message = fromJSON[SendRequest](msg)
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "templates": out})
}

func (a *API) saveTemplate(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var t Template
	if err := decode(r, &t); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("name is required"))
		return
	}
	t.Message.To = ""
	if err := t.Message.validateContent(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if t.Message.Media != nil && t.Message.Media.Base64 != "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("templates must use media.url or media.media_id"))
		return
	}
	now := time.Now().Unix()
	if id := r.PathValue("id"); id != "" {
		res, err := a.m.db.ExecContext(r.Context(), `UPDATE templates SET name=?, message=?, updated_at=? WHERE id=? AND owner_id=?`,
			t.Name, jsonText(t.Message), now, id, p.UserID)
		if n, _ := rowsAffected(res, err); n == 0 {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("template not found"))
			return
		}
		t.ID, _ = strconv.ParseInt(id, 10, 64)
	} else {
		res, err := a.m.db.ExecContext(r.Context(), `INSERT INTO templates(owner_id, name, message, created_at, updated_at) VALUES(?,?,?,?,?)`,
			p.UserID, t.Name, jsonText(t.Message), now, now)
		if err != nil {
			a.fail(w, err)
			return
		}
		t.ID, _ = res.LastInsertId()
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "template": t})
}

func (a *API) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM templates WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("template not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func rowsAffected(res sql.Result, err error) (int64, error) {
	if err != nil || res == nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ======================= campaigns =======================

type Audience struct {
	Contacts    []int64  `json:"contacts"`
	Tags        []string `json:"tags"`
	ExcludeTags []string `json:"exclude_tags"`
	Phones      []string `json:"phones"`
	Groups      []string `json:"groups"`
	Channels    []string `json:"channels"`
	Statuses    bool     `json:"statuses"`
	Lists       []int64  `json:"lists"` // saved send lists (phones, contacts, groups, channels)
}

type Schedule struct {
	At      int64  `json:"at"`
	Repeat  string `json:"repeat"` // none | daily | weekly | monthly
	Days    []int  `json:"days"`   // weekly: 0=Sun … 6=Sat
	Until   int64  `json:"until"`
	NextRun int64  `json:"next_run"`
	Runs    int    `json:"runs"`
}

type Campaign struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Sessions   []string       `json:"sessions"`
	Message    SendRequest    `json:"message"`
	Audience   Audience       `json:"audience"`
	Schedule   Schedule       `json:"schedule"`
	Speed      string         `json:"speed"`
	Rotation   string         `json:"rotation"` // smart (keep each person on one number) | random | turns (1 → 2 → 3 → 1 …)
	GapMin     int            `json:"gap_min"`  // seconds between messages (0 = account pace)
	GapMax     int            `json:"gap_max"`
	CreatedAt  int64          `json:"created_at"`
	StartedAt  int64          `json:"started_at"`
	FinishedAt int64          `json:"finished_at"`
	Stats      map[string]int `json:"stats"`
	OwnerID    int64          `json:"owner_id"`
	Draft      bool           `json:"draft,omitempty"` // input only
}

const campaignCols = `id, owner_id, name, status, sessions, message, audience, sched_at, repeat, days, until, next_run, runs, speed, created_at, started_at, finished_at, rotation, gap_min, gap_max`

func scanCampaign(row interface{ Scan(...any) error }) (*Campaign, error) {
	var c Campaign
	var sessions, msg, aud, days string
	if err := row.Scan(&c.ID, &c.OwnerID, &c.Name, &c.Status, &sessions, &msg, &aud, &c.Schedule.At, &c.Schedule.Repeat, &days,
		&c.Schedule.Until, &c.Schedule.NextRun, &c.Schedule.Runs, &c.Speed, &c.CreatedAt, &c.StartedAt, &c.FinishedAt, &c.Rotation, &c.GapMin, &c.GapMax); err != nil {
		return nil, err
	}
	c.Sessions = fromJSON[[]string](sessions)
	c.Message = fromJSON[SendRequest](msg)
	c.Audience = fromJSON[Audience](aud)
	c.Schedule.Days = fromJSON[[]int](days)
	return &c, nil
}

func (m *Manager) campaignStats(ctx context.Context, id int64) map[string]int {
	out := map[string]int{"total": 0, "queued": 0, "sending": 0, "sent": 0, "failed": 0, "skipped": 0, "cancelled": 0, "paused": 0}
	rows, err := m.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM gw_outbox WHERE campaign_id=? GROUP BY status`, id)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if rows.Scan(&st, &n) == nil {
			out[st] = n
			out["total"] += n
		}
	}
	return out
}

func speedFactor(speed string) int {
	switch speed {
	case "safe":
		return 2
	case "slow":
		return 4
	}
	return 1
}

// firstRun is when a new campaign should first go out: the chosen time, or
// now for an immediate send; a repeating campaign whose start is in the past
// waits for its next occurrence instead of firing at once.
func firstRun(s Schedule, now time.Time) int64 {
	if s.At > now.Unix() {
		return s.At
	}
	if s.Repeat != "" && s.Repeat != "none" && s.At > 0 {
		return nextRun(s, now)
	}
	return now.Unix()
}

// nextRun returns the next time after `after` for a repeating schedule (0 = no more runs).
func nextRun(s Schedule, after time.Time) int64 {
	if s.Repeat == "" || s.Repeat == "none" {
		return 0
	}
	base := time.Unix(s.At, 0).In(time.Local)
	if s.At == 0 {
		base = after
	}
	h, mi := base.Hour(), base.Minute()
	at := func(d time.Time) time.Time { return time.Date(d.Year(), d.Month(), d.Day(), h, mi, 0, 0, time.Local) }
	var next time.Time
	switch s.Repeat {
	case "daily":
		next = at(after)
		if !next.After(after) {
			next = at(after.AddDate(0, 0, 1))
		}
	case "weekly":
		days := s.Days
		if len(days) == 0 {
			days = []int{int(base.Weekday())}
		}
		for i := 0; i <= 7; i++ {
			d := at(after.AddDate(0, 0, i))
			if d.After(after) && slices.Contains(days, int(d.Weekday())) {
				next = d
				break
			}
		}
	case "monthly":
		day := base.Day()
		for i := 0; i <= 12; i++ {
			y, mo, _ := after.AddDate(0, i, -after.Day()+1).Date()
			last := time.Date(y, mo+1, 0, 0, 0, 0, 0, time.Local).Day()
			d := time.Date(y, mo, min(day, last), h, mi, 0, 0, time.Local)
			if d.After(after) {
				next = d
				break
			}
		}
	default:
		return 0
	}
	if next.IsZero() || (s.Until > 0 && next.Unix() > s.Until) {
		return 0
	}
	return next.Unix()
}

func (a *API) campaignFromRequest(r *http.Request, p *Principal, c *Campaign) error {
	ctx := r.Context()
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		c.Name = "Campaign " + time.Now().Format("02 Jan 15:04")
	}
	if len(c.Sessions) == 0 {
		return errors.New("choose at least one number")
	}
	for _, n := range c.Sessions {
		s, err := a.m.get(n)
		if err != nil || !p.canUse(s) {
			return fmt.Errorf("number %q not found", n)
		}
	}
	c.Message.To = ""
	if err := c.Message.validateContent(); err != nil {
		return err
	}
	for _, m := range append([]SendRequest{c.Message}, c.Message.Variants...) {
		if m.Media != nil && m.Media.Base64 != "" {
			return errors.New("campaign media must be media.url or media.media_id (upload it to the Media library)")
		}
	}
	switch c.Speed {
	case "", "normal":
		c.Speed = "normal"
	case "safe", "slow":
	default:
		return errors.New("speed must be safe, normal or slow")
	}
	switch c.Rotation {
	case "", "smart":
		c.Rotation = "smart"
	case "random", "turns":
	default:
		return errors.New("rotation must be smart, random or turns")
	}
	if c.GapMin < 0 || c.GapMax < 0 || c.GapMin > 3600 || c.GapMax > 3600 {
		return errors.New("gap_min / gap_max must be 0-3600 seconds")
	}
	if c.GapMin > 0 {
		if floor := a.m.adminPacing(ctx).FloorGap; c.GapMin < floor {
			return fmt.Errorf("the gap must be at least %d seconds (set by the administrator)", floor)
		}
		c.GapMax = max(c.GapMax, c.GapMin)
	} else {
		c.GapMax = 0
	}
	switch c.Schedule.Repeat {
	case "", "none":
		c.Schedule.Repeat = "none"
	case "daily", "weekly", "monthly":
	default:
		return errors.New("repeat must be none, daily, weekly or monthly")
	}
	a2 := c.Audience
	for _, id := range a2.Lists {
		if _, err := a.m.loadList(ctx, p.UserID, id); err != nil {
			return fmt.Errorf("audience.lists must be your own lists (list %d was not found)", id)
		}
	}
	if len(a2.Contacts)+len(a2.Tags)+len(a2.Phones)+len(a2.Groups)+len(a2.Channels)+len(a2.Lists) == 0 && !a2.Statuses {
		return errors.New("choose who should receive this campaign")
	}
	if len(a2.Phones) > 10000 {
		return errors.New("at most 10000 pasted numbers; import them as contacts instead")
	}
	lic := a.lic
	if err := lic.require(ctx, p.UserID, "campaigns"); err != nil {
		return err
	}
	if c.Schedule.Repeat != "none" || c.Schedule.At > time.Now().Unix()+60 {
		if err := lic.require(ctx, p.UserID, "scheduler"); err != nil {
			return err
		}
	}
	if len(c.Sessions) > 1 {
		if err := lic.require(ctx, p.UserID, "pool"); err != nil {
			return err
		}
	}
	if a2.Statuses {
		if err := lic.require(ctx, p.UserID, "status"); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) listCampaigns(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := `SELECT ` + campaignCols + ` FROM campaigns WHERE owner_id=?`
	args := []any{p.UserID}
	if st := r.URL.Query().Get("status"); st != "" {
		q += ` AND status=?`
		args = append(args, st)
	}
	rows, err := a.m.db.QueryContext(r.Context(), q+` ORDER BY id DESC LIMIT 500`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	var list []*Campaign
	for rows.Next() {
		if c, err := scanCampaign(rows); err == nil {
			list = append(list, c)
		}
	}
	rows.Close()
	out := []*Campaign{}
	for _, c := range list {
		c.Stats = a.m.campaignStats(r.Context(), c.ID)
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "campaigns": out})
}

func (a *API) ownedCampaign(w http.ResponseWriter, r *http.Request) *Campaign {
	p := principalOf(r)
	c, err := scanCampaign(a.m.db.QueryRowContext(r.Context(), `SELECT `+campaignCols+` FROM campaigns WHERE id=?`, r.PathValue("id")))
	if err != nil || (c.OwnerID != p.UserID && !p.Admin()) {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("campaign not found"))
		return nil
	}
	c.Stats = a.m.campaignStats(r.Context(), c.ID)
	return c
}

func (a *API) createCampaign(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var c Campaign
	if err := decode(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if err := a.campaignFromRequest(r, p, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	now := time.Now()
	status, next := "scheduled", firstRun(c.Schedule, now)
	if c.Draft {
		status, next = "draft", 0
	}
	res, err := a.m.db.ExecContext(r.Context(), `INSERT INTO campaigns(owner_id, name, status, sessions, message, audience, sched_at, repeat, days, until, next_run, speed, rotation, gap_min, gap_max, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.UserID, c.Name, status, jsonText(c.Sessions), jsonText(c.Message), jsonText(c.Audience),
		c.Schedule.At, c.Schedule.Repeat, jsonText(orInts(c.Schedule.Days)), c.Schedule.Until, next, c.Speed, c.Rotation, c.GapMin, c.GapMax, now.Unix(), now.Unix())
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	a.m.pokeScheduler()
	saved, err := scanCampaign(a.m.db.QueryRowContext(r.Context(), `SELECT `+campaignCols+` FROM campaigns WHERE id=?`, id))
	if err != nil {
		a.fail(w, err)
		return
	}
	saved.Stats = a.m.campaignStats(r.Context(), id)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "campaign": saved})
}

func orInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}

func (a *API) getCampaign(w http.ResponseWriter, r *http.Request) {
	c := a.ownedCampaign(w, r)
	if c == nil {
		return
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT id, to_addr, session, status, error, sent_at FROM gw_outbox WHERE campaign_id=? ORDER BY
		CASE status WHEN 'sending' THEN 0 WHEN 'failed' THEN 1 ELSE 2 END, finished_at DESC, id DESC LIMIT 50`, c.ID)
	recent := []map[string]any{}
	if err == nil {
		for rows.Next() {
			var id, sentAt int64
			var to, sess, st, e string
			if rows.Scan(&id, &to, &sess, &st, &e, &sentAt) == nil {
				recent = append(recent, map[string]any{"id": id, "to": to, "session": sess, "status": st, "error": e, "sent_at": sentAt})
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "campaign": c, "recent": recent})
}

func (a *API) updateCampaign(w http.ResponseWriter, r *http.Request) {
	old := a.ownedCampaign(w, r)
	if old == nil {
		return
	}
	if old.Status != "draft" && old.Status != "scheduled" {
		writeErr(w, http.StatusConflict, "not_editable", errors.New("only draft or scheduled campaigns can be edited; duplicate it instead"))
		return
	}
	c := *old
	if err := decode(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if err := a.campaignFromRequest(r, principalOf(r), &c); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	status, next := old.Status, firstRun(c.Schedule, time.Now())
	if old.Schedule.Runs > 0 && c.Schedule.Repeat != "none" {
		next = nextRun(c.Schedule, time.Now()) // already ran once: only future repeats
	}
	if c.Draft {
		status, next = "draft", 0
	} else if status == "draft" {
		status = "scheduled"
	}
	if status == "scheduled" && next == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("this schedule has no future run; check the dates"))
		return
	}
	_, err := a.m.db.ExecContext(r.Context(), `UPDATE campaigns SET name=?, status=?, sessions=?, message=?, audience=?, sched_at=?, repeat=?, days=?, until=?, next_run=?, speed=?, rotation=?, gap_min=?, gap_max=?, updated_at=? WHERE id=?`,
		c.Name, status, jsonText(c.Sessions), jsonText(c.Message), jsonText(c.Audience), c.Schedule.At, c.Schedule.Repeat, jsonText(orInts(c.Schedule.Days)),
		c.Schedule.Until, next, c.Speed, c.Rotation, c.GapMin, c.GapMax, time.Now().Unix(), old.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.m.pokeScheduler()
	saved, err := scanCampaign(a.m.db.QueryRowContext(r.Context(), `SELECT `+campaignCols+` FROM campaigns WHERE id=?`, old.ID))
	if err != nil {
		a.fail(w, err)
		return
	}
	saved.Stats = a.m.campaignStats(r.Context(), old.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "campaign": saved})
}

func (a *API) campaignAction(w http.ResponseWriter, r *http.Request) {
	c := a.ownedCampaign(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	db := a.m.db
	now := time.Now().Unix()
	switch r.PathValue("action") {
	case "pause":
		if c.Status != "running" && c.Status != "scheduled" {
			writeErr(w, http.StatusConflict, "bad_state", errors.New("only running or scheduled campaigns can be paused"))
			return
		}
		_, _ = db.ExecContext(ctx, `UPDATE gw_outbox SET status='paused' WHERE campaign_id=? AND status='queued'`, c.ID)
		_, _ = db.ExecContext(ctx, `UPDATE campaigns SET status='paused', updated_at=? WHERE id=?`, now, c.ID)
	case "resume":
		if c.Status != "paused" {
			writeErr(w, http.StatusConflict, "bad_state", errors.New("campaign is not paused"))
			return
		}
		_, _ = db.ExecContext(ctx, `UPDATE gw_outbox SET status='queued' WHERE campaign_id=? AND status='paused'`, c.ID)
		next := "running"
		if c.Stats["paused"] == 0 && c.Stats["queued"] == 0 && c.Schedule.NextRun > 0 {
			next = "scheduled"
		} else if c.StartedAt == 0 {
			next = "scheduled"
		}
		_, _ = db.ExecContext(ctx, `UPDATE campaigns SET status=?, updated_at=? WHERE id=?`, next, now, c.ID)
		a.m.pokeAll()
	case "cancel":
		_, _ = db.ExecContext(ctx, `UPDATE gw_outbox SET status='cancelled' WHERE campaign_id=? AND status IN ('queued','paused')`, c.ID)
		_, _ = db.ExecContext(ctx, `UPDATE campaigns SET status='cancelled', next_run=0, finished_at=?, updated_at=? WHERE id=?`, now, now, c.ID)
	case "run-now":
		if c.Status == "running" {
			writeErr(w, http.StatusConflict, "bad_state", errors.New("campaign is already running"))
			return
		}
		_, _ = db.ExecContext(ctx, `UPDATE campaigns SET status='scheduled', next_run=?, updated_at=? WHERE id=?`, now, now, c.ID)
		a.m.pokeScheduler()
	case "duplicate":
		res, err := db.ExecContext(ctx, `INSERT INTO campaigns(owner_id, name, status, sessions, message, audience, sched_at, repeat, days, until, next_run, speed, rotation, gap_min, gap_max, created_at, updated_at)
			SELECT owner_id, name || ' (copy)', 'draft', sessions, message, audience, 0, repeat, days, until, 0, speed, rotation, gap_min, gap_max, ?, ? FROM campaigns WHERE id=?`, now, now, c.ID)
		if err != nil {
			a.fail(w, err)
			return
		}
		id, _ := res.LastInsertId()
		nc, _ := scanCampaign(db.QueryRowContext(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE id=?`, id))
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "campaign": nc})
		return
	default:
		writeErr(w, http.StatusNotFound, "not_found", errors.New("unknown action"))
		return
	}
	nc := a.ownedCampaign(w, r)
	if nc == nil {
		return
	}
	a.m.dispatchOwner(nc.OwnerID, "campaign.status", map[string]any{"id": nc.ID, "name": nc.Name, "status": nc.Status, "stats": nc.Stats})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "campaign": nc})
}

func (a *API) deleteCampaign(w http.ResponseWriter, r *http.Request) {
	c := a.ownedCampaign(w, r)
	if c == nil {
		return
	}
	_, _ = a.m.db.ExecContext(r.Context(), `UPDATE gw_outbox SET status='cancelled' WHERE campaign_id=? AND status IN ('queued','paused')`, c.ID)
	_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM campaigns WHERE id=?`, c.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) campaignReport(w http.ResponseWriter, r *http.Request) {
	c := a.ownedCampaign(w, r)
	if c == nil {
		return
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT to_addr, session, status, error, wa_id, created_at, sent_at FROM gw_outbox WHERE campaign_id=? ORDER BY id`, c.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="campaign-%d.csv"`, c.ID))
	_, _ = w.Write([]byte("\ufeff"))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"to", "number", "status", "error", "message_id", "queued_at", "sent_at"})
	ts := func(t int64) string {
		if t == 0 {
			return ""
		}
		return time.Unix(t, 0).Format("2006-01-02 15:04:05")
	}
	for rows.Next() {
		var to, sess, st, e, wa string
		var created, sent int64
		if rows.Scan(&to, &sess, &st, &e, &wa, &created, &sent) == nil {
			_ = cw.Write([]string{strings.Split(to, "@")[0], sess, st, csvSafe(e), wa, ts(created), ts(sent)})
		}
	}
	cw.Flush()
}

// ---------- queue insertion shared by campaigns and the bulk API ----------

type enqueueResult struct {
	Queued     int            `json:"queued"`
	Skipped    int            `json:"skipped_opted_out"`
	Dupes      int            `json:"skipped_duplicates"`
	PerSession map[string]int `json:"per_session"`
	IDs        []int64        `json:"-"`
}

// enqueue puts messages in the outbox, spreading people over the pool.
func (m *Manager) enqueue(ctx context.Context, owner int64, pool []string, campaignID int64, label string, msgs []SendRequest, sendAfter int64, factor int, rotation string) (*enqueueResult, error) {
	random, turns := rotation == "random", rotation == "turns"
	poolCol := ""
	if len(pool) > 1 {
		poolCol = strings.Join(pool, ",")
	}
	cands := m.poolCandidates(ctx, pool)
	res := &enqueueResult{PerSession: map[string]int{}}
	type row struct {
		session, to, payload string
		priority             int
	}
	rows := make([]row, 0, len(msgs))
	inBatch := map[string]bool{}
	for i := range msgs {
		to, err := parseTarget(msgs[i].To, m.cfg.DefaultCountryCode)
		if err != nil {
			continue
		}
		key := to.String()
		isPerson := to.Server == types.DefaultUserServer || to.Server == types.HiddenUserServer
		if isPerson && isOptedOut(ctx, m.db, owner, key) {
			res.Skipped++
			continue
		}
		if inBatch[key] {
			res.Dupes++
			continue
		}
		inBatch[key] = true
		session := pool[len(rows)%len(pool)]
		if !isPerson {
			session = pool[0] // groups, channels and Status are posted by the first number
		} else if turns && len(pool) > 1 {
			session = "" // assigned in turn order below, after sorting by priority
		} else if random && len(pool) > 1 {
			session = pool[mrand.IntN(len(pool))] // "random number": each person gets a random sender
		} else if len(pool) > 1 {
			if pick := m.pickSession(ctx, cands, key); pick != "" {
				session = pick
			}
		}
		p, _ := json.Marshal(msgs[i])
		prio := 0
		if isPerson {
			prio = m.priorityFor(ctx, pool, key)
		} else {
			prio = 3 // posts first
		}
		rows = append(rows, row{session: session, to: key, payload: string(p), priority: prio})
	}
	if turns && len(pool) > 1 {
		// Highest priority first, then hand out numbers 1, 2, 3, 1, 2, 3 …
		slices.SortStableFunc(rows, func(a, b row) int { return b.priority - a.priority })
		k := 0
		for i := range rows {
			if rows[i].session == "" {
				rows[i].session = pool[k%len(pool)]
				k++
			}
		}
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO gw_outbox(session,campaign,campaign_id,owner_id,to_addr,payload,status,not_before,priority,pool,speed_factor,created_at)
		VALUES(?,?,?,?,?,?,'queued',?,?,?,?,?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	for _, rw := range rows {
		r, err := stmt.ExecContext(ctx, rw.session, label, campaignID, owner, rw.to, rw.payload, sendAfter, rw.priority, poolCol, max(factor, 1), now)
		if err != nil {
			return nil, err
		}
		id, _ := r.LastInsertId()
		res.IDs = append(res.IDs, id)
		res.PerSession[rw.session]++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	res.Queued = len(res.IDs)
	for name := range res.PerSession {
		if s, err := m.get(name); err == nil {
			s.poke()
		}
	}
	return res, nil
}

// ======================= scheduler =======================

func (m *Manager) pokeScheduler() {
	select {
	case m.schedWake <- struct{}{}:
	default:
	}
}

func (m *Manager) pokeAll() {
	for _, s := range m.list() {
		s.poke()
	}
}

func (m *Manager) runScheduler() {
	for {
		m.schedulerTick()
		select {
		case <-m.ctx.Done():
			return
		case <-m.schedWake:
		case <-time.After(20 * time.Second):
		}
	}
}

func (m *Manager) schedulerTick() {
	ctx := m.ctx
	now := time.Now().Unix()
	m.runSequences(ctx)
	m.runRenewals(ctx)
	// 1. start due campaigns
	rows, err := m.db.QueryContext(ctx, `SELECT id FROM campaigns WHERE status='scheduled' AND next_run>0 AND next_run<=? ORDER BY next_run LIMIT 20`, now)
	if err == nil {
		var due []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				due = append(due, id)
			}
		}
		rows.Close()
		for _, id := range due {
			m.startCampaign(ctx, id)
		}
	}
	// 2. finish campaigns whose queue is empty
	rows, err = m.db.QueryContext(ctx, `SELECT c.id FROM campaigns c WHERE c.status='running' AND NOT EXISTS(
		SELECT 1 FROM gw_outbox o WHERE o.campaign_id=c.id AND o.status IN ('queued','sending','paused'))`)
	if err == nil {
		var done []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				done = append(done, id)
			}
		}
		rows.Close()
		for _, id := range done {
			c, err := scanCampaign(m.db.QueryRowContext(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE id=?`, id))
			if err != nil {
				continue
			}
			status := "done"
			if c.Schedule.NextRun > 0 {
				status = "scheduled"
			}
			_, _ = m.db.ExecContext(ctx, `UPDATE campaigns SET status=?, finished_at=?, updated_at=? WHERE id=? AND status='running'`, status, now, now, id)
			m.dispatchOwner(c.OwnerID, "campaign.status", map[string]any{"id": id, "name": c.Name, "status": status, "stats": m.campaignStats(ctx, id)})
		}
	}
}

// startCampaign expands the audience into queue rows.
func (m *Manager) startCampaign(ctx context.Context, id int64) {
	c, err := scanCampaign(m.db.QueryRowContext(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE id=?`, id))
	if err != nil {
		return
	}
	now := time.Now()
	fail := func(reason string) {
		_, _ = m.db.ExecContext(ctx, `UPDATE campaigns SET status='paused', updated_at=? WHERE id=?`, now.Unix(), id)
		m.log.Warn("campaign could not start", "id", id, "reason", reason)
		m.dispatchOwner(c.OwnerID, "campaign.status", map[string]any{"id": id, "name": c.Name, "status": "paused", "error": reason})
	}
	if err := m.lic.require(ctx, c.OwnerID, "campaigns"); err != nil {
		fail(err.Error())
		return
	}
	var pool []string
	for _, n := range c.Sessions {
		if s, err := m.get(n); err == nil && (s.owner() == c.OwnerID || m.lic.roleOf(ctx, c.OwnerID) == "superadmin") {
			pool = append(pool, n)
		}
	}
	if len(pool) == 0 {
		fail("none of the selected numbers exist any more")
		return
	}
	msgs := m.expandAudience(ctx, c)
	next := nextRun(c.Schedule, now)
	label := fmt.Sprintf("c%d", c.ID)
	res, err := m.enqueue(ctx, c.OwnerID, pool, c.ID, label, msgs, 0, speedFactor(c.Speed), c.Rotation)
	if err != nil {
		fail(err.Error())
		return
	}
	started := c.StartedAt
	if started == 0 {
		started = now.Unix()
	}
	_, _ = m.db.ExecContext(ctx, `UPDATE campaigns SET status='running', runs=runs+1, next_run=?, started_at=?, updated_at=? WHERE id=?`,
		next, started, now.Unix(), id)
	m.dispatchOwner(c.OwnerID, "campaign.status", map[string]any{"id": id, "name": c.Name, "status": "running", "queued": res.Queued,
		"skipped_opted_out": res.Skipped})
}

// expandAudience turns the audience into one message per recipient with personal variables.
func (m *Manager) expandAudience(ctx context.Context, c *Campaign) []SendRequest {
	var out []SendRequest
	add := func(to string, vars map[string]string) {
		msg := c.Message
		msg.To = to
		merged := map[string]string{}
		for k, v := range vars {
			merged[k] = v
		}
		for k, v := range c.Message.Vars {
			if _, ok := merged[k]; !ok {
				merged[k] = v
			}
		}
		msg.Vars = merged
		out = append(out, msg)
	}
	aud := c.Audience
	exclude := map[string]bool{}
	for _, t := range aud.ExcludeTags {
		exclude[strings.ToLower(strings.TrimSpace(t))] = true
	}
	seen := map[int64]bool{}
	listNames := map[string]string{}
	addContact := func(ct *Contact) {
		if seen[ct.ID] {
			return
		}
		for _, t := range ct.Tags {
			if exclude[t] {
				return
			}
		}
		seen[ct.ID] = true
		add(ct.Phone, contactVars(ct))
	}
	q := func(where string, args ...any) {
		rows, err := m.db.QueryContext(ctx, `SELECT `+contactCols+` FROM contacts c WHERE c.owner_id=? AND `+where, append([]any{c.OwnerID}, args...)...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			if ct, err := scanContact(rows); err == nil {
				addContact(ct)
			}
		}
	}
	for _, t := range aud.Tags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			q(`c.tags LIKE ?`, "%,"+t+",%")
		}
	}
	for _, id := range aud.Contacts {
		q(`c.id=?`, id)
	}
	// Saved lists are read when the campaign starts, so later changes to a
	// list are used by repeating campaigns too.
	for _, lid := range aud.Lists {
		l, err := m.loadList(ctx, c.OwnerID, lid)
		if err != nil {
			continue
		}
		for _, it := range l.Items {
			switch itemKind(it.To) {
			case "contacts":
				if n, err := strconv.ParseInt(strings.TrimPrefix(it.To, "contact:"), 10, 64); err == nil {
					q(`c.id=?`, n)
				}
			case "groups":
				aud.Groups = append(aud.Groups, it.To)
			case "channels":
				aud.Channels = append(aud.Channels, it.To)
			default:
				aud.Phones = append(aud.Phones, it.To)
				if it.Name != "" {
					listNames[it.To] = it.Name
				}
			}
		}
	}
	for _, ph := range aud.Phones {
		if p := normPhone(ph, m.cfg.DefaultCountryCode); p != "" {
			var ct *Contact
			ct, _ = scanContact(m.db.QueryRowContext(ctx, `SELECT `+contactCols+` FROM contacts c WHERE c.owner_id=? AND c.phone=?`, c.OwnerID, p))
			if ct != nil {
				addContact(ct)
			} else {
				name := "there"
				if n := listNames[p]; n != "" {
					name = n
				}
				add(p, map[string]string{"phone": p, "name": name, "first_name": strings.Fields(name)[0]})
			}
		}
	}
	for _, g := range aud.Groups {
		add(g, nil)
	}
	for _, ch := range aud.Channels {
		add(ch, nil)
	}
	if aud.Statuses {
		add("status", nil)
	}
	return out
}

// dispatchOwner sends an event that isn't tied to one number (campaigns).
func (m *Manager) dispatchOwner(owner int64, event string, data any) {
	body := payload("", owner, event, data)
	m.hooks.enqueue(webhookJob{url: m.cfg.WebhookURL, secret: m.hooks.secret, body: body, evt: event})
	rows, err := m.db.Query(`SELECT id, url, secret, events FROM webhook_endpoints WHERE user_id=? AND active=1`, owner)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var url, secret, evts string
		if rows.Scan(&id, &url, &secret, &evts) == nil && eventWanted(fromJSON[[]string](evts), event) {
			m.hooks.enqueue(webhookJob{url: url, secret: secret, body: body, evt: event, endpointID: id, guarded: !m.cfg.AllowPrivateURLs})
		}
	}
}
