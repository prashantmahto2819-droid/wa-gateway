package main

import (
	"fmt"
	"testing"
)

// Each account works only on its own numbers — the super admin included. A customer's
// number must not turn up in the super admin's own Numbers page or Send picker, where
// nothing would tell it apart from their own and a campaign could go out from it.
func TestNumbersStayWithTheirOwner(t *testing.T) {
	env := newTestEnv(t, nil)
	owner := env.browser() // the super admin
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	mine := owner.ok("POST", "/api/sessions", map[string]any{"name": "mine"})["session"].(map[string]any)["name"].(string)

	biz := env.browser() // an ordinary customer
	biz.ok("POST", "/auth/signup", map[string]any{"name": "Biz", "email": "biz@example.com", "password": "secret123"})
	shop := biz.ok("POST", "/api/sessions", map[string]any{"name": "shop"})["session"].(map[string]any)["name"].(string)

	names := func(c *client) []string {
		out := []string{}
		for _, s := range c.ok("GET", "/api/sessions", nil)["sessions"].([]any) {
			out = append(out, s.(map[string]any)["name"].(string))
		}
		return out
	}
	// the customer sees only their own, and the super admin's number does not exist for them
	if got := names(biz); len(got) != 1 || got[0] != shop {
		t.Fatalf("customer's list: %v", got)
	}
	if code, _ := biz.do("GET", "/api/sessions/"+mine, nil, true); code != 404 {
		t.Fatalf("customer reached the admin's number: %d", code)
	}
	// and the super admin sees only their own too
	if got := names(owner); len(got) != 1 || got[0] != mine {
		t.Fatalf("super admin's list: %v", got)
	}
	for _, call := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/sessions/" + shop, nil},
		{"POST", "/api/sessions/" + shop + "/send", map[string]any{"to": "9811111111", "text": "hi"}},
		{"PATCH", "/api/sessions/" + shop, map[string]any{"label": "taken over"}},
		{"GET", "/api/sessions/" + shop + "/groups", nil},
		{"POST", "/api/sessions/" + shop + "/logout", nil},
		{"DELETE", "/api/sessions/" + shop, nil},
	} {
		if code, out := owner.do(call.method, call.path, call.body, true); code != 404 {
			t.Fatalf("super admin reached a customer's number: %s %s → %d %v", call.method, call.path, code, out)
		}
	}

	// Admin → Numbers lists every number against its owner, for support
	adm := owner.ok("GET", "/api/admin/numbers", nil)["numbers"].([]any)
	byName := map[string]map[string]any{}
	for _, n := range adm {
		m := n.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if len(byName) != 2 || byName[shop] == nil || byName[mine] == nil {
		t.Fatalf("admin list: %v", adm)
	}
	if byName[shop]["owner"].(map[string]any)["email"] != "biz@example.com" {
		t.Fatalf("owner not shown: %v", byName[shop])
	}
	// the two support actions work, and the customer sees the result
	owner.ok("POST", fmt.Sprintf("/api/admin/numbers/%s/pause", shop), nil)
	if biz.ok("GET", "/api/sessions/"+shop, nil)["session"].(map[string]any)["paused"] != true {
		t.Fatal("pause did not reach the customer's number")
	}
	owner.ok("POST", fmt.Sprintf("/api/admin/numbers/%s/resume", shop), nil)
	if biz.ok("GET", "/api/sessions/"+shop, nil)["session"].(map[string]any)["paused"] != false {
		t.Fatal("resume did not reach the customer's number")
	}
	// but support cannot send from it or unlink it: no such route
	if code, _ := owner.do("POST", fmt.Sprintf("/api/admin/numbers/%s/send", shop), map[string]any{"to": "9811111111", "text": "hi"}, true); code < 400 {
		t.Fatalf("admin send route exists: %d", code)
	}
	// a customer cannot use the admin pages at all
	if code, _ := biz.do("GET", "/api/admin/numbers", nil, true); code != 403 && code != 404 {
		t.Fatalf("customer reached admin numbers: %d", code)
	}

	// the server's own .env key keeps full access, so automation and support scripts still work
	master := env.withKey(testKey)
	if got := names(master); len(got) != 2 {
		t.Fatalf("master key list: %v", got)
	}
	master.ok("GET", "/api/sessions/"+shop, nil)
}
