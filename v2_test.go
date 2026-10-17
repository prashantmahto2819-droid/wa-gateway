package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
	"image"
	"image/color"
	pngenc "image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "test-master-key-0123456789abcdef"

type testEnv struct {
	t   *testing.T
	srv *httptest.Server
	api *API
}

func newTestEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{Port: "0", DataDir: dir, APIKey: testKey, DeviceName: "test", DefaultCountryCode: "91",
		MinDelay: time.Second, MaxDelay: 2 * time.Second, DailyLimit: 250, NewContactLimit: 40, SendHourFrom: 0, SendHourTo: 24,
		MaxMediaBytes: 5 << 20, PerContactWeekly: 3, RecentReplyDays: 30, HealthMinSample: 30, HealthMaxFail: 0.25, HealthMinReply: 0.03,
		TakeoverDefault: 30, AllowPrivateURLs: true}
	if mutate != nil {
		mutate(cfg)
	}
	db, err := openDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hooks := newWebhooks("", log)
	hooks.db = db
	lic, err := newLicensing(ctx, db, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	m, err := newManager(ctx, cfg, db, hooks, lic, log, "ERROR")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.loadAll(); err != nil {
		t.Fatal(err)
	}
	api := &API{m: m, cfg: cfg, lic: lic, started: time.Now()}
	srv := httptest.NewServer(api.routes())
	t.Cleanup(func() {
		srv.Close()
		cancel()
		m.closeAll()
		db.Close()
	})
	return &testEnv{t: t, srv: srv, api: api}
}

type client struct {
	env    *testEnv
	http   *http.Client
	bearer string
}

func (e *testEnv) browser() *client {
	jar, _ := cookiejar.New(nil)
	return &client{env: e, http: &http.Client{Jar: jar}}
}

func (e *testEnv) withKey(k string) *client { return &client{env: e, http: &http.Client{}, bearer: k} }

func (c *client) do(method, path string, body any, csrf bool) (int, map[string]any) {
	c.env.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.env.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set("X-Requested-With", "vivid")
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.env.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	if len(out) == 0 {
		out["_raw"] = string(raw)
	}
	return resp.StatusCode, out
}

func (c *client) ok(method, path string, body any) map[string]any {
	c.env.t.Helper()
	code, out := c.do(method, path, body, true)
	if code >= 300 || out["ok"] != true {
		c.env.t.Fatalf("%s %s → %d %v", method, path, code, out)
	}
	return out
}

func (c *client) upload(path, field, name, content string, extra map[string]string) (int, map[string]any) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile(field, name)
	_, _ = fw.Write([]byte(content))
	for k, v := range extra {
		_ = mw.WriteField(k, v)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", c.env.srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Requested-With", "vivid")
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.env.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func TestV2AccountsLicencesAndIsolation(t *testing.T) {
	env := newTestEnv(t, nil)
	owner := env.browser()

	// Setup needs the master key and happens once.
	if code, _ := owner.do("POST", "/auth/setup", map[string]any{"api_key": "wrong", "name": "Owner", "email": "owner@example.com", "password": "secret123"}, true); code != 401 {
		t.Fatalf("setup with wrong key: %d", code)
	}
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "owner@example.com", "password": "secret123"})
	if code, _ := owner.do("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "X", "email": "x@example.com", "password": "secret123"}, true); code != 409 {
		t.Fatalf("second setup: %d", code)
	}
	me := owner.ok("GET", "/api/me", nil)
	if me["user"].(map[string]any)["role"] != "superadmin" || me["licence"].(map[string]any)["source"] != "admin" {
		t.Fatalf("owner me: %v", me)
	}
	// CSRF: cookie requests without the header are refused.
	if code, _ := owner.do("PATCH", "/api/me", map[string]any{"name": "x"}, false); code != 403 {
		t.Fatalf("csrf guard: %d", code)
	}

	// Customer signs up and gets the trial.
	alice := env.browser()
	alice.ok("POST", "/auth/signup", map[string]any{"name": "Alice", "email": "alice@example.com", "phone": "9876543210", "password": "alicepass1"})
	lic := alice.ok("GET", "/api/licence", nil)["licence"].(map[string]any)
	if lic["source"] != "trial" || lic["status"] != "active" || num(lic["days_left"]) != 7 {
		t.Fatalf("trial licence: %v", lic)
	}
	// Trial allows 1 number.
	s1 := alice.ok("POST", "/api/sessions", map[string]any{"label": "Shop"})["session"].(map[string]any)
	if !strings.HasPrefix(s1["name"].(string), "u2-shop") || s1["label"] != "Shop" {
		t.Fatalf("session naming: %v", s1)
	}
	if code, out := alice.do("POST", "/api/sessions", map[string]any{"label": "Second"}, true); code != 429 || out["limit"] != "numbers" {
		t.Fatalf("number limit: %d %v", code, out)
	}
	// Pool is not in the trial plan.
	if code, out := alice.do("POST", "/api/queue", map[string]any{"pool": []string{s1["name"].(string), s1["name"].(string) + "x"}, "messages": []any{}}, true); code != 404 {
		t.Fatalf("unknown pool session: %d %v", code, out)
	}

	// Another customer can't see or use Alice's number.
	bob := env.browser()
	bob.ok("POST", "/auth/signup", map[string]any{"name": "Bob", "email": "bob@example.com", "phone": "9876500000", "password": "bobpass12"})
	if n := len(bob.ok("GET", "/api/sessions", nil)["sessions"].([]any)); n != 0 {
		t.Fatalf("bob sees %d sessions", n)
	}
	if code, _ := bob.do("GET", "/api/sessions/"+s1["name"].(string), nil, true); code != 404 {
		t.Fatalf("bob reads alice's session: %d", code)
	}
	if code, _ := bob.do("GET", "/api/admin/users", nil, true); code != 403 {
		t.Fatalf("bob admin: %d", code)
	}
	// Owner sees everything.
	if n := len(owner.ok("GET", "/api/sessions", nil)["sessions"].([]any)); n != 1 {
		t.Fatalf("owner sees %d sessions", n)
	}

	// Admin creates a licence key; Alice redeems it; the trial ends.
	plans := owner.ok("GET", "/api/admin/plans", nil)["plans"].([]any)
	var growth float64
	for _, p := range plans {
		if p.(map[string]any)["name"] == "Growth" {
			growth = p.(map[string]any)["id"].(float64)
		}
	}
	keys := owner.ok("POST", "/api/admin/licences", map[string]any{"plan_id": growth, "kind": "saas", "days": 30, "count": 2})["licences"].([]any)
	key := keys[0].(map[string]any)["key"].(string)
	if code, _ := alice.do("POST", "/api/licence/redeem", map[string]any{"key": "VR-NOPE"}, true); code != 400 {
		t.Fatal("bad key accepted")
	}
	lic = alice.ok("POST", "/api/licence/redeem", map[string]any{"key": strings.ToLower(key)})["licence"].(map[string]any)
	if lic["source"] != "licence" || lic["plan"].(map[string]any)["name"] != "Growth" || num(lic["days_left"]) != 30 {
		t.Fatalf("after redeem: %v", lic)
	}
	if code, _ := bob.do("POST", "/api/licence/redeem", map[string]any{"key": key}, true); code != 400 {
		t.Fatal("key redeemed twice")
	}
	alice.ok("POST", "/api/sessions", map[string]any{"label": "Second"}) // Growth: 3 numbers

	// Suspend → sends refused with licence_expired.
	lics := owner.ok("GET", "/api/admin/licences?kind=saas", nil)["licences"].([]any)
	var lid float64
	for _, l := range lics {
		if l.(map[string]any)["key"] == key {
			lid = l.(map[string]any)["id"].(float64)
		}
	}
	owner.ok("PATCH", fmt.Sprintf("/api/admin/licences/%d", int(lid)), map[string]any{"status": "suspended"})
	if code, out := alice.do("POST", "/api/contacts", map[string]any{"phone": "9876543211"}, true); code != 402 {
		t.Fatalf("suspended licence still works: %d %v", code, out)
	}
	owner.ok("PATCH", fmt.Sprintf("/api/admin/licences/%d", int(lid)), map[string]any{"status": "active", "extend_days": 10})
	lic = alice.ok("GET", "/api/licence", nil)["licence"].(map[string]any)
	if num(lic["days_left"]) != 40 {
		t.Fatalf("extend: %v", lic)
	}

	// API keys work like the login and stay scoped to the user.
	sec := alice.ok("POST", "/api/keys", map[string]any{"name": "BizCRT"})["secret"].(string)
	ak := env.withKey(sec)
	if n := len(ak.ok("GET", "/api/sessions", nil)["sessions"].([]any)); n != 2 {
		t.Fatalf("api key sees %d sessions", n)
	}
	if code, _ := env.withKey("vr_wrong").do("GET", "/api/me", nil, false); code != 401 {
		t.Fatal("bad api key accepted")
	}
	// Master key still works for everything (v1 compatibility).
	if n := len(env.withKey(testKey).ok("GET", "/api/sessions", nil)["sessions"].([]any)); n != 2 {
		t.Fatalf("master key sees %d", n)
	}

	// Login / logout / bad password
	again := env.browser()
	if code, _ := again.do("POST", "/auth/login", map[string]any{"email": "alice@example.com", "password": "nope"}, true); code != 401 {
		t.Fatal("bad password accepted")
	}
	again.ok("POST", "/auth/login", map[string]any{"email": "ALICE@example.com", "password": "alicepass1"})
	again.ok("POST", "/auth/logout", nil)
	if code, _ := again.do("GET", "/api/me", nil, true); code != 401 {
		t.Fatal("still logged in after logout")
	}
	// Admin can see the audit trail.
	if len(owner.ok("GET", "/api/admin/audit", nil)["entries"].([]any)) == 0 {
		t.Fatal("audit is empty")
	}
}

func TestV2ContactsMediaCampaigns(t *testing.T) {
	env := newTestEnv(t, nil)
	env.browser().ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	c := env.withKey(testKey)
	sess := c.ok("POST", "/api/sessions", map[string]any{"name": "main", "label": "Main"})["session"].(map[string]any)["name"].(string)

	// CSV import with custom fields, tags, invalid rows and Excel BOM.
	csv := "\ufeffPhone,Name,Email,Tags,City\n9876543210,Asha,a@x.com,vip;,Korba\n98765 43211,Ravi,,\"vip,new\",Raipur\nabc,Bad,,,\n"
	code, out := c.upload("/api/contacts/import", "file", "c.csv", csv, map[string]string{"tag": "oct"})
	if code != 200 || num(out["created"]) != 2 || num(out["invalid"]) != 1 {
		t.Fatalf("import: %d %v", code, out)
	}
	list := c.ok("GET", "/api/contacts?tag=vip", nil)
	if num(list["total"]) != 2 {
		t.Fatalf("tag filter: %v", list)
	}
	var first map[string]any
	for _, x := range list["contacts"].([]any) {
		if x.(map[string]any)["name"] == "Asha" {
			first = x.(map[string]any)
		}
	}
	if first["custom"].(map[string]any)["city"] == nil {
		t.Fatalf("custom field lost: %v", first)
	}
	tags := c.ok("GET", "/api/contacts/tags", nil)["tags"].([]any)
	if len(tags) < 2 {
		t.Fatalf("tags: %v", tags)
	}
	// Export is CSV
	req, _ := http.NewRequest("GET", env.srv.URL+"/api/contacts/export.csv", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "919876543210") || !strings.Contains(string(body), "city") {
		t.Fatalf("export: %s", body)
	}
	// Bulk opt-out marks contacts
	id := first["id"].(float64)
	c.ok("POST", "/api/contacts/bulk", map[string]any{"ids": []float64{id}, "action": "opt_out"})
	if n := num(c.ok("GET", "/api/contacts?opted_out=1", nil)["total"]); n != 1 {
		t.Fatalf("opted out count %d", n)
	}

	// Media upload + library + type guard
	code, out = c.upload("/api/media", "file", "offer.png", "\x89PNG\r\n\x1a\n0000000000000000", nil)
	if code != 201 {
		t.Fatalf("upload: %d %v", code, out)
	}
	mid := out["media"].(map[string]any)["id"].(float64)
	if code, _ := c.upload("/api/media", "file", "x.html", "<html><script>alert(1)</script></html>", nil); code != 400 {
		t.Fatal("html upload allowed")
	}
	if len(c.ok("GET", "/api/media", nil)["media"].([]any)) != 1 {
		t.Fatal("media list")
	}

	// Templates
	c.ok("POST", "/api/templates", map[string]any{"name": "Offer", "message": map[string]any{"text": "Hi {{name}}", "media": map[string]any{"type": "image", "media_id": mid}}})
	if code, _ := c.do("POST", "/api/templates", map[string]any{"name": "Bad", "message": map[string]any{"poll": map[string]any{"name": "Q", "options": []string{"one"}}}}, true); code != 400 {
		t.Fatal("bad poll accepted")
	}

	// Campaign to a tag minus an excluded tag, plus pasted numbers and a group.
	camp := c.ok("POST", "/api/campaigns", map[string]any{
		"name": "Diwali", "sessions": []string{sess},
		"message":  map[string]any{"text": "Namaste {{name}} from {{city}}"},
		"audience": map[string]any{"tags": []string{"oct"}, "exclude_tags": []string{"new"}, "phones": []string{"9000000001"}, "groups": []string{"120363000000000001@g.us"}},
		"speed":    "safe",
	})["campaign"].(map[string]any)
	cid := camp["id"].(float64)
	// The scheduler picks it up: wait for queue rows.
	var stats map[string]any
	for i := 0; i < 50; i++ {
		env.api.m.pokeScheduler()
		time.Sleep(100 * time.Millisecond)
		got := c.ok("GET", fmt.Sprintf("/api/campaigns/%d", int(cid)), nil)
		stats = got["campaign"].(map[string]any)["stats"].(map[string]any)
		if num(stats["total"]) > 0 {
			break
		}
	}
	// oct tag: Asha (opted out → skipped at enqueue) + Ravi (excluded by "new") → 0; phone 1 + group 1 = 2 rows
	if num(stats["total"]) != 2 {
		rows, _ := env.api.m.db.Query(`SELECT to_addr FROM gw_outbox WHERE campaign_id=?`, int(cid))
		for rows.Next() {
			var a string
			rows.Scan(&a)
			t.Log("row:", a)
		}
		rows.Close()
		t.Fatalf("campaign rows: %v", stats)
	}
	var payload string
	_ = env.api.m.db.QueryRow(`SELECT payload FROM gw_outbox WHERE campaign_id=? AND to_addr LIKE '%@g.us'`, int(cid)).Scan(&payload)
	if !strings.Contains(payload, `"text":"Namaste {{name}} from {{city}}"`) {
		t.Fatalf("group payload: %s", payload)
	}
	var factor int
	_ = env.api.m.db.QueryRow(`SELECT speed_factor FROM gw_outbox WHERE campaign_id=? LIMIT 1`, int(cid)).Scan(&factor)
	if factor != 2 {
		t.Fatalf("speed factor %d", factor)
	}
	// Pause / resume / cancel
	if st := c.ok("POST", fmt.Sprintf("/api/campaigns/%d/pause", int(cid)), nil)["campaign"].(map[string]any)["status"]; st != "paused" {
		t.Fatalf("pause: %v", st)
	}
	c.ok("POST", fmt.Sprintf("/api/campaigns/%d/resume", int(cid)), nil)
	cancelled := c.ok("POST", fmt.Sprintf("/api/campaigns/%d/cancel", int(cid)), nil)["campaign"].(map[string]any)
	if cancelled["status"] != "cancelled" || num(cancelled["stats"].(map[string]any)["cancelled"]) != 2 {
		t.Fatalf("cancel: %v", cancelled)
	}
	dup := c.ok("POST", fmt.Sprintf("/api/campaigns/%d/duplicate", int(cid)), nil)["campaign"].(map[string]any)
	if dup["status"] != "draft" {
		t.Fatalf("duplicate: %v", dup)
	}

	// A weekly scheduled campaign gets a next_run in the future.
	future := time.Now().Add(48 * time.Hour).Unix()
	sc := c.ok("POST", "/api/campaigns", map[string]any{"name": "Weekly", "sessions": []string{sess}, "message": map[string]any{"text": "hi"},
		"audience": map[string]any{"phones": []string{"9000000002"}}, "schedule": map[string]any{"at": future, "repeat": "weekly", "days": []int{1, 4}}})["campaign"].(map[string]any)
	if sc["status"] != "scheduled" || num(sc["schedule"].(map[string]any)["next_run"]) != future {
		t.Fatalf("scheduled: %v", sc)
	}

	// Auto-replies validate their input
	c.ok("POST", "/api/autoreplies", map[string]any{"name": "Price", "trigger": "keyword", "keywords": []string{"price", "rate"}, "reply": map[string]any{"text": "Our prices…"}})
	if code, _ := c.do("POST", "/api/autoreplies", map[string]any{"trigger": "keyword", "reply": map[string]any{"text": "x"}}, true); code != 400 {
		t.Fatal("keyword rule without keywords accepted")
	}
	if code, _ := c.do("POST", "/api/autoreplies", map[string]any{"trigger": "regex", "keywords": []string{"("}, "reply": map[string]any{"text": "x"}}, true); code != 400 {
		t.Fatal("bad regex accepted")
	}

	// Webhook endpoint + API docs + stats
	wh := c.ok("POST", "/api/webhooks", map[string]any{"url": "https://example.com/hook", "events": []string{"message", "bogus"}})["webhook"].(map[string]any)
	if !strings.HasPrefix(wh["secret"].(string), "whsec_") || len(wh["events"].([]any)) != 1 {
		t.Fatalf("webhook: %v", wh)
	}
	if len(c.ok("GET", "/api/stats?days=7", nil)["days"].([]any)) != 7 {
		t.Fatal("stats days")
	}
	code, doc := c.do("GET", "/openapi.json", nil, false)
	if code != 200 || doc["openapi"] != "3.0.3" {
		t.Fatalf("openapi: %d", code)
	}
}

func TestV2SelfHostedLicence(t *testing.T) {
	// The licence server (a normal SaaS install)…
	server := newTestEnv(t, nil)
	admin := server.withKey(testKey)
	plans := admin.ok("GET", "/api/admin/plans", nil)["plans"].([]any)
	planID := plans[1].(map[string]any)["id"].(float64)
	key := admin.ok("POST", "/api/admin/licences", map[string]any{"plan_id": planID, "kind": "selfhosted", "days": 365, "max_instances": 1})["licences"].([]any)[0].(map[string]any)["key"].(string)
	pub := admin.ok("GET", "/api/admin/licence-server", nil)["public_key"].(string)

	// …and a customer's install that checks in with it.
	client := newTestEnv(t, func(c *Config) {
		c.LicenceServer, c.LicenceKey, c.LicencePublicKey = server.srv.URL, key, pub
	})
	if err := client.api.lic.checkIn(context.Background()); err != nil {
		t.Fatalf("check-in: %v", err)
	}
	eff := client.api.lic.effective(context.Background(), 0)
	if eff.Status != "active" || eff.Source != "selfhosted" || eff.Plan == nil || eff.DaysLeft != 365 {
		t.Fatalf("client licence: %+v", eff)
	}
	// Client mode: no signups, no licence admin.
	if code, _ := client.withKey(testKey).do("GET", "/api/admin/plans", nil, false); code != 403 {
		t.Fatalf("client admin plans: %d", code)
	}
	// A second server with the same key is refused (max_instances=1).
	other := newTestEnv(t, func(c *Config) { c.LicenceServer, c.LicenceKey, c.LicencePublicKey = server.srv.URL, key, pub })
	if err := other.api.lic.checkIn(context.Background()); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("second instance: %v", err)
	}
	// A forged token (signed by another key) is rejected.
	fake := newTestEnv(t, nil)
	tok := fake.api.lic.sign(tokenPayload{Key: key, InstanceID: "x", ExpiresAt: time.Now().Add(time.Hour).Unix(), ValidUntil: time.Now().Add(time.Hour).Unix()})
	if _, err := verifyToken(tok, pub); err == nil {
		t.Fatal("forged token accepted")
	}
	// Suspending on the server stops the client at its next check-in.
	lics := admin.ok("GET", "/api/admin/licences?kind=selfhosted", nil)["licences"].([]any)
	lid := lics[0].(map[string]any)["id"].(float64)
	if num(lics[0].(map[string]any)["activations"]) != 1 {
		t.Fatalf("activations: %v", lics[0])
	}
	admin.ok("PATCH", fmt.Sprintf("/api/admin/licences/%d", int(lid)), map[string]any{"status": "suspended"})
	_ = client.api.lic.checkIn(context.Background())
	if client.api.lic.effective(context.Background(), 0).Status == "active" {
		t.Fatal("suspended licence still active on client")
	}
}

func TestV2PublicSite(t *testing.T) {
	env := newTestEnv(t, nil)
	get := func(path string) (int, string, http.Header) {
		resp, err := http.Get(env.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	for _, p := range []string{"/", "/features", "/pricing", "/about", "/contact", "/privacy", "/terms", "/refund", "/docs", "/login", "/signup", "/setup"} {
		code, body, h := get(p)
		if code != 200 || !strings.Contains(body, "</html>") || !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'self'") {
			t.Fatalf("%s → %d", p, code)
		}
	}
	if code, body, _ := get("/pricing"); code != 200 || !strings.Contains(body, "Growth") || !strings.Contains(body, "₹1,299") {
		t.Fatalf("pricing page lacks plans: %d", code)
	}
	if code, _, _ := get("/nope"); code != 404 {
		t.Fatalf("404 page: %d", code)
	}
	if code, _, _ := get("/static/site.css"); code != 200 {
		t.Fatal("static css")
	}
	// /app redirects to /setup before any account exists.
	resp, _ := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(env.srv.URL + "/app")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/setup" {
		t.Fatalf("/app redirect: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	c := env.browser()
	c.ok("POST", "/public/contact", map[string]any{"name": "Ravi", "email": "r@x.com", "message": "Need 5 numbers"})
	if n := len(env.withKey(testKey).ok("GET", "/api/admin/inbox", nil)["messages"].([]any)); n != 1 {
		t.Fatalf("inbox %d", n)
	}
}

func TestNextRun(t *testing.T) {
	loc := time.Local
	at := time.Date(2026, 10, 19, 9, 30, 0, 0, loc) // Monday
	after := time.Date(2026, 10, 20, 10, 0, 0, 0, loc)
	if got := time.Unix(nextRun(Schedule{At: at.Unix(), Repeat: "daily"}, after), 0); !got.Equal(time.Date(2026, 10, 21, 9, 30, 0, 0, loc)) {
		t.Fatalf("daily: %v", got)
	}
	if got := time.Unix(nextRun(Schedule{At: at.Unix(), Repeat: "weekly", Days: []int{1, 4}}, after), 0); !got.Equal(time.Date(2026, 10, 22, 9, 30, 0, 0, loc)) {
		t.Fatalf("weekly: %v", got)
	}
	jan31 := time.Date(2026, 1, 31, 8, 0, 0, 0, loc)
	if got := time.Unix(nextRun(Schedule{At: jan31.Unix(), Repeat: "monthly"}, jan31), 0); !got.Equal(time.Date(2026, 2, 28, 8, 0, 0, 0, loc)) {
		t.Fatalf("monthly clamp: %v", got)
	}
	if nextRun(Schedule{At: at.Unix(), Repeat: "daily", Until: after.Unix()}, after) != 0 {
		t.Fatal("until not respected")
	}
}

func TestAutoReplyMatching(t *testing.T) {
	now := time.Date(2026, 10, 18, 23, 0, 0, 0, time.Local) // Sunday night
	cases := []struct {
		r    AutoReply
		text string
		want bool
	}{
		{AutoReply{Trigger: "keyword", Keywords: []string{"Price"}}, "price!!", true},
		{AutoReply{Trigger: "keyword", Keywords: []string{"price"}}, "what is the price", false},
		{AutoReply{Trigger: "contains", Keywords: []string{"price"}}, "What is the PRICE?", true},
		{AutoReply{Trigger: "contains", Keywords: []string{"दाम"}}, "इसका दाम क्या है", true},
		{AutoReply{Trigger: "regex", Keywords: []string{`order\s*#?\d+`}}, "status of order #123", true},
		{AutoReply{Trigger: "away", Hours: Hours{From: 9, To: 21, Days: []int{1, 2, 3, 4, 5, 6}}}, "hi", true},
		{AutoReply{Trigger: "away", Hours: Hours{From: 9, To: 21}}, "hi", true},
		{AutoReply{Trigger: "menu", Keywords: []string{"price"}}, "", false},
	}
	for i, c := range cases {
		if got := c.r.matches(false, c.text, nil, now); got != c.want {
			t.Errorf("case %d (%s %q): got %v", i, c.r.Trigger, c.text, got)
		}
	}
	r := AutoReply{Trigger: "menu", Keywords: []string{"price"}}
	if !r.matches(false, "1", &MenuOpt{ID: "price", Text: "Price list"}, now) {
		t.Error("menu choice")
	}
	if !(&AutoReply{Trigger: "welcome"}).matches(true, "", nil, now) {
		t.Error("welcome")
	}
	if renderMenu(&MenuReq{Title: "Help", Options: []MenuOpt{{ID: "a", Text: "Price"}, {ID: "b", Text: "Timing"}}}, "") !=
		"*Help*\n\n1. Price\n2. Timing\n\n_Reply with the number of your choice._" {
		t.Error("menu text")
	}
}

func TestButtonsAndLists(t *testing.T) {
	b := &ButtonsReq{Title: "Offer", Footer: "Vivid", Buttons: []ButtonReq{{Text: "Yes"}, {Text: "No", ID: "no"}}}
	if err := b.validate(); err != nil {
		t.Fatal(err)
	}
	if b.Buttons[0].ID != "btn_1" || b.Buttons[0].Type != "reply" {
		t.Fatalf("defaults: %+v", b.Buttons[0])
	}
	msg, nodes := buildButtons(b, "Interested?", nil, nil)
	nf := msg.GetInteractiveMessage().GetNativeFlowMessage()
	if len(nf.GetButtons()) != 2 || nf.GetButtons()[0].GetName() != "quick_reply" || !strings.Contains(nf.GetButtons()[1].GetButtonParamsJSON(), `"id":"no"`) {
		t.Fatalf("native flow: %v", nf)
	}
	if msg.GetInteractiveMessage().GetHeader().GetTitle() != "Offer" || msg.GetInteractiveMessage().GetFooter().GetText() != "Vivid" || msg.ViewOnceMessage != nil {
		t.Fatal("header/footer/wrapper wrong")
	}
	if len(nodes) != 1 || nodes[0].Tag != "biz" || nodes[0].Content.([]waBinary.Node)[0].Attrs["type"] != "native_flow" ||
		nodes[0].Content.([]waBinary.Node)[0].Content.([]waBinary.Node)[0].Attrs["name"] != "mixed" {
		t.Fatalf("biz node: %+v", nodes)
	}
	// mixing reply + cta is refused; cta kinds are built right
	bad := &ButtonsReq{Buttons: []ButtonReq{{Text: "Yes"}, {Type: "url", Text: "Open", URL: "https://x.in"}}}
	if bad.validate() == nil {
		t.Fatal("mixed buttons accepted")
	}
	cta := &ButtonsReq{Buttons: []ButtonReq{{Type: "url", Text: "Site", URL: "https://x.in/p"}, {Type: "call", Text: "Call", Phone: "98765 43210"}, {Type: "copy", Text: "Code", Code: "VR50"}}}
	if err := cta.validate(); err != nil {
		t.Fatal(err)
	}
	m2, _ := buildButtons(cta, "hi", nil, nil)
	names := []string{}
	for _, x := range m2.GetInteractiveMessage().GetNativeFlowMessage().GetButtons() {
		names = append(names, x.GetName())
	}
	if strings.Join(names, ",") != "cta_url,cta_call,cta_copy" {
		t.Fatal(names)
	}
	if !strings.Contains(cta.fallbackText("hi"), "Call: +9876543210") {
		t.Fatal(cta.fallbackText("hi"))
	}
	if fb := b.fallbackText("Interested?"); !strings.Contains(fb, "1. Yes") || !strings.Contains(fb, "2. No") {
		t.Fatal(fb)
	}
	if len(b.menuOptions()) != 2 || len(cta.menuOptions()) != 0 {
		t.Fatal("menu options")
	}

	l := &ListReq{Title: "Menu", Sections: []ListSection{{Title: "Food", Rows: []ListRow{{Title: "Thali", Description: "₹120"}, {Title: "Dosa"}}}}}
	if err := l.validate(); err != nil {
		t.Fatal(err)
	}
	lm := buildList(l, "What would you like?", nil).GetListMessage()
	if lm.GetListType() != waE2E.ListMessage_SINGLE_SELECT || lm.GetButtonText() != "Choose" || len(lm.GetSections()[0].GetRows()) != 2 || lm.GetSections()[0].GetRows()[0].GetRowID() != "row_1_1" {
		t.Fatalf("list: %v", lm)
	}
	big := &ListReq{Sections: []ListSection{{Rows: make([]ListRow, 11)}}}
	for i := range big.Sections[0].Rows {
		big.Sections[0].Rows[i].Title = "x"
	}
	if big.validate() == nil {
		t.Fatal("11 rows accepted")
	}

	// incoming taps
	id, text, ok := extractInteractiveReply(&waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
		Body: &waE2E.InteractiveResponseMessage_Body{Text: proto.String("Yes")},
		InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
			Name: proto.String("quick_reply"), ParamsJSON: proto.String(`{"id":"yes"}`)}}}})
	if !ok || id != "yes" || text != "Yes" {
		t.Fatal(id, text, ok)
	}
	c := extractContent(&waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{Title: proto.String("Thali"),
		SingleSelectReply: &waE2E.ListResponseMessage_SingleSelectReply{SelectedRowID: proto.String("row_1_1")}}})
	if c["type"] != "list_reply" || c["button_id"] != "row_1_1" {
		t.Fatal(c)
	}

	// request validation through SendRequest
	r := SendRequest{To: "1", Text: "Pick", Buttons: b, Poll: &PollReq{Name: "q", Options: []string{"a", "b"}}}
	if r.validate() == nil {
		t.Fatal("buttons + poll accepted")
	}
	r = SendRequest{To: "1", Text: "Pick", Buttons: b, Media: &Media{Type: "image", URL: "https://x/a.jpg"}}
	if err := r.validate(); err != nil || r.kind() != "buttons" {
		t.Fatal(err)
	}
	r = SendRequest{To: "1", List: l}
	if r.validate() == nil {
		t.Fatal("list without text accepted")
	}
}

func TestLinkPreview(t *testing.T) {
	// A tiny site with Open Graph tags and a PNG image.
	var png bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 640, 320))
	for y := 0; y < 320; y++ {
		for x := 0; x < 640; x++ {
			img.Set(x, y, color.RGBA{uint8(x / 3), 120, uint8(y), 255})
		}
	}
	_ = pngenc.Encode(&png, img)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/offer":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><title>Fallback title</title>
				<meta property="og:title" content="Diwali Mega Offer">
				<meta property="og:description" content="20% off till Friday">
				<meta property="og:image" content="/img/offer.png"></head><body>hi</body></html>`))
		case "/img/offer.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()

	if l := firstLink("Dekho: " + site.URL + "/offer, aaj hi!"); l != site.URL+"/offer" {
		t.Fatalf("firstLink %q", l)
	}
	if firstLink("no link here") != "" {
		t.Fatal("found a link in plain text")
	}
	p := linkPreview(context.Background(), site.URL+"/offer", true)
	if p == nil || p.Title != "Diwali Mega Offer" || p.Description != "20% off till Friday" || p.Thumb == nil {
		t.Fatalf("preview: %+v", p)
	}
	th, _, err := image.Decode(bytes.NewReader(p.Thumb))
	if err != nil || th.Bounds().Dx() != 192 || th.Bounds().Dy() != 96 || len(p.Thumb) > 60000 {
		t.Fatalf("thumbnail: %v %v %d", err, th.Bounds(), len(p.Thumb))
	}
	if p.HQ == nil || p.HQW != 640 || p.HQH != 320 {
		t.Fatalf("hq thumbnail: %d %dx%d", len(p.HQ), p.HQW, p.HQH)
	}
	// Cached: a second call doesn't need the server.
	site.Close()
	if linkPreview(context.Background(), site.URL+"/offer", true) == nil {
		t.Fatal("cache miss")
	}
	msg := &waE2E.Message{Conversation: proto.String("Dekho " + site.URL + "/offer")}
	applyLinkPreview(context.Background(), nil, msg, "Dekho "+site.URL+"/offer", true)
	ext := msg.GetExtendedTextMessage()
	if msg.Conversation != nil || ext.GetTitle() != "Diwali Mega Offer" || ext.GetMatchedText() != site.URL+"/offer" || ext.GetPreviewType() != waE2E.ExtendedTextMessage_NONE || ext.GetJPEGThumbnail() == nil {
		t.Fatalf("applied: %v", ext)
	}
	// Private addresses are refused when the guard is on.
	if linkPreview(context.Background(), "http://127.0.0.1:1/x", false) != nil {
		t.Fatal("private address fetched")
	}
}

func TestCarousel(t *testing.T) {
	c := &CarouselReq{Cards: []CarouselCard{
		{Title: "Thali", Body: "₹120", Buttons: []ButtonReq{{Text: "Order Thali", ID: "thali"}}},
		{Title: "Dosa", Body: "₹80", Media: &Media{Type: "image", URL: "https://x/d.jpg"}, Buttons: []ButtonReq{{Type: "url", Text: "Menu", URL: "https://x/menu"}}},
	}}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if (&CarouselReq{Cards: c.Cards[:1]}).validate() == nil {
		t.Fatal("single card accepted")
	}
	headers := []*waE2E.InteractiveMessage_Header{
		{Title: proto.String("Thali"), HasMediaAttachment: proto.Bool(false)},
		{Title: proto.String("Dosa"), HasMediaAttachment: proto.Bool(true), Media: &waE2E.InteractiveMessage_Header_ImageMessage{ImageMessage: &waE2E.ImageMessage{URL: proto.String("u")}}},
	}
	msg, nodes := buildCarousel(c, headers, "Today's specials", nil)
	im := msg.GetInteractiveMessage()
	car := im.GetCarouselMessage()
	if car == nil || len(car.GetCards()) != 2 || car.GetMessageVersion() != 1 || car.CarouselCardType != nil {
		t.Fatalf("carousel root: %v", im)
	}
	if im.GetHeader() == nil || im.GetHeader().GetHasMediaAttachment() || im.ContextInfo == nil || msg.ViewOnceMessage != nil {
		t.Fatal("root header/context/wrapper wrong")
	}
	c0 := car.GetCards()[0]
	if c0.GetNativeFlowMessage().MessageVersion != nil || c0.GetNativeFlowMessage().GetMessageParamsJSON() != "{}" {
		t.Fatalf("card params: %v", c0.GetNativeFlowMessage())
	}
	if !strings.Contains(c0.GetBody().GetText(), `Reply "Order Thali"`) {
		t.Fatalf("iOS hint missing: %q", c0.GetBody().GetText())
	}
	if car.GetCards()[1].GetNativeFlowMessage().GetButtons()[0].GetName() != "cta_url" {
		t.Fatal("cta button on card 2")
	}
	if nodes[0].Content.([]waBinary.Node)[0].Content.([]waBinary.Node)[0].Attrs["name"] != "mixed" {
		t.Fatal("biz node")
	}
	if opts := c.menuOptions(); len(opts) != 1 || opts[0].ID != "thali" {
		t.Fatalf("menu options %v", opts)
	}
	r := SendRequest{To: "1", Text: "Specials", Carousel: c}
	if err := r.validate(); err != nil || r.kind() != "carousel" {
		t.Fatal(err)
	}
	// a Web/iOS tap on a carousel card arrives as a template reply
	id, text, ok := extractInteractiveReply(&waE2E.Message{TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{
		SelectedID: proto.String("thali"), SelectedDisplayText: proto.String("Order Thali"), SelectedCarouselCardIndex: proto.Uint32(0)}})
	if !ok || id != "thali" || text != "Order Thali" {
		t.Fatal(id, text, ok)
	}
}
