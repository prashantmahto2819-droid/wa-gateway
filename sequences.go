package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ======================= follow-up sequences, renewals, tasks =======================
//
// A sequence is a timed plan for one person: day 0 welcome message, day 1
// call, day 7 reminder … Steps of kind auto_whatsapp are sent by the
// gateway through the normal outbox (so pacing, turns and daily limits all
// apply); the other kinds (whatsapp, call, task) become tasks for staff.
// A run is one person going through a sequence. Replies can stop a run.
//
// Renewals are dated things to remind about (a policy premium, a
// subscription, a service due): a message goes N days before / after the
// due date, and recurring ones roll forward when marked done.
//
// Tasks are the staff to-do list: checklist items from the kit (repeating),
// steps from sequences, renewal calls and anything added by hand.

type Sequence struct {
	ID           int64     `json:"id"`
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Trigger      string    `json:"trigger"`
	TriggerValue string    `json:"trigger_value"`
	BusinessDays bool      `json:"business_days"`
	StopOnReply  bool      `json:"stop_on_reply"`
	Steps        []KitStep `json:"steps"`
	Session      string    `json:"session"`
	Active       bool      `json:"active"`
	Source       string    `json:"source"`
	Runs         int       `json:"runs"`
	CreatedAt    int64     `json:"created_at"`
	UpdatedAt    int64     `json:"updated_at"`
}

const seqCols = `id, key, name, trigger_type, trigger_value, business_days, stop_on_reply, steps, session, active, source, created_at, updated_at`

func scanSeq(row interface{ Scan(...any) error }) (*Sequence, error) {
	var s Sequence
	var steps string
	var bd, sr, act int
	if err := row.Scan(&s.ID, &s.Key, &s.Name, &s.Trigger, &s.TriggerValue, &bd, &sr, &steps, &s.Session, &act, &s.Source, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.BusinessDays, s.StopOnReply, s.Active = bd == 1, sr == 1, act == 1
	s.Steps = fromJSON[[]KitStep](steps)
	if s.Steps == nil {
		s.Steps = []KitStep{}
	}
	return &s, nil
}

func (m *Manager) sequence(ctx context.Context, owner, id int64) (*Sequence, error) {
	return scanSeq(m.db.QueryRowContext(ctx, `SELECT `+seqCols+` FROM sequences WHERE id=? AND owner_id=?`, id, owner))
}

// stepTime is when step i of a run that started at start comes up.
func stepTime(start time.Time, step KitStep, businessDays bool) time.Time {
	t := start.AddDate(0, 0, max(step.Day, 0))
	if step.Day > 0 {
		// later steps go out at 10:30 local, not at the odd hour the run began
		t = time.Date(t.Year(), t.Month(), t.Day(), 10, 30, 0, 0, t.Location())
	}
	if businessDays {
		for t.Weekday() == time.Sunday {
			t = t.AddDate(0, 0, 1)
		}
	}
	return t
}

// enroll starts a sequence for a person (one active run per person per sequence).
func (m *Manager) enroll(ctx context.Context, owner int64, seq *Sequence, phone, name string, contactID int64, vars map[string]string) (bool, error) {
	if len(seq.Steps) == 0 || !seq.Active {
		return false, nil
	}
	var n int
	_ = m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sequence_runs WHERE sequence_id=? AND phone=? AND status='active'`, seq.ID, phone).Scan(&n)
	if n > 0 {
		return false, nil
	}
	now := time.Now()
	next := stepTime(now, seq.Steps[0], seq.BusinessDays)
	if vars == nil {
		vars = map[string]string{}
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO sequence_runs(owner_id, sequence_id, contact_id, phone, name, step, next_at, status, vars, started_at, updated_at)
		VALUES(?,?,?,?,?,0,?,'active',?,?,?)`, owner, seq.ID, contactID, phone, truncate(name, 120), next.Unix(), jsonText(vars), now.Unix(), now.Unix())
	if err != nil {
		return false, err
	}
	m.pokeScheduler()
	return true, nil
}

// enrollByTrigger starts every active sequence of this owner with the given
// trigger (lead_new, tag:<x>, stage:<x>) for a person.
func (m *Manager) enrollByTrigger(ctx context.Context, owner int64, trigger, value, phone, name string, contactID int64) int {
	rows, err := m.db.QueryContext(ctx, `SELECT `+seqCols+` FROM sequences WHERE owner_id=? AND active=1 AND trigger_type=? AND (trigger_value='' OR trigger_value=?)`, owner, trigger, value)
	if err != nil {
		return 0
	}
	var seqs []*Sequence
	for rows.Next() {
		if s, err := scanSeq(rows); err == nil {
			seqs = append(seqs, s)
		}
	}
	rows.Close()
	n := 0
	for _, s := range seqs {
		if ok, _ := m.enroll(ctx, owner, s, phone, name, contactID, nil); ok {
			n++
		}
	}
	return n
}

// stopRunsOnReply ends runs that stop when the person writes back.
func (m *Manager) stopRunsOnReply(ctx context.Context, owner int64, phone string) {
	_, _ = m.db.ExecContext(ctx, `UPDATE sequence_runs SET status='replied', updated_at=? WHERE owner_id=? AND phone=? AND status='active'
		AND sequence_id IN (SELECT id FROM sequences WHERE owner_id=? AND stop_on_reply=1)`, time.Now().Unix(), owner, phone, owner)
}

// ownerPool is the numbers of this workspace that can send (optionally just one).
func (m *Manager) ownerPool(ctx context.Context, owner int64, only string) []string {
	var pool []string
	for _, s := range m.list() {
		if s.owner() != owner || (only != "" && s.name != only) {
			continue
		}
		if r := s.row(ctx); r.jid != "" && !r.paused {
			pool = append(pool, s.name)
		}
	}
	return pool
}

// runSequences does the due steps of all active runs (called by the scheduler).
func (m *Manager) runSequences(ctx context.Context) {
	now := time.Now()
	rows, err := m.db.QueryContext(ctx, `SELECT r.id, r.owner_id, r.sequence_id, r.contact_id, r.phone, r.name, r.step, r.vars, r.started_at
		FROM sequence_runs r WHERE r.status='active' AND r.next_at<=? ORDER BY r.next_at LIMIT 200`, now.Unix())
	if err != nil {
		return
	}
	type due struct {
		id, owner, seqID, contactID int64
		phone, name, vars           string
		step                        int
		startedAt                   int64
	}
	var list []due
	for rows.Next() {
		var d due
		if rows.Scan(&d.id, &d.owner, &d.seqID, &d.contactID, &d.phone, &d.name, &d.step, &d.vars, &d.startedAt) == nil {
			list = append(list, d)
		}
	}
	rows.Close()
	seqCache := map[int64]*Sequence{}
	bizCache := map[int64]map[string]string{}
	for _, d := range list {
		seq, ok := seqCache[d.seqID]
		if !ok {
			seq, _ = m.sequence(ctx, d.owner, d.seqID)
			seqCache[d.seqID] = seq
		}
		if seq == nil || !seq.Active || d.step >= len(seq.Steps) {
			_, _ = m.db.ExecContext(ctx, `UPDATE sequence_runs SET status='done', updated_at=? WHERE id=?`, now.Unix(), d.id)
			continue
		}
		step := seq.Steps[d.step]
		vars := fromJSON[map[string]string](d.vars)
		if vars == nil {
			vars = map[string]string{}
		}
		if _, ok := bizCache[d.owner]; !ok {
			bizCache[d.owner] = m.businessVars(ctx, d.owner)
		}
		for k, v := range bizCache[d.owner] {
			if _, has := vars[k]; !has {
				vars[k] = v
			}
		}
		vars["phone"] = d.phone
		if vars["name"] == "" {
			vars["name"] = firstWord(d.name, "जी")
		}
		var note string
		switch step.Action {
		case "auto_whatsapp":
			if strings.TrimSpace(step.Message) == "" {
				break
			}
			pool := m.ownerPool(ctx, d.owner, seq.Session)
			if len(pool) == 0 {
				note = "no number linked"
				break
			}
			msg := SendRequest{To: d.phone, Text: step.Message, Vars: vars}
			res, err := m.enqueue(ctx, d.owner, pool, 0, fmt.Sprintf("seq%d", seq.ID), []SendRequest{msg}, now.Unix(), 1, "smart")
			if err != nil {
				note = err.Error()
			} else if res.Queued == 0 {
				note = "skipped (opted out)"
			}
		default: // whatsapp, call, task — a job for a person
			title := step.Title
			if title == "" {
				title = step.Action
			}
			if d.name != "" {
				title = title + " — " + d.name
			}
			_, _ = m.db.ExecContext(ctx, `INSERT INTO tasks(owner_id, title, due, repeat, at_time, done_at, assignee_id, contact_id, phone, source, note, created_at)
				VALUES(?,?,?,'','',0,0,?,?,'sequence',?,?)`, d.owner, truncate(title, 200), now.Unix(), d.contactID, d.phone, truncate(renderText(step.Message, vars, nil), 2000), now.Unix())
		}
		_, _ = m.db.ExecContext(ctx, `INSERT INTO sequence_log(run_id, step, action, title, note, at) VALUES(?,?,?,?,?,?)`, d.id, d.step, step.Action, step.Title, note, now.Unix())
		next := d.step + 1
		if next >= len(seq.Steps) {
			_, _ = m.db.ExecContext(ctx, `UPDATE sequence_runs SET step=?, status='done', updated_at=? WHERE id=?`, next, now.Unix(), d.id)
			continue
		}
		start := time.Unix(d.startedAt, 0)
		nextAt := stepTime(start, seq.Steps[next], seq.BusinessDays)
		if !nextAt.After(now) { // several steps on the same day: 2 minutes apart
			nextAt = now.Add(2 * time.Minute)
		}
		_, _ = m.db.ExecContext(ctx, `UPDATE sequence_runs SET step=?, next_at=?, updated_at=? WHERE id=?`, next, nextAt.Unix(), now.Unix(), d.id)
	}
}

func firstWord(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return fallback
	}
	if i := strings.IndexByte(name, ' '); i > 0 {
		return name[:i]
	}
	return name
}

// ---------- renewals ----------

type RenewalType struct {
	ID           int64      `json:"id"`
	Key          string     `json:"key"`
	Label        string     `json:"label"`
	Icon         string     `json:"icon"`
	Frequency    string     `json:"frequency"`
	RemindBefore []int      `json:"remind_before"`
	RemindAfter  []int      `json:"remind_after"`
	StaffTask    bool       `json:"staff_task"`
	Fields       []KitField `json:"fields"`
	Message      string     `json:"message"`
	Session      string     `json:"session"`
	Source       string     `json:"source"`
	Open         int        `json:"open"`
}

const rtCols = `id, key, label, icon, frequency, remind_before, remind_after, staff_task, fields, message, session, source`

func scanRT(row interface{ Scan(...any) error }) (*RenewalType, error) {
	var t RenewalType
	var before, after, fields string
	var st int
	if err := row.Scan(&t.ID, &t.Key, &t.Label, &t.Icon, &t.Frequency, &before, &after, &st, &fields, &t.Message, &t.Session, &t.Source); err != nil {
		return nil, err
	}
	t.StaffTask = st == 1
	t.RemindBefore = orEmptyInts(fromJSON[[]int](before))
	t.RemindAfter = orEmptyInts(fromJSON[[]int](after))
	t.Fields = fromJSON[[]KitField](fields)
	if t.Fields == nil {
		t.Fields = []KitField{}
	}
	return &t, nil
}

type Renewal struct {
	ID        int64             `json:"id"`
	TypeID    int64             `json:"type_id"`
	Type      string            `json:"type,omitempty"`
	Icon      string            `json:"icon,omitempty"`
	ContactID int64             `json:"contact_id"`
	Phone     string            `json:"phone"`
	Name      string            `json:"name"`
	Title     string            `json:"title"`
	Amount    string            `json:"amount"`
	Due       int64             `json:"due"` // unix at local midnight
	Fields    map[string]string `json:"fields"`
	Status    string            `json:"status"` // open, done, lost
	Reminded  []string          `json:"reminded"`
	Note      string            `json:"note"`
	CreatedAt int64             `json:"created_at"`
}

// nextDue rolls a due date forward by one period.
func nextDue(due time.Time, freq string) (time.Time, bool) {
	switch freq {
	case "yearly":
		return due.AddDate(1, 0, 0), true
	case "half_yearly":
		return due.AddDate(0, 6, 0), true
	case "quarterly":
		return due.AddDate(0, 3, 0), true
	case "monthly":
		return due.AddDate(0, 1, 0), true
	case "weekly":
		return due.AddDate(0, 0, 7), true
	}
	return due, false
}

// runRenewals sends today's reminders (called by the scheduler, idempotent per day/offset).
func (m *Manager) runRenewals(ctx context.Context) {
	now := time.Now()
	if now.Hour() < 9 { // reminders go out from 9 am local
		return
	}
	today := dayStart(now)
	rows, err := m.db.QueryContext(ctx, `SELECT r.id, r.owner_id, r.type_id, r.contact_id, r.phone, r.name, r.title, r.amount, r.due, r.reminded
		FROM renewals r WHERE r.status='open' AND r.due BETWEEN ? AND ?`, today.AddDate(0, 0, -60).Unix(), today.AddDate(0, 0, 60).Unix())
	if err != nil {
		return
	}
	type row struct {
		id, owner, typeID, contactID int64
		phone, name, title, amount   string
		due                          int64
		reminded                     string
	}
	var list []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.owner, &r.typeID, &r.contactID, &r.phone, &r.name, &r.title, &r.amount, &r.due, &r.reminded) == nil {
			list = append(list, r)
		}
	}
	rows.Close()
	types := map[int64]*RenewalType{}
	biz := map[int64]map[string]string{}
	for _, r := range list {
		t, ok := types[r.typeID]
		if !ok {
			t, _ = scanRT(m.db.QueryRowContext(ctx, `SELECT `+rtCols+` FROM renewal_types WHERE id=?`, r.typeID))
			types[r.typeID] = t
		}
		if t == nil {
			continue
		}
		due := dayStart(time.Unix(r.due, 0))
		days := int(due.Sub(today).Hours() / 24) // >0 before due, <0 after
		key := ""
		for _, b := range t.RemindBefore {
			if b == days {
				key = fmt.Sprintf("b%d", b)
			}
		}
		for _, a := range t.RemindAfter {
			if -a == days {
				key = fmt.Sprintf("a%d", a)
			}
		}
		if key == "" {
			continue
		}
		done := fromJSON[[]string](r.reminded)
		already := false
		for _, d := range done {
			if d == key {
				already = true
			}
		}
		if already {
			continue
		}
		done = append(done, key)
		if _, err := m.db.ExecContext(ctx, `UPDATE renewals SET reminded=? WHERE id=? AND reminded=?`, jsonText(done), r.id, r.reminded); err != nil {
			continue
		}
		if _, ok := biz[r.owner]; !ok {
			biz[r.owner] = m.businessVars(ctx, r.owner)
		}
		vars := map[string]string{"name": firstWord(r.name, "जी"), "phone": r.phone, "title": r.title, "amount": r.amount, "due": due.Format("02 Jan 2006")}
		for k, v := range biz[r.owner] {
			vars[k] = v
		}
		if strings.TrimSpace(t.Message) != "" && r.phone != "" {
			if pool := m.ownerPool(ctx, r.owner, t.Session); len(pool) > 0 {
				_, _ = m.enqueue(ctx, r.owner, pool, 0, fmt.Sprintf("renewal%d", t.ID), []SendRequest{{To: r.phone, Text: t.Message, Vars: vars}}, now.Unix(), 1, "smart")
			}
		}
		if t.StaffTask && strings.HasPrefix(key, "b") {
			title := fmt.Sprintf("%s %s — %s (%s)", t.Icon, t.Label, firstWord(r.name, r.phone), vars["due"])
			_, _ = m.db.ExecContext(ctx, `INSERT INTO tasks(owner_id, title, due, repeat, at_time, done_at, assignee_id, contact_id, phone, source, note, created_at)
				VALUES(?,?,?,'','',0,0,?,?,'renewal',?,?)`, r.owner, truncate(title, 200), now.Unix(), r.contactID, r.phone, r.title, now.Unix())
		}
	}
}

// ---------- API: sequences ----------

func (a *API) listSequences(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT `+seqCols+`, (SELECT COUNT(*) FROM sequence_runs x WHERE x.sequence_id=s.id AND x.status='active') FROM sequences s WHERE owner_id=? ORDER BY id`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []*Sequence{}
	for rows.Next() {
		var s Sequence
		var steps string
		var bd, sr, act int
		if rows.Scan(&s.ID, &s.Key, &s.Name, &s.Trigger, &s.TriggerValue, &bd, &sr, &steps, &s.Session, &act, &s.Source, &s.CreatedAt, &s.UpdatedAt, &s.Runs) == nil {
			s.BusinessDays, s.StopOnReply, s.Active = bd == 1, sr == 1, act == 1
			s.Steps = orEmptySteps(fromJSON[[]KitStep](steps))
			out = append(out, &s)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sequences": out})
}

func orEmptySteps(s []KitStep) []KitStep {
	if s == nil {
		return []KitStep{}
	}
	return s
}

func (a *API) saveSequence(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	s := &Sequence{Active: true, Trigger: "manual", Source: "manual"}
	if id := r.PathValue("id"); id != "" {
		n, _ := strconv.ParseInt(id, 10, 64)
		old, err := a.m.sequence(ctx, p.UserID, n)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("sequence not found"))
			return
		}
		s = old
	}
	if err := decode(r, s); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("give the sequence a name"))
		return
	}
	switch s.Trigger {
	case "lead_new", "stage", "tag", "manual", "form":
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("trigger must be lead_new, stage, tag, form or manual"))
		return
	}
	if len(s.Steps) == 0 || len(s.Steps) > 30 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("a sequence needs 1 to 30 steps"))
		return
	}
	for i := range s.Steps {
		st := &s.Steps[i]
		st.Day = max(st.Day, 0)
		switch st.Action {
		case "auto_whatsapp", "whatsapp", "call", "task":
		default:
			st.Action = "task"
		}
		if st.Action == "auto_whatsapp" && strings.TrimSpace(st.Message) == "" {
			writeErr(w, http.StatusBadRequest, "bad_request", fmt.Errorf("step %d: an automatic message needs text", i+1))
			return
		}
		st.Title = truncate(strings.TrimSpace(st.Title), 200)
		st.Message = truncate(st.Message, 4000)
	}
	if s.Session != "" {
		if x, err := a.m.get(s.Session); err != nil || (!p.Admin() && x.owner() != p.UserID) {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("number not found"))
			return
		}
	}
	now := time.Now().Unix()
	if s.ID > 0 {
		_, err := a.m.db.ExecContext(ctx, `UPDATE sequences SET key=?, name=?, trigger_type=?, trigger_value=?, business_days=?, stop_on_reply=?, steps=?, session=?, active=?, updated_at=? WHERE id=? AND owner_id=?`,
			s.Key, s.Name, s.Trigger, s.TriggerValue, b2i(s.BusinessDays), b2i(s.StopOnReply), jsonText(s.Steps), s.Session, b2i(s.Active), now, s.ID, p.UserID)
		if err != nil {
			a.fail(w, err)
			return
		}
	} else {
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO sequences(owner_id, key, name, trigger_type, trigger_value, business_days, stop_on_reply, steps, session, active, source, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,'manual',?,?)`, p.UserID, s.Key, s.Name, s.Trigger, s.TriggerValue, b2i(s.BusinessDays), b2i(s.StopOnReply), jsonText(s.Steps), s.Session, b2i(s.Active), now, now)
		if err != nil {
			a.fail(w, err)
			return
		}
		s.ID, _ = res.LastInsertId()
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sequence": s})
}

func (a *API) deleteSequence(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM sequences WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("sequence not found"))
		return
	}
	_, _ = a.m.db.ExecContext(r.Context(), `UPDATE sequence_runs SET status='stopped' WHERE sequence_id=? AND status='active'`, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST /api/sequences/{id}/enroll {contacts:[ids], phones:["91…"], list_id}
func (a *API) enrollSequence(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	seq, err := a.m.sequence(ctx, p.UserID, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("sequence not found"))
		return
	}
	var b struct {
		Contacts []int64  `json:"contacts"`
		Phones   []string `json:"phones"`
		ListID   int64    `json:"list_id"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	type person struct {
		phone, name string
		id          int64
	}
	var people []person
	for _, cid := range b.Contacts {
		c, err := a.getContactByID(ctx, p.UserID, strconv.FormatInt(cid, 10))
		if err == nil {
			people = append(people, person{c.Phone, c.Name, c.ID})
		}
	}
	for _, ph := range b.Phones {
		if n := normPhone(ph, a.cfg.DefaultCountryCode); n != "" {
			people = append(people, person{phone: n})
		}
	}
	if b.ListID > 0 {
		if l, err := a.m.loadList(ctx, p.UserID, b.ListID); err == nil {
			for _, it := range l.Items {
				switch itemKind(it.To) {
				case "phones":
					people = append(people, person{phone: it.To, name: it.Name})
				case "contacts":
					if c, err := a.getContactByID(ctx, p.UserID, strings.TrimPrefix(it.To, "contact:")); err == nil {
						people = append(people, person{c.Phone, c.Name, c.ID})
					}
				}
			}
		}
	}
	if len(people) > 5000 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("at most 5,000 people at a time"))
		return
	}
	started := 0
	for _, x := range people {
		if x.name == "" && x.id == 0 {
			if c, err := a.m.contactByPhone(ctx, p.UserID, x.phone); err == nil {
				x.name, x.id = c.Name, c.ID
			}
		}
		if ok, _ := a.m.enroll(ctx, p.UserID, seq, x.phone, x.name, x.id, nil); ok {
			started++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": started, "people": len(people)})
}

func (m *Manager) contactByPhone(ctx context.Context, owner int64, phone string) (*Contact, error) {
	var c Contact
	err := m.db.QueryRowContext(ctx, `SELECT id, name FROM contacts WHERE owner_id=? AND phone=?`, owner, phone).Scan(&c.ID, &c.Name)
	c.Phone = phone
	return &c, err
}

// GET /api/sequences/runs?status=active&sequence=ID
func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()
	where, args := `r.owner_id=?`, []any{p.UserID}
	if st := q.Get("status"); st != "" && st != "all" {
		where += ` AND r.status=?`
		args = append(args, st)
	}
	if sid := q.Get("sequence"); sid != "" {
		where += ` AND r.sequence_id=?`
		args = append(args, sid)
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT r.id, r.sequence_id, s.name, r.contact_id, r.phone, r.name, r.step, r.next_at, r.status, r.started_at, r.updated_at,
		(SELECT COUNT(*) FROM json_each(s.steps)) FROM sequence_runs r JOIN sequences s ON s.id=r.sequence_id WHERE `+where+` ORDER BY r.updated_at DESC LIMIT 500`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, seqID, cid, nextAt, started, updated int64
		var seqName, phone, name, status string
		var step, total int
		if rows.Scan(&id, &seqID, &seqName, &cid, &phone, &name, &step, &nextAt, &status, &started, &updated, &total) == nil {
			out = append(out, map[string]any{"id": id, "sequence_id": seqID, "sequence": seqName, "contact_id": cid, "phone": phone, "name": name,
				"step": step, "steps": total, "next_at": nextAt, "status": status, "started_at": started, "updated_at": updated})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runs": out})
}

// DELETE /api/sequences/runs/{id} — stop a run.
func (a *API) stopRun(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `UPDATE sequence_runs SET status='stopped', updated_at=? WHERE id=? AND owner_id=? AND status='active'`, time.Now().Unix(), r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("active run not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- API: renewals ----------

func (a *API) listRenewalTypes(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT `+rtCols+`, (SELECT COUNT(*) FROM renewals x WHERE x.type_id=t.id AND x.status='open') FROM renewal_types t WHERE owner_id=? ORDER BY id`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []*RenewalType{}
	for rows.Next() {
		var t RenewalType
		var before, after, fields string
		var st int
		if rows.Scan(&t.ID, &t.Key, &t.Label, &t.Icon, &t.Frequency, &before, &after, &st, &fields, &t.Message, &t.Session, &t.Source, &t.Open) == nil {
			t.StaffTask = st == 1
			t.RemindBefore, t.RemindAfter = orEmptyInts(fromJSON[[]int](before)), orEmptyInts(fromJSON[[]int](after))
			t.Fields = fromJSON[[]KitField](fields)
			if t.Fields == nil {
				t.Fields = []KitField{}
			}
			out = append(out, &t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "types": out})
}

func (a *API) saveRenewalType(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	t := &RenewalType{Frequency: "yearly", RemindBefore: []int{7, 1}, RemindAfter: []int{}, Fields: []KitField{}, Icon: "🔔"}
	if id := r.PathValue("id"); id != "" {
		old, err := scanRT(a.m.db.QueryRowContext(ctx, `SELECT `+rtCols+` FROM renewal_types WHERE id=? AND owner_id=?`, id, p.UserID))
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("renewal type not found"))
			return
		}
		t = old
	}
	if err := decode(r, t); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	t.Label = strings.TrimSpace(t.Label)
	if t.Label == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("give it a name"))
		return
	}
	switch t.Frequency {
	case "yearly", "half_yearly", "quarterly", "monthly", "weekly", "once":
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("frequency must be yearly, half_yearly, quarterly, monthly, weekly or once"))
		return
	}
	if t.Key == "" {
		t.Key = strings.ToLower(strings.ReplaceAll(stripEmoji(t.Label), " ", "_"))
	}
	if t.ID > 0 {
		_, err := a.m.db.ExecContext(ctx, `UPDATE renewal_types SET key=?, label=?, icon=?, frequency=?, remind_before=?, remind_after=?, staff_task=?, fields=?, message=?, session=? WHERE id=? AND owner_id=?`,
			t.Key, truncate(t.Label, 120), truncate(t.Icon, 8), t.Frequency, jsonText(orEmptyInts(t.RemindBefore)), jsonText(orEmptyInts(t.RemindAfter)), b2i(t.StaffTask), jsonText(t.Fields), truncate(t.Message, 4000), t.Session, t.ID, p.UserID)
		if err != nil {
			a.fail(w, err)
			return
		}
	} else {
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO renewal_types(owner_id, key, label, icon, frequency, remind_before, remind_after, staff_task, fields, message, session, source, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,'manual',?)`, p.UserID, t.Key, truncate(t.Label, 120), truncate(t.Icon, 8), t.Frequency, jsonText(orEmptyInts(t.RemindBefore)), jsonText(orEmptyInts(t.RemindAfter)),
			b2i(t.StaffTask), jsonText(t.Fields), truncate(t.Message, 4000), t.Session, time.Now().Unix())
		if err != nil {
			a.fail(w, err)
			return
		}
		t.ID, _ = res.LastInsertId()
		t.Source = "manual"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "type": t})
}

func (a *API) deleteRenewalType(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var open int
	_ = a.m.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM renewals WHERE type_id=? AND owner_id=? AND status='open'`, r.PathValue("id"), p.UserID).Scan(&open)
	if open > 0 {
		writeErr(w, http.StatusConflict, "in_use", fmt.Errorf("%d open renewals use this type — close or move them first", open))
		return
	}
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM renewal_types WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("renewal type not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

const renCols = `r.id, r.type_id, COALESCE(t.label,''), COALESCE(t.icon,''), r.contact_id, r.phone, r.name, r.title, r.amount, r.due, r.fields, r.status, r.reminded, r.note, r.created_at`

func scanRenewal(row interface{ Scan(...any) error }) (*Renewal, error) {
	var x Renewal
	var fields, reminded string
	if err := row.Scan(&x.ID, &x.TypeID, &x.Type, &x.Icon, &x.ContactID, &x.Phone, &x.Name, &x.Title, &x.Amount, &x.Due, &fields, &x.Status, &reminded, &x.Note, &x.CreatedAt); err != nil {
		return nil, err
	}
	x.Fields = orEmpty(fromJSON[map[string]string](fields))
	x.Reminded = fromJSON[[]string](reminded)
	if x.Reminded == nil {
		x.Reminded = []string{}
	}
	return &x, nil
}

// GET /api/renewals?status=open|done|lost|all&type=ID&q=text&due=week|month|overdue
func (a *API) listRenewals(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()
	where, args := `r.owner_id=?`, []any{p.UserID}
	st := q.Get("status")
	if st == "" {
		st = "open"
	}
	if st != "all" {
		where += ` AND r.status=?`
		args = append(args, st)
	}
	if t := q.Get("type"); t != "" {
		where += ` AND r.type_id=?`
		args = append(args, t)
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		where += ` AND (r.name LIKE ? OR r.phone LIKE ? OR r.title LIKE ?)`
		like := "%" + s + "%"
		args = append(args, like, like, like)
	}
	today := dayStart(time.Now())
	switch q.Get("due") {
	case "overdue":
		where += ` AND r.due<?`
		args = append(args, today.Unix())
	case "week":
		where += ` AND r.due BETWEEN ? AND ?`
		args = append(args, today.Unix(), today.AddDate(0, 0, 7).Unix())
	case "month":
		where += ` AND r.due BETWEEN ? AND ?`
		args = append(args, today.Unix(), today.AddDate(0, 1, 0).Unix())
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT `+renCols+` FROM renewals r LEFT JOIN renewal_types t ON t.id=r.type_id WHERE `+where+` ORDER BY r.due LIMIT 1000`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []*Renewal{}
	for rows.Next() {
		if x, err := scanRenewal(rows); err == nil {
			out = append(out, x)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "renewals": out, "today": today.Unix()})
}

func (a *API) saveRenewal(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	x := &Renewal{Status: "open", Fields: map[string]string{}}
	if id := r.PathValue("id"); id != "" {
		old, err := scanRenewal(a.m.db.QueryRowContext(ctx, `SELECT `+renCols+` FROM renewals r LEFT JOIN renewal_types t ON t.id=r.type_id WHERE r.id=? AND r.owner_id=?`, id, p.UserID))
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("renewal not found"))
			return
		}
		x = old
	}
	wasOpen := x.Status == "open"
	if err := decode(r, x); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if x.TypeID == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("choose a renewal type"))
		return
	}
	t, err := scanRT(a.m.db.QueryRowContext(ctx, `SELECT `+rtCols+` FROM renewal_types WHERE id=? AND owner_id=?`, x.TypeID, p.UserID))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("renewal type not found"))
		return
	}
	if x.ContactID > 0 {
		if c, err := a.getContactByID(ctx, p.UserID, strconv.FormatInt(x.ContactID, 10)); err == nil {
			x.Phone = c.Phone
			if x.Name == "" {
				x.Name = c.Name
			}
		}
	}
	x.Phone = normPhone(x.Phone, a.cfg.DefaultCountryCode)
	if x.Phone == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("a phone number is needed"))
		return
	}
	if x.Due <= 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("a due date is needed"))
		return
	}
	x.Due = dayStart(time.Unix(x.Due, 0)).Unix()
	switch x.Status {
	case "open", "done", "lost":
	default:
		x.Status = "open"
	}
	if x.Title == "" {
		x.Title = t.Label
	}
	now := time.Now().Unix()
	if x.ID > 0 {
		_, err = a.m.db.ExecContext(ctx, `UPDATE renewals SET type_id=?, contact_id=?, phone=?, name=?, title=?, amount=?, due=?, fields=?, status=?, note=? WHERE id=? AND owner_id=?`,
			x.TypeID, x.ContactID, x.Phone, truncate(x.Name, 120), truncate(x.Title, 200), truncate(x.Amount, 40), x.Due, jsonText(x.Fields), x.Status, truncate(x.Note, 2000), x.ID, p.UserID)
	} else {
		var res sql.Result
		res, err = a.m.db.ExecContext(ctx, `INSERT INTO renewals(owner_id, type_id, contact_id, phone, name, title, amount, due, fields, status, reminded, note, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,'[]',?,?)`, p.UserID, x.TypeID, x.ContactID, x.Phone, truncate(x.Name, 120), truncate(x.Title, 200), truncate(x.Amount, 40), x.Due, jsonText(x.Fields), x.Status, truncate(x.Note, 2000), now)
		if err == nil {
			x.ID, _ = res.LastInsertId()
			x.CreatedAt = now
		}
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	x.Type, x.Icon = t.Label, t.Icon
	// a recurring renewal marked done comes back for the next period
	var next *Renewal
	if wasOpen && x.Status == "done" {
		if d, ok := nextDue(time.Unix(x.Due, 0), t.Frequency); ok {
			res, err := a.m.db.ExecContext(ctx, `INSERT INTO renewals(owner_id, type_id, contact_id, phone, name, title, amount, due, fields, status, reminded, note, created_at)
				VALUES(?,?,?,?,?,?,?,?,?,'open','[]','',?)`, p.UserID, x.TypeID, x.ContactID, x.Phone, x.Name, x.Title, x.Amount, d.Unix(), jsonText(x.Fields), now)
			if err == nil {
				id, _ := res.LastInsertId()
				next = &Renewal{ID: id, Due: d.Unix(), Title: x.Title}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "renewal": x, "next": next})
}

func (a *API) deleteRenewal(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM renewals WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("renewal not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- API: tasks ----------

type Task struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Due        int64  `json:"due"`
	Repeat     string `json:"repeat"`
	AtTime     string `json:"at_time"`
	DoneAt     int64  `json:"done_at"`
	AssigneeID int64  `json:"assignee_id"`
	ContactID  int64  `json:"contact_id"`
	Phone      string `json:"phone"`
	Source     string `json:"source"`
	Note       string `json:"note"`
	CreatedAt  int64  `json:"created_at"`
}

const taskCols = `id, title, due, repeat, at_time, done_at, assignee_id, contact_id, phone, source, note, created_at`

// GET /api/tasks?view=today|upcoming|done|all
func (a *API) listTasks(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	where, args := `owner_id=?`, []any{p.UserID}
	today := dayStart(time.Now())
	switch r.URL.Query().Get("view") {
	case "done":
		where += ` AND done_at>0`
	case "upcoming":
		where += ` AND done_at=0 AND due>=?`
		args = append(args, today.AddDate(0, 0, 1).Unix())
	case "all":
	default: // today: due today or overdue
		where += ` AND done_at=0 AND due<?`
		args = append(args, today.AddDate(0, 0, 1).Unix())
	}
	if p.ActorID != p.UserID && !p.can("automation") { // agents see their own tasks and unassigned ones
		where += ` AND (assignee_id=0 OR assignee_id=?)`
		args = append(args, p.ActorID)
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT `+taskCols+` FROM tasks WHERE `+where+` ORDER BY done_at>0, due LIMIT 500`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		if rows.Scan(&t.ID, &t.Title, &t.Due, &t.Repeat, &t.AtTime, &t.DoneAt, &t.AssigneeID, &t.ContactID, &t.Phone, &t.Source, &t.Note, &t.CreatedAt) == nil {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tasks": out, "today": today.Unix()})
}

func (a *API) saveTask(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	t := Task{Due: time.Now().Unix()}
	if id := r.PathValue("id"); id != "" {
		err := a.m.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=? AND owner_id=?`, id, p.UserID).
			Scan(&t.ID, &t.Title, &t.Due, &t.Repeat, &t.AtTime, &t.DoneAt, &t.AssigneeID, &t.ContactID, &t.Phone, &t.Source, &t.Note, &t.CreatedAt)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("task not found"))
			return
		}
	}
	wasDone := t.DoneAt > 0
	var in struct {
		Title      *string `json:"title"`
		Due        *int64  `json:"due"`
		Repeat     *string `json:"repeat"`
		AtTime     *string `json:"at_time"`
		Done       *bool   `json:"done"`
		AssigneeID *int64  `json:"assignee_id"`
		Phone      *string `json:"phone"`
		Note       *string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if in.Title != nil {
		t.Title = truncate(strings.TrimSpace(*in.Title), 200)
	}
	if in.Due != nil {
		t.Due = *in.Due
	}
	if in.Repeat != nil {
		switch *in.Repeat {
		case "", "daily", "weekly", "monthly":
			t.Repeat = *in.Repeat
		}
	}
	if in.AtTime != nil {
		t.AtTime = truncate(*in.AtTime, 5)
	}
	if in.AssigneeID != nil {
		t.AssigneeID = *in.AssigneeID
	}
	if in.Phone != nil {
		t.Phone = normPhone(*in.Phone, a.cfg.DefaultCountryCode)
	}
	if in.Note != nil {
		t.Note = truncate(*in.Note, 2000)
	}
	if t.Title == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("what is the task?"))
		return
	}
	now := time.Now()
	if in.Done != nil {
		switch {
		case *in.Done && t.Repeat != "" && !wasDone:
			// a repeating task rolls to its next occurrence instead of closing
			t.Due = nextTaskDue(t.Repeat, t.AtTime, max2(time.Unix(t.Due, 0), now)).Unix()
			_, _ = a.m.db.ExecContext(ctx, `INSERT INTO task_log(owner_id, task_id, title, at) VALUES(?,?,?,?)`, p.UserID, t.ID, t.Title, now.Unix())
		case *in.Done:
			t.DoneAt = now.Unix()
		default:
			t.DoneAt = 0
		}
	}
	if t.ID > 0 {
		_, err := a.m.db.ExecContext(ctx, `UPDATE tasks SET title=?, due=?, repeat=?, at_time=?, done_at=?, assignee_id=?, phone=?, note=? WHERE id=? AND owner_id=?`,
			t.Title, t.Due, t.Repeat, t.AtTime, t.DoneAt, t.AssigneeID, t.Phone, t.Note, t.ID, p.UserID)
		if err != nil {
			a.fail(w, err)
			return
		}
	} else {
		t.Source, t.CreatedAt = "manual", now.Unix()
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO tasks(owner_id, title, due, repeat, at_time, done_at, assignee_id, contact_id, phone, source, note, created_at)
			VALUES(?,?,?,?,?,?,?,0,?,'manual',?,?)`, p.UserID, t.Title, t.Due, t.Repeat, t.AtTime, t.DoneAt, t.AssigneeID, t.Phone, t.Note, t.CreatedAt)
		if err != nil {
			a.fail(w, err)
			return
		}
		t.ID, _ = res.LastInsertId()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": t})
}

func max2(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (a *API) deleteTask(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM tasks WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("task not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
