package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ======================= Business Kit =======================
//
// A kit is a ready-made setup for one kind of business (insurance agent,
// clinic, school, news portal, …): the questions customers usually ask and
// the answers (auto-replies), follow-up sequences, renewal reminders, daily
// checklists, lead fields and public enquiry forms. The owner picks a kit
// (and sub-categories) once; "apply" writes everything into their workspace
// where every piece can then be edited like anything made by hand.
//
// The data comes from BizCRT's kits (kits/kits.json), ported as-is plus a
// news / media kit.

//go:embed kits/kits.json
var kitsFS embed.FS

type KitField struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text, number, date, select, yesno, radio, textarea, phone
	Options  []string `json:"options,omitempty"`
	Cats     []string `json:"cats,omitempty"` // only for these sub-categories
	Required bool     `json:"required,omitempty"`
}

type KitForm struct {
	Key         string     `json:"key"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Fields      []KitField `json:"fields"`
}

type KitStep struct {
	Day     int    `json:"day"`
	Action  string `json:"action"` // auto_whatsapp (sent by the gateway), whatsapp / call / task (a task for staff)
	Title   string `json:"title"`
	Message string `json:"message,omitempty"`
}

type KitPlan struct {
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Trigger      string    `json:"trigger"` // lead_new, stage, tag, manual
	TriggerValue string    `json:"trigger_value,omitempty"`
	BusinessDays bool      `json:"business_days"`
	StopOnReply  bool      `json:"stop_on_reply"`
	Steps        []KitStep `json:"steps"`
}

type KitRenewal struct {
	Key          string     `json:"key"`
	Label        string     `json:"label"`
	Icon         string     `json:"icon"`
	Frequency    string     `json:"frequency"` // yearly, half_yearly, quarterly, monthly, weekly, once
	RemindBefore []int      `json:"remind_before"`
	RemindAfter  []int      `json:"remind_after"`
	StaffTask    bool       `json:"staff_task"`
	Fields       []KitField `json:"fields"`
	Message      string     `json:"message"`
	Category     string     `json:"category,omitempty"`
}

type KitChecklist struct {
	Title  string `json:"title"`
	Repeat string `json:"repeat"` // daily, weekly, monthly
	Time   string `json:"time"`   // "10:00"
}

type KitSub struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Icon  string `json:"icon"`
	Hint  string `json:"hint"`
}

type Kit struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Icon       string            `json:"icon"`
	Group      string            `json:"group"`
	Tagline    string            `json:"tagline"`
	Stages     map[string]string `json:"stages"`
	LeadFields []KitField        `json:"lead_fields"`
	Forms      []KitForm         `json:"forms"`
	Plans      []KitPlan         `json:"plans"`
	Renewals   []KitRenewal      `json:"renewals"`
	Checklists []KitChecklist    `json:"checklists"`
	Bot        [][2]string       `json:"bot"` // [question/button, answer]
	Modules    []string          `json:"modules"`
	Tips       []string          `json:"tips"`
	KPIs       []string          `json:"kpis"`
	Subs       []KitSub          `json:"subs"`
}

type kitCatalog struct {
	Groups map[string]string `json:"groups"`
	Kits   []*Kit            `json:"kits"`
}

var (
	kitsOnce sync.Once
	kitsData *kitCatalog
	kitsByID map[string]*Kit
)

func kits() *kitCatalog {
	kitsOnce.Do(func() {
		raw, err := kitsFS.ReadFile("kits/kits.json")
		if err != nil {
			panic("kits.json missing: " + err.Error())
		}
		var c kitCatalog
		if err := json.Unmarshal(raw, &c); err != nil {
			panic("kits.json: " + err.Error())
		}
		kitsByID = map[string]*Kit{}
		for _, k := range c.Kits {
			kitsByID[k.ID] = k
		}
		kitsData = &c
	})
	return kitsData
}

func kitByID(id string) *Kit {
	kits()
	return kitsByID[id]
}

// forSubs keeps the fields that apply to the chosen sub-categories (fields
// without "cats" always apply; with no sub-categories chosen all apply).
func fieldsForSubs(fields []KitField, subs []string) []KitField {
	if len(subs) == 0 {
		return fields
	}
	want := map[string]bool{}
	for _, s := range subs {
		want[s] = true
	}
	out := make([]KitField, 0, len(fields))
	for _, f := range fields {
		if len(f.Cats) == 0 {
			out = append(out, f)
			continue
		}
		for _, c := range f.Cats {
			if want[c] {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// ---------- business profile ----------

type Business struct {
	Kit       string            `json:"kit"`
	Subs      []string          `json:"subs"`
	Name      string            `json:"name"`
	Phone     string            `json:"phone"` // WhatsApp number customers should write to (for form thank-you pages)
	City      string            `json:"city"`
	Code      string            `json:"code"` // public code used in form links
	Fields    []KitField        `json:"fields"`
	Forms     []KitForm         `json:"forms"`
	Stages    map[string]string `json:"stages"`
	AppliedAt int64             `json:"applied_at"`
	UpdatedAt int64             `json:"updated_at"`
}

func (m *Manager) business(ctx context.Context, owner int64) (*Business, error) {
	var b Business
	var subs, fields, forms, stages string
	err := m.db.QueryRowContext(ctx, `SELECT kit, subs, name, phone, city, code, fields, forms, stages, applied_at, updated_at FROM business WHERE owner_id=?`, owner).
		Scan(&b.Kit, &subs, &b.Name, &b.Phone, &b.City, &b.Code, &fields, &forms, &stages, &b.AppliedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &Business{Subs: []string{}, Fields: []KitField{}, Forms: []KitForm{}, Stages: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	b.Subs = fromJSON[[]string](subs)
	b.Fields = fromJSON[[]KitField](fields)
	b.Forms = fromJSON[[]KitForm](forms)
	b.Stages = fromJSON[map[string]string](stages)
	for _, p := range []any{&b.Subs, &b.Fields, &b.Forms} {
		switch v := p.(type) {
		case *[]string:
			if *v == nil {
				*v = []string{}
			}
		case *[]KitField:
			if *v == nil {
				*v = []KitField{}
			}
		case *[]KitForm:
			if *v == nil {
				*v = []KitForm{}
			}
		}
	}
	if b.Stages == nil {
		b.Stages = map[string]string{}
	}
	return &b, nil
}

func (m *Manager) saveBusiness(ctx context.Context, owner int64, b *Business) error {
	if b.Code == "" {
		raw := make([]byte, 5)
		_, _ = rand.Read(raw)
		b.Code = hex.EncodeToString(raw)
	}
	b.UpdatedAt = time.Now().Unix()
	_, err := m.db.ExecContext(ctx, `INSERT INTO business(owner_id, kit, subs, name, phone, city, code, fields, forms, stages, applied_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_id) DO UPDATE SET kit=excluded.kit, subs=excluded.subs, name=excluded.name, phone=excluded.phone,
		city=excluded.city, fields=excluded.fields, forms=excluded.forms, stages=excluded.stages, applied_at=excluded.applied_at, updated_at=excluded.updated_at`,
		owner, b.Kit, jsonText(b.Subs), truncate(b.Name, 120), b.Phone, truncate(b.City, 80), b.Code, jsonText(b.Fields), jsonText(b.Forms), jsonText(b.Stages), b.AppliedAt, b.UpdatedAt)
	return err
}

// businessVars are the placeholders every automatic message can use.
func (m *Manager) businessVars(ctx context.Context, owner int64) map[string]string {
	v := map[string]string{"business": "", "business_phone": "", "city": ""}
	if b, err := m.business(ctx, owner); err == nil {
		v["business"], v["business_phone"], v["city"] = b.Name, b.Phone, b.City
	}
	if v["business"] == "" {
		var name string
		_ = m.db.QueryRowContext(ctx, `SELECT name FROM users WHERE id=?`, owner).Scan(&name)
		v["business"] = name
	}
	return v
}

// ---------- apply ----------

type applyResult struct {
	AutoReplies int `json:"autoreplies"`
	Sequences   int `json:"sequences"`
	Renewals    int `json:"renewal_types"`
	Tasks       int `json:"tasks"`
}

// applyKit writes the kit into the workspace. Everything it made earlier
// (source='kit') is replaced; things the owner made by hand are kept.
func (m *Manager) applyKit(ctx context.Context, owner int64, k *Kit, b *Business, session string) (*applyResult, error) {
	now := time.Now().Unix()
	res := &applyResult{}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Sequences and renewal types the kit made before are updated in place
	// (by key) so people in them and open renewals carry on; the rest goes.
	keys := func(items []string) string {
		q := make([]string, len(items))
		for i, k := range items {
			q[i] = "'" + strings.ReplaceAll(k, "'", "''") + "'"
		}
		if len(q) == 0 {
			return "''"
		}
		return strings.Join(q, ",")
	}
	var planKeys, renKeys []string
	for _, p := range k.Plans {
		planKeys = append(planKeys, p.Key)
	}
	for _, r := range k.Renewals {
		renKeys = append(renKeys, r.Key)
	}
	for _, q := range []string{`DELETE FROM autoreplies WHERE owner_id=? AND source='kit'`,
		`DELETE FROM sequences WHERE owner_id=? AND source='kit' AND key NOT IN (` + keys(planKeys) + `) AND id NOT IN (SELECT sequence_id FROM sequence_runs WHERE owner_id=? AND status='active')`,
		`DELETE FROM renewal_types WHERE owner_id=? AND source='kit' AND key NOT IN (` + keys(renKeys) + `) AND id NOT IN (SELECT type_id FROM renewals WHERE owner_id=? AND status='open')`,
		`DELETE FROM tasks WHERE owner_id=? AND source='kit' AND done_at=0`} {
		args := []any{owner}
		if strings.Count(q, "?") == 2 {
			args = append(args, owner)
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return nil, err
		}
	}
	// 1. auto-replies: a welcome menu with the kit's questions, one reply per
	//    question (menu choice or typed), and "menu" / "help" to show it again.
	if len(k.Bot) > 0 {
		opts := make([]MenuOpt, 0, len(k.Bot))
		for i, qa := range k.Bot {
			opts = append(opts, MenuOpt{ID: fmt.Sprintf("k%d", i+1), Text: qa[0]})
		}
		welcome := SendRequest{Text: "नमस्ते {{name}} जी 🙏 {{business}} में आपका स्वागत है। बताइए, कैसे मदद करें?",
			Menu: &MenuReq{Title: "", Footer: "नंबर लिखकर या option चुनकर जवाब दें", Options: opts}}
		menuRule := func(name, trigger string, keywords []string, reply SendRequest, cooldown, prio int) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO autoreplies(name, session, trigger_type, keywords, reply, ai, ai_prompt, only_dm, cooldown_min, hours, active, priority, owner_id, created_at, source)
				VALUES(?,?,?,?,?,0,'',1,?,'{}',1,?,?,?,'kit')`, truncate(name, 100), session, trigger, jsonText(keywords), jsonText(reply), cooldown, prio, owner, now)
			if err == nil {
				res.AutoReplies++
			}
			return err
		}
		if err := menuRule(k.Icon+" स्वागत menu", "welcome", []string{}, welcome, 720, 20); err != nil {
			return nil, err
		}
		if err := menuRule(k.Icon+" Menu फिर दिखाएँ", "keyword", []string{"menu", "help", "hi", "hello", "नमस्ते", "start", "0"}, welcome, 1, 15); err != nil {
			return nil, err
		}
		for i, qa := range k.Bot {
			id := fmt.Sprintf("k%d", i+1)
			words := append([]string{id}, qa[0], stripEmoji(qa[0]))
			if err := menuRule(stripEmoji(qa[0]), "menu", words, SendRequest{Text: qa[1]}, 0, 30); err != nil {
				return nil, err
			}
		}
	}
	// 2. follow-up sequences
	for _, p := range k.Plans {
		if len(p.Steps) == 0 {
			continue
		}
		r, err := tx.ExecContext(ctx, `UPDATE sequences SET name=?, trigger_type=?, trigger_value=?, business_days=?, stop_on_reply=?, steps=?, session=?, updated_at=? WHERE owner_id=? AND source='kit' AND key=?`,
			truncate(p.Name, 120), p.Trigger, p.TriggerValue, b2i(p.BusinessDays), b2i(p.StopOnReply), jsonText(p.Steps), session, now, owner, p.Key)
		if n, _ := rowsAffected(r, err); n == 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO sequences(owner_id, key, name, trigger_type, trigger_value, business_days, stop_on_reply, steps, session, active, source, created_at, updated_at)
				VALUES(?,?,?,?,?,?,?,?,?,1,'kit',?,?)`, owner, p.Key, truncate(p.Name, 120), p.Trigger, p.TriggerValue, b2i(p.BusinessDays), b2i(p.StopOnReply), jsonText(p.Steps), session, now, now)
		}
		if err != nil {
			return nil, err
		}
		res.Sequences++
	}
	// 3. renewal types
	for _, r := range k.Renewals {
		x, err := tx.ExecContext(ctx, `UPDATE renewal_types SET label=?, icon=?, frequency=?, remind_before=?, remind_after=?, staff_task=?, fields=?, message=?, session=? WHERE owner_id=? AND source='kit' AND key=?`,
			truncate(r.Label, 120), r.Icon, r.Frequency, jsonText(orEmptyInts(r.RemindBefore)), jsonText(orEmptyInts(r.RemindAfter)), b2i(r.StaffTask), jsonText(r.Fields), r.Message, session, owner, r.Key)
		if n, _ := rowsAffected(x, err); n == 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO renewal_types(owner_id, key, label, icon, frequency, remind_before, remind_after, staff_task, fields, message, session, source, created_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,'kit',?)`, owner, r.Key, truncate(r.Label, 120), r.Icon, r.Frequency, jsonText(orEmptyInts(r.RemindBefore)), jsonText(orEmptyInts(r.RemindAfter)),
				b2i(r.StaffTask), jsonText(r.Fields), r.Message, session, now)
		}
		if err != nil {
			return nil, err
		}
		res.Renewals++
	}
	// 4. checklists → repeating tasks, first due at the next occurrence
	for _, c := range k.Checklists {
		due := nextTaskDue(c.Repeat, c.Time, time.Now())
		_, err := tx.ExecContext(ctx, `INSERT INTO tasks(owner_id, title, due, repeat, at_time, done_at, assignee_id, contact_id, phone, source, created_at)
			VALUES(?,?,?,?,?,0,0,0,'','kit',?)`, owner, truncate(c.Title, 200), due.Unix(), c.Repeat, c.Time, now)
		if err != nil {
			return nil, err
		}
		res.Tasks++
	}
	// 5. profile
	b.Kit = k.ID
	b.Fields = fieldsForSubs(k.LeadFields, b.Subs)
	b.Forms = k.Forms
	b.Stages = k.Stages
	b.AppliedAt = now
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, m.saveBusiness(ctx, owner, b)
}

func orEmptyInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}

// stripEmoji drops leading symbols ("📋 Quote चाहिए" → "Quote चाहिए").
func stripEmoji(s string) string {
	s = strings.TrimSpace(s)
	for len(s) > 0 {
		r := []rune(s)[0]
		if r > 0x2000 && (r < 0x0900 || r > 0x097F) && !(r >= 'A' && r <= 'z') {
			s = strings.TrimSpace(strings.TrimPrefix(s, string(r)))
			continue
		}
		break
	}
	return s
}

// nextTaskDue is the next time a repeating checklist item comes up.
func nextTaskDue(repeat, at string, from time.Time) time.Time {
	hh, mm := 10, 0
	_, _ = fmt.Sscanf(at, "%d:%d", &hh, &mm)
	d := time.Date(from.Year(), from.Month(), from.Day(), hh, mm, 0, 0, from.Location())
	if !d.After(from) {
		d = d.AddDate(0, 0, 1)
	}
	switch repeat {
	case "weekly":
		for d.Weekday() != time.Monday {
			d = d.AddDate(0, 0, 1)
		}
	case "monthly":
		if d.Day() != 1 {
			d = time.Date(d.Year(), d.Month()+1, 1, hh, mm, 0, 0, d.Location())
		}
	}
	return d
}

// ---------- API ----------

// GET /api/kits — the catalogue (short form) with groups.
func (a *API) listKits(w http.ResponseWriter, r *http.Request) {
	c := kits()
	out := make([]map[string]any, 0, len(c.Kits))
	for _, k := range c.Kits {
		out = append(out, map[string]any{"id": k.ID, "name": k.Name, "icon": k.Icon, "group": k.Group, "tagline": k.Tagline, "subs": k.Subs,
			"counts": map[string]int{"bot": len(k.Bot), "plans": len(k.Plans), "renewals": len(k.Renewals), "checklists": len(k.Checklists), "forms": len(k.Forms), "fields": len(k.LeadFields)}})
	}
	groups := make([]map[string]string, 0, len(c.Groups))
	for id, label := range c.Groups {
		groups = append(groups, map[string]string{"id": id, "label": label})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i]["id"] < groups[j]["id"] })
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": groups, "kits": out})
}

// GET /api/kits/{id} — the full kit.
func (a *API) getKit(w http.ResponseWriter, r *http.Request) {
	k := kitByID(r.PathValue("id"))
	if k == nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("kit not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kit": k})
}

// GET /api/business — this workspace's profile and what the kit set up.
func (a *API) getBusiness(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	b, err := a.m.business(ctx, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := map[string]any{"ok": true, "business": b, "form_base": a.cfg.PublicURL + "/f/" + b.Code + "/"}
	if k := kitByID(b.Kit); k != nil {
		out["kit"] = map[string]any{"id": k.ID, "name": k.Name, "icon": k.Icon, "tagline": k.Tagline, "tips": k.Tips, "kpis": k.KPIs, "subs": k.Subs}
	}
	counts := map[string]int{}
	for col, q := range map[string]string{
		"autoreplies": `SELECT COUNT(*) FROM autoreplies WHERE owner_id=? AND active=1`,
		"sequences":   `SELECT COUNT(*) FROM sequences WHERE owner_id=? AND active=1`,
		"runs_active": `SELECT COUNT(*) FROM sequence_runs WHERE owner_id=? AND status='active'`,
		"renewals":    `SELECT COUNT(*) FROM renewals WHERE owner_id=? AND status='open'`,
		"tasks_today": `SELECT COUNT(*) FROM tasks WHERE owner_id=? AND done_at=0 AND due<?`,
	} {
		args := []any{p.UserID}
		if strings.Count(q, "?") == 2 {
			args = append(args, time.Now().Add(24*time.Hour).Unix())
		}
		var n int
		_ = a.m.db.QueryRowContext(ctx, q, args...).Scan(&n)
		counts[col] = n
	}
	out["counts"] = counts
	writeJSON(w, http.StatusOK, out)
}

// PATCH /api/business — name, phone, city, sub-categories, fields, forms.
func (a *API) saveBusiness(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	b, err := a.m.business(ctx, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	var in struct {
		Name   *string           `json:"name"`
		Phone  *string           `json:"phone"`
		City   *string           `json:"city"`
		Subs   []string          `json:"subs"`
		Fields []KitField        `json:"fields"`
		Forms  []KitForm         `json:"forms"`
		Stages map[string]string `json:"stages"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if in.Name != nil {
		b.Name = strings.TrimSpace(*in.Name)
	}
	if in.Phone != nil {
		b.Phone = normPhone(*in.Phone, a.cfg.DefaultCountryCode)
	}
	if in.City != nil {
		b.City = strings.TrimSpace(*in.City)
	}
	if in.Subs != nil {
		b.Subs = in.Subs
	}
	if in.Fields != nil {
		b.Fields = in.Fields
	}
	if in.Forms != nil {
		b.Forms = in.Forms
	}
	if in.Stages != nil {
		b.Stages = in.Stages
	}
	if err := a.m.saveBusiness(ctx, p.UserID, b); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "business": b})
}

// POST /api/business/apply {kit, subs, name, phone, city, session}
func (a *API) applyKit(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	var in struct {
		Kit     string   `json:"kit"`
		Subs    []string `json:"subs"`
		Name    string   `json:"name"`
		Phone   string   `json:"phone"`
		City    string   `json:"city"`
		Session string   `json:"session"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	k := kitByID(in.Kit)
	if k == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("choose a business type"))
		return
	}
	if err := a.lic.require(ctx, p.UserID, "autoreply"); err != nil {
		a.fail(w, err)
		return
	}
	if in.Session != "" {
		if s, err := a.m.get(in.Session); err != nil || (!p.Admin() && s.owner() != p.UserID) {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("number not found"))
			return
		}
	}
	b, err := a.m.business(ctx, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	if in.Name != "" {
		b.Name = strings.TrimSpace(in.Name)
	}
	if in.Phone != "" {
		b.Phone = normPhone(in.Phone, a.cfg.DefaultCountryCode)
	}
	if in.City != "" {
		b.City = strings.TrimSpace(in.City)
	}
	b.Subs = in.Subs
	if b.Subs == nil {
		b.Subs = []string{}
	}
	res, err := a.m.applyKit(ctx, p.UserID, k, b, in.Session)
	if err != nil {
		a.fail(w, err)
		return
	}
	audit(ctx, a.m.db, p.ActorID, clientIP(r), "kit.apply", k.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": res, "business": b})
}
