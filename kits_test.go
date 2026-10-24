package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestKitCatalogue(t *testing.T) {
	c := kits()
	if len(c.Kits) != 30 {
		t.Fatalf("kits: %d", len(c.Kits))
	}
	k := kitByID("news_media")
	if k == nil || len(k.Bot) == 0 || len(k.Plans) == 0 || len(k.Forms) == 0 {
		t.Fatalf("news kit incomplete: %+v", k)
	}
	for _, k := range c.Kits {
		if _, ok := c.Groups[k.Group]; !ok {
			t.Errorf("%s: unknown group %q", k.ID, k.Group)
		}
		for _, p := range k.Plans {
			for _, st := range p.Steps {
				if st.Action == "auto_whatsapp" && strings.TrimSpace(st.Message) == "" {
					t.Errorf("%s/%s: automatic step without text", k.ID, p.Key)
				}
			}
		}
	}
	if stripEmoji("📋 Quote चाहिए") != "Quote चाहिए" || stripEmoji("Plain") != "Plain" {
		t.Fatalf("stripEmoji: %q", stripEmoji("📋 Quote चाहिए"))
	}
}

func TestKitApplySequencesRenewalsForms(t *testing.T) {
	env := newTestEnv(t, nil)
	owner := env.browser()
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	ctx := context.Background()
	db := env.api.m.db

	// Catalogue + apply the insurance kit for two sub-categories.
	cat := owner.ok("GET", "/api/kits", nil)
	if len(cat["kits"].([]any)) != 30 {
		t.Fatalf("catalogue: %d", len(cat["kits"].([]any)))
	}
	ap := owner.ok("POST", "/api/business/apply", map[string]any{"kit": "insurance", "subs": []string{"motor", "health"}, "name": "Sharma Insurance", "phone": "9812345678", "city": "Korba"})
	created := ap["created"].(map[string]any)
	k := kitByID("insurance")
	if num(created["autoreplies"]) != int64(len(k.Bot)+2) || num(created["sequences"]) != int64(len(k.Plans)) || num(created["renewal_types"]) != int64(len(k.Renewals)) || num(created["tasks"]) != int64(len(k.Checklists)) {
		t.Fatalf("apply counts: %v", created)
	}
	biz := owner.ok("GET", "/api/business", nil)
	b := biz["business"].(map[string]any)
	if b["kit"] != "insurance" || b["code"] == "" || len(b["forms"].([]any)) != len(k.Forms) {
		t.Fatalf("business: %v", b)
	}
	// Fields are filtered by sub-category: life-only fields are gone, motor ones stay.
	var hasLife, hasMotor bool
	for _, f := range b["fields"].([]any) {
		switch f.(map[string]any)["id"] {
		case "nominee":
			hasLife = true
		case "vehicle_no":
			hasMotor = true
		}
	}
	if hasLife || !hasMotor {
		t.Fatalf("fields not filtered by sub-category: life=%v motor=%v", hasLife, hasMotor)
	}
	// Kit auto-replies: welcome menu + one per question; re-applying replaces them, keeps hand-made ones.
	owner.ok("POST", "/api/autoreplies", map[string]any{"name": "mine", "trigger": "keyword", "keywords": []string{"price"}, "reply": map[string]any{"text": "₹100"}})
	owner.ok("POST", "/api/business/apply", map[string]any{"kit": "insurance", "subs": []string{"motor"}})
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM autoreplies WHERE owner_id=1`).Scan(&n)
	if n != len(k.Bot)+2+1 {
		t.Fatalf("autoreplies after re-apply: %d", n)
	}
	var menuRule AutoReply
	row := db.QueryRow(`SELECT ` + arCols + ` FROM autoreplies WHERE trigger_type='menu' AND source='kit' ORDER BY id LIMIT 1`)
	ar, err := scanAR(row)
	if err != nil {
		t.Fatal(err)
	}
	menuRule = *ar
	if !menuRule.matches(false, "", &MenuOpt{ID: "k1"}, time.Now()) || !menuRule.matches(false, "", &MenuOpt{ID: "x", Text: k.Bot[0][0]}, time.Now()) {
		t.Fatalf("menu rule does not match its option: %v", menuRule.Keywords)
	}

	// Sequences: enrol a contact, run the scheduler → step 0 (auto message) needs a
	// number → logged; day-0 call step becomes a task.
	seqs := owner.ok("GET", "/api/sequences", nil)["sequences"].([]any)
	var leadSeq map[string]any
	for _, s := range seqs {
		if s.(map[string]any)["trigger"] == "lead_new" {
			leadSeq = s.(map[string]any)
		}
	}
	if leadSeq == nil {
		t.Fatal("no lead_new sequence")
	}
	cid := num(owner.ok("POST", "/api/contacts", map[string]any{"phone": "9811111111", "name": "Ravi Kumar"})["contact"].(map[string]any)["id"])
	en := owner.ok("POST", fmt.Sprintf("/api/sequences/%v/enroll", leadSeq["id"]), map[string]any{"contacts": []int64{cid}})
	if num(en["started"]) != 1 {
		t.Fatalf("enrol: %v", en)
	}
	en = owner.ok("POST", fmt.Sprintf("/api/sequences/%v/enroll", leadSeq["id"]), map[string]any{"contacts": []int64{cid}})
	if num(en["started"]) != 0 {
		t.Fatalf("double enrol: %v", en)
	}
	env.api.m.runSequences(ctx) // step 0: auto_whatsapp (no number linked → noted)
	env.api.m.runSequences(ctx) // step 1 is same-day → 2 minutes later, not yet
	runs := owner.ok("GET", "/api/sequences/runs?status=active", nil)["runs"].([]any)
	if len(runs) != 1 || num(runs[0].(map[string]any)["step"]) != 1 {
		t.Fatalf("run after first tick: %v", runs)
	}
	var note string
	_ = db.QueryRow(`SELECT note FROM sequence_log WHERE step=0`).Scan(&note)
	if note != "no number linked" {
		t.Fatalf("step 0 note: %q", note)
	}
	_, _ = db.Exec(`UPDATE sequence_runs SET next_at=0`)
	env.api.m.runSequences(ctx) // step 1: call → task
	tasks := owner.ok("GET", "/api/tasks?view=today", nil)["tasks"].([]any)
	var callTask map[string]any
	for _, x := range tasks {
		if x.(map[string]any)["source"] == "sequence" {
			callTask = x.(map[string]any)
		}
	}
	if callTask == nil || !strings.Contains(callTask["title"].(string), "Ravi Kumar") {
		t.Fatalf("no call task: %v", tasks)
	}
	// A reply stops the run (stop_on_reply plan).
	env.api.m.stopRunsOnReply(ctx, 1, "919811111111")
	var st string
	_ = db.QueryRow(`SELECT status FROM sequence_runs`).Scan(&st)
	if st != "replied" {
		t.Fatalf("run after reply: %s", st)
	}
	// Repeating checklist task rolls forward when done.
	var kitTask map[string]any
	for _, x := range owner.ok("GET", "/api/tasks?view=all", nil)["tasks"].([]any) {
		if x.(map[string]any)["source"] == "kit" {
			kitTask = x.(map[string]any)
		}
	}
	before := num(kitTask["due"])
	tk := owner.ok("PATCH", fmt.Sprintf("/api/tasks/%v", kitTask["id"]), map[string]any{"done": true})["task"].(map[string]any)
	if num(tk["done_at"]) != 0 || num(tk["due"]) <= before {
		t.Fatalf("repeating task did not roll forward: %v", tk)
	}

	// Renewals: a type from the kit, one renewal due in 7 days → reminder "b7" today.
	types := owner.ok("GET", "/api/renewal-types", nil)["types"].([]any)
	var life map[string]any
	for _, x := range types {
		if x.(map[string]any)["key"] == "life_premium" {
			life = x.(map[string]any)
		}
	}
	if life == nil {
		t.Fatal("no life_premium type")
	}
	due := dayStart(time.Now()).AddDate(0, 0, 7).Unix()
	ren := owner.ok("POST", "/api/renewals", map[string]any{"type_id": life["id"], "contact_id": cid, "title": "LIC Jeevan", "amount": "12000", "due": due})["renewal"].(map[string]any)
	if ren["phone"] != "919811111111" || ren["name"] != "Ravi Kumar" {
		t.Fatalf("renewal from contact: %v", ren)
	}
	now := time.Now()
	if now.Hour() >= 9 {
		env.api.m.runRenewals(ctx)
		env.api.m.runRenewals(ctx)
		var reminded string
		_ = db.QueryRow(`SELECT reminded FROM renewals WHERE id=?`, ren["id"]).Scan(&reminded)
		if reminded != `["b7"]` {
			t.Fatalf("reminded: %s", reminded)
		}
		_ = db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE source='renewal'`).Scan(&n)
		if n != 1 {
			t.Fatalf("renewal staff task: %d", n)
		}
	}
	// Done → next year's renewal appears.
	out := owner.ok("PATCH", fmt.Sprintf("/api/renewals/%v", ren["id"]), map[string]any{"status": "done"})
	next := out["next"].(map[string]any)
	if num(next["due"]) != time.Unix(due, 0).AddDate(1, 0, 0).Unix() {
		t.Fatalf("next renewal: %v", next)
	}
	if n := len(owner.ok("GET", "/api/renewals?status=open", nil)["renewals"].([]any)); n != 1 {
		t.Fatalf("open renewals: %d", n)
	}

	// Public form: GET renders, POST creates a contact, task, entry and starts the lead sequence.
	code := b["code"].(string)
	resp, err := http.Get(env.srv.URL + "/f/" + code + "/quote")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("form page: %v %v", err, resp)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "Sharma Insurance") || !strings.Contains(string(page), `name="policy_type"`) {
		t.Fatalf("form page content: %.300s", page)
	}
	form := url.Values{"name": {"Sita Devi"}, "phone": {"98222 22222"}, "policy_type": {"Health (स्वास्थ्य)"}, "city": {"Korba"}}
	resp, err = http.PostForm(env.srv.URL+"/f/"+code+"/quote", form)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("form post: %v %v", err, resp)
	}
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "धन्यवाद") || !strings.Contains(string(page), "wa.me/919812345678") {
		t.Fatalf("thank-you page: %.300s", page)
	}
	cs := owner.ok("GET", "/api/contacts?q=Sita", nil)["contacts"].([]any)
	if len(cs) != 1 {
		t.Fatalf("form contact: %v", cs)
	}
	sc := cs[0].(map[string]any)
	if sc["phone"] != "919822222222" || sc["custom"].(map[string]any)["policy_type"] != "Health (स्वास्थ्य)" || sc["source"] != "form" {
		t.Fatalf("form contact fields: %v", sc)
	}
	entries := owner.ok("GET", "/api/business/entries", nil)["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries: %v", entries)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sequence_runs WHERE phone='919822222222' AND status='active'`).Scan(&n)
	if n != 1 {
		t.Fatalf("lead sequence not started from form: %d", n)
	}
	// Missing required field → 400 with the page re-rendered.
	resp, _ = http.PostForm(env.srv.URL+"/f/"+code+"/quote", url.Values{"name": {"X"}, "phone": {"9833333333"}})
	if resp.StatusCode != 400 {
		t.Fatalf("incomplete form accepted: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Unknown form → 404.
	resp, _ = http.Get(env.srv.URL + "/f/" + code + "/nope")
	if resp.StatusCode != 404 {
		t.Fatalf("unknown form: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
