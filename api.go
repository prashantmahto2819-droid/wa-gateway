package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	lic     *Licensing
	started time.Time
}

func (a *API) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)

	// Website, dashboard and static files
	for path := range sitePages {
		mux.HandleFunc("GET "+path+map[bool]string{true: "{$}", false: ""}[path == "/"], a.sitePage)
	}
	mux.HandleFunc("GET /app", a.appShell)
	mux.HandleFunc("GET /app/", a.appShell)
	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { a.renderPage(w, r, "404", http.StatusNotFound) })

	// Public JSON
	mux.HandleFunc("GET /public/plans", a.publicPlans)
	mux.HandleFunc("GET /public/settings", a.publicSettings)
	mux.HandleFunc("POST /public/contact", limitBody(a.publicContact))
	mux.HandleFunc("POST /licence/v1/activate", limitBody(a.licenceActivate))
	mux.HandleFunc("GET /openapi.json", a.openAPI)

	// Accounts
	mux.HandleFunc("GET /auth/state", a.authState)
	mux.HandleFunc("POST /auth/setup", limitBody(a.authSetup))
	mux.HandleFunc("POST /auth/signup", limitBody(a.authSignup))
	mux.HandleFunc("POST /auth/login", limitBody(a.authLogin))
	mux.HandleFunc("POST /auth/logout", a.authLogout)

	auth := a.auth
	admin := func(h http.HandlerFunc) http.HandlerFunc { return a.auth(a.adminOnly(h)) }
	server := func(h http.HandlerFunc) http.HandlerFunc { return a.auth(a.serverOnly(h)) }

	mux.HandleFunc("GET /api/me", auth(a.me))
	mux.HandleFunc("PATCH /api/me", auth(a.updateMe))
	mux.HandleFunc("POST /api/me/password", auth(a.changePassword))
	mux.HandleFunc("GET /api/me/ai", auth(a.getAI))
	mux.HandleFunc("GET /api/me/pacing", auth(a.getMyPacing))
	mux.HandleFunc("PUT /api/me/pacing", auth(a.requirePerm("settings", a.saveMyPacing)))
	mux.HandleFunc("PATCH /api/me/ai", auth(a.requirePerm("settings", a.setAI)))
	mux.HandleFunc("POST /api/me/ai/test", auth(a.requirePerm("settings", a.testAI)))
	mux.HandleFunc("GET /api/licence", auth(a.licenceInfo))
	mux.HandleFunc("POST /api/licence/redeem", auth(a.requirePerm("settings", a.redeemLicence)))
	mux.HandleFunc("GET /api/keys", auth(a.requirePerm("integration", a.listKeys)))
	mux.HandleFunc("POST /api/keys", auth(a.requirePerm("integration", a.createKey)))
	mux.HandleFunc("DELETE /api/keys/{id}", auth(a.requirePerm("integration", a.deleteKey)))
	mux.HandleFunc("GET /api/webhooks", auth(a.requirePerm("integration", a.listWebhooks)))
	mux.HandleFunc("POST /api/webhooks", auth(a.requirePerm("integration", a.createWebhook)))
	mux.HandleFunc("PATCH /api/webhooks/{id}", auth(a.requirePerm("integration", a.updateWebhook)))
	mux.HandleFunc("DELETE /api/webhooks/{id}", auth(a.requirePerm("integration", a.deleteWebhook)))
	mux.HandleFunc("POST /api/webhooks/{id}/test", auth(a.requirePerm("integration", a.testWebhook)))
	mux.HandleFunc("GET /api/stats", auth(a.stats))

	// Staff (workspace members) and invites
	mux.HandleFunc("GET /api/team", auth(a.listMembers))
	mux.HandleFunc("POST /api/team/invites", auth(a.requirePerm("staff", a.createInvite)))
	mux.HandleFunc("DELETE /api/team/invites/{id}", auth(a.requirePerm("staff", a.deleteInvite)))
	mux.HandleFunc("PATCH /api/team/{id}", auth(a.requirePerm("staff", a.updateMember)))
	mux.HandleFunc("DELETE /api/team/{id}", auth(a.requirePerm("staff", a.removeMember)))
	mux.HandleFunc("GET /auth/invite", a.inviteInfo)
	mux.HandleFunc("POST /auth/join", limitBody(func(w http.ResponseWriter, r *http.Request) {
		if requireHeader(w, r) {
			a.acceptInvite(w, r)
		}
	}))

	// Numbers
	mux.HandleFunc("GET /api/sessions", auth(a.listSessions))
	mux.HandleFunc("POST /api/sessions", auth(a.requirePerm("numbers", a.createSession)))
	mux.HandleFunc("GET /api/sessions/{name}", auth(a.getSession))
	mux.HandleFunc("PATCH /api/sessions/{name}", auth(a.requirePerm("numbers", a.updateSession)))
	mux.HandleFunc("DELETE /api/sessions/{name}", auth(a.requirePerm("numbers", a.deleteSession)))
	mux.HandleFunc("POST /api/sessions/{name}/qr", auth(a.requirePerm("numbers", a.startQR)))
	mux.HandleFunc("GET /api/sessions/{name}/qr", auth(a.requirePerm("numbers", a.getQR)))
	mux.HandleFunc("POST /api/sessions/{name}/pair-code", auth(a.requirePerm("numbers", a.pairCode)))
	mux.HandleFunc("POST /api/sessions/{name}/logout", auth(a.requirePerm("numbers", a.logout)))
	mux.HandleFunc("POST /api/sessions/{name}/reconnect", auth(a.requirePerm("numbers", a.reconnect)))
	mux.HandleFunc("POST /api/sessions/{name}/proxy-test", auth(a.requirePerm("numbers", a.proxyTest)))

	// Sending
	mux.HandleFunc("POST /api/sessions/{name}/send", auth(a.requirePerm("send", a.send)))
	mux.HandleFunc("POST /api/sessions/{name}/react", auth(a.requirePerm("send", func(w http.ResponseWriter, r *http.Request) { a.msgAction(w, r, "react") })))
	mux.HandleFunc("POST /api/sessions/{name}/edit", auth(a.requirePerm("send", func(w http.ResponseWriter, r *http.Request) { a.msgAction(w, r, "edit") })))
	mux.HandleFunc("POST /api/sessions/{name}/delete", auth(a.requirePerm("send", func(w http.ResponseWriter, r *http.Request) { a.msgAction(w, r, "delete") })))
	mux.HandleFunc("POST /api/sessions/{name}/status", auth(a.requirePerm("bulk", a.postStatus)))
	mux.HandleFunc("POST /api/sessions/{name}/read", auth(a.requirePerm("send", a.markRead)))
	mux.HandleFunc("POST /api/sessions/{name}/typing", auth(a.requirePerm("send", a.typing)))
	mux.HandleFunc("POST /api/sessions/{name}/check", auth(a.requirePerm("send", a.check)))
	mux.HandleFunc("POST /api/sessions/{name}/queue", auth(a.requirePerm("bulk", a.enqueue)))
	mux.HandleFunc("GET /api/sessions/{name}/queue", auth(a.queueStats))
	mux.HandleFunc("POST /api/queue", auth(a.requirePerm("bulk", a.enqueuePool)))
	mux.HandleFunc("GET /api/queue/{id}", auth(a.queueItem))
	mux.HandleFunc("GET /api/queue/campaigns/{campaign}", auth(a.campaignStatsByLabel))
	mux.HandleFunc("POST /api/queue/campaigns/{campaign}/cancel", auth(a.requirePerm("bulk", a.cancelCampaignByLabel)))

	// Groups, communities, channels
	mux.HandleFunc("GET /api/sessions/{name}/groups", auth(a.groups))
	mux.HandleFunc("POST /api/sessions/{name}/groups", auth(a.requirePerm("numbers", a.createGroup)))
	mux.HandleFunc("POST /api/sessions/{name}/groups/join", auth(a.requirePerm("numbers", a.joinGroup)))
	mux.HandleFunc("GET /api/sessions/{name}/groups/{jid}", auth(a.groupInfo))
	mux.HandleFunc("POST /api/sessions/{name}/groups/{jid}/participants", auth(a.requirePerm("numbers", a.groupParticipants)))
	mux.HandleFunc("GET /api/sessions/{name}/groups/{jid}/invite", auth(a.requirePerm("numbers", a.groupInvite)))
	mux.HandleFunc("POST /api/sessions/{name}/groups/{jid}/settings", auth(a.requirePerm("numbers", a.groupSettings)))
	mux.HandleFunc("POST /api/sessions/{name}/groups/{jid}/leave", auth(a.requirePerm("numbers", a.leaveGroup)))
	mux.HandleFunc("POST /api/sessions/{name}/groups/{jid}/save-contacts", auth(a.requirePerm("bulk", a.saveGroupContacts)))
	mux.HandleFunc("GET /api/sessions/{name}/channels", auth(a.channels))
	mux.HandleFunc("GET /api/sessions/{name}/wa-contacts", auth(a.waContacts))
	mux.HandleFunc("GET /api/sessions/{name}/status-prep", auth(a.statusPrepInfo))
	mux.HandleFunc("POST /api/sessions/{name}/status-prep", auth(a.requirePerm("bulk", a.statusPrepStart)))
	mux.HandleFunc("GET /api/link-preview", auth(a.previewTest))
	mux.HandleFunc("POST /api/sessions/{name}/channels", auth(a.requirePerm("numbers", a.createChannel)))
	mux.HandleFunc("GET /api/sessions/{name}/channels/resolve", auth(a.resolveChannel))

	// CRM, media, templates, campaigns, automation
	mux.HandleFunc("GET /api/lists", auth(a.listLists))
	mux.HandleFunc("POST /api/lists", auth(a.requirePerm("bulk", a.saveList)))
	mux.HandleFunc("GET /api/lists/{id}", auth(a.getList))
	mux.HandleFunc("PATCH /api/lists/{id}", auth(a.requirePerm("bulk", a.saveList)))
	mux.HandleFunc("DELETE /api/lists/{id}", auth(a.requirePerm("bulk", a.deleteList)))
	mux.HandleFunc("POST /api/sessions/{name}/members-to-list", auth(a.requirePerm("bulk", a.groupMembersToList)))
	mux.HandleFunc("GET /api/contacts", auth(a.listContacts))
	mux.HandleFunc("POST /api/contacts", auth(a.requirePerm("send", a.saveContact)))
	mux.HandleFunc("GET /api/contacts/tags", auth(a.contactTags))
	mux.HandleFunc("GET /api/contacts/export.csv", auth(a.requirePerm("export", a.exportContacts)))
	mux.HandleFunc("POST /api/contacts/import", auth(a.requirePerm("contacts", a.importContacts)))
	mux.HandleFunc("POST /api/contacts/bulk", auth(a.requirePerm("contacts", a.bulkContacts)))
	mux.HandleFunc("PATCH /api/contacts/{id}", auth(a.requirePerm("send", a.saveContact)))
	mux.HandleFunc("DELETE /api/contacts/{id}", auth(a.requirePerm("contacts", a.deleteContact)))
	mux.HandleFunc("GET /api/optouts", auth(a.listOptouts))
	mux.HandleFunc("POST /api/optouts", auth(a.requirePerm("contacts", a.addOptouts)))
	mux.HandleFunc("DELETE /api/optouts/{phone}", auth(a.requirePerm("contacts", a.removeOptout)))
	mux.HandleFunc("GET /api/media", auth(a.listMedia))
	mux.HandleFunc("POST /api/media", auth(a.requirePerm("contacts", a.uploadMedia)))
	mux.HandleFunc("GET /api/media/{id}/file", auth(a.mediaFile))
	mux.HandleFunc("DELETE /api/media/{id}", auth(a.requirePerm("contacts", a.deleteMedia)))
	mux.HandleFunc("GET /api/templates", auth(a.listTemplates))
	mux.HandleFunc("POST /api/templates", auth(a.requirePerm("automation", a.saveTemplate)))
	mux.HandleFunc("PATCH /api/templates/{id}", auth(a.requirePerm("automation", a.saveTemplate)))
	mux.HandleFunc("DELETE /api/templates/{id}", auth(a.requirePerm("automation", a.deleteTemplate)))
	mux.HandleFunc("GET /api/campaigns", auth(a.listCampaigns))
	mux.HandleFunc("POST /api/campaigns", auth(a.requirePerm("bulk", a.createCampaign)))
	mux.HandleFunc("GET /api/campaigns/{id}", auth(a.getCampaign))
	mux.HandleFunc("PATCH /api/campaigns/{id}", auth(a.requirePerm("bulk", a.updateCampaign)))
	mux.HandleFunc("DELETE /api/campaigns/{id}", auth(a.requirePerm("bulk", a.deleteCampaign)))
	mux.HandleFunc("POST /api/campaigns/{id}/{action}", auth(a.requirePerm("bulk", a.campaignAction)))
	mux.HandleFunc("GET /api/campaigns/{id}/report.csv", auth(a.requirePerm("export", a.campaignReport)))
	mux.HandleFunc("GET /api/autoreplies", auth(a.listAutoReplies))
	mux.HandleFunc("POST /api/autoreplies", auth(a.requirePerm("automation", a.saveAutoReply)))
	mux.HandleFunc("PATCH /api/autoreplies/{id}", auth(a.requirePerm("automation", a.saveAutoReply)))
	mux.HandleFunc("DELETE /api/autoreplies/{id}", auth(a.requirePerm("automation", a.deleteAutoReply)))

	// Admin
	mux.HandleFunc("GET /api/admin/overview", admin(a.adminOverview))
	mux.HandleFunc("GET /api/admin/users", admin(a.adminUsers))
	mux.HandleFunc("PATCH /api/admin/users/{id}", admin(a.adminUpdateUser))
	mux.HandleFunc("POST /api/admin/users/{id}/reset-password", admin(a.adminResetPassword))
	mux.HandleFunc("POST /api/admin/users/{id}/grant", server(a.adminGrant))
	mux.HandleFunc("DELETE /api/admin/users/{id}", admin(a.adminDeleteUser))
	mux.HandleFunc("GET /api/admin/plans", server(a.adminPlans))
	mux.HandleFunc("POST /api/admin/plans", server(a.adminSavePlan))
	mux.HandleFunc("PATCH /api/admin/plans/{id}", server(a.adminSavePlan))
	mux.HandleFunc("DELETE /api/admin/plans/{id}", server(a.adminDeletePlan))
	mux.HandleFunc("GET /api/admin/licences", server(a.adminLicences))
	mux.HandleFunc("POST /api/admin/licences", server(a.adminCreateLicences))
	mux.HandleFunc("PATCH /api/admin/licences/{id}", server(a.adminUpdateLicence))
	mux.HandleFunc("DELETE /api/admin/licences/{id}", server(a.adminDeleteLicence))
	mux.HandleFunc("GET /api/admin/activations", server(a.adminActivations))
	mux.HandleFunc("GET /api/admin/licence-server", server(a.adminLicenceServer))
	mux.HandleFunc("GET /api/admin/settings", admin(a.adminSettings))
	mux.HandleFunc("GET /api/admin/pacing", admin(a.getAdminPacing))
	mux.HandleFunc("PUT /api/admin/pacing", admin(a.saveAdminPacing))
	mux.HandleFunc("PATCH /api/admin/settings", admin(a.adminSaveSettings))
	mux.HandleFunc("GET /api/admin/inbox", admin(a.adminInbox))
	mux.HandleFunc("POST /api/admin/inbox/{id}/read", admin(a.adminInboxRead))
	mux.HandleFunc("DELETE /api/admin/inbox/{id}", admin(a.adminInboxDelete))
	mux.HandleFunc("GET /api/admin/audit", admin(a.adminAudit))
	return securityHeaders(mux)
}

func limitBody(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		h(w, r)
	}
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.ServeHTTP(w, r)
	})
}

// auth lets in the master key, user API keys and logged-in browsers.
func (a *API) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := a.resolvePrincipal(r)
		if err != nil {
			writeErr(w, http.StatusForbidden, "csrf", err)
			return
		}
		if p == nil {
			time.Sleep(200 * time.Millisecond) // slow down guessing
			writeErr(w, http.StatusUnauthorized, "unauthorized", errors.New("log in or send a valid API key"))
			return
		}
		if p.ViaKey && !p.Admin() {
			if err := a.lic.require(r.Context(), p.UserID, "api"); err != nil {
				a.fail(w, err)
				return
			}
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		}
		h(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
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
	case errors.Is(err, errLicenceRequired):
		return http.StatusPaymentRequired, "licence_required"
	case errors.Is(err, errLicenceExpired):
		return http.StatusPaymentRequired, "licence_expired"
	case errors.Is(err, errFeatureLocked):
		return http.StatusForbidden, "feature_locked"
	case errors.Is(err, errLimitReached):
		return http.StatusTooManyRequests, "limit_reached"
	case errors.Is(err, errForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, errDupStatus):
		return http.StatusConflict, "duplicate_status"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout"
	case isPermanent(err):
		return http.StatusBadRequest, "bad_request"
	}
	msg := err.Error()
	if strings.Contains(msg, "SQL") || strings.Contains(msg, "sqlite") || strings.Contains(msg, "constraint") || strings.Contains(msg, "database") {
		return http.StatusInternalServerError, "server_error"
	}
	return http.StatusBadGateway, "whatsapp_error"
}

func (a *API) fail(w http.ResponseWriter, err error) {
	code, kind := errStatus(err)
	var le *limitError
	if errors.As(err, &le) {
		writeJSON(w, code, map[string]any{"ok": false, "code": kind, "limit": le.name, "error": err.Error()})
		return
	}
	if kind == "server_error" {
		a.m.log.Error("request failed", "err", err)
		err = errors.New("something went wrong on the server; please try again")
	}
	writeErr(w, code, kind, err)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

// session returns the number in the URL if the caller may use it.
func (a *API) session(w http.ResponseWriter, r *http.Request) *Session {
	s, err := a.m.get(r.PathValue("name"))
	if err == nil {
		if p := principalOf(r); p != nil && !p.Admin() && s.owner() != p.UserID {
			err = errNoSession // don't reveal other customers' numbers
		}
	}
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
		"today": map[string]any{"sent": sent, "limit": s.effectiveDailyLimit(ctx, r.pairedAt), "new_contacts": sentNew, "new_contact_limit": a.cfg.NewContactLimit},
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
	var label string
	_ = a.m.db.QueryRowContext(ctx, `SELECT label FROM gw_sessions WHERE name=?`, s.name).Scan(&label)
	if label == "" {
		label = s.name
	}
	info["label"], info["owner_id"], info["settings"] = label, s.owner(), s.settings(ctx)
	return info
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, s := range a.m.listFor(principalOf(r)) {
		out = append(out, a.sessionInfo(r.Context(), s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": out})
}

func (a *API) createSession(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var body struct {
		Name       string `json:"name"`
		Label      string `json:"label"`
		WebhookURL string `json:"webhook_url"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if err := a.lic.checkLimit(r.Context(), p.UserID, "numbers", 1); err != nil {
		a.fail(w, err)
		return
	}
	if body.WebhookURL != "" && !validWebhookURL(body.WebhookURL) {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("webhook_url must be an http(s) address"))
		return
	}
	label := truncate(strings.TrimSpace(body.Label), 60)
	name := strings.ToLower(strings.TrimSpace(body.Name))
	if name == "" || !p.Admin() {
		// Customers get unique internal names; the label is what they see.
		slug := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(firstNonEmpty(label, name, "number")), "-"), "-._")
		slug = strings.ReplaceAll(slug, ".", "-")
		if len(slug) > 16 {
			slug = slug[:16]
		}
		if slug == "" {
			slug = "number"
		}
		name = fmt.Sprintf("u%d-%s", p.UserID, slug)
		for i := 2; ; i++ {
			if _, err := a.m.get(name); err != nil {
				break
			}
			name = fmt.Sprintf("u%d-%s-%d", p.UserID, slug, i)
		}
	}
	if label == "" {
		label = name
	}
	s, err := a.m.create(name, label, p.UserID, strings.TrimSpace(body.WebhookURL))
	if err != nil {
		a.fail(w, err)
		return
	}
	audit(r.Context(), a.m.db, p.ActorID, clientIP(r), "number.create", name)
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
		WebhookURL *string          `json:"webhook_url"`
		Paused     *bool            `json:"paused"`
		Proxy      *string          `json:"proxy"` // "" removes it
		Label      *string          `json:"label"`
		Settings   *SessionSettings `json:"settings"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx := r.Context()
	if body.Label != nil && strings.TrimSpace(*body.Label) != "" {
		_, _ = a.m.db.ExecContext(ctx, `UPDATE gw_sessions SET label=? WHERE name=?`, truncate(strings.TrimSpace(*body.Label), 60), s.name)
	}
	if body.Settings != nil {
		st := *body.Settings
		st.RejectMessage = truncate(strings.TrimSpace(st.RejectMessage), 500)
		st.TakeoverMinutes = min(max(st.TakeoverMinutes, 0), 24*60)
		_, _ = a.m.db.ExecContext(ctx, `UPDATE gw_sessions SET settings=? WHERE name=?`, jsonText(st), s.name)
		if cli, err := s.ready(); err == nil {
			if st.AlwaysOnline {
				_ = cli.SendPresence(ctx, types.PresenceAvailable)
			} else {
				_ = cli.SendPresence(ctx, types.PresenceUnavailable)
			}
		}
	}
	if body.WebhookURL != nil && *body.WebhookURL != "" && !validWebhookURL(*body.WebhookURL) {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("webhook_url must be an http(s) address"))
		return
	}
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
		p, err := vetProxy(ctx, *body.Proxy, a.cfg.AllowPrivateURLs)
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
	s := a.session(w, r)
	if s == nil {
		return
	}
	audit(r.Context(), a.m.db, principalOf(r).ActorID, clientIP(r), "number.delete", s.name)
	if err := a.m.remove(r.Context(), s.name); err != nil {
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
	Random    bool     `json:"random"`     // pool: pick a random number per person (default: keep each person on one number)
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
	p := principalOf(r)
	seen := map[string]bool{}
	var clean []string
	for _, n := range pool {
		n = strings.TrimSpace(n)
		if seen[n] {
			continue
		}
		s, err := a.m.get(n)
		if err != nil || (!p.Admin() && s.owner() != p.UserID) {
			writeErr(w, http.StatusNotFound, "session_not_found", errors.New("unknown session in pool: "+n))
			return
		}
		seen[n] = true
		clean = append(clean, n)
	}
	pool = clean
	if err := a.lic.require(ctx, p.UserID, "campaigns"); err != nil {
		a.fail(w, err)
		return
	}
	if len(pool) > 1 {
		if err := a.lic.require(ctx, p.UserID, "pool"); err != nil {
			a.fail(w, err)
			return
		}
	}
	if body.SendAfter > time.Now().Unix()+60 {
		if err := a.lic.require(ctx, p.UserID, "scheduler"); err != nil {
			a.fail(w, err)
			return
		}
		if body.SendAfter > time.Now().AddDate(1, 0, 0).Unix() {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("send_after must be within one year"))
			return
		}
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
	for i := range msgs {
		if err := msgs[i].validate(); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("message "+itoa(i)+": "+err.Error()))
			return
		}
		if _, err := parseTarget(msgs[i].To, a.cfg.DefaultCountryCode); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("message "+itoa(i)+": "+err.Error()))
			return
		}
		if msgs[i].Media != nil && msgs[i].Media.Base64 != "" {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("message "+itoa(i)+": queued messages must use media.url or media.media_id, not base64"))
			return
		}
	}
	owner := p.UserID
	if s, err := a.m.get(pool[0]); err == nil {
		owner = s.owner()
	}
	rot := ""
	if body.Random {
		rot = "random"
	}
	res, err := a.m.enqueue(ctx, owner, pool, 0, body.Campaign, msgs, body.SendAfter, 1, rot)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := map[string]any{"ok": true, "queued": res.Queued, "skipped_opted_out": res.Skipped, "skipped_duplicates": res.Dupes,
		"per_session": res.PerSession, "campaign": body.Campaign}
	if len(res.IDs) > 0 {
		out["first_id"], out["last_id"] = res.IDs[0], res.IDs[len(res.IDs)-1]
	}
	if len(res.IDs) <= 100 {
		out["ids"] = res.IDs
	}
	writeJSON(w, http.StatusAccepted, out)
}

func itoa(i int) string { return strconv.Itoa(i) }

func statusCounts(ctx context.Context, db *sql.DB, where string, arg any) map[string]int {
	out := map[string]int{"queued": 0, "sending": 0, "sent": 0, "failed": 0, "skipped": 0, "cancelled": 0, "paused": 0}
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
	p := principalOf(r)
	q := `SELECT id, session, campaign, to_addr, status, error, wa_id, attempts, created_at, sent_at FROM gw_outbox WHERE id=?`
	args := []any{r.PathValue("id")}
	if !p.Admin() {
		q += ` AND owner_id=?`
		args = append(args, p.UserID)
	}
	err := a.m.db.QueryRowContext(r.Context(), q, args...).Scan(&it.ID, &it.Session, &it.Campaign, &it.To, &it.Status, &it.Error, &it.WaID, &it.Attempts, &it.CreatedAt, &it.SentAt)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("queue item not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": map[string]any{"id": it.ID, "session": it.Session, "campaign": it.Campaign,
		"to": it.To, "status": it.Status, "error": it.Error, "wa_id": it.WaID, "attempts": it.Attempts, "created_at": it.CreatedAt, "sent_at": it.SentAt}})
}

func (a *API) campaignStatsByLabel(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	where, arg := "campaign=? AND owner_id="+strconv.FormatInt(p.UserID, 10), any(r.PathValue("campaign"))
	if p.Admin() {
		where = "campaign=?"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "campaign": r.PathValue("campaign"),
		"counts": statusCounts(r.Context(), a.m.db, where, arg)})
}

func (a *API) cancelCampaignByLabel(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := `UPDATE gw_outbox SET status='cancelled' WHERE campaign=? AND status='queued'`
	args := []any{r.PathValue("campaign")}
	if !p.Admin() {
		q += ` AND owner_id=?`
		args = append(args, p.UserID)
	}
	res, err := a.m.db.ExecContext(r.Context(), q, args...)
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
			"announce_only": g.IsAnnounce, "i_am_admin": admin,
			// Only admins may post where "only admins can send" is on (always so in community announcements).
			"can_post": kind != "community" && (!g.IsAnnounce || admin)}
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
	p, err := vetProxy(r.Context(), raw, a.cfg.AllowPrivateURLs)
	if err != nil || p == "" {
		if err == nil {
			err = errors.New("no valid proxy to test")
		}
		writeErr(w, http.StatusBadRequest, "bad_proxy", err)
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
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT phone, keyword, session, at FROM optouts WHERE owner_id=? ORDER BY at DESC LIMIT 5000`, p.UserID)
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
	p := principalOf(r)
	var body struct {
		Numbers []string `json:"numbers"`
	}
	if err := decode(r, &body); err != nil || len(body.Numbers) == 0 || len(body.Numbers) > 10000 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("numbers (1-10000) is required"))
		return
	}
	added := 0
	for _, n := range body.Numbers {
		j, err := parseTarget(n, a.cfg.DefaultCountryCode)
		if err != nil || j.Server != types.DefaultUserServer {
			continue
		}
		if addOptout(r.Context(), a.m.db, p.UserID, j.User, "manual", "") {
			added++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added})
}

func (a *API) removeOptout(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	j, err := parseTarget(r.PathValue("phone"), a.cfg.DefaultCountryCode)
	if err != nil {
		a.fail(w, err)
		return
	}
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM optouts WHERE owner_id=? AND phone=?`, p.UserID, digitsOf(j.String()))
	if err != nil {
		a.fail(w, err)
		return
	}
	n, _ := res.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": n})
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
