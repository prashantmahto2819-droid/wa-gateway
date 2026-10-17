package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	db, err := openDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &Config{DailyLimit: 100, NewContactLimit: 40, RecentReplyDays: 30, HealthMinSample: 10, HealthMaxFail: 0.25, HealthMinReply: 0.05, DataDir: t.TempDir()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lic, err := newLicensing(context.Background(), db, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	return &Manager{ctx: context.Background(), cfg: cfg, db: db, log: log, hooks: newWebhooks("", log), lic: lic, sessions: map[string]*Session{}, schedWake: make(chan struct{}, 1)}
}

func addSession(t *testing.T, m *Manager, name, jid string) *Session {
	t.Helper()
	if _, err := m.db.Exec(`INSERT INTO gw_sessions(name, jid, created_at) VALUES(?,?,?)`, name, jid, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	s := newSession(m, name)
	m.sessions[name] = s
	return s
}

func TestNormKeyword(t *testing.T) {
	for in, want := range map[string]string{"STOP!!": "stop", "  Stop karo 🙏": "stop karo", "बंद करो।": "बंद करो", "stop.please": "stop please"} {
		if got := normKeyword(in); got != want {
			t.Errorf("normKeyword(%q)=%q want %q", in, got, want)
		}
	}
}

func TestProxyHelpers(t *testing.T) {
	if p, err := validateProxy("socks5://u:secret@1.2.3.4:1080"); err != nil || p == "" {
		t.Fatal(p, err)
	}
	for _, bad := range []string{"ftp://h:1", "socks5://host", "nonsense"} {
		if _, err := validateProxy(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if m := maskProxy("http://user:secret@h:8080"); m != "http://user@h:8080" {
		t.Fatal(m)
	}
}

func TestPublicIP(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "169.254.169.254", "100.100.100.200", "::1", "64:ff9b::a9fe:a9fe", "0.0.0.0"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("%s must be blocked", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "142.250.1.1", "2606:4700::1111"} {
		if !publicIP(net.ParseIP(ip)) {
			t.Errorf("%s must be allowed", ip)
		}
	}
}

func TestPoolPickAndPriority(t *testing.T) {
	m := testManager(t)
	addSession(t, m, "a", "911111111111@s.whatsapp.net")
	addSession(t, m, "b", "912222222222@s.whatsapp.net")
	addSession(t, m, "off", "") // not linked: never chosen
	ctx := context.Background()
	friend := "919876543210@s.whatsapp.net"
	rememberContact(ctx, m.db, "b", friend)

	cands := m.poolCandidates(ctx, []string{"a", "b", "off"})
	if len(cands) != 2 {
		t.Fatalf("want 2 usable numbers, got %d", len(cands))
	}
	if got := m.pickSession(ctx, cands, friend); got != "b" {
		t.Fatalf("known contact must stay on its number, got %s", got)
	}
	counts := map[string]int{}
	for i := range 10 {
		counts[m.pickSession(ctx, cands, "9190000000"+string(rune('0'+i))+"0@s.whatsapp.net")]++
	}
	if counts["a"] != 5 || counts["b"] != 5 {
		t.Fatalf("new people should be spread evenly: %v", counts)
	}
	if p := m.priorityFor(ctx, []string{"a", "b"}, friend); p != 2 {
		t.Fatalf("recent replier priority %d", p)
	}
	if p := m.priorityFor(ctx, []string{"a", "b"}, "919999999999@s.whatsapp.net"); p != 0 {
		t.Fatalf("stranger priority %d", p)
	}
}

func TestHealthGuard(t *testing.T) {
	m := testManager(t)
	s := addSession(t, m, "a", "911111111111@s.whatsapp.net")
	ctx := context.Background()
	now := time.Now().Unix()
	ins := func(status string, newc int, to string) {
		sent := int64(0)
		if status == "sent" {
			sent = now - 60
		}
		_, err := m.db.Exec(`INSERT INTO gw_outbox(session,to_addr,payload,status,new_contact,created_at,sent_at,finished_at,wa_error) VALUES('a',?,'{}',?,?,?,?,?,?)`,
			to, status, newc, now-120, sent, now-60, b2i(status == "failed"))
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range 8 {
		ins("sent", 0, "x"+string(rune('a'+i)))
	}
	if h := s.health(ctx, s.row(ctx)); h.State != "ok" {
		t.Fatalf("too few samples, should be ok: %+v", h)
	}
	for range 5 {
		ins("failed", 0, "y")
	}
	if h := s.health(ctx, s.row(ctx)); h.State != "paused" {
		t.Fatalf("5/13 failed should pause: %+v", h)
	}
	// After a manual resume the window restarts.
	_, _ = m.db.Exec(`UPDATE gw_sessions SET health_since=? WHERE name='a'`, now+1)
	if h := s.health(ctx, s.row(ctx)); h.State != "ok" {
		t.Fatalf("resume should reset: %+v", h)
	}
	// Many new contacts, nobody replies -> slow.
	_, _ = m.db.Exec(`UPDATE gw_sessions SET health_since=0 WHERE name='a'`)
	_, _ = m.db.Exec(`DELETE FROM gw_outbox`)
	for i := range 12 {
		ins("sent", 1, "n"+string(rune('a'+i)))
	}
	if h := s.health(ctx, s.row(ctx)); h.State != "slow" {
		t.Fatalf("no replies should slow down: %+v", h)
	}
	_, _ = m.db.Exec(`INSERT INTO gw_contacts(session,jid,last_in) VALUES('a','na',?)`, now)
	if h := s.health(ctx, s.row(ctx)); h.Replied != 1 {
		t.Fatalf("reply should be counted: %+v", h)
	}
}

func TestOptoutAndWeekly(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	_, _ = m.db.Exec(`INSERT INTO optouts(owner_id, phone, at) VALUES(7, '919876543210', 1)`)
	if !isOptedOut(ctx, m.db, 7, "919876543210@s.whatsapp.net") || isOptedOut(ctx, m.db, 7, "919876543211@s.whatsapp.net") ||
		isOptedOut(ctx, m.db, 8, "919876543210@s.whatsapp.net") {
		t.Fatal("opt-out lookup wrong")
	}
	now := time.Now().Unix()
	for _, age := range []int64{3600, 2 * 86400, 10 * 86400} {
		_, _ = m.db.Exec(`INSERT INTO gw_outbox(session,to_addr,payload,status,created_at,sent_at) VALUES('a','p','{}','sent',?,?)`, now-age, now-age)
	}
	if n := weeklyCount(ctx, m.db, "p"); n != 2 {
		t.Fatalf("weekly count %d", n)
	}
}
