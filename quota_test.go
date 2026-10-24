package main

import (
	"fmt"
	"testing"
	"time"
)

func TestQuotaEngineAndProxyPool(t *testing.T) {
	cfg := &Config{DailyLimit: 1000, WarmupStart: 20, NewContactLimit: 40}
	a := envPacing(cfg)
	now := time.Now()
	day := func(d int) int64 { return now.Add(-time.Duration(d) * 24 * time.Hour).Unix() }
	if q := quotaFor(a, cfg, day(0), now); q != 20 {
		t.Fatalf("day 1: %d", q)
	}
	if q := quotaFor(a, cfg, day(1), now); q != 32 {
		t.Fatalf("day 2: %d", q)
	}
	if q := quotaFor(a, cfg, day(30), now); q != 1000 {
		t.Fatalf("ceiling: %d", q)
	}
	a.WarmupStart, a.WarmupGrowthPct, a.DailyLimit = 50, 100, 300 // admin override
	if q := quotaFor(a, cfg, day(2), now); q != 200 {
		t.Fatalf("admin growth: %d", q)
	}
	if q := quotaFor(a, cfg, day(5), now); q != 300 {
		t.Fatalf("admin ceiling: %d", q)
	}
	a.WarmupStart = 0
	if q := quotaFor(a, cfg, day(0), now); q != 300 {
		t.Fatalf("no warm-up: %d", q)
	}

	env := newTestEnv(t, nil)
	owner := env.browser()
	owner.ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	biz := env.browser()
	biz.ok("POST", "/auth/signup", map[string]any{"name": "Biz", "email": "biz@example.com", "password": "secret123"})
	shop := biz.ok("POST", "/api/sessions", map[string]any{"name": "shop"})["session"].(map[string]any)["name"].(string)

	// Admin sets the quota; a number's own cap can only lower it.
	owner.ok("PUT", "/api/admin/pacing", map[string]any{"gap_min": 20, "gap_max": 40, "api_gap_min": 2, "api_gap_max": 5, "floor_gap": 8, "warmup_start": 0, "daily_limit": 80, "new_contact_limit": 25})
	info := biz.ok("GET", "/api/sessions", nil)["sessions"].([]any)[0].(map[string]any)
	q := info["quota"].(map[string]any)
	if num(q["effective_limit"]) != 80 || num(q["new_contact_limit"]) != 25 {
		t.Fatalf("quota after admin: %v", q)
	}
	biz.ok("PATCH", "/api/sessions/"+shop, map[string]any{"settings": map[string]any{"daily_limit": 60, "takeover_minutes": 30}})
	info = biz.ok("GET", "/api/sessions", nil)["sessions"].([]any)[0].(map[string]any)
	if num(info["quota"].(map[string]any)["effective_limit"]) != 60 || num(info["today"].(map[string]any)["limit"]) != 60 {
		t.Fatalf("own cap: %v", info["quota"])
	}

	// Proxy pool: a dedicated proxy for another user is invisible to Biz; shared ones show masked.
	ownerID := float64(1)
	_ = ownerID
	shared := owner.ok("POST", "/api/admin/proxies", map[string]any{"label": "Mumbai 1", "url": "socks5://u:p@203.0.113.10:1080", "kind": "shared"})["proxy"].(map[string]any)
	ded := owner.ok("POST", "/api/admin/proxies", map[string]any{"label": "Private", "url": "http://203.0.113.11:3128", "kind": "dedicated", "assigned_to": 1})["proxy"].(map[string]any)
	if shared["url"] != nil || shared["masked"] == "" {
		t.Fatalf("admin response leaks URL: %v", shared)
	}
	mine := biz.ok("GET", "/api/proxies", nil)["proxies"].([]any)
	if len(mine) != 1 || mine[0].(map[string]any)["label"] != "Mumbai 1" {
		t.Fatalf("user sees: %v", mine)
	}
	if code, _ := biz.do("PATCH", "/api/sessions/"+shop, map[string]any{"proxy_id": ded["id"]}, true); code != 400 {
		t.Fatalf("dedicated proxy of another user attached: %d", code)
	}
	biz.ok("PATCH", "/api/sessions/"+shop, map[string]any{"proxy_id": shared["id"]})
	var px string
	var pid int64
	_ = env.api.m.db.QueryRow(`SELECT proxy, proxy_id FROM gw_sessions WHERE name=?`, shop).Scan(&px, &pid)
	if px != "socks5://u:p@203.0.113.10:1080" || fmt.Sprint(pid) != fmt.Sprint(int64(num(shared["id"]))) {
		t.Fatalf("pool proxy not applied: %q %d", px, pid)
	}
	info = biz.ok("GET", "/api/sessions", nil)["sessions"].([]any)[0].(map[string]any)
	if info["proxy_label"] != "Mumbai 1" || info["proxy"] == px {
		t.Fatalf("session info: %v %v", info["proxy_label"], info["proxy"])
	}
	// Admin turns off own URLs → user can't set a raw proxy; re-assigning the pool proxy to someone else detaches it.
	owner.ok("POST", "/api/admin/proxies/settings", map[string]any{"own_allowed": false})
	if code, _ := biz.do("PATCH", "/api/sessions/"+shop, map[string]any{"proxy": "socks5://x:y@203.0.113.12:1080"}, true); code != 403 {
		t.Fatalf("own proxy allowed: %d", code)
	}
	owner.ok("PATCH", fmt.Sprintf("/api/admin/proxies/%v", shared["id"]), map[string]any{"kind": "dedicated", "assigned_to": 1})
	_ = env.api.m.db.QueryRow(`SELECT proxy, proxy_id FROM gw_sessions WHERE name=?`, shop).Scan(&px, &pid)
	if px != "" || pid != 0 {
		t.Fatalf("not detached after re-assignment: %q %d", px, pid)
	}
	usage := owner.ok("GET", "/api/admin/proxies", nil)
	if len(usage["proxies"].([]any)) != 2 {
		t.Fatalf("admin list: %v", usage)
	}
}
