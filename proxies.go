package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ======================= Proxy pool =======================
//
// The super admin keeps a pool of proxies (bought from a provider) and
// decides who may use which: a dedicated proxy is allocated to one user,
// a shared one is open to everybody. Users then attach a pool proxy to a
// number without ever seeing the credentials. A number can still use its
// own proxy URL when the admin allows that.
//
// What a proxy does here, honestly: only the gateway's connection to
// WhatsApp for that number goes through it (so WhatsApp sees the proxy's
// IP and country). It does not change the phone's IP, and it does not make
// an unofficial gateway official — pacing and warm-up still matter most.

type Proxy struct {
	ID            int64  `json:"id"`
	Label         string `json:"label"`
	URL           string `json:"url,omitempty"` // full URL, admin only
	Masked        string `json:"masked"`
	Kind          string `json:"kind"` // shared, dedicated
	AssignedTo    int64  `json:"assigned_to"`
	AssignedEmail string `json:"assigned_email"`
	Country       string `json:"country"`
	Note          string `json:"note"`
	Active        bool   `json:"active"`
	InUse         int    `json:"in_use"` // numbers using it
	LastTest      int64  `json:"last_test"`
	LastResult    string `json:"last_result"`
	CreatedAt     int64  `json:"created_at"`
}

const proxyCols = `p.id, p.label, p.url, p.kind, p.assigned_to, COALESCE(u.email,''), p.country, p.note, p.active, p.last_test, p.last_result, p.created_at,
	(SELECT COUNT(*) FROM gw_sessions g WHERE g.proxy_id=p.id)`

func scanProxy(row interface{ Scan(...any) error }) (*Proxy, error) {
	var p Proxy
	var active int
	if err := row.Scan(&p.ID, &p.Label, &p.URL, &p.Kind, &p.AssignedTo, &p.AssignedEmail, &p.Country, &p.Note, &active, &p.LastTest, &p.LastResult, &p.CreatedAt, &p.InUse); err != nil {
		return nil, err
	}
	p.Active = active == 1
	p.Masked = maskProxy(p.URL)
	return &p, nil
}

// proxiesFor lists the pool proxies a user may attach (dedicated to them, or shared).
func (m *Manager) proxiesFor(ctx context.Context, owner int64, admin bool) ([]*Proxy, error) {
	q := `SELECT ` + proxyCols + ` FROM proxies p LEFT JOIN users u ON u.id=p.assigned_to WHERE p.active=1 AND (p.kind='shared' OR p.assigned_to=?) ORDER BY p.kind, p.label`
	if admin {
		q = `SELECT ` + proxyCols + ` FROM proxies p LEFT JOIN users u ON u.id=p.assigned_to WHERE p.active=1 OR ?=? ORDER BY p.kind, p.label`
	}
	args := []any{owner}
	if admin {
		args = []any{1, 1}
	}
	rows, err := m.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Proxy{}
	for rows.Next() {
		if p, err := scanProxy(rows); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// proxyByID returns a pool proxy this user may use (admin: any).
func (m *Manager) proxyByID(ctx context.Context, id, owner int64, admin bool) (*Proxy, error) {
	p, err := scanProxy(m.db.QueryRowContext(ctx, `SELECT `+proxyCols+` FROM proxies p LEFT JOIN users u ON u.id=p.assigned_to WHERE p.id=?`, id))
	if err != nil {
		return nil, errors.New("proxy not found")
	}
	if !admin && (!p.Active || (p.Kind != "shared" && p.AssignedTo != owner)) {
		return nil, errors.New("this proxy is not available to you")
	}
	return p, nil
}

// ---------- user side ----------

// GET /api/proxies — what this workspace may attach to a number.
func (a *API) listMyProxies(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	list, err := a.m.proxiesFor(r.Context(), p.UserID, false)
	if err != nil {
		a.fail(w, err)
		return
	}
	for _, x := range list {
		x.URL, x.Note, x.AssignedEmail = "", "", ""
	}
	allowOwn := getSetting(r.Context(), a.m.db, "proxy_own_allowed") != "0"
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proxies": list, "own_allowed": allowOwn || p.Admin()})
}

// ---------- admin side ----------

func (a *API) adminProxies(w http.ResponseWriter, r *http.Request) {
	list, err := a.m.proxiesFor(r.Context(), 0, true)
	if err != nil {
		a.fail(w, err)
		return
	}
	for _, x := range list {
		x.URL = "" // never echo credentials back; the masked form is enough
	}
	// which numbers use which proxy
	rows, _ := a.m.db.QueryContext(r.Context(), `SELECT g.name, g.label, g.proxy_id, g.owner_id, COALESCE(u.email,'') FROM gw_sessions g LEFT JOIN users u ON u.id=g.owner_id WHERE g.proxy_id>0`)
	usage := []map[string]any{}
	if rows != nil {
		for rows.Next() {
			var name, label, email string
			var pid, owner int64
			if rows.Scan(&name, &label, &pid, &owner, &email) == nil {
				usage = append(usage, map[string]any{"session": name, "label": label, "proxy_id": pid, "owner_id": owner, "email": email})
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proxies": list, "usage": usage, "own_allowed": getSetting(r.Context(), a.m.db, "proxy_own_allowed") != "0"})
}

func (a *API) adminSaveProxy(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	var in struct {
		Label      *string `json:"label"`
		URL        *string `json:"url"`
		Kind       *string `json:"kind"`
		AssignedTo *int64  `json:"assigned_to"`
		Country    *string `json:"country"`
		Note       *string `json:"note"`
		Active     *bool   `json:"active"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	x := &Proxy{Kind: "shared", Active: true}
	if id := r.PathValue("id"); id != "" {
		n, _ := strconv.ParseInt(id, 10, 64)
		old, err := a.m.proxyByID(ctx, n, 0, true)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err)
			return
		}
		x = old
	}
	if in.Label != nil {
		x.Label = truncate(strings.TrimSpace(*in.Label), 80)
	}
	if in.URL != nil && strings.TrimSpace(*in.URL) != "" {
		u, err := vetProxy(ctx, *in.URL, a.cfg.AllowPrivateURLs)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_proxy", err)
			return
		}
		x.URL = u
	}
	if in.Kind != nil {
		if *in.Kind != "shared" && *in.Kind != "dedicated" {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("kind must be shared or dedicated"))
			return
		}
		x.Kind = *in.Kind
	}
	if in.AssignedTo != nil {
		x.AssignedTo = *in.AssignedTo
	}
	if x.Kind == "shared" {
		x.AssignedTo = 0
	}
	if in.Country != nil {
		x.Country = truncate(strings.TrimSpace(*in.Country), 40)
	}
	if in.Note != nil {
		x.Note = truncate(*in.Note, 500)
	}
	if in.Active != nil {
		x.Active = *in.Active
	}
	if x.Label == "" {
		x.Label = maskProxy(x.URL)
	}
	if x.URL == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("proxy URL is needed (socks5://user:pass@host:port or http://host:port)"))
		return
	}
	now := time.Now().Unix()
	if x.ID == 0 {
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO proxies(label, url, kind, assigned_to, country, note, active, last_test, last_result, created_at) VALUES(?,?,?,?,?,?,?,0,'',?)`,
			x.Label, x.URL, x.Kind, x.AssignedTo, x.Country, x.Note, b2i(x.Active), now)
		if err != nil {
			a.fail(w, err)
			return
		}
		x.ID, _ = res.LastInsertId()
	} else {
		if _, err := a.m.db.ExecContext(ctx, `UPDATE proxies SET label=?, url=?, kind=?, assigned_to=?, country=?, note=?, active=? WHERE id=?`,
			x.Label, x.URL, x.Kind, x.AssignedTo, x.Country, x.Note, b2i(x.Active), x.ID); err != nil {
			a.fail(w, err)
			return
		}
		// numbers using it get the new URL (and lose it if it was deactivated or re-assigned)
		a.m.syncProxy(ctx, x)
	}
	audit(ctx, a.m.db, p.ActorID, clientIP(r), "proxy.save", x.Label)
	x.Masked = maskProxy(x.URL)
	x.URL = ""
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proxy": x})
}

// syncProxy pushes a pool proxy's current URL to the numbers that use it,
// detaching numbers that may no longer use it.
func (m *Manager) syncProxy(ctx context.Context, x *Proxy) {
	rows, err := m.db.QueryContext(ctx, `SELECT name, owner_id FROM gw_sessions WHERE proxy_id=?`, x.ID)
	if err != nil {
		return
	}
	type row struct {
		name  string
		owner int64
	}
	var list []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.name, &r.owner) == nil {
			list = append(list, r)
		}
	}
	rows.Close()
	for _, r := range list {
		allowed := x.Active && (x.Kind == "shared" || x.AssignedTo == r.owner)
		if allowed {
			_, _ = m.db.ExecContext(ctx, `UPDATE gw_sessions SET proxy=? WHERE name=?`, x.URL, r.name)
		} else {
			_, _ = m.db.ExecContext(ctx, `UPDATE gw_sessions SET proxy='', proxy_id=0 WHERE name=?`, r.name)
		}
		if s, err := m.get(r.name); err == nil {
			s.reconnectIfLinked(ctx)
		}
	}
}

func (a *API) adminDeleteProxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	x, err := a.m.proxyByID(ctx, id, 0, true)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", err)
		return
	}
	x.Active = false
	a.m.syncProxy(ctx, x) // detach numbers first
	_, _ = a.m.db.ExecContext(ctx, `DELETE FROM proxies WHERE id=?`, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST /api/admin/proxies/{id}/test
func (a *API) adminTestProxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	x, err := a.m.proxyByID(ctx, id, 0, true)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", err)
		return
	}
	info, err := testProxy(ctx, x.URL)
	result := "ok"
	if err != nil {
		result = err.Error()
	} else {
		result = "ok " + info.IP
		if info.Country != "" {
			result += " " + info.Country
		}
		if x.Country == "" && info.Country != "" {
			_, _ = a.m.db.ExecContext(ctx, `UPDATE proxies SET country=? WHERE id=?`, info.Country, id)
		}
	}
	_, _ = a.m.db.ExecContext(ctx, `UPDATE proxies SET last_test=?, last_result=? WHERE id=?`, time.Now().Unix(), truncate(result, 200), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "proxy_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": info})
}

// POST /api/admin/proxies/settings {own_allowed: bool}
func (a *API) adminProxySettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OwnAllowed bool `json:"own_allowed"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	v := "1"
	if !in.OwnAllowed {
		v = "0"
	}
	setSetting(r.Context(), a.m.db, "proxy_own_allowed", v)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "own_allowed": in.OwnAllowed})
}
