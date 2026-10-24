package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func signedInbound(t *testing.T, env *testEnv, key, secret string, body []byte, ts int64) (int, map[string]any) {
	t.Helper()
	tsS := strconv.FormatInt(ts, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsS + "."))
	mac.Write(body)
	req, _ := http.NewRequest("POST", env.srv.URL+"/api/inbound", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Relay-Key", key)
	req.Header.Set("X-Relay-Timestamp", tsS)
	req.Header.Set("X-Relay-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestWebsiteInboundAndPublisher(t *testing.T) {
	env := newTestEnv(t, nil)
	owner := env.browser()
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	owner.ok("POST", "/api/sessions", map[string]any{"name": "news"})
	_, _ = env.api.m.db.Exec(`UPDATE gw_sessions SET jid='919800000000@s.whatsapp.net' WHERE name='news'`)
	list := owner.ok("POST", "/api/lists", map[string]any{"name": "Readers", "items": []map[string]any{{"to": "9811111111"}, {"to": "9822222222"}}})["list"].(map[string]any)
	lid := fmt.Sprintf("list:%v", list["id"])

	// Register a website with a default route and a "sports" route.
	ws := owner.ok("POST", "/api/websites", map[string]any{"name": "Charameti", "url": "https://charameti.com",
		"routes": []map[string]any{
			{"category": "", "targets": []map[string]any{{"to": lid, "name": "Readers"}}, "auto": false},
			{"category": "sports", "targets": []map[string]any{{"to": "120363000000000001@g.us", "name": "Sports group"}}, "auto": true},
		}})["website"].(map[string]any)
	key, secret := ws["key"].(string), ws["secret"].(string)
	if key == "" || secret == "" {
		t.Fatalf("no key/secret: %v", ws)
	}
	if owner.ok("GET", "/api/websites", nil)["websites"].([]any)[0].(map[string]any)["secret"] != nil {
		t.Fatal("secret shown again in list")
	}

	body := []byte(`{"id":"post:1","event":"post.published","title":"Korba news","summary":"Big story","url":"https://charameti.com/p/1","category":"news"}`)
	now := time.Now().Unix()
	// bad signature / old timestamp / unknown key
	if code, _ := signedInbound(t, env, key, "wrong", body, now); code != 403 {
		t.Fatalf("bad signature accepted: %d", code)
	}
	if code, _ := signedInbound(t, env, key, secret, body, now-600); code != 401 {
		t.Fatalf("old timestamp accepted: %d", code)
	}
	if code, _ := signedInbound(t, env, "wk_nope", secret, body, now); code != 401 {
		t.Fatalf("unknown key accepted: %d", code)
	}
	// good → pending (default route: not auto)
	code, out := signedInbound(t, env, key, secret, body, now)
	if code != 200 || out["status"] != "pending" {
		t.Fatalf("inbound: %d %v", code, out)
	}
	// same id again → duplicate
	if _, out := signedInbound(t, env, key, secret, body, now); out["status"] != "duplicate" {
		t.Fatalf("duplicate not detected: %v", out)
	}
	// update while pending changes the title
	upd := []byte(`{"id":"post:1","event":"post.updated","title":"Korba news (updated)","url":"https://charameti.com/p/1","category":"news"}`)
	if _, out := signedInbound(t, env, key, secret, upd, now); out["updated"] != true {
		t.Fatalf("update: %v", out)
	}
	// auto route: sports → queued straight away when the website says auto
	sports := []byte(`{"id":"post:2","event":"post.published","title":"Match tonight","url":"https://charameti.com/p/2","category":"sports","auto":true}`)
	if _, out := signedInbound(t, env, key, secret, sports, now); out["status"] != "queued" {
		t.Fatalf("auto route: %v", out)
	}
	// ping
	req, _ := http.NewRequest("GET", env.srv.URL+"/api/inbound/ping", nil)
	tsS := strconv.FormatInt(now, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsS + "."))
	req.Header.Set("X-Relay-Key", key)
	req.Header.Set("X-Relay-Timestamp", tsS)
	req.Header.Set("X-Relay-Signature", hex.EncodeToString(mac.Sum(nil)))
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("ping: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Inbox: one pending (post:1), one queued (post:2)
	inbox := owner.ok("GET", "/api/posts?status=pending", nil)
	posts := inbox["posts"].([]any)
	if len(posts) != 1 || num(inbox["pending"]) != 1 {
		t.Fatalf("inbox: %v", inbox)
	}
	p1 := posts[0].(map[string]any)
	if p1["title"] != "Korba news (updated)" || p1["website"] != "Charameti" || len(p1["targets"].([]any)) != 1 {
		t.Fatalf("pending post: %v", p1)
	}
	// Edit text then approve → 2 outbox rows (the list's two numbers)
	owner.ok("PATCH", fmt.Sprintf("/api/posts/%v", p1["id"]), map[string]any{"text": "📰 {{title}}\n{{url}}"})
	ap := owner.ok("POST", fmt.Sprintf("/api/posts/%v/approve", p1["id"]), nil)
	if ap["post"].(map[string]any)["status"] != "queued" {
		t.Fatalf("approve: %v", ap)
	}
	var n int
	_ = env.api.m.db.QueryRow(`SELECT COUNT(*) FROM gw_outbox WHERE campaign=?`, fmt.Sprintf("post%v", p1["id"])).Scan(&n)
	if n != 2 {
		t.Fatalf("outbox rows for post: %d", n)
	}
	var payload string
	_ = env.api.m.db.QueryRow(`SELECT payload FROM gw_outbox WHERE campaign=? LIMIT 1`, fmt.Sprintf("post%v", p1["id"])).Scan(&payload)
	var msg SendRequest
	_ = json.Unmarshal([]byte(payload), &msg)
	if msg.Vars["title"] != "Korba news (updated)" || msg.Text != "📰 {{title}}\n{{url}}" {
		t.Fatalf("queued message: %s", payload)
	}
	if code, _ := owner.do("POST", fmt.Sprintf("/api/posts/%v/approve", p1["id"]), nil, true); code != 409 {
		t.Fatalf("re-approve: %d", code)
	}
	queued := owner.ok("GET", "/api/posts?status=queued", nil)["posts"].([]any)
	if len(queued) != 2 || queued[0].(map[string]any)["stats"] == nil {
		t.Fatalf("queued list: %v", queued)
	}

	// Bulk links (no fetch): two pending posts, one rejected, one approved to Status.
	bulk := owner.ok("POST", "/api/posts", map[string]any{"urls": []string{"https://charameti.com/a\ncharameti.com/b, not a link"}, "fetch": false, "text": "{{url}}"})
	if num(bulk["created"]) != 2 {
		t.Fatalf("bulk: %v", bulk)
	}
	made := bulk["posts"].([]any)
	a, b := made[0].(map[string]any), made[1].(map[string]any)
	if a["title"] != "https://charameti.com/a" || b["url"] != "https://charameti.com/b" {
		t.Fatalf("bulk posts: %v %v", a, b)
	}
	owner.ok("POST", fmt.Sprintf("/api/posts/%v/reject", a["id"]), map[string]any{"note": "old"})
	if code, _ := owner.do("POST", fmt.Sprintf("/api/posts/%v/approve", b["id"]), nil, true); code != 400 {
		t.Fatalf("approve without targets: %d", code)
	}
	owner.ok("POST", fmt.Sprintf("/api/posts/%v/approve", b["id"]), map[string]any{"targets": []map[string]any{{"to": "status"}}})
	_ = env.api.m.db.QueryRow(`SELECT COUNT(*) FROM gw_outbox WHERE campaign=? AND to_addr='status@broadcast'`, fmt.Sprintf("post%v", b["id"])).Scan(&n)
	if n != 1 {
		t.Fatalf("status post queued: %d", n)
	}
	if num(owner.ok("GET", "/api/posts", nil)["pending"]) != 0 {
		t.Fatal("pending left")
	}
	// Rotating the secret invalidates the old one.
	rot := owner.ok("PATCH", fmt.Sprintf("/api/websites/%v", ws["id"]), map[string]any{"rotate_secret": true})["website"].(map[string]any)
	if rot["secret"] == secret || rot["secret"] == "" {
		t.Fatalf("rotate: %v", rot)
	}
	if code, _ := signedInbound(t, env, key, secret, sports, now); code != 401 {
		t.Fatalf("old key still works after rotation: %d", code)
	}
}
