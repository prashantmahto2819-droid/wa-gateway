package main

import (
	"net/http"
	"regexp"
	"strings"
)

// apiDoc lists the public API for /openapi.json (import into Postman, n8n, Insomnia…).
var apiDoc = [][4]string{
	{"GET", "/api/me", "Account", "Current user, licence and usage"},
	{"GET", "/api/licence", "Account", "Licence and usage"},
	{"POST", "/api/licence/redeem", "Account", "Redeem a licence key {key}"},
	{"GET", "/api/keys", "Developer", "List API keys"},
	{"POST", "/api/keys", "Developer", "Create API key {name}"},
	{"DELETE", "/api/keys/{id}", "Developer", "Revoke API key"},
	{"GET", "/api/webhooks", "Developer", "List webhooks"},
	{"POST", "/api/webhooks", "Developer", "Create webhook {url, events[]}"},
	{"PATCH", "/api/webhooks/{id}", "Developer", "Update webhook"},
	{"DELETE", "/api/webhooks/{id}", "Developer", "Delete webhook"},
	{"POST", "/api/webhooks/{id}/test", "Developer", "Send a test event"},
	{"GET", "/api/stats", "Account", "Daily sent/failed/received (?days=14)"},
	{"GET", "/api/sessions", "Numbers", "List numbers"},
	{"POST", "/api/sessions", "Numbers", "Add a number {label}"},
	{"GET", "/api/sessions/{name}", "Numbers", "Number details"},
	{"PATCH", "/api/sessions/{name}", "Numbers", "Update label, webhook_url, paused, proxy, settings"},
	{"DELETE", "/api/sessions/{name}", "Numbers", "Remove number"},
	{"POST", "/api/sessions/{name}/qr", "Numbers", "Start QR linking"},
	{"GET", "/api/sessions/{name}/qr", "Numbers", "Current QR (poll every 3s)"},
	{"POST", "/api/sessions/{name}/pair-code", "Numbers", "Link with a code {phone}"},
	{"POST", "/api/sessions/{name}/logout", "Numbers", "Unlink"},
	{"POST", "/api/sessions/{name}/reconnect", "Numbers", "Reconnect"},
	{"POST", "/api/sessions/{name}/proxy-test", "Numbers", "Test a proxy {proxy}"},
	{"POST", "/api/sessions/{name}/send", "Messages", "Send now: {to, text | media | poll | location | contact | menu}"},
	{"POST", "/api/sessions/{name}/react", "Messages", "React {chat, id, sender?, emoji}"},
	{"POST", "/api/sessions/{name}/edit", "Messages", "Edit {chat, id, text}"},
	{"POST", "/api/sessions/{name}/delete", "Messages", "Delete for everyone {chat, id, sender?}"},
	{"POST", "/api/sessions/{name}/status", "Messages", "Post WhatsApp Status {text | media, background?, status_audience?}"},
	{"POST", "/api/sessions/{name}/read", "Messages", "Mark read {chat, ids[]}"},
	{"POST", "/api/sessions/{name}/typing", "Messages", "Typing indicator {to, state}"},
	{"POST", "/api/sessions/{name}/check", "Messages", "Check numbers on WhatsApp {numbers[]}"},
	{"POST", "/api/sessions/{name}/queue", "Messages", "Queue bulk messages safely {messages[] | template+recipients[]}"},
	{"GET", "/api/sessions/{name}/queue", "Messages", "Queue counts for a number"},
	{"POST", "/api/queue", "Messages", "Queue over several numbers {pool[], …}"},
	{"GET", "/api/queue/{id}", "Messages", "One queued message"},
	{"GET", "/api/sessions/{name}/groups", "Groups", "Groups and communities"},
	{"POST", "/api/sessions/{name}/groups", "Groups", "Create group/community {name, participants[], community?}"},
	{"POST", "/api/sessions/{name}/groups/join", "Groups", "Join by invite link {link}"},
	{"GET", "/api/sessions/{name}/groups/{jid}", "Groups", "Group info and participants"},
	{"POST", "/api/sessions/{name}/groups/{jid}/participants", "Groups", "Add/remove/promote/demote {action, participants[]}"},
	{"GET", "/api/sessions/{name}/groups/{jid}/invite", "Groups", "Invite link (?reset=1)"},
	{"POST", "/api/sessions/{name}/groups/{jid}/settings", "Groups", "Name, topic, announce_only, locked"},
	{"POST", "/api/sessions/{name}/groups/{jid}/leave", "Groups", "Leave group"},
	{"POST", "/api/sessions/{name}/groups/{jid}/save-contacts", "Groups", "Save members as contacts {tag}"},
	{"GET", "/api/sessions/{name}/channels", "Channels", "Channels you follow/own"},
	{"POST", "/api/sessions/{name}/channels", "Channels", "Create channel {name, description}"},
	{"GET", "/api/sessions/{name}/channels/resolve", "Channels", "Channel from invite link (?invite=)"},
	{"GET", "/api/sessions/{name}/wa-contacts", "Contacts", "The linked phone's WhatsApp contacts (saved + unsaved), A to Z"},
	{"GET", "/api/contacts", "Contacts", "Search contacts (?q=&tag=&page=&limit=)"},
	{"POST", "/api/contacts", "Contacts", "Create/update by phone"},
	{"PATCH", "/api/contacts/{id}", "Contacts", "Update contact"},
	{"DELETE", "/api/contacts/{id}", "Contacts", "Delete contact"},
	{"POST", "/api/contacts/bulk", "Contacts", "Bulk tag/opt-out/delete {ids[], action, tag?}"},
	{"POST", "/api/contacts/import", "Contacts", "Import CSV (multipart file) or JSON rows"},
	{"GET", "/api/contacts/export.csv", "Contacts", "Export CSV"},
	{"GET", "/api/contacts/tags", "Contacts", "Tags with counts"},
	{"GET", "/api/optouts", "Contacts", "Opt-out list"},
	{"POST", "/api/optouts", "Contacts", "Add opt-outs {numbers[]}"},
	{"DELETE", "/api/optouts/{phone}", "Contacts", "Remove opt-out"},
	{"GET", "/api/media", "Media", "Media library"},
	{"POST", "/api/media", "Media", "Upload (multipart file)"},
	{"GET", "/api/media/{id}/file", "Media", "Download file"},
	{"DELETE", "/api/media/{id}", "Media", "Delete file"},
	{"GET", "/api/templates", "Templates", "List templates"},
	{"POST", "/api/templates", "Templates", "Create template {name, message}"},
	{"PATCH", "/api/templates/{id}", "Templates", "Update template"},
	{"DELETE", "/api/templates/{id}", "Templates", "Delete template"},
	{"GET", "/api/campaigns", "Campaigns", "List campaigns"},
	{"POST", "/api/campaigns", "Campaigns", "Create {name, sessions[], message, audience, schedule?, speed?}"},
	{"GET", "/api/campaigns/{id}", "Campaigns", "Campaign with stats"},
	{"PATCH", "/api/campaigns/{id}", "Campaigns", "Edit draft/scheduled campaign"},
	{"DELETE", "/api/campaigns/{id}", "Campaigns", "Delete campaign"},
	{"POST", "/api/campaigns/{id}/{action}", "Campaigns", "pause | resume | cancel | duplicate | run-now"},
	{"GET", "/api/campaigns/{id}/report.csv", "Campaigns", "Report CSV"},
	{"GET", "/api/autoreplies", "Automation", "Auto-reply rules"},
	{"POST", "/api/autoreplies", "Automation", "Create rule"},
	{"PATCH", "/api/autoreplies/{id}", "Automation", "Update rule"},
	{"DELETE", "/api/autoreplies/{id}", "Automation", "Delete rule"},
	{"GET", "/public/plans", "Public", "Public plans"},
	{"POST", "/public/contact", "Public", "Contact form"},
	{"GET", "/health", "Public", "Health check"},
}

var pathParam = regexp.MustCompile(`\{([a-z]+)\}`)

func (a *API) openAPI(w http.ResponseWriter, r *http.Request) {
	paths := map[string]map[string]any{}
	for _, d := range apiDoc {
		method, path, tag, summary := strings.ToLower(d[0]), d[1], d[2], d[3]
		op := map[string]any{"tags": []string{tag}, "summary": summary,
			"responses": map[string]any{"200": map[string]any{"description": "`{ok:true, …}`"}, "default": map[string]any{"description": "`{ok:false, code, error}`"}}}
		var params []map[string]any
		for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]string{"type": "string"}})
		}
		if params != nil {
			op["parameters"] = params
		}
		if method == "post" || method == "patch" {
			op["requestBody"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]string{"type": "object"}}}}
		}
		if strings.HasPrefix(path, "/public") || path == "/health" {
			op["security"] = []any{}
		}
		if paths[path] == nil {
			paths[path] = map[string]any{}
		}
		paths[path][method] = op
	}
	server := a.cfg.PublicURL
	if server == "" {
		scheme := "http"
		if isHTTPS(r) {
			scheme = "https"
		}
		server = scheme + "://" + r.Host
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{"title": "Vivid Relay API", "version": version,
			"description": "WhatsApp gateway API. Full guide: /docs. Errors: {ok:false, code, error}. Webhooks are signed with X-Signature: sha256=HMAC(secret, timestamp + '.' + body)."},
		"servers":    []map[string]string{{"url": server}},
		"components": map[string]any{"securitySchemes": map[string]any{"bearer": map[string]string{"type": "http", "scheme": "bearer", "description": "API key (vr_…)"}}},
		"security":   []map[string][]string{{"bearer": {}}},
		"paths":      paths,
	})
}
