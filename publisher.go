package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ======================= Link Publisher & website registry =======================
//
// A post is one thing to share on WhatsApp: a link with title, summary and
// picture, going to a set of targets (numbers, lists, groups, channels or
// Status). Posts come from two places:
//   - the owner pastes URLs in bulk (metadata is fetched for each), or
//   - a registered website (Charameti, a WordPress plugin, a Cloudflare
//     Worker …) sends a signed webhook to POST /api/inbound.
// A post waits in the approval inbox until someone approves it (or the
// website's route is marked automatic), then goes through the normal
// outbox with the usual pacing. Routes on a website decide, by category,
// which number sends and where it goes.

type Target = ListItem // to: phone, contact:<id>, list:<id>, …@g.us, …@newsletter or "status"

type Route struct {
	Category string   `json:"category"` // "" or "*" = default
	Session  string   `json:"session"`  // "" = any linked number
	Targets  []Target `json:"targets"`
	Text     string   `json:"text"` // message template; blank = website default
	Auto     bool     `json:"auto"` // skip approval
}

type Website struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	Key       string  `json:"key"`
	Secret    string  `json:"secret,omitempty"` // shown once at creation / rotation
	Text      string  `json:"text"`             // default message template
	Routes    []Route `json:"routes"`
	Active    bool    `json:"active"`
	Received  int     `json:"received"`
	LastSeen  int64   `json:"last_seen"`
	CreatedAt int64   `json:"created_at"`
}

const defaultPostText = "*{{title}}*\n\n{{summary}}\n\n{{url}}"

type Post struct {
	ID        int64             `json:"id"`
	WebsiteID int64             `json:"website_id"`
	Website   string            `json:"website,omitempty"`
	ExtID     string            `json:"ext_id"`
	Event     string            `json:"event"`
	Title     string            `json:"title"`
	Summary   string            `json:"summary"`
	Text      string            `json:"text"` // final message template (editable before approval)
	URL       string            `json:"url"`
	Image     string            `json:"image"`
	Category  string            `json:"category"`
	Session   string            `json:"session"`
	Targets   []Target          `json:"targets"`
	Status    string            `json:"status"` // pending, queued, rejected, failed
	Source    string            `json:"source"` // website, manual
	Label     string            `json:"label"`
	Note      string            `json:"note"`
	Stats     map[string]int    `json:"stats,omitempty"`
	CreatedAt int64             `json:"created_at"`
	DecidedAt int64             `json:"decided_at"`
	DecidedBy int64             `json:"decided_by"`
	Vars      map[string]string `json:"-"`
}

const postCols = `p.id, p.website_id, COALESCE(w.name,''), p.ext_id, p.event, p.title, p.summary, p.text, p.url, p.image, p.category, p.session, p.targets, p.status, p.source, p.note, p.created_at, p.decided_at, p.decided_by`

func scanPost(row interface{ Scan(...any) error }) (*Post, error) {
	var p Post
	var targets string
	if err := row.Scan(&p.ID, &p.WebsiteID, &p.Website, &p.ExtID, &p.Event, &p.Title, &p.Summary, &p.Text, &p.URL, &p.Image, &p.Category, &p.Session, &targets, &p.Status, &p.Source, &p.Note, &p.CreatedAt, &p.DecidedAt, &p.DecidedBy); err != nil {
		return nil, err
	}
	p.Targets = cleanTargets(fromJSON[[]Target](targets), "")
	p.Label = fmt.Sprintf("post%d", p.ID)
	return &p, nil
}

func (p *Post) vars() map[string]string {
	host := ""
	if u, err := url.Parse(p.URL); err == nil {
		host = u.Host
	}
	return map[string]string{"title": p.Title, "summary": p.Summary, "url": p.URL, "image": p.Image, "category": p.Category, "site": host}
}

// cleanTargets accepts list:<id> and status on top of what lists accept.
func cleanTargets(items []Target, cc string) []Target {
	out := make([]Target, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		to := strings.TrimSpace(it.To)
		if to == "status" || strings.HasPrefix(to, "list:") {
			if !seen[to] {
				seen[to] = true
				out = append(out, Target{To: to, Name: truncate(it.Name, 80)})
			}
			continue
		}
		c := cleanItems([]ListItem{it}, cc)
		if len(c) == 1 && !seen[c[0].To] {
			seen[c[0].To] = true
			out = append(out, c[0])
		}
	}
	return out
}

// expandTargets turns targets into concrete recipients (lists and contacts resolved).
func (m *Manager) expandTargets(ctx context.Context, owner int64, targets []Target) []string {
	var out []string
	seen := map[string]bool{}
	add := func(to string) {
		if to != "" && !seen[to] {
			seen[to] = true
			out = append(out, to)
		}
	}
	contact := func(id string) {
		var phone string
		if m.db.QueryRowContext(ctx, `SELECT phone FROM contacts WHERE owner_id=? AND id=?`, owner, id).Scan(&phone) == nil {
			add(phone)
		}
	}
	for _, t := range targets {
		switch {
		case strings.HasPrefix(t.To, "list:"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(t.To, "list:"), 10, 64)
			if l, err := m.loadList(ctx, owner, id); err == nil {
				for _, it := range l.Items {
					if itemKind(it.To) == "contacts" {
						contact(strings.TrimPrefix(it.To, "contact:"))
					} else {
						add(it.To)
					}
				}
			}
		case strings.HasPrefix(t.To, "contact:"):
			contact(strings.TrimPrefix(t.To, "contact:"))
		default:
			add(t.To)
		}
	}
	return out
}

// publish queues an approved post. Status posts go as one message to
// "status"; everything else one per recipient with link preview.
func (m *Manager) publish(ctx context.Context, p *Post, actor int64) error {
	pool := m.ownerPool(ctx, m.postOwner(ctx, p.ID), p.Session)
	if len(pool) == 0 {
		return errors.New("no linked number can send right now")
	}
	owner := m.postOwner(ctx, p.ID)
	tos := m.expandTargets(ctx, owner, p.Targets)
	if len(tos) == 0 {
		return errors.New("choose at least one recipient, list, group, channel or Status")
	}
	text := p.Text
	if strings.TrimSpace(text) == "" {
		text = defaultPostText
	}
	vars := p.vars()
	msgs := make([]SendRequest, 0, len(tos))
	for _, to := range tos {
		msg := SendRequest{To: to, Text: text, Vars: vars}
		if p.Image != "" && to == "status" { // Status: picture with caption
			msg.Media = &Media{Type: "image", URL: p.Image}
		}
		msgs = append(msgs, msg)
	}
	now := time.Now().Unix()
	res, err := m.enqueue(ctx, owner, pool, 0, fmt.Sprintf("post%d", p.ID), msgs, now, 1, "smart")
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `UPDATE posts SET status='queued', decided_at=?, decided_by=?, note=? WHERE id=?`, now, actor,
		fmt.Sprintf("%d queued, %d opted out", res.Queued, res.Skipped), p.ID)
	m.dispatchOwner(owner, "post.queued", map[string]any{"id": p.ID, "title": p.Title, "url": p.URL, "queued": res.Queued})
	return err
}

func (m *Manager) postOwner(ctx context.Context, id int64) int64 {
	var o int64
	_ = m.db.QueryRowContext(ctx, `SELECT owner_id FROM posts WHERE id=?`, id).Scan(&o)
	return o
}

func (m *Manager) postStats(ctx context.Context, id int64) map[string]int {
	st := map[string]int{}
	rows, err := m.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM gw_outbox WHERE campaign=? GROUP BY status`, fmt.Sprintf("post%d", id))
	if err != nil {
		return st
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var n int
		if rows.Scan(&s, &n) == nil {
			st[s] = n
		}
	}
	return st
}

// ---------- websites ----------

func randomToken(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

const wsCols = `id, name, url, key, secret, text, routes, active, received, last_seen, created_at`

func scanWebsite(row interface{ Scan(...any) error }) (*Website, error) {
	var w Website
	var routes string
	var active int
	if err := row.Scan(&w.ID, &w.Name, &w.URL, &w.Key, &w.Secret, &w.Text, &routes, &active, &w.Received, &w.LastSeen, &w.CreatedAt); err != nil {
		return nil, err
	}
	w.Active = active == 1
	w.Routes = fromJSON[[]Route](routes)
	if w.Routes == nil {
		w.Routes = []Route{}
	}
	return &w, nil
}

func (a *API) listWebsites(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT `+wsCols+` FROM websites WHERE owner_id=? ORDER BY id`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []*Website{}
	for rows.Next() {
		if x, err := scanWebsite(rows); err == nil {
			x.Secret = ""
			out = append(out, x)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "websites": out, "inbound_url": a.cfg.PublicURL + "/api/inbound"})
}

func (a *API) saveWebsite(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	site := &Website{Active: true, Text: defaultPostText, Routes: []Route{}}
	isNew := true
	if id := r.PathValue("id"); id != "" {
		old, err := scanWebsite(a.m.db.QueryRowContext(ctx, `SELECT `+wsCols+` FROM websites WHERE id=? AND owner_id=?`, id, p.UserID))
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("website not found"))
			return
		}
		site, isNew = old, false
	}
	var in struct {
		Name   *string `json:"name"`
		URL    *string `json:"url"`
		Text   *string `json:"text"`
		Routes []Route `json:"routes"`
		Active *bool   `json:"active"`
		Rotate bool    `json:"rotate_secret"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if in.Name != nil {
		site.Name = truncate(strings.TrimSpace(*in.Name), 100)
	}
	if in.URL != nil {
		site.URL = truncate(strings.TrimSpace(*in.URL), 300)
	}
	if in.Text != nil {
		site.Text = truncate(*in.Text, 4000)
	}
	if in.Active != nil {
		site.Active = *in.Active
	}
	if in.Routes != nil {
		for i := range in.Routes {
			rt := &in.Routes[i]
			rt.Category = strings.ToLower(strings.TrimSpace(rt.Category))
			if rt.Category == "*" {
				rt.Category = ""
			}
			rt.Targets = cleanTargets(rt.Targets, a.cfg.DefaultCountryCode)
			rt.Text = truncate(rt.Text, 4000)
			if rt.Session != "" {
				if s, err := a.m.get(rt.Session); err != nil || (!p.Admin() && s.owner() != p.UserID) {
					writeErr(w, http.StatusBadRequest, "bad_request", fmt.Errorf("route %d: number not found", i+1))
					return
				}
			}
		}
		site.Routes = in.Routes
	}
	if site.Name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("give the website a name"))
		return
	}
	showSecret := isNew || in.Rotate
	if showSecret {
		site.Key = randomToken("wk_", 12)
		site.Secret = randomToken("ws_", 24)
	}
	now := time.Now().Unix()
	if isNew {
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO websites(owner_id, name, url, key, secret, text, routes, active, received, last_seen, created_at) VALUES(?,?,?,?,?,?,?,?,0,0,?)`,
			p.UserID, site.Name, site.URL, site.Key, site.Secret, site.Text, jsonText(site.Routes), b2i(site.Active), now)
		if err != nil {
			a.fail(w, err)
			return
		}
		site.ID, _ = res.LastInsertId()
		site.CreatedAt = now
	} else if _, err := a.m.db.ExecContext(ctx, `UPDATE websites SET name=?, url=?, key=?, secret=?, text=?, routes=?, active=? WHERE id=? AND owner_id=?`,
		site.Name, site.URL, site.Key, site.Secret, site.Text, jsonText(site.Routes), b2i(site.Active), site.ID, p.UserID); err != nil {
		a.fail(w, err)
		return
	}
	audit(ctx, a.m.db, p.ActorID, clientIP(r), "website.save", site.Name)
	out := *site
	if !showSecret {
		out.Secret = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "website": out, "inbound_url": a.cfg.PublicURL + "/api/inbound"})
}

func (a *API) deleteWebsite(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM websites WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("website not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- signed inbound ----------

var inboundLimiter = &attemptLimiter{hits: map[string][]time.Time{}}

// verifyInbound checks key, timestamp and HMAC; returns the website and its owner.
func (a *API) verifyInbound(w http.ResponseWriter, r *http.Request, body []byte) (*Website, int64, bool) {
	key := strings.TrimSpace(r.Header.Get("X-Relay-Key"))
	ts := strings.TrimSpace(r.Header.Get("X-Relay-Timestamp"))
	sig := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("X-Relay-Signature")), "sha256=")
	if key == "" || ts == "" || sig == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized", errors.New("X-Relay-Key, X-Relay-Timestamp and X-Relay-Signature headers are required"))
		return nil, 0, false
	}
	if !inboundLimiter.allow("in:"+clientIP(r), 600, time.Hour) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", errTooMany)
		return nil, 0, false
	}
	var owner int64
	row := a.m.db.QueryRowContext(r.Context(), `SELECT owner_id, `+wsCols+` FROM websites WHERE key=?`, key)
	var site Website
	var routes string
	var active int
	if err := row.Scan(&owner, &site.ID, &site.Name, &site.URL, &site.Key, &site.Secret, &site.Text, &routes, &active, &site.Received, &site.LastSeen, &site.CreatedAt); err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", errors.New("unknown website key"))
		return nil, 0, false
	}
	site.Active = active == 1
	site.Routes = fromJSON[[]Route](routes)
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(n, 0)).Abs() > 5*time.Minute {
		writeErr(w, http.StatusUnauthorized, "unauthorized", errors.New("timestamp is missing or more than 5 minutes off"))
		return nil, 0, false
	}
	mac := hmac.New(sha256.New, []byte(site.Secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(sig)), []byte(want)) != 1 {
		writeErr(w, http.StatusForbidden, "bad_signature", errors.New("signature does not match"))
		return nil, 0, false
	}
	if !site.Active {
		writeErr(w, http.StatusForbidden, "disabled", errors.New("this website is turned off in the gateway"))
		return nil, 0, false
	}
	_, _ = a.m.db.ExecContext(r.Context(), `UPDATE websites SET last_seen=? WHERE id=?`, time.Now().Unix(), site.ID)
	return &site, owner, true
}

// GET /api/inbound/ping — connection test for website integrations.
func (a *API) inboundPing(w http.ResponseWriter, r *http.Request) {
	site, _, ok := a.verifyInbound(w, r, nil)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "website": site.Name, "routes": len(site.Routes), "time": time.Now().Unix()})
}

// routeFor picks the website route for a category (exact, else default).
func (site *Website) routeFor(category string) *Route {
	category = strings.ToLower(strings.TrimSpace(category))
	var def *Route
	for i := range site.Routes {
		rt := &site.Routes[i]
		if rt.Category == category && category != "" {
			return rt
		}
		if rt.Category == "" && def == nil {
			def = rt
		}
	}
	return def
}

// POST /api/inbound — a website hands over something to publish.
func (a *API) inbound(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	site, owner, ok := a.verifyInbound(w, r, body)
	if !ok {
		return
	}
	var in struct {
		ID       string `json:"id"`
		Event    string `json:"event"`
		Title    string `json:"title"`
		Summary  string `json:"summary"`
		Text     string `json:"text"`
		URL      string `json:"url"`
		Image    string `json:"image"`
		Category string `json:"category"`
		Audience string `json:"audience"`
		Auto     bool   `json:"auto"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON: %w", err))
		return
	}
	ctx := r.Context()
	in.Title = truncate(strings.TrimSpace(in.Title), 300)
	in.URL = strings.TrimSpace(in.URL)
	if in.Title == "" && in.Text == "" && in.URL == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("send at least a title, text or url"))
		return
	}
	if in.ID == "" {
		in.ID = "sha:" + hex.EncodeToString(sha256.New().Sum([]byte(in.URL + "|" + in.Title)))[:24]
	}
	in.ID = truncate(in.ID, 200)
	var existing int64
	var status string
	if a.m.db.QueryRowContext(ctx, `SELECT id, status FROM posts WHERE website_id=? AND ext_id=?`, site.ID, in.ID).Scan(&existing, &status) == nil {
		if in.Event == "post.updated" && status == "pending" {
			_, _ = a.m.db.ExecContext(ctx, `UPDATE posts SET title=?, summary=?, url=?, image=?, category=? WHERE id=?`, in.Title, truncate(in.Summary, 1000), in.URL, truncate(in.Image, 1000), truncate(in.Category, 60), existing)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": existing, "status": "pending", "updated": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": existing, "status": "duplicate", "previous_status": status})
		return
	}
	rt := site.routeFor(in.Category)
	text := strings.TrimSpace(in.Text)
	session := ""
	var targets []Target
	auto := false
	if rt != nil {
		session, targets, auto = rt.Session, rt.Targets, rt.Auto
		if text == "" {
			text = rt.Text
		}
	}
	if text == "" {
		text = site.Text
	}
	if in.Audience != "" { // a named list overrides the route's targets
		var lid int64
		if a.m.db.QueryRowContext(ctx, `SELECT id FROM send_lists WHERE owner_id=? AND name=? COLLATE NOCASE`, owner, in.Audience).Scan(&lid) == nil {
			targets = []Target{{To: "list:" + strconv.FormatInt(lid, 10), Name: in.Audience}}
		}
	}
	// Images and link previews are fetched later by the sender; a missing
	// picture is filled from the page when the owner opens the inbox.
	now := time.Now().Unix()
	res, err := a.m.db.ExecContext(ctx, `INSERT INTO posts(owner_id, website_id, ext_id, event, title, summary, text, url, image, category, session, targets, status, source, note, created_at, decided_at, decided_by)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'pending','website','',?,0,0)`, owner, site.ID, in.ID, truncate(in.Event, 40), in.Title, truncate(in.Summary, 1000), text, in.URL, truncate(in.Image, 1000),
		truncate(strings.ToLower(in.Category), 60), session, jsonText(cleanTargets(targets, a.cfg.DefaultCountryCode)), now)
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	_, _ = a.m.db.ExecContext(ctx, `UPDATE websites SET received=received+1 WHERE id=?`, site.ID)
	out := map[string]any{"ok": true, "id": id, "status": "pending"}
	if auto && in.Auto && len(targets) > 0 {
		p, _ := scanPost(a.m.db.QueryRowContext(ctx, `SELECT `+postCols+` FROM posts p LEFT JOIN websites w ON w.id=p.website_id WHERE p.id=?`, id))
		if p != nil {
			if err := a.m.publish(ctx, p, 0); err != nil {
				_, _ = a.m.db.ExecContext(ctx, `UPDATE posts SET note=? WHERE id=?`, "auto-send failed: "+err.Error(), id)
			} else {
				out["status"] = "queued"
			}
		}
	}
	if out["status"] == "pending" {
		a.m.dispatchOwner(owner, "post.pending", map[string]any{"id": id, "website": site.Name, "title": in.Title, "url": in.URL, "category": in.Category})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- posts API ----------

// GET /api/posts?status=pending|queued|rejected|all&q=
func (a *API) listPosts(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()
	where, args := `p.owner_id=?`, []any{p.UserID}
	st := q.Get("status")
	if st == "" {
		st = "pending"
	}
	if st != "all" {
		where += ` AND p.status=?`
		args = append(args, st)
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		where += ` AND (p.title LIKE ? OR p.url LIKE ? OR p.category LIKE ?)`
		like := "%" + s + "%"
		args = append(args, like, like, like)
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT `+postCols+` FROM posts p LEFT JOIN websites w ON w.id=p.website_id WHERE `+where+` ORDER BY p.id DESC LIMIT 300`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []*Post{}
	for rows.Next() {
		if x, err := scanPost(rows); err == nil {
			if x.Status == "queued" {
				x.Stats = a.m.postStats(r.Context(), x.ID)
			}
			out = append(out, x)
		}
	}
	var pending int
	_ = a.m.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM posts WHERE owner_id=? AND status='pending'`, p.UserID).Scan(&pending)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "posts": out, "pending": pending})
}

// POST /api/posts — bulk: {urls:[…], text, session, targets:[…], category, fetch:true}
func (a *API) createPosts(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	var in struct {
		URLs     []string `json:"urls"`
		Text     string   `json:"text"`
		Session  string   `json:"session"`
		Targets  []Target `json:"targets"`
		Category string   `json:"category"`
		Fetch    *bool    `json:"fetch"`
		Approve  bool     `json:"approve"` // queue right away
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	var urls []string
	seen := map[string]bool{}
	for _, u := range in.URLs {
		for _, line := range strings.FieldsFunc(u, func(c rune) bool { return c == '\n' || c == '\r' || c == ' ' || c == ',' || c == '\t' }) {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
				if strings.Contains(line, ".") && !strings.ContainsAny(line, " ") {
					line = "https://" + line
				} else {
					continue
				}
			}
			if pu, err := url.Parse(line); err == nil && pu.Host != "" && !seen[line] {
				seen[line] = true
				urls = append(urls, line)
			}
		}
	}
	if len(urls) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("paste at least one link"))
		return
	}
	if len(urls) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("at most 100 links at a time"))
		return
	}
	if in.Session != "" {
		if s, err := a.m.get(in.Session); err != nil || (!p.Admin() && s.owner() != p.UserID) {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("number not found"))
			return
		}
	}
	targets := cleanTargets(in.Targets, a.cfg.DefaultCountryCode)
	if in.Approve && len(targets) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("choose where to send"))
		return
	}
	fetch := in.Fetch == nil || *in.Fetch
	now := time.Now().Unix()
	text := truncate(in.Text, 4000)
	created := []*Post{}
	failed := 0
	for _, u := range urls {
		title, summary, image := "", "", ""
		if fetch {
			fctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			if pv := getPreview(fctx, u, a.cfg.AllowPrivateURLs, false); pv != nil && pv.Error == "" {
				title, summary, image = pv.Title, pv.Description, pv.Image
			}
			cancel()
		}
		if title == "" {
			title = u
		}
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO posts(owner_id, website_id, ext_id, event, title, summary, text, url, image, category, session, targets, status, source, note, created_at, decided_at, decided_by)
			VALUES(?,0,'','manual',?,?,?,?,?,?,?,?,'pending','manual','',?,0,0)`, p.UserID, truncate(title, 300), truncate(summary, 1000), text, u, truncate(image, 1000), truncate(strings.ToLower(in.Category), 60), in.Session, jsonText(targets), now)
		if err != nil {
			failed++
			continue
		}
		id, _ := res.LastInsertId()
		post, _ := scanPost(a.m.db.QueryRowContext(ctx, `SELECT `+postCols+` FROM posts p LEFT JOIN websites w ON w.id=p.website_id WHERE p.id=?`, id))
		if post == nil {
			continue
		}
		if in.Approve {
			if err := a.m.publish(ctx, post, p.ActorID); err != nil {
				_, _ = a.m.db.ExecContext(ctx, `UPDATE posts SET note=? WHERE id=?`, err.Error(), id)
				post.Note = err.Error()
			} else {
				post.Status = "queued"
			}
		}
		created = append(created, post)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "posts": created, "created": len(created), "failed": failed})
}

func (a *API) loadPost(w http.ResponseWriter, r *http.Request) *Post {
	p := principalOf(r)
	post, err := scanPost(a.m.db.QueryRowContext(r.Context(), `SELECT `+postCols+` FROM posts p LEFT JOIN websites w ON w.id=p.website_id WHERE p.id=? AND p.owner_id=?`, r.PathValue("id"), p.UserID))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("post not found"))
		return nil
	}
	return post
}

// PATCH /api/posts/{id} — edit before approval (title, summary, text, image, session, targets, category).
func (a *API) savePost(w http.ResponseWriter, r *http.Request) {
	post := a.loadPost(w, r)
	if post == nil {
		return
	}
	var in struct {
		Title    *string  `json:"title"`
		Summary  *string  `json:"summary"`
		Text     *string  `json:"text"`
		Image    *string  `json:"image"`
		URL      *string  `json:"url"`
		Session  *string  `json:"session"`
		Category *string  `json:"category"`
		Targets  []Target `json:"targets"`
		Refetch  bool     `json:"refetch"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	set := func(dst *string, v *string, n int) {
		if v != nil {
			*dst = truncate(strings.TrimSpace(*v), n)
		}
	}
	set(&post.Title, in.Title, 300)
	set(&post.Summary, in.Summary, 1000)
	set(&post.Text, in.Text, 4000)
	set(&post.Image, in.Image, 1000)
	set(&post.URL, in.URL, 1000)
	set(&post.Session, in.Session, 60)
	set(&post.Category, in.Category, 60)
	if in.Targets != nil {
		post.Targets = cleanTargets(in.Targets, a.cfg.DefaultCountryCode)
	}
	if in.Refetch && post.URL != "" {
		fctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		if pv := getPreview(fctx, post.URL, a.cfg.AllowPrivateURLs, true); pv != nil && pv.Error == "" {
			if pv.Title != "" {
				post.Title = truncate(pv.Title, 300)
			}
			if pv.Description != "" {
				post.Summary = truncate(pv.Description, 1000)
			}
			if pv.Image != "" {
				post.Image = truncate(pv.Image, 1000)
			}
		}
		cancel()
	}
	_, err := a.m.db.ExecContext(r.Context(), `UPDATE posts SET title=?, summary=?, text=?, image=?, url=?, session=?, category=?, targets=? WHERE id=?`,
		post.Title, post.Summary, post.Text, post.Image, post.URL, post.Session, post.Category, jsonText(post.Targets), post.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "post": post})
}

// POST /api/posts/{id}/approve {targets?, session?, text?}
func (a *API) approvePost(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	post := a.loadPost(w, r)
	if post == nil {
		return
	}
	if post.Status != "pending" {
		writeErr(w, http.StatusConflict, "not_pending", fmt.Errorf("this post is already %s", post.Status))
		return
	}
	var in struct {
		Targets []Target `json:"targets"`
		Session *string  `json:"session"`
		Text    *string  `json:"text"`
	}
	_ = decode(r, &in)
	if in.Targets != nil {
		post.Targets = cleanTargets(in.Targets, a.cfg.DefaultCountryCode)
	}
	if in.Session != nil {
		post.Session = *in.Session
	}
	if in.Text != nil {
		post.Text = truncate(*in.Text, 4000)
	}
	_, _ = a.m.db.ExecContext(r.Context(), `UPDATE posts SET targets=?, session=?, text=? WHERE id=?`, jsonText(post.Targets), post.Session, post.Text, post.ID)
	if err := a.m.publish(r.Context(), post, p.ActorID); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	post.Status = "queued"
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "post": post})
}

// POST /api/posts/{id}/reject
func (a *API) rejectPost(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	post := a.loadPost(w, r)
	if post == nil {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = decode(r, &in)
	_, err := a.m.db.ExecContext(r.Context(), `UPDATE posts SET status='rejected', decided_at=?, decided_by=?, note=? WHERE id=? AND status='pending'`, time.Now().Unix(), p.ActorID, truncate(in.Note, 500), post.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) deletePost(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM posts WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("post not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
