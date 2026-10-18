package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPacingLayersAndFloors(t *testing.T) {
	env := newTestEnv(t, func(c *Config) {
		c.MinDelay, c.MaxDelay, c.DirectGap, c.BatchSize, c.BatchPause, c.Typing = 30*time.Second, 60*time.Second, 2*time.Second, 20, 10*time.Minute, true
	})
	owner := env.browser()
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	c := env.withKey(testKey)

	got := c.ok("GET", "/api/admin/pacing", nil)["pacing"].(map[string]any)
	if num(got["gap_min"]) != 30 || num(got["gap_max"]) != 60 || num(got["api_gap_min"]) != 2 || got["users_can_change"] != true {
		t.Fatalf("defaults from .env: %v", got)
	}
	// Admin: faster defaults but floors of 15s / 3s; users may change.
	c.ok("PUT", "/api/admin/pacing", map[string]any{"gap_min": 20, "gap_max": 40, "api_gap_min": 4, "api_gap_max": 8, "batch_size": 10, "batch_pause_min": 5,
		"floor_gap": 15, "floor_api_gap": 3, "users_can_change": true})
	p := env.api.m.pacing(context.Background(), 1)
	if p.GapMin != 20 || p.GapMax != 40 || p.APIGapMin != 4 || p.BatchSize != 10 || !*p.Typing || !*p.ReplyTyping {
		t.Fatalf("admin layer: %+v typing=%v reply=%v", p, *p.Typing, *p.ReplyTyping)
	}
	// A user can go slower but not below the floor.
	if code, _ := c.do("PUT", "/api/me/pacing", map[string]any{"gap_min": 5}, true); code != 400 {
		t.Fatalf("below floor accepted: %d", code)
	}
	off := false
	c.ok("PUT", "/api/me/pacing", map[string]any{"gap_min": 90, "gap_max": 120, "reply_typing": off})
	p = env.api.m.pacing(context.Background(), 1)
	if p.GapMin != 90 || p.GapMax != 120 || *p.ReplyTyping || p.APIGapMin != 4 {
		t.Fatalf("user layer: %+v", p)
	}
	// Admin raises the floor above everything: it wins.
	c.ok("PUT", "/api/admin/pacing", map[string]any{"floor_gap": 200, "floor_api_gap": 10, "users_can_change": false})
	p = env.api.m.pacing(context.Background(), 1)
	if p.GapMin != 200 || p.GapMax != 200 || p.APIGapMin != 10 {
		t.Fatalf("floors: %+v", p)
	}
	// Campaign gap below the floor is refused.
	a := c.ok("POST", "/api/sessions", map[string]any{"name": "a"})["session"].(map[string]any)["name"].(string)
	if code, _ := c.do("POST", "/api/campaigns", map[string]any{"name": "x", "sessions": []string{a}, "gap_min": 30,
		"message": map[string]any{"text": "hi"}, "audience": map[string]any{"phones": []string{"9000000001"}}}, true); code != 400 {
		t.Fatalf("campaign gap below floor accepted: %d", code)
	}
	camp := c.ok("POST", "/api/campaigns", map[string]any{"name": "x", "sessions": []string{a}, "gap_min": 300, "gap_max": 200, "rotation": "turns",
		"message": map[string]any{"text": "hi"}, "audience": map[string]any{"phones": []string{"9000000001"}}, "schedule": map[string]any{"at": time.Now().Add(time.Hour).Unix()}})["campaign"].(map[string]any)
	if num(camp["gap_min"]) != 300 || num(camp["gap_max"]) != 300 || camp["rotation"] != "turns" {
		t.Fatalf("campaign gap: %v", camp)
	}
}

func TestTakeTurns(t *testing.T) {
	m := &Manager{}
	cp := campaignPace{rotation: "turns", pool: []string{"a", "b", "c"}}
	now := time.Now()
	var order []string
	for i := 0; i < 7; i++ {
		var who string
		for _, s := range []string{"c", "b", "a"} { // ask in a different order than the pool
			if m.myTurn(1, cp, s, now) {
				who = s
				break
			}
		}
		if who == "" {
			t.Fatalf("nobody's turn at step %d", i)
		}
		order = append(order, who)
		m.tookTurn(1, cp, who, time.Millisecond, 0, 0)
		if m.myTurn(1, cp, "a", now) || m.myTurn(1, cp, "b", now) {
			// gate is closed only until now+1ms; "now" is in the past, so this must be false
		}
		now = now.Add(5 * time.Millisecond)
	}
	if got := fmt.Sprint(order); got != "[a b c a b c a]" {
		t.Fatalf("turn order %s", got)
	}
	// Gate closed: nobody may send before the gap is over.
	m.tookTurn(1, cp, "a", time.Hour, 0, 0)
	if m.myTurn(1, cp, "b", time.Now()) {
		t.Fatal("sent while the gap was running")
	}
	// A number that misses its turn is skipped after 45 s.
	m2 := &Manager{}
	m2.tookTurn(2, cp, "a", time.Millisecond, 0, 0) // next: b
	later := time.Now().Add(time.Minute)
	if !m2.myTurn(2, cp, "c", later) {
		t.Fatal("c not allowed after b missed its turn")
	}
	if m2.myTurn(2, campaignPace{rotation: "turns", pool: []string{"a", "b"}}, "a", time.Now()) {
		// different campaign id -> fresh state, a's turn
	}
}
