package main

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	u, err := getUser(ctx, a.m.db, p.UserID)
	if err != nil {
		if p.Master && p.UserID == 0 { // master key before setup
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": map[string]any{"id": 0, "role": "superadmin", "name": "Master key"},
				"licence": a.lic.effective(ctx, 0), "usage": a.lic.usage(ctx, 0), "mode": a.mode()})
			return
		}
		writeErr(w, http.StatusUnauthorized, "unauthorized", errors.New("account not found"))
		return
	}
	out := map[string]any{"ok": true, "user": u, "licence": a.lic.effective(ctx, u.ID), "usage": a.lic.usage(ctx, u.ID), "mode": a.mode(),
		"workspace": map[string]any{"id": p.UserID, "role": p.WorkspaceRole, "actor_id": p.ActorID}}
	if p.ActorID != p.UserID {
		if me, err := getUser(ctx, a.m.db, p.ActorID); err == nil {
			out["actor"] = me
		}
		if owner, err := getUser(ctx, a.m.db, p.UserID); err == nil {
			out["workspace"].(map[string]any)["owner"] = owner.Name
		}
	}
	can := map[string]bool{}
	for k := range perms {
		can[k] = p.can(k)
	}
	out["can"] = can
	if a.lic.clientMode() {
		out["licence_error"] = a.lic.clientError()
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) updateMe(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var b struct {
		Name  *string `json:"name"`
		Phone *string `json:"phone"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if b.Name != nil {
		_, _ = a.m.db.ExecContext(r.Context(), `UPDATE users SET name=? WHERE id=?`, truncate(strings.TrimSpace(*b.Name), 100), p.UserID)
	}
	if b.Phone != nil {
		_, _ = a.m.db.ExecContext(r.Context(), `UPDATE users SET phone=? WHERE id=?`, truncate(strings.TrimSpace(*b.Phone), 20), p.UserID)
	}
	a.me(w, r)
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var b struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	u, err := getUser(r.Context(), a.m.db, p.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", errors.New("account not found"))
		return
	}
	if _, err := checkPassword(r.Context(), a.m.db, u.Email, b.Current); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_credentials", errors.New("current password is wrong"))
		return
	}
	if err := setPassword(r.Context(), a.m.db, u.ID, b.New); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	audit(r.Context(), a.m.db, u.ID, clientIP(r), "password.change", "")
	a.startLogin(w, r, u) // keep this browser logged in
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) licenceInfo(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "licence": a.lic.effective(r.Context(), p.UserID), "usage": a.lic.usage(r.Context(), p.UserID)})
}

func (a *API) redeemLicence(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if a.lic.clientMode() {
		writeErr(w, http.StatusBadRequest, "client_mode", errors.New("this install is licensed through LICENCE_KEY in .env"))
		return
	}
	var b struct {
		Key string `json:"key"`
	}
	if err := decode(r, &b); err != nil || b.Key == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("key is required"))
		return
	}
	if !loginLimiter.allow("redeem:"+strconv.FormatInt(p.UserID, 10), 10, time.Hour) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", errTooMany)
		return
	}
	if err := a.lic.redeem(r.Context(), p.UserID, b.Key); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_licence", err)
		return
	}
	audit(r.Context(), a.m.db, p.ActorID, clientIP(r), "licence.redeem", maskKey(strings.ToUpper(b.Key)))
	a.licenceInfo(w, r)
}

// ---------- API keys ----------

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT id, name, prefix, created_at, last_used FROM api_keys WHERE user_id=? ORDER BY id DESC`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, created, used int64
		var name, prefix string
		if rows.Scan(&id, &name, &prefix, &created, &used) == nil {
			out = append(out, map[string]any{"id": id, "name": name, "prefix": prefix, "created_at": created, "last_used": used})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "keys": out})
}

func (a *API) createKey(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p.UserID == 0 {
		writeErr(w, http.StatusBadRequest, "setup_needed", errors.New("finish setup (create the owner account) first"))
		return
	}
	if err := a.lic.require(r.Context(), p.UserID, "api"); err != nil {
		a.fail(w, err)
		return
	}
	var b struct {
		Name string `json:"name"`
	}
	_ = decode(r, &b)
	var n int
	_ = a.m.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM api_keys WHERE user_id=?`, p.UserID).Scan(&n)
	if n >= 20 {
		writeErr(w, http.StatusBadRequest, "too_many_keys", errors.New("at most 20 API keys"))
		return
	}
	secret := "vr_" + randToken(24)
	prefix := secret[:7]
	now := time.Now().Unix()
	res, err := a.m.db.ExecContext(r.Context(), `INSERT INTO api_keys(user_id, name, prefix, key_hash, created_at) VALUES(?,?,?,?,?)`,
		p.UserID, truncate(strings.TrimSpace(b.Name), 60), prefix, sha(secret), now)
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	audit(r.Context(), a.m.db, p.ActorID, clientIP(r), "apikey.create", b.Name)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "secret": secret,
		"key": map[string]any{"id": id, "name": b.Name, "prefix": prefix, "created_at": now, "last_used": 0}})
}

func (a *API) deleteKey(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM api_keys WHERE id=? AND user_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("key not found"))
		return
	}
	audit(r.Context(), a.m.db, p.ActorID, clientIP(r), "apikey.delete", r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- webhook endpoints ----------

var webhookEvents = []string{"*", "message", "message.receipt", "message.deleted", "poll.vote", "call", "queue.status", "campaign.status",
	"session.status", "session.paired", "session.timelock", "session.health", "contact.optout", "contact.optin", "group.update", "test"}

func validWebhookURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func (a *API) listWebhooks(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT id, url, secret, events, active, last_status, last_at, created_at FROM webhook_endpoints WHERE user_id=? ORDER BY id`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, last, created int64
		var u, secret, evts string
		var active, status int
		if rows.Scan(&id, &u, &secret, &evts, &active, &status, &last, &created) == nil {
			out = append(out, map[string]any{"id": id, "url": u, "secret": maskKey(secret), "events": fromJSON[[]string](evts),
				"active": active == 1, "last_status": status, "last_at": last, "created_at": created})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "webhooks": out, "events": webhookEvents})
}

func cleanEvents(list []string) []string {
	ok := map[string]bool{}
	for _, e := range webhookEvents {
		ok[e] = true
	}
	var out []string
	for _, e := range list {
		if ok[e] {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		out = []string{"*"}
	}
	return out
}

func (a *API) createWebhook(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p.UserID == 0 {
		writeErr(w, http.StatusBadRequest, "setup_needed", errors.New("finish setup (create the owner account) first, or use WEBHOOK_URL in .env"))
		return
	}
	if err := a.lic.require(r.Context(), p.UserID, "webhooks"); err != nil {
		a.fail(w, err)
		return
	}
	var b struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if err := decode(r, &b); err != nil || !validWebhookURL(b.URL) {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("a valid http(s) url is required"))
		return
	}
	secret := "whsec_" + randToken(20)
	res, err := a.m.db.ExecContext(r.Context(), `INSERT INTO webhook_endpoints(user_id, url, secret, events, created_at) VALUES(?,?,?,?,?)`,
		p.UserID, strings.TrimSpace(b.URL), secret, jsonText(cleanEvents(b.Events)), time.Now().Unix())
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "webhook": map[string]any{"id": id, "url": b.URL, "secret": secret,
		"events": cleanEvents(b.Events), "active": true}})
}

func (a *API) updateWebhook(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var b struct {
		URL    *string  `json:"url"`
		Events []string `json:"events"`
		Active *bool    `json:"active"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	var id int64
	if a.m.db.QueryRowContext(r.Context(), `SELECT id FROM webhook_endpoints WHERE id=? AND user_id=?`, r.PathValue("id"), p.UserID).Scan(&id) != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("webhook not found"))
		return
	}
	if b.URL != nil {
		if !validWebhookURL(*b.URL) {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("a valid http(s) url is required"))
			return
		}
		_, _ = a.m.db.ExecContext(r.Context(), `UPDATE webhook_endpoints SET url=? WHERE id=?`, strings.TrimSpace(*b.URL), id)
	}
	if b.Events != nil {
		_, _ = a.m.db.ExecContext(r.Context(), `UPDATE webhook_endpoints SET events=? WHERE id=?`, jsonText(cleanEvents(b.Events)), id)
	}
	if b.Active != nil {
		_, _ = a.m.db.ExecContext(r.Context(), `UPDATE webhook_endpoints SET active=? WHERE id=?`, b2i(*b.Active), id)
	}
	a.listWebhooks(w, r)
}

func (a *API) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM webhook_endpoints WHERE id=? AND user_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("webhook not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) testWebhook(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var id int64
	var u, secret string
	if a.m.db.QueryRowContext(r.Context(), `SELECT id, url, secret FROM webhook_endpoints WHERE id=? AND user_id=?`, r.PathValue("id"), p.UserID).Scan(&id, &u, &secret) != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("webhook not found"))
		return
	}
	job := webhookJob{url: u, secret: secret, body: payload("", p.UserID, "test", map[string]any{"hello": "from Vivid Relay"}), evt: "test",
		endpointID: id, guarded: !a.cfg.AllowPrivateURLs}
	status, err := a.m.hooks.post(r.Context(), job)
	_, _ = a.m.db.ExecContext(r.Context(), `UPDATE webhook_endpoints SET last_status=?, last_at=? WHERE id=?`, status, time.Now().Unix(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "webhook_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

// ---------- stats ----------

func (a *API) stats(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 14
	}
	from := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	where, args := "owner_id=?", []any{p.UserID}
	if p.Admin() && r.URL.Query().Get("all") == "1" {
		where, args = "1=1", nil
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT day, SUM(sent), SUM(failed), SUM(received) FROM stats_daily WHERE `+where+` AND day>=? GROUP BY day`, append(args, from)...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	byDay := map[string][3]int{}
	for rows.Next() {
		var d string
		var s, f, rc int
		if rows.Scan(&d, &s, &f, &rc) == nil {
			byDay[d] = [3]int{s, f, rc}
		}
	}
	out := []map[string]any{}
	var ts, tf, tr int
	for i := days - 1; i >= 0; i-- {
		d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		v := byDay[d]
		ts, tf, tr = ts+v[0], tf+v[1], tr+v[2]
		out = append(out, map[string]any{"day": d, "sent": v[0], "failed": v[1], "received": v[2]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "days": out, "totals": map[string]int{"sent": ts, "failed": tf, "received": tr}})
}
