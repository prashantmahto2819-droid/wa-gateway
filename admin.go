package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *API) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !principalOf(r).Admin() {
			writeErr(w, http.StatusForbidden, "forbidden", errForbidden)
			return
		}
		h(w, r)
	}
}

// licence-server features are only on the main (SaaS) install.
func (a *API) serverOnly(h http.HandlerFunc) http.HandlerFunc {
	return a.adminOnly(func(w http.ResponseWriter, r *http.Request) {
		if a.lic.clientMode() {
			writeErr(w, http.StatusForbidden, "client_mode", errors.New("plans and licences are managed on your licence server"))
			return
		}
		h(w, r)
	})
}

func (a *API) adminOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	db := a.m.db
	count := func(q string, args ...any) int {
		var n int
		_ = db.QueryRowContext(ctx, q, args...).Scan(&n)
		return n
	}
	connected := 0
	for _, s := range a.m.list() {
		if _, err := s.ready(); err == nil {
			connected++
		}
	}
	today := time.Now().Format("2006-01-02")
	month := time.Now().Format("2006-01") + "-01"
	writeJSON(w, http.StatusOK, map[string]any{"ok": true,
		"users":             count(`SELECT COUNT(*) FROM users`),
		"active_licences":   count(`SELECT COUNT(*) FROM licences WHERE status='active' AND expires_at>?`, time.Now().Unix()),
		"sessions":          len(a.m.list()),
		"connected":         connected,
		"sent_today":        count(`SELECT COALESCE(SUM(sent),0) FROM stats_daily WHERE day=?`, today),
		"sent_month":        count(`SELECT COALESCE(SUM(sent),0) FROM stats_daily WHERE day>=?`, month),
		"messages_unread":   count(`SELECT COUNT(*) FROM inbox WHERE read=0`),
		"queued":            count(`SELECT COUNT(*) FROM gw_outbox WHERE status='queued'`),
		"running_campaigns": count(`SELECT COUNT(*) FROM campaigns WHERE status='running'`),
		"version":           version, "mode": a.mode()})
}

func (a *API) adminUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := `SELECT ` + userCols + ` FROM users`
	var args []any
	if s := strings.TrimSpace(r.URL.Query().Get("q")); s != "" {
		q += ` WHERE email LIKE ? OR name LIKE ? OR phone LIKE ?`
		like := "%" + s + "%"
		args = append(args, like, like, like)
	}
	rows, err := a.m.db.QueryContext(ctx, q+` ORDER BY id DESC LIMIT 1000`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	var users []*User
	for rows.Next() {
		if u, err := scanUser(rows); err == nil {
			users = append(users, u)
		}
	}
	rows.Close()
	out := []map[string]any{}
	for _, u := range users {
		var sessions int
		_ = a.m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gw_sessions WHERE owner_id=?`, u.ID).Scan(&sessions)
		out = append(out, map[string]any{"id": u.ID, "email": u.Email, "name": u.Name, "phone": u.Phone, "role": u.Role, "status": u.Status,
			"created_at": u.CreatedAt, "last_login": u.LastLogin, "sessions": sessions, "licence": a.lic.effective(ctx, u.ID), "usage": a.lic.usage(ctx, u.ID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "users": out})
}

func (a *API) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u, err := getUser(r.Context(), a.m.db, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("user not found"))
		return
	}
	var b struct {
		Role   *string `json:"role"`
		Status *string `json:"status"`
		Name   *string `json:"name"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if (b.Role != nil || b.Status != nil) && id == p.UserID {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("you can't change your own role or status"))
		return
	}
	if b.Role != nil {
		if *b.Role != "user" && *b.Role != "superadmin" {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("role must be user or superadmin"))
			return
		}
		_, _ = a.m.db.ExecContext(r.Context(), `UPDATE users SET role=? WHERE id=?`, *b.Role, id)
	}
	if b.Status != nil {
		if *b.Status != "active" && *b.Status != "disabled" {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("status must be active or disabled"))
			return
		}
		_, _ = a.m.db.ExecContext(r.Context(), `UPDATE users SET status=? WHERE id=?`, *b.Status, id)
		if *b.Status == "disabled" {
			_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM auth_tokens WHERE user_id=?`, id)
		}
	}
	if b.Name != nil {
		_, _ = a.m.db.ExecContext(r.Context(), `UPDATE users SET name=? WHERE id=?`, truncate(*b.Name, 100), id)
	}
	audit(r.Context(), a.m.db, p.ActorID, clientIP(r), "user.update", u.Email)
	nu, _ := getUser(r.Context(), a.m.db, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": nu})
}

func (a *API) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u, err := getUser(r.Context(), a.m.db, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("user not found"))
		return
	}
	pw := randToken(8)
	if err := setPassword(r.Context(), a.m.db, id, pw); err != nil {
		a.fail(w, err)
		return
	}
	audit(r.Context(), a.m.db, principalOf(r).ActorID, clientIP(r), "user.reset_password", u.Email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "password": pw})
}

func (a *API) adminGrant(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u, err := getUser(r.Context(), a.m.db, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("user not found"))
		return
	}
	var b struct {
		PlanID int64 `json:"plan_id"`
		Days   int64 `json:"days"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if _, err := a.lic.grant(r.Context(), id, b.PlanID, b.Days, "GRANT-", ""); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	_, _ = a.m.db.ExecContext(r.Context(), `UPDATE licences SET status='expired', expires_at=? WHERE user_id=? AND lkey LIKE 'TRIAL-%' AND status='active'`, time.Now().Unix(), id)
	audit(r.Context(), a.m.db, principalOf(r).ActorID, clientIP(r), "licence.grant", fmt.Sprintf("%s plan=%d days=%d", u.Email, b.PlanID, b.Days))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "licence": a.lic.effective(r.Context(), id)})
}

func (a *API) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if id == p.UserID {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("you can't delete yourself"))
		return
	}
	u, err := getUser(r.Context(), a.m.db, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("user not found"))
		return
	}
	// Unlink and remove their numbers, then their data.
	for _, s := range a.m.list() {
		if s.owner() == id {
			_ = a.m.remove(r.Context(), s.name)
		}
	}
	ctx := r.Context()
	for _, q := range []string{`DELETE FROM contacts WHERE owner_id=?`, `DELETE FROM templates WHERE owner_id=?`, `DELETE FROM campaigns WHERE owner_id=?`,
		`DELETE FROM autoreplies WHERE owner_id=?`, `DELETE FROM media WHERE owner_id=?`, `DELETE FROM users WHERE id=?`} {
		_, _ = a.m.db.ExecContext(ctx, q, id)
	}
	_ = os.RemoveAll(a.m.mediaDir(id))
	audit(ctx, a.m.db, p.ActorID, clientIP(r), "user.delete", u.Email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- plans ----------

func (a *API) adminPlans(w http.ResponseWriter, r *http.Request) {
	plans := listPlans(r.Context(), a.m.db, false)
	if plans == nil {
		plans = []*Plan{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plans": plans, "features": allFeatures})
}

func (a *API) adminSavePlan(w http.ResponseWriter, r *http.Request) {
	var p Plan
	if id := r.PathValue("id"); id != "" {
		old, err := getPlan(r.Context(), a.m.db, mustInt(id))
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("plan not found"))
			return
		}
		p = *old
	}
	if err := decode(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if id := r.PathValue("id"); id != "" {
		p.ID = mustInt(id)
	} else {
		p.ID = 0
	}
	id, err := savePlan(r.Context(), a.m.db, &p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	audit(r.Context(), a.m.db, principalOf(r).ActorID, clientIP(r), "plan.save", p.Name)
	saved, _ := getPlan(r.Context(), a.m.db, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": saved})
}

func (a *API) adminDeletePlan(w http.ResponseWriter, r *http.Request) {
	var used int
	_ = a.m.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM licences WHERE plan_id=?`, r.PathValue("id")).Scan(&used)
	if used > 0 {
		writeErr(w, http.StatusConflict, "plan_in_use", errors.New("licences use this plan; hide it (public=false) instead of deleting"))
		return
	}
	_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM plans WHERE id=?`, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func mustInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// ---------- licences ----------

func (a *API) adminLicences(w http.ResponseWriter, r *http.Request) {
	q := `SELECT l.id, l.lkey, l.kind, l.status, l.plan_id, COALESCE(p.name,''), COALESCE(l.user_id,0), COALESCE(u.email,''), l.days, l.max_instances,
		(SELECT COUNT(*) FROM licence_activations x WHERE x.licence_id=l.id), l.notes, l.created_at, l.activated_at, l.expires_at
		FROM licences l LEFT JOIN plans p ON p.id=l.plan_id LEFT JOIN users u ON u.id=l.user_id WHERE l.lkey NOT LIKE 'TRIAL-%'`
	var args []any
	if s := r.URL.Query().Get("status"); s != "" {
		q += ` AND l.status=?`
		args = append(args, s)
	}
	if k := r.URL.Query().Get("kind"); k != "" {
		q += ` AND l.kind=?`
		args = append(args, k)
	}
	rows, err := a.m.db.QueryContext(r.Context(), q+` ORDER BY l.id DESC LIMIT 2000`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	now := time.Now().Unix()
	for rows.Next() {
		var id, planID, userID, days, maxInst, acts, created, activated, exp int64
		var key, kind, status, planName, email, notes string
		if rows.Scan(&id, &key, &kind, &status, &planID, &planName, &userID, &email, &days, &maxInst, &acts, &notes, &created, &activated, &exp) != nil {
			continue
		}
		if status == "active" && exp > 0 && exp < now {
			status = "expired"
		}
		out = append(out, map[string]any{"id": id, "key": key, "kind": kind, "status": status, "plan_id": planID, "plan_name": planName,
			"user_id": userID, "user_email": email, "days": days, "max_instances": maxInst, "activations": acts, "notes": notes,
			"created_at": created, "activated_at": activated, "expires_at": exp})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "licences": out})
}

func (a *API) adminCreateLicences(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PlanID       int64  `json:"plan_id"`
		Kind         string `json:"kind"`
		Days         int64  `json:"days"`
		Count        int    `json:"count"`
		MaxInstances int64  `json:"max_instances"`
		UserEmail    string `json:"user_email"`
		Notes        string `json:"notes"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx := r.Context()
	if _, err := getPlan(ctx, a.m.db, b.PlanID); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("choose a plan"))
		return
	}
	if b.Kind != "selfhosted" {
		b.Kind = "saas"
	}
	if b.Days <= 0 {
		b.Days = 30
	}
	b.Count = min(max(b.Count, 1), 500)
	b.MaxInstances = max(b.MaxInstances, 1)
	var userID int64
	if e := strings.TrimSpace(b.UserEmail); e != "" {
		if a.m.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email=?`, e).Scan(&userID) != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("no user with that email"))
			return
		}
		if b.Kind != "saas" {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("only SaaS licences can be assigned to a user"))
			return
		}
		b.Count = 1
	}
	var keys []map[string]any
	for i := 0; i < b.Count; i++ {
		key := newLicenceKey(b.Kind)
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO licences(lkey, kind, status, plan_id, days, max_instances, notes, created_at) VALUES(?,?,'unused',?,?,?,?,?)`,
			key, b.Kind, b.PlanID, b.Days, b.MaxInstances, truncate(b.Notes, 500), time.Now().Unix())
		if err != nil {
			a.fail(w, err)
			return
		}
		id, _ := res.LastInsertId()
		if userID > 0 {
			if _, err := a.lic.grant(ctx, userID, b.PlanID, b.Days, "", key); err != nil {
				a.fail(w, err)
				return
			}
		}
		keys = append(keys, map[string]any{"id": id, "key": key, "kind": b.Kind, "plan_id": b.PlanID, "days": b.Days})
	}
	audit(ctx, a.m.db, principalOf(r).UserID, clientIP(r), "licence.create", fmt.Sprintf("%d x %s plan=%d", b.Count, b.Kind, b.PlanID))
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "licences": keys})
}

func (a *API) adminUpdateLicence(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var status, kind string
	var exp, activated int64
	if a.m.db.QueryRowContext(ctx, `SELECT status, kind, expires_at, activated_at FROM licences WHERE id=?`, id).Scan(&status, &kind, &exp, &activated) != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("licence not found"))
		return
	}
	var b struct {
		Status     *string `json:"status"`
		ExtendDays int64   `json:"extend_days"`
		ExpiresAt  *int64  `json:"expires_at"`
		UserEmail  *string `json:"user_email"`
		Notes      *string `json:"notes"`
		MaxInst    *int64  `json:"max_instances"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if b.Status != nil {
		switch *b.Status {
		case "suspended":
			_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET status='suspended' WHERE id=?`, id)
		case "active":
			if activated == 0 {
				writeErr(w, http.StatusBadRequest, "bad_request", errors.New("an unused key is activated by redeeming or by assigning it to a user"))
				return
			}
			_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET status='active' WHERE id=?`, id)
		default:
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("status must be active or suspended"))
			return
		}
	}
	if b.ExtendDays != 0 {
		if exp == 0 { // unused key: change its duration
			_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET days=MAX(1, days+?) WHERE id=?`, b.ExtendDays, id)
		} else {
			base := max(exp, time.Now().Unix())
			_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET expires_at=?, status=CASE WHEN status='expired' THEN 'active' ELSE status END WHERE id=?`,
				base+b.ExtendDays*86400, id)
		}
	}
	if b.ExpiresAt != nil && exp != 0 {
		_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET expires_at=? WHERE id=?`, *b.ExpiresAt, id)
	}
	if b.Notes != nil {
		_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET notes=? WHERE id=?`, truncate(*b.Notes, 500), id)
	}
	if b.MaxInst != nil && kind == "selfhosted" {
		_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET max_instances=? WHERE id=?`, max(*b.MaxInst, 1), id)
	}
	if b.UserEmail != nil && kind == "saas" {
		var uid int64
		if a.m.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email=?`, strings.TrimSpace(*b.UserEmail)).Scan(&uid) != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("no user with that email"))
			return
		}
		if status == "unused" {
			var key string
			var planID, days int64
			_ = a.m.db.QueryRowContext(ctx, `SELECT lkey, plan_id, days FROM licences WHERE id=?`, id).Scan(&key, &planID, &days)
			if _, err := a.lic.grant(ctx, uid, planID, days, "", key); err != nil {
				a.fail(w, err)
				return
			}
		} else {
			_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET user_id=? WHERE id=?`, uid, id)
		}
	}
	audit(ctx, a.m.db, principalOf(r).UserID, clientIP(r), "licence.update", id)
	a.adminLicences(w, r)
}

func (a *API) adminDeleteLicence(w http.ResponseWriter, r *http.Request) {
	_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM licences WHERE id=?`, r.PathValue("id"))
	audit(r.Context(), a.m.db, principalOf(r).ActorID, clientIP(r), "licence.delete", r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) adminActivations(w http.ResponseWriter, r *http.Request) {
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT x.licence_id, l.lkey, x.instance_id, x.domain, x.version, x.ip, x.first_seen, x.last_seen
		FROM licence_activations x JOIN licences l ON l.id=x.licence_id ORDER BY x.last_seen DESC LIMIT 2000`)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var lid, first, last int64
		var key, iid, domain, ver, ip string
		if rows.Scan(&lid, &key, &iid, &domain, &ver, &ip, &first, &last) == nil {
			out = append(out, map[string]any{"licence_id": lid, "key": key, "instance_id": iid, "domain": domain, "version": ver, "ip": ip,
				"first_seen": first, "last_seen": last})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "activations": out})
}

func (a *API) adminLicenceServer(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "public_key": a.lic.publicKey(), "server_url": a.cfg.PublicURL,
		"key_file": filepath.Join(a.cfg.DataDir, "licence_ed25519.seed")})
}

// ---------- settings, inbox, audit ----------

func (a *API) adminSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": getSettings(r.Context(), a.m.db)})
}

func (a *API) adminSaveSettings(w http.ResponseWriter, r *http.Request) {
	var b map[string]any
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	for k, v := range b {
		if _, known := defaultSettings[k]; !known {
			continue
		}
		val := strings.TrimSpace(fmt.Sprint(v))
		if bv, ok := v.(bool); ok {
			val = strconv.FormatBool(bv)
		}
		if f, ok := v.(float64); ok {
			val = strconv.FormatFloat(f, 'f', -1, 64)
		}
		_ = setSetting(r.Context(), a.m.db, k, truncate(val, 2000))
	}
	audit(r.Context(), a.m.db, principalOf(r).ActorID, clientIP(r), "settings.update", "")
	a.adminSettings(w, r)
}

func (a *API) adminInbox(w http.ResponseWriter, r *http.Request) {
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT id, name, email, phone, message, ip, read, created_at FROM inbox ORDER BY id DESC LIMIT 500`)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, created int64
		var name, email, phone, msg, ip string
		var read int
		if rows.Scan(&id, &name, &email, &phone, &msg, &ip, &read, &created) == nil {
			out = append(out, map[string]any{"id": id, "name": name, "email": email, "phone": phone, "message": msg, "ip": ip, "read": read == 1, "created_at": created})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": out})
}

func (a *API) adminInboxRead(w http.ResponseWriter, r *http.Request) {
	_, _ = a.m.db.ExecContext(r.Context(), `UPDATE inbox SET read=1 WHERE id=?`, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) adminInboxDelete(w http.ResponseWriter, r *http.Request) {
	_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM inbox WHERE id=?`, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) adminAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT x.id, x.user_id, COALESCE(u.email,''), x.action, x.detail, x.ip, x.at FROM audit x
		LEFT JOIN users u ON u.id=x.user_id ORDER BY x.id DESC LIMIT ?`, limit)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, uid, at int64
		var email, action, detail, ip string
		if rows.Scan(&id, &uid, &email, &action, &detail, &ip, &at) == nil {
			out = append(out, map[string]any{"id": id, "user_id": uid, "user_email": email, "action": action, "detail": detail, "ip": ip, "at": at})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": out})
}
