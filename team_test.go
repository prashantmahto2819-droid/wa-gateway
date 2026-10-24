package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestStaffInviteAndRoles(t *testing.T) {
	env := newTestEnv(t, nil)
	owner := env.browser()
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	// A normal (non-superadmin) business owner.
	biz := env.browser()
	biz.ok("POST", "/auth/signup", map[string]any{"name": "Biz", "email": "biz@example.com", "password": "secret123"})
	sess := biz.ok("POST", "/api/sessions", map[string]any{"name": "shop"})["session"].(map[string]any)["name"].(string)

	// Invite an agent by link.
	inv := biz.ok("POST", "/api/team/invites", map[string]any{"name": "Ravi", "email": "ravi@example.com", "role": "agent"})
	link := inv["link"].(string)
	token := link[strings.Index(link, "token=")+6:]
	if code, _ := biz.do("POST", "/api/team/invites", map[string]any{"role": "owner"}, true); code != 400 {
		t.Fatalf("owner role invitable: %d", code)
	}
	// Public info + join.
	info := env.browser().ok("GET", "/auth/invite?token="+token, nil)
	if info["role"] != "agent" || info["business"] != "Biz" {
		t.Fatalf("invite info: %v", info)
	}
	agent := env.browser()
	agent.ok("POST", "/auth/join", map[string]any{"token": token, "password": "agentpass1"})
	me := agent.ok("GET", "/api/me", nil)
	ws := me["workspace"].(map[string]any)
	if ws["role"] != "agent" || num(ws["id"]) == num(ws["actor_id"]) || me["user"].(map[string]any)["email"] != "biz@example.com" {
		t.Fatalf("agent acts in the owner's workspace: %v", ws)
	}
	// The agent sees the owner's numbers but can't link new ones or export.
	if n := len(agent.ok("GET", "/api/sessions", nil)["sessions"].([]any)); n != 1 {
		t.Fatalf("agent sees %d numbers", n)
	}
	if code, _ := agent.do("POST", "/api/sessions", map[string]any{"name": "x"}, true); code != 403 {
		t.Fatalf("agent linked a number: %d", code)
	}
	if code, _ := agent.do("GET", "/api/contacts/export.csv", nil, true); code != 403 {
		t.Fatalf("agent exported: %d", code)
	}
	if code, _ := agent.do("POST", "/api/team/invites", map[string]any{"role": "agent"}, true); code != 403 {
		t.Fatalf("agent invited staff: %d", code)
	}
	agent.ok("POST", "/api/contacts", map[string]any{"phone": "9811111111", "name": "By agent"}) // agents may add contacts
	if n := num(biz.ok("GET", "/api/contacts", nil)["total"]); n != 1 {
		t.Fatalf("contact not in the workspace: %v", n)
	}
	// Used invite can't be reused; disabled member is logged out.
	if code, _ := env.browser().do("POST", "/auth/join", map[string]any{"token": token, "password": "another123"}, true); code != 404 {
		t.Fatalf("invite reused: %d", code)
	}
	members := biz.ok("GET", "/api/team", nil)["members"].([]any)
	uid := members[0].(map[string]any)["user_id"]
	biz.ok("PATCH", fmt.Sprintf("/api/team/%v", uid), map[string]any{"role": "manager"})
	if agent.ok("GET", "/api/me", nil)["workspace"].(map[string]any)["role"] != "manager" {
		t.Fatal("role change not applied")
	}
	biz.ok("PATCH", fmt.Sprintf("/api/team/%v", uid), map[string]any{"status": "disabled"})
	if code, _ := agent.do("GET", "/api/me", nil, true); code != 401 {
		t.Fatalf("disabled staff still logged in: %d", code)
	}
	_ = sess
}
