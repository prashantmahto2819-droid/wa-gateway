package main

import (
	"fmt"
	"testing"
	"time"
)

func TestSendLists(t *testing.T) {
	env := newTestEnv(t, nil)
	env.browser().ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	c := env.withKey(testKey)
	sess := c.ok("POST", "/api/sessions", map[string]any{"name": "main"})["session"].(map[string]any)["name"].(string)
	ct := c.ok("POST", "/api/contacts", map[string]any{"phone": "9811111111", "name": "Asha"})["contact"].(map[string]any)

	l := c.ok("POST", "/api/lists", map[string]any{"name": "Diwali VIP", "items": []map[string]any{
		{"to": "98222 22222", "name": "Ravi Sharma"}, {"to": "919822222222"}, // same number twice
		{"to": fmt.Sprintf("contact:%v", ct["id"])},
		{"to": "120363000000000001@g.us", "name": "Korba Traders"},
		{"to": "120363111111111111@newsletter"},
		{"to": "abc"}, // invalid
	}})["list"].(map[string]any)
	cnt := l["counts"].(map[string]any)
	if num(cnt["total"]) != 4 || num(cnt["phones"]) != 1 || num(cnt["contacts"]) != 1 || num(cnt["groups"]) != 1 || num(cnt["channels"]) != 1 {
		t.Fatalf("counts: %v", cnt)
	}
	id := l["id"]
	l = c.ok("PATCH", fmt.Sprintf("/api/lists/%v", id), map[string]any{"add": []map[string]any{{"to": "9833333333"}}, "remove": []string{"120363111111111111@newsletter"}})["list"].(map[string]any)
	if num(l["counts"].(map[string]any)["total"]) != 4 || num(l["counts"].(map[string]any)["channels"]) != 0 {
		t.Fatalf("patch: %v", l["counts"])
	}
	if len(c.ok("GET", "/api/lists", nil)["lists"].([]any)) != 1 {
		t.Fatal("list missing from index")
	}

	// A campaign to the list reaches every kind of recipient in it.
	camp := c.ok("POST", "/api/campaigns", map[string]any{"name": "To list", "sessions": []string{sess}, "message": map[string]any{"text": "Hi {{name}}"},
		"audience": map[string]any{"lists": []any{id}}})["campaign"].(map[string]any)
	var stats map[string]any
	for i := 0; i < 50; i++ {
		stats = c.ok("GET", fmt.Sprintf("/api/campaigns/%v", camp["id"]), nil)["campaign"].(map[string]any)["stats"].(map[string]any)
		if num(stats["total"]) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if num(stats["total"]) != 4 { // Ravi, Asha (contact), 9833333333, Korba Traders group
		t.Fatalf("campaign rows: %v", stats)
	}
	var name string
	_ = env.api.m.db.QueryRow(`SELECT payload FROM gw_outbox WHERE to_addr='919822222222@s.whatsapp.net'`).Scan(&name)
	if name == "" || !contains(name, `"name":"Ravi Sharma"`) {
		t.Fatalf("list name not used as {{name}}: %s", name)
	}
	if code, _ := c.do("POST", "/api/campaigns", map[string]any{"name": "x", "sessions": []string{sess}, "message": map[string]any{"text": "Hi"},
		"audience": map[string]any{"lists": []int{999}}}, true); code != 400 {
		t.Fatalf("unknown list accepted: %d", code)
	}
	c.ok("DELETE", fmt.Sprintf("/api/lists/%v", id), nil)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
