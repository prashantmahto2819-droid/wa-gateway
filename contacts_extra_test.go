package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestContactFiltersMergeVCF(t *testing.T) {
	env := newTestEnv(t, nil)
	owner := env.browser()
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	a := owner.ok("POST", "/api/contacts", map[string]any{"phone": "9811111111", "name": "Ravi Kumar", "tags": []string{"vip"}, "notes": "old number"})["contact"].(map[string]any)
	b := owner.ok("POST", "/api/contacts", map[string]any{"phone": "9822222222", "name": "ravi kumar", "email": "ravi@x.in", "custom": map[string]string{"city": "Korba"}})["contact"].(map[string]any)
	owner.ok("POST", "/api/contacts", map[string]any{"phone": "9833333333", "name": ""})
	st := owner.ok("GET", "/api/contacts/stats", nil)
	if num(st["total"]) != 3 || num(st["duplicates"]) != 2 || num(st["noname"]) != 1 || num(st["untagged"]) != 2 {
		t.Fatalf("stats: %v", st)
	}
	if n := len(owner.ok("GET", "/api/contacts?filter=duplicates", nil)["contacts"].([]any)); n != 2 {
		t.Fatalf("duplicates filter: %d", n)
	}
	if n := len(owner.ok("GET", "/api/contacts?filter=noname", nil)["contacts"].([]any)); n != 1 {
		t.Fatalf("noname filter: %d", n)
	}
	if n := len(owner.ok("GET", "/api/contacts?source=manual", nil)["contacts"].([]any)); n != 3 {
		t.Fatalf("source filter: %d", n)
	}
	// merge b into a
	m := owner.ok("POST", "/api/contacts/merge", map[string]any{"keep": a["id"], "remove": []any{b["id"]}})
	c := m["contact"].(map[string]any)
	if num(m["merged"]) != 1 || c["email"] != "ravi@x.in" || c["custom"].(map[string]any)["city"] != "Korba" || c["custom"].(map[string]any)["other_phone"] != "919822222222" {
		t.Fatalf("merged: %v", c)
	}
	if n := num(owner.ok("GET", "/api/contacts", nil)["total"]); n != 2 {
		t.Fatalf("after merge: %v", n)
	}
	// vcf
	req, _ := http.NewRequest("GET", env.srv.URL+"/api/contacts/export.vcf", nil)
	req.Header.Set("X-API-Key", testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("vcf: %v %v", err, resp)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	vcf := string(body)
	if strings.Count(vcf, "BEGIN:VCARD") != 2 || !strings.Contains(vcf, "TEL;TYPE=CELL:+919811111111") || !strings.Contains(vcf, "FN:Ravi Kumar") || !strings.Contains(vcf, "CATEGORIES:vip") {
		t.Fatalf("vcf content: %s", vcf)
	}
}

// A status we can see means that person has our number saved and we have
// theirs. It is recorded on the contact and tagged, so a campaign can go to
// exactly those people — and nothing is ever sent back automatically.
func TestStatusSeenMarksTheContact(t *testing.T) {
	env := newTestEnv(t, nil)
	owner := env.browser()
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	sess := owner.ok("POST", "/api/sessions", map[string]any{"name": "shop"})["session"].(map[string]any)["name"].(string)
	s, err := env.api.m.get(sess)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	find := func(phone string) map[string]any {
		t.Helper()
		got := owner.ok("GET", "/api/contacts?q="+phone, nil)["contacts"].([]any)
		if len(got) != 1 {
			t.Fatalf("looking for %s: %v", phone, got)
		}
		return got[0].(map[string]any)
	}
	// someone already in Contacts
	owner.ok("POST", "/api/contacts", map[string]any{"phone": "9811111111", "name": "Ravi"})
	s.noteStatusSeen(ctx, "919811111111", "Ravi")
	c := find("9811111111")
	if num(c["status_seen"]) == 0 {
		t.Fatalf("status not recorded: %v", c)
	}
	tags := c["tags"].([]any)
	if len(tags) != 1 || tags[0] != statusTag {
		t.Fatalf("tag not added: %v", tags)
	}
	// a second status the same day must not write again
	first := num(c["status_seen"])
	s.noteStatusSeen(ctx, "919811111111", "Ravi")
	c = find("9811111111")
	if num(c["status_seen"]) != first {
		t.Fatal("wrote twice in one day")
	}

	// somebody not in Contacts is added, because this number saves contacts itself
	s.noteStatusSeen(ctx, "919822222222", "Sita")
	list := owner.ok("GET", "/api/contacts?filter=status", nil)
	if num(list["total"]) != 2 {
		t.Fatalf("status filter: %v", num(list["total"]))
	}
	if num(owner.ok("GET", "/api/contacts/stats", nil)["status"]) != 2 {
		t.Fatal("stats count")
	}
	var src string
	_ = env.api.m.db.QueryRow(`SELECT source FROM contacts WHERE phone='919822222222'`).Scan(&src)
	if src != "status" {
		t.Fatalf("source: %q", src)
	}

	// with auto-save off, an unknown person is left alone
	owner.ok("PATCH", "/api/sessions/"+sess, map[string]any{"settings": map[string]any{"auto_save_contacts": false, "takeover_minutes": 30}})
	s.noteStatusSeen(ctx, "919833333333", "Mohan")
	if n := num(owner.ok("GET", "/api/contacts", nil)["total"]); n != 2 {
		t.Fatalf("contact added while auto-save is off: %v", n)
	}
	// but an existing contact is still marked
	s.noteStatusSeen(ctx, "919811111111", "Ravi")

	// the tag is a normal one, so a campaign can be aimed at it
	owner.ok("POST", "/api/campaigns", map[string]any{"name": "Monthly", "sessions": []string{sess}, "draft": true,
		"message": map[string]any{"text": "महीने की याद"}, "audience": map[string]any{"tags": []string{statusTag}}})
}
