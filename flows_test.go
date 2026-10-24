package main

import (
	"fmt"
	"testing"
)

func TestFlowEngineViaSandbox(t *testing.T) {
	env := newTestEnv(t, nil)
	owner := env.browser()
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})

	graph := map[string]any{
		"nodes": []map[string]any{
			{"id": "s", "type": "start", "data": map[string]any{}},
			{"id": "m1", "type": "message", "data": map[string]any{"label": "Welcome", "message": map[string]any{"type": "buttons", "body": "नमस्ते {{name}} जी, क्या चाहिए?", "buttons": []map[string]any{{"id": "quote", "title": "Quote"}, {"id": "talk", "title": "बात करें"}}}}},
			{"id": "q1", "type": "question", "data": map[string]any{"text": "कितने लोगों का?", "var": "members", "kind": "number", "retry": "सिर्फ़ संख्या लिखें"}},
			{"id": "c1", "type": "condition", "data": map[string]any{"var": "members", "op": "gt", "value": "4"}},
			{"id": "big", "type": "message", "data": map[string]any{"message": map[string]any{"type": "text", "text": "बड़ा परिवार — family floater सही रहेगा ({{members}} लोग)"}}},
			{"id": "small", "type": "message", "data": map[string]any{"message": map[string]any{"type": "text", "text": "Individual plan ठीक है"}}},
			{"id": "t1", "type": "tag", "data": map[string]any{"add": []string{"health-lead"}}},
			{"id": "h", "type": "assign", "data": map[string]any{"minutes": 60}},
			{"id": "e", "type": "end", "data": map[string]any{}},
		},
		"edges": []map[string]any{
			{"source": "s", "port": "next", "target": "m1"},
			{"source": "m1", "port": "quote", "target": "q1"},
			{"source": "m1", "port": "talk", "target": "h"},
			{"source": "q1", "port": "next", "target": "c1"},
			{"source": "c1", "port": "yes", "target": "big"},
			{"source": "c1", "port": "no", "target": "small"},
			{"source": "big", "port": "next", "target": "t1"},
			{"source": "small", "port": "next", "target": "t1"},
			{"source": "t1", "port": "next", "target": "e"},
			{"source": "h", "port": "next", "target": "e"},
		},
	}
	// Activating with a broken graph is refused; saving inactive keeps the problems list.
	bad := map[string]any{"nodes": []map[string]any{{"id": "s", "type": "start"}}, "edges": []any{}}
	if code, _ := owner.do("POST", "/api/flows", map[string]any{"name": "Broken", "trigger": "keyword", "keywords": []string{"x"}, "graph": bad, "active": true}, true); code != 400 {
		t.Fatalf("broken flow activated: %d", code)
	}
	f := owner.ok("POST", "/api/flows", map[string]any{"name": "Health quote", "trigger": "keyword", "keywords": []string{"health", "बीमा"}, "graph": graph, "active": true})["flow"].(map[string]any)
	if len(f["problems"].([]any)) != 0 {
		t.Fatalf("problems: %v", f["problems"])
	}
	id := fmt.Sprintf("%v", f["id"])
	test := func(body map[string]any) map[string]any { return owner.ok("POST", "/api/flows/"+id+"/test", body) }
	// start → welcome buttons, waits
	r := test(map[string]any{"reset": true, "text": "health"})
	reps := r["replies"].([]any)
	if len(reps) != 1 || r["waiting_at"] != "m1" || reps[0].(map[string]any)["buttons"] == nil {
		t.Fatalf("start: %v", r)
	}
	if txt := reps[0].(map[string]any)["text"].(string); txt != "नमस्ते Owner जी, क्या चाहिए?" {
		t.Fatalf("rendered: %q", txt)
	}
	// unknown answer → repeats the buttons
	r = test(map[string]any{"text": "???"})
	if r["waiting_at"] != "m1" || len(r["replies"].([]any)) != 1 {
		t.Fatalf("nomatch: %v", r)
	}
	// "1" picks the first button → question
	r = test(map[string]any{"text": "1"})
	if r["waiting_at"] != "q1" {
		t.Fatalf("choice by number: %v", r)
	}
	// invalid number → retry text, still waiting
	r = test(map[string]any{"text": "five"})
	if r["waiting_at"] != "q1" || r["replies"].([]any)[0].(map[string]any)["text"] != "सिर्फ़ संख्या लिखें" {
		t.Fatalf("retry: %v", r)
	}
	// valid → condition yes → big → tag → end
	r = test(map[string]any{"text": "6"})
	if r["done"] != true || r["replies"].([]any)[0].(map[string]any)["text"] != "बड़ा परिवार — family floater सही रहेगा (6 लोग)" {
		t.Fatalf("finish: %v", r)
	}
	if r["vars"].(map[string]any)["members"] != "6" {
		t.Fatalf("vars: %v", r["vars"])
	}
	// second path: button id "talk" → hand over → end
	r = test(map[string]any{"reset": true})
	r = test(map[string]any{"button_id": "talk"})
	events := r["events"].([]any)
	found := false
	for _, e := range events {
		if e.(map[string]any)["event"] == "assign" {
			found = true
		}
	}
	if !found || r["done"] != true {
		t.Fatalf("handover path: %v", r)
	}
	// Real matching: the flow triggers on its keyword for a session owner's number.
	if m := env.api.m.matchFlow(t.Context(), 1, "any", "919811111111", false, "  Health "); m == nil {
		t.Fatal("keyword flow not matched")
	}
	if m := env.api.m.matchFlow(t.Context(), 1, "any", "919811111111", false, "hello"); m != nil {
		t.Fatal("matched on unrelated text")
	}
	owner.ok("PATCH", "/api/flows/"+id, map[string]any{"test_mode": true, "test_phones": []string{"9811111111"}})
	if m := env.api.m.matchFlow(t.Context(), 1, "any", "919822222222", false, "health"); m != nil {
		t.Fatal("test mode let another phone in")
	}
	if m := env.api.m.matchFlow(t.Context(), 1, "any", "919811111111", false, "health"); m == nil {
		t.Fatal("test phone not matched")
	}
	// helpers
	if checkAnswer("phone", "98111 11111") != "919811111111" || checkAnswer("date", "5/1/26") != "2026-01-05" || checkAnswer("email", "x") != "" {
		t.Fatal("checkAnswer")
	}
	if pickPath(map[string]any{"a": []any{map[string]any{"b": 3.0}}}, "a.0.b") != "3" {
		t.Fatal("pickPath")
	}
}
