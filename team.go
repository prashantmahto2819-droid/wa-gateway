package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ======================= workspaces, staff and roles =======================
//
// Every account (user) that owns numbers is a business workspace. The
// workspace id is the owner's user id, so everything that already keys on
// owner_id keeps working. Staff are users whose `members` row points at a
// workspace: when they log in, the request runs as the workspace (UserID =
// owner) with their own role; ActorID stays their own id for audit.
//
// Roles (workspace level), most to least power:
//   owner      everything, billing, delete workspace
//   admin      everything except billing / deleting the workspace
//   manager    numbers, campaigns, lists, kits, automations, exports; no staff / API keys / webhooks
//   supervisor manager minus number linking / deleting and settings
//   agent      send messages, view contacts and campaigns; no bulk / exports / automations
//   analyst    read only
//
// Permissions are checked on the server by route (see routePerms); the
// dashboard only hides what a role can't do.

var roleRank = map[string]int{"analyst": 1, "agent": 2, "supervisor": 3, "manager": 4, "admin": 5, "owner": 6}

// Permission keys used by handlers / routes → minimum role.
var perms = map[string]string{
	"read":        "analyst",
	"send":        "agent",
	"contacts":    "agent",
	"bulk":        "supervisor", // campaigns, lists, link publisher, status posts
	"automation":  "supervisor", // auto-replies, flows, kits
	"numbers":     "manager",    // link / unlink numbers, number settings, groups admin
	"export":      "manager",
	"settings":    "admin", // pacing, AI keys, templates of the workspace
	"integration": "admin", // API keys, webhooks, website registry
	"staff":       "admin",
	"billing":     "owner",
}

func (p *Principal) can(perm string) bool {
	if p == nil {
		return false
	}
	if p.Role == "superadmin" || p.Master {
		return true
	}
	need, ok := perms[perm]
	if !ok {
		return false
	}
	return roleRank[p.WorkspaceRole] >= roleRank[need]
}

type Member struct {
	UserID    int64  `json:"user_id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Team      string `json:"team"`
	Status    string `json:"status"`
	AddedAt   int64  `json:"added_at"`
	LastLogin int64  `json:"last_login"`
}

// workspaceOf returns the workspace (owner id) and role a user acts in.
func workspaceOf(ctx context.Context, db *sql.DB, userID int64) (owner int64, role string) {
	var o int64
	var r string
	err := db.QueryRowContext(ctx, `SELECT workspace_id, role FROM members WHERE user_id=? AND status='active'`, userID).Scan(&o, &r)
	if err != nil {
		return userID, "owner"
	}
	return o, r
}

func (a *API) requirePerm(perm string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !principalOf(r).can(perm) {
			writeErr(w, http.StatusForbidden, "forbidden", errors.New("your role can't do this; ask the workspace owner"))
			return
		}
		h(w, r)
	}
}

// ---------- API: members ----------

func (a *API) listMembers(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT m.user_id, u.name, u.email, m.role, m.team, m.status, m.added_at, u.last_login
		FROM members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? ORDER BY m.added_at`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Role, &m.Team, &m.Status, &m.AddedAt, &m.LastLogin) == nil {
			out = append(out, m)
		}
	}
	owner, _ := getUser(r.Context(), a.m.db, p.UserID)
	inv := []map[string]any{}
	irows, err := a.m.db.QueryContext(r.Context(), `SELECT id, name, email, role, team, expires_at FROM invites WHERE workspace_id=? AND used_at=0 AND expires_at>?`, p.UserID, time.Now().Unix())
	if err == nil {
		defer irows.Close()
		for irows.Next() {
			var id, exp int64
			var name, email, role, team string
			if irows.Scan(&id, &name, &email, &role, &team, &exp) == nil {
				inv = append(inv, map[string]any{"id": id, "name": name, "email": email, "role": role, "team": team, "expires_at": exp})
			}
		}
	}
	seats := a.lic.effective(r.Context(), p.UserID).Plan
	limit := int64(0)
	if seats != nil && !seats.Unlimited {
		limit = seats.Limit("staff")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "owner": owner, "members": out, "invites": inv, "seat_limit": limit,
		"roles": []string{"admin", "manager", "supervisor", "agent", "analyst"}, "perms": perms, "my_role": p.WorkspaceRole})
}

// createInvite makes a one-time link the owner shares on WhatsApp / email.
func (a *API) createInvite(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var b struct {
		Name, Email, Role, Team string
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	b.Role = strings.ToLower(strings.TrimSpace(b.Role))
	if roleRank[b.Role] == 0 || b.Role == "owner" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("role must be admin, manager, supervisor, agent or analyst"))
		return
	}
	if roleRank[b.Role] > roleRank[p.WorkspaceRole] && !p.Admin() {
		writeErr(w, http.StatusForbidden, "forbidden", errors.New("you can't invite a role above your own"))
		return
	}
	ctx := r.Context()
	if err := a.seatCheck(ctx, p.UserID, 1); err != nil {
		a.fail(w, err)
		return
	}
	token := randToken(24)
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	res, err := a.m.db.ExecContext(ctx, `INSERT INTO invites(workspace_id, token_hash, name, email, role, team, created_by, created_at, expires_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		p.UserID, sha(token), strings.TrimSpace(b.Name), strings.ToLower(strings.TrimSpace(b.Email)), b.Role, strings.TrimSpace(b.Team), p.ActorID, time.Now().Unix(), exp)
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	audit(ctx, a.m.db, p.ActorID, clientIP(r), "staff.invite", b.Email+" as "+b.Role)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "link": strings.TrimRight(a.cfg.PublicURL, "/") + "/join?token=" + token, "expires_at": exp})
}

func (a *API) deleteInvite(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM invites WHERE id=? AND workspace_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("invite not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// seatCheck enforces the plan's staff seats (owner not counted).
func (a *API) seatCheck(ctx context.Context, workspace int64, adding int) error {
	eff := a.lic.effective(ctx, workspace)
	if eff.Plan == nil || eff.Plan.Unlimited {
		return nil
	}
	limit := eff.Plan.Limit("staff")
	if limit <= 0 {
		return nil
	}
	var n int64
	_ = a.m.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM members WHERE workspace_id=? AND status='active') + (SELECT COUNT(*) FROM invites WHERE workspace_id=? AND used_at=0 AND expires_at>?)`,
		workspace, workspace, time.Now().Unix()).Scan(&n)
	if n+int64(adding) > limit {
		return &limitError{name: "staff"}
	}
	return nil
}

// countCheck enforces a plan "limits" entry (websites, flows, sequences,
// lists, renewal_types …) against the rows this workspace already has.
func (a *API) countCheck(ctx context.Context, workspace int64, name, table string) error {
	eff := a.lic.effective(ctx, workspace)
	if eff.Plan == nil || eff.Plan.Unlimited {
		return nil
	}
	limit := eff.Plan.Limit(name)
	if limit <= 0 {
		return nil
	}
	var n int64
	_ = a.m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE owner_id=?`, workspace).Scan(&n)
	if n+1 > limit {
		return &limitError{name: name}
	}
	return nil
}

// inviteInfo (public): who is inviting, for the join page.
func (a *API) inviteInfo(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	var ws int64
	var name, email, role string
	err := a.m.db.QueryRowContext(r.Context(), `SELECT workspace_id, name, email, role FROM invites WHERE token_hash=? AND used_at=0 AND expires_at>?`, sha(token), time.Now().Unix()).
		Scan(&ws, &name, &email, &role)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("this invite link is invalid or has expired"))
		return
	}
	owner, _ := getUser(r.Context(), a.m.db, ws)
	biz := ""
	if owner != nil {
		biz = owner.Name
	}
	if n := getSetting(r.Context(), a.m.db, "biz_name_"+strconv.FormatInt(ws, 10)); n != "" {
		biz = n
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "business": biz, "name": name, "email": email, "role": role})
}

// acceptInvite (public, CSRF header): creates the staff login and membership.
func (a *API) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Token, Name, Email, Password string
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx := r.Context()
	if !loginLimiter.allow("join:"+clientIP(r), 10, 15*time.Minute) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", errTooMany)
		return
	}
	var id, ws int64
	var invName, invEmail, role, team string
	err := a.m.db.QueryRowContext(ctx, `SELECT id, workspace_id, name, email, role, team FROM invites WHERE token_hash=? AND used_at=0 AND expires_at>?`, sha(b.Token), time.Now().Unix()).
		Scan(&id, &ws, &invName, &invEmail, &role, &team)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("this invite link is invalid or has expired"))
		return
	}
	email := strings.ToLower(strings.TrimSpace(firstNonEmpty(invEmail, b.Email)))
	if !strings.Contains(email, "@") {
		writeErr(w, http.StatusBadRequest, "bad_request", errBadEmail)
		return
	}
	if len(b.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "bad_request", errWeakPassword)
		return
	}
	name := strings.TrimSpace(firstNonEmpty(b.Name, invName))
	if err := a.seatCheck(ctx, ws, 0); err != nil {
		a.fail(w, err)
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(b.Password), bcrypt.DefaultCost)
	tx, err := a.m.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	var uid int64
	if tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email=?`, email).Scan(&uid) == nil {
		// An existing login joins the workspace (if it isn't an owner of numbers itself).
		var owns int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gw_sessions WHERE owner_id=?`, uid).Scan(&owns)
		var already int64
		if tx.QueryRowContext(ctx, `SELECT workspace_id FROM members WHERE user_id=?`, uid).Scan(&already) == nil && already != ws {
			writeErr(w, http.StatusConflict, "conflict", errors.New("this email already belongs to another workspace"))
			return
		}
		if owns > 0 {
			writeErr(w, http.StatusConflict, "conflict", errors.New("this email owns its own account; use a different email for staff"))
			return
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET pass_hash=?, name=? WHERE id=?`, string(hash), name, uid); err != nil {
			a.fail(w, err)
			return
		}
	} else {
		res, err := tx.ExecContext(ctx, `INSERT INTO users(email, name, pass_hash, role, status, created_at) VALUES(?,?,?,'user','active',?)`, email, name, string(hash), now)
		if err != nil {
			a.fail(w, err)
			return
		}
		uid, _ = res.LastInsertId()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO members(workspace_id, user_id, role, team, status, added_at) VALUES(?,?,?,?,'active',?)
		ON CONFLICT(user_id) DO UPDATE SET role=excluded.role, team=excluded.team, status='active'`, ws, uid, role, team, now); err != nil {
		a.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invites SET used_at=?, used_by=? WHERE id=?`, now, uid, id); err != nil {
		a.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	audit(ctx, a.m.db, uid, clientIP(r), "staff.join", email+" as "+role)
	if u, err := getUser(ctx, a.m.db, uid); err == nil {
		a.startLogin(w, r, u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) updateMember(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	uid, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var b struct {
		Role   *string `json:"role"`
		Team   *string `json:"team"`
		Status *string `json:"status"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx := r.Context()
	var cur string
	if a.m.db.QueryRowContext(ctx, `SELECT role FROM members WHERE workspace_id=? AND user_id=?`, p.UserID, uid).Scan(&cur) != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("staff member not found"))
		return
	}
	if roleRank[cur] > roleRank[p.WorkspaceRole] && !p.Admin() {
		writeErr(w, http.StatusForbidden, "forbidden", errors.New("you can't change a role above your own"))
		return
	}
	if b.Role != nil {
		rr := strings.ToLower(*b.Role)
		if roleRank[rr] == 0 || rr == "owner" || (roleRank[rr] > roleRank[p.WorkspaceRole] && !p.Admin()) {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("invalid role"))
			return
		}
		_, _ = a.m.db.ExecContext(ctx, `UPDATE members SET role=? WHERE user_id=?`, rr, uid)
	}
	if b.Team != nil {
		_, _ = a.m.db.ExecContext(ctx, `UPDATE members SET team=? WHERE user_id=?`, strings.TrimSpace(*b.Team), uid)
	}
	if b.Status != nil {
		st := "active"
		if *b.Status != "active" {
			st = "disabled"
			_, _ = a.m.db.ExecContext(ctx, `DELETE FROM auth_tokens WHERE user_id=?`, uid) // log them out
		}
		_, _ = a.m.db.ExecContext(ctx, `UPDATE members SET status=? WHERE user_id=?`, st, uid)
	}
	audit(ctx, a.m.db, p.ActorID, clientIP(r), "staff.update", strconv.FormatInt(uid, 10))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) removeMember(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	uid, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ctx := r.Context()
	res, err := a.m.db.ExecContext(ctx, `DELETE FROM members WHERE workspace_id=? AND user_id=?`, p.UserID, uid)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("staff member not found"))
		return
	}
	_, _ = a.m.db.ExecContext(ctx, `DELETE FROM auth_tokens WHERE user_id=?`, uid)
	_, _ = a.m.db.ExecContext(ctx, `DELETE FROM api_keys WHERE user_id=?`, uid)
	audit(ctx, a.m.db, p.ActorID, clientIP(r), "staff.remove", strconv.FormatInt(uid, 10))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
