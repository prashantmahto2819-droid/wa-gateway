package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	gopath "path"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type sitePage struct {
	file, title, desc string
}

var sitePages = map[string]sitePage{
	"/":         {"home.html", "", "Send safe WhatsApp campaigns, schedule posts to groups, channels and Status, auto-reply and connect your apps with a simple API."},
	"/features": {"features.html", "Features", "Everything Vivid Relay can do: multi-number linking, safe campaigns, scheduler, groups, channels, polls, auto-replies, CRM, API and webhooks."},
	"/pricing":  {"pricing.html", "Pricing", "Simple plans for WhatsApp campaigns and automation. Self-hosted licences available."},
	"/about":    {"about.html", "About", "About Vivid Relay."},
	"/contact":  {"contact.html", "Contact", "Talk to us about plans, licences and setup."},
	"/privacy":  {"privacy.html", "Privacy policy", "How we handle your data."},
	"/terms":    {"terms.html", "Terms of service", "Terms for using Vivid Relay."},
	"/refund":   {"refund.html", "Refund policy", "Refunds and cancellations."},
	"/docs":     {"docs.html", "API docs", "Developer quickstart: send messages, queue campaigns and verify webhooks."},
	"/login":    {"login.html", "Log in", "Log in to your Vivid Relay dashboard."},
	"/signup":   {"signup.html", "Start free trial", "Create your Vivid Relay account."},
	"/join":     {"join.html", "Join your team", "Accept an invite to a Vivid Relay workspace."},
	"/setup":    {"setup.html", "First-time setup", "Create the owner account."},
}

var tmplCache sync.Map // file -> *template.Template

func siteFuncs() template.FuncMap {
	return template.FuncMap{
		"money": func(price float64, currency string) string {
			if price == 0 {
				return "Free"
			}
			sym := map[string]string{"INR": "₹", "USD": "$", "EUR": "€", "GBP": "£"}[strings.ToUpper(currency)]
			if sym == "" {
				sym = strings.ToUpper(currency) + " "
			}
			if price == math.Trunc(price) {
				return sym + indianGroup(int64(price))
			}
			return sym + strconv.FormatFloat(price, 'f', 2, 64)
		},
		"num": func(v any) string {
			switch n := v.(type) {
			case int64:
				return indianGroup(n)
			case int:
				return indianGroup(int64(n))
			case float64:
				return indianGroup(int64(n))
			}
			return fmt.Sprint(v)
		},
		"safeURL": func(s string) template.URL { return template.URL(s) },
	}
}

// indianGroup formats 1234567 as 12,34,567.
func indianGroup(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	if neg {
		s = "-" + s
	}
	return s
}

func pageTemplate(file string) (*template.Template, error) {
	if t, ok := tmplCache.Load(file); ok {
		return t.(*template.Template), nil
	}
	t, err := template.New("").Funcs(siteFuncs()).ParseFS(webFS, "web/site/layout.html", "web/site/"+file)
	if err != nil {
		return nil, err
	}
	tmplCache.Store(file, t)
	return t, nil
}

func planMaps(plans []*Plan) []map[string]any {
	out := []map[string]any{}
	for _, p := range plans {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "description": p.Description, "price": p.Price, "currency": p.Currency,
			"period_days": p.PeriodDays, "max_numbers": p.MaxNumbers, "daily_limit": p.DailyLimit, "monthly_messages": p.MonthlyMessages,
			"max_contacts": p.MaxContacts, "storage_mb": p.StorageMB, "highlight": p.Highlight, "trial": p.Trial, "features": p.Features})
	}
	return out
}

func (a *API) renderPage(w http.ResponseWriter, r *http.Request, path string, status int) {
	pg, ok := sitePages[path]
	if !ok {
		pg, status = sitePage{"404.html", "Page not found", ""}, http.StatusNotFound
	}
	t, err := pageTemplate(pg.file)
	if err != nil {
		a.m.log.Error("template", "file", pg.file, "err", err)
		http.Error(w, "page error", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	settings := getSettings(ctx, a.m.db)
	title := settings["site_name"]
	if pg.title != "" {
		title = pg.title + " · " + settings["site_name"]
	} else if settings["tagline"] != "" {
		title = settings["site_name"] + " — " + settings["tagline"]
	}
	p, _ := a.resolvePrincipal(r)
	var plans []*Plan
	if !a.lic.clientMode() {
		plans = listPlans(ctx, a.m.db, true)
	}
	data := map[string]any{
		"Title": title, "Description": pg.desc, "Page": strings.TrimSuffix(pg.file, ".html"), "Year": time.Now().Year(),
		"LoggedIn": p != nil, "SignupOpen": a.signupOpen(ctx), "NeedsSetup": countUsers(ctx, a.m.db) == 0,
		"Settings": settings, "Plans": planMaps(plans),
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		a.m.log.Error("render", "file", pg.file, "err", err)
		http.Error(w, "page error", http.StatusInternalServerError)
		return
	}
	setPageHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

const pageCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; img-src 'self' data: blob: https:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

func setPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", pageCSP)
	w.Header().Set("Cache-Control", "no-cache")
}

func (a *API) sitePage(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	// Logged-in visitors don't need the login page.
	if path == "/login" || path == "/signup" {
		if p, _ := a.resolvePrincipal(r); p != nil {
			http.Redirect(w, r, "/app", http.StatusFound)
			return
		}
	}
	if path == "/setup" && countUsers(r.Context(), a.m.db) > 0 {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	a.renderPage(w, r, path, http.StatusOK)
}

// appShell serves the dashboard single-page app for /app and /app/*.
func (a *API) appShell(w http.ResponseWriter, r *http.Request) {
	if p, _ := a.resolvePrincipal(r); p == nil {
		if countUsers(r.Context(), a.m.db) == 0 {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/login?next="+urlQueryEscape(r.URL.Path), http.StatusFound)
		return
	}
	b, err := webFS.ReadFile("web/app/index.html")
	if err != nil {
		http.Error(w, "dashboard missing", http.StatusInternalServerError)
		return
	}
	html := strings.NewReplacer(
		`"/static/app.css"`, `"/static/app.css?v=`+buildStamp+`"`,
		`"/static/app/theme-boot.js"`, `"/static/app/theme-boot.js?v=`+buildStamp+`"`,
		`"/static/app.js"`, `"/static/app.js?v=`+buildStamp+`"`,
	).Replace(string(b))
	setPageHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}

func urlQueryEscape(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "?", "%3F", "#", "%23", " ", "%20").Replace(s)
}

// buildStamp fingerprints every dashboard file that ships inside the binary.
// It changes with each update, so a browser can never mix a new app.js with a
// page module it cached before the update (that used to show "fn is not a
// function" on a page added by the update).
var buildStamp = func() string {
	h := sha256.New()
	_ = fs.WalkDir(webFS, "web/static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := webFS.ReadFile(p)
		if err != nil {
			return nil
		}
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

func staticHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web/static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		switch ext := strings.ToLower(gopath.Ext(r.URL.Path)); {
		case r.URL.Query().Get("v") == buildStamp:
			// The address carries this build's stamp, so it can be kept forever.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case ext == ".js" || ext == ".css" || ext == ".json":
			// Code: ask the browser to check back. Unchanged files answer 304.
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", `"`+buildStamp+`"`)
		default:
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		fileServer.ServeHTTP(w, r)
	}))
}

// ---------- public JSON ----------

func (a *API) publicPlans(w http.ResponseWriter, r *http.Request) {
	plans := []*Plan{}
	if !a.lic.clientMode() {
		if p := listPlans(r.Context(), a.m.db, true); p != nil {
			plans = p
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plans": plans})
}

func (a *API) publicSettings(w http.ResponseWriter, r *http.Request) {
	all := getSettings(r.Context(), a.m.db)
	out := map[string]string{}
	for _, k := range publicSettingKeys {
		out[k] = all[k]
	}
	out["signup_open"] = strconv.FormatBool(a.signupOpen(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": out})
}

func (a *API) publicContact(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Phone   string `json:"phone"`
		Message string `json:"message"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if !loginLimiter.allow("contact:"+clientIP(r), 5, time.Hour) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", errTooMany)
		return
	}
	b.Name, b.Email, b.Message = strings.TrimSpace(b.Name), strings.TrimSpace(b.Email), strings.TrimSpace(b.Message)
	if b.Name == "" || b.Message == "" || (b.Email == "" && b.Phone == "") {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("please add your name, a message and an email or phone"))
		return
	}
	_, err := a.m.db.ExecContext(r.Context(), `INSERT INTO inbox(name, email, phone, message, ip, created_at) VALUES(?,?,?,?,?,?)`,
		truncate(b.Name, 100), truncate(b.Email, 200), truncate(b.Phone, 30), truncate(b.Message, 5000), clientIP(r), time.Now().Unix())
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Thanks! We'll get back to you soon."})
}
