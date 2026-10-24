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
