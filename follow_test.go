package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A message can carry files that follow it to the same person: the offer text or
// link first, then the price list and the brochure, each its own message.
func TestFilesFollowingAMessage(t *testing.T) {
	env := newTestEnv(t, nil)
	owner := env.browser()
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	owner.ok("POST", "/api/sessions", map[string]any{"name": "shop"})
	_, _ = env.api.m.db.Exec(`UPDATE gw_sessions SET jid='919800000000@s.whatsapp.net' WHERE name='shop'`)

	msg := map[string]any{
		"text":        "आज का ऑफ़र: https://shop.example.com/sale",
		"unique_code": "ref",
		"then": []map[string]any{
			{"media": map[string]any{"type": "document", "url": "https://shop.example.com/rates.pdf", "filename": "rates.pdf"}, "text": "रेट लिस्ट"},
			{"media": map[string]any{"type": "image", "url": "https://shop.example.com/shop.jpg"}},
		},
	}
	// queued to two people: one row each, both carrying the files
	q := owner.ok("POST", "/api/sessions/shop/queue", map[string]any{"campaign": "offer",
		"template": msg, "recipients": []map[string]any{{"to": "9811111111"}, {"to": "9822222222"}}})
	if num(q["queued"]) != 2 || num(q["skipped_duplicates"]) != 0 {
		t.Fatalf("queued: %v", q)
	}
	var payload string
	if err := env.api.m.db.QueryRow(`SELECT payload FROM gw_outbox WHERE to_addr='919811111111@s.whatsapp.net'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var got SendRequest
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Then) != 2 || got.Then[0].Media.FileName != "rates.pdf" || got.Then[0].Text != "रेट लिस्ट" {
		t.Fatalf("queued files: %+v", got.Then)
	}
	if got.Then[1].Media.Type != "image" || got.Then[1].Text != "" {
		t.Fatalf("second file: %+v", got.Then[1])
	}

	// what the rules refuse
	bad := []struct {
		why  string
		then []map[string]any
	}{
		{"a follow-up with its own 'to'", []map[string]any{{"to": "9833333333", "media": map[string]any{"type": "image", "url": "https://x/1.jpg"}}}},
		{"a follow-up with follow-ups", []map[string]any{{"media": map[string]any{"type": "image", "url": "https://x/1.jpg"},
			"then": []map[string]any{{"media": map[string]any{"type": "image", "url": "https://x/2.jpg"}}}}}},
		{"six files", func() []map[string]any {
			out := []map[string]any{}
			for i := 0; i < 6; i++ {
				out = append(out, map[string]any{"media": map[string]any{"type": "image", "url": "https://x/a.jpg"}})
			}
			return out
		}()},
		{"an empty follow-up", []map[string]any{{"text": ""}}},
	}
	for _, c := range bad {
		code, out := owner.do("POST", "/api/sessions/shop/queue", map[string]any{"campaign": "x",
			"template": map[string]any{"text": "hi", "then": c.then}, "recipients": []map[string]any{{"to": "9811111111"}}}, true)
		if code != 400 {
			t.Fatalf("%s was accepted: %d %v", c.why, code, out)
		}
	}
	// base64 is refused for queued files, like the main message's media
	if code, _ := owner.do("POST", "/api/sessions/shop/queue", map[string]any{"campaign": "x",
		"template":   map[string]any{"text": "hi", "then": []map[string]any{{"media": map[string]any{"type": "image", "base64": "aGk="}}}},
		"recipients": []map[string]any{{"to": "9811111111"}}}, true); code != 400 {
		t.Fatal("base64 file was queued")
	}

	// a campaign carries them too, through validation and back out of the database
	c := owner.ok("POST", "/api/campaigns", map[string]any{"name": "Offer", "sessions": []string{"shop"}, "draft": true,
		"message": msg, "audience": map[string]any{"phones": []string{"9811111111"}}})["campaign"].(map[string]any)
	back := owner.ok("GET", "/api/campaigns", nil)["campaigns"].([]any)
	var saved map[string]any
	for _, x := range back {
		if x.(map[string]any)["id"] == c["id"] {
			saved = x.(map[string]any)
		}
	}
	files, _ := saved["message"].(map[string]any)["then"].([]any)
	if len(files) != 2 {
		t.Fatalf("campaign files: %v", saved["message"])
	}

	// each message keeps its own code: the follow-ups inherit the setting, not the code
	a := withUniqueCode("रेट लिस्ट", "ref", "", env.api.m.sessions["shop"].rnd)
	b := withUniqueCode("रेट लिस्ट", "ref", "", env.api.m.sessions["shop"].rnd)
	if a == b || !strings.Contains(a, "Ref:") {
		t.Fatalf("codes repeat: %q %q", a, b)
	}
}
