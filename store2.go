package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Version-2 tables: accounts, licences, CRM, campaigns, automation.
const schema2 = `
CREATE TABLE IF NOT EXISTS users (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	email      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	name       TEXT NOT NULL DEFAULT '',
	phone      TEXT NOT NULL DEFAULT '',
	pass_hash  TEXT NOT NULL,
	role       TEXT NOT NULL DEFAULT 'user',
	status     TEXT NOT NULL DEFAULT 'active',
	created_at INTEGER NOT NULL,
	last_login INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS auth_tokens (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	ip         TEXT NOT NULL DEFAULT '',
	ua         TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS api_keys (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name       TEXT NOT NULL DEFAULT '',
	prefix     TEXT NOT NULL,
	key_hash   TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	last_used  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS plans (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	name             TEXT NOT NULL,
	description      TEXT NOT NULL DEFAULT '',
	price            REAL NOT NULL DEFAULT 0,
	currency         TEXT NOT NULL DEFAULT 'INR',
	period_days      INTEGER NOT NULL DEFAULT 30,
	max_numbers      INTEGER NOT NULL DEFAULT 1,
	daily_limit      INTEGER NOT NULL DEFAULT 250,
	monthly_messages INTEGER NOT NULL DEFAULT 5000,
	max_contacts     INTEGER NOT NULL DEFAULT 5000,
	storage_mb       INTEGER NOT NULL DEFAULT 200,
	features         TEXT NOT NULL DEFAULT '{}',
	public           INTEGER NOT NULL DEFAULT 1,
	trial            INTEGER NOT NULL DEFAULT 0,
	highlight        INTEGER NOT NULL DEFAULT 0,
	sort             INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS licences (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	lkey          TEXT NOT NULL UNIQUE,
	kind          TEXT NOT NULL DEFAULT 'saas',
	status        TEXT NOT NULL DEFAULT 'unused',
	plan_id       INTEGER NOT NULL REFERENCES plans(id),
	user_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
	days          INTEGER NOT NULL DEFAULT 30,
	max_instances INTEGER NOT NULL DEFAULT 1,
	notes         TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL,
	activated_at  INTEGER NOT NULL DEFAULT 0,
	expires_at    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS licences_user ON licences(user_id, status, expires_at);
CREATE TABLE IF NOT EXISTS licence_activations (
	licence_id  INTEGER NOT NULL REFERENCES licences(id) ON DELETE CASCADE,
	instance_id TEXT NOT NULL,
	domain      TEXT NOT NULL DEFAULT '',
	version     TEXT NOT NULL DEFAULT '',
	ip          TEXT NOT NULL DEFAULT '',
	first_seen  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	PRIMARY KEY (licence_id, instance_id)
);
CREATE TABLE IF NOT EXISTS contacts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_id   INTEGER NOT NULL,
	phone      TEXT NOT NULL,
	name       TEXT NOT NULL DEFAULT '',
	email      TEXT NOT NULL DEFAULT '',
	tags       TEXT NOT NULL DEFAULT '',   -- ",vip,korba," (comma wrapped for LIKE)
	notes      TEXT NOT NULL DEFAULT '',
	custom     TEXT NOT NULL DEFAULT '{}',
	source     TEXT NOT NULL DEFAULT 'manual',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL DEFAULT 0,
	UNIQUE (owner_id, phone)
);
CREATE TABLE IF NOT EXISTS media (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_id   INTEGER NOT NULL,
	name       TEXT NOT NULL,
	mime       TEXT NOT NULL,
	kind       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	path       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS templates (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_id   INTEGER NOT NULL,
	name       TEXT NOT NULL,
	message    TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS campaigns (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_id    INTEGER NOT NULL,
	name        TEXT NOT NULL,
	status      TEXT NOT NULL,
	sessions    TEXT NOT NULL,
	message     TEXT NOT NULL,
	audience    TEXT NOT NULL,
	sched_at    INTEGER NOT NULL DEFAULT 0,
	repeat      TEXT NOT NULL DEFAULT 'none',
	days        TEXT NOT NULL DEFAULT '[]',
	until       INTEGER NOT NULL DEFAULT 0,
	next_run    INTEGER NOT NULL DEFAULT 0,
	runs        INTEGER NOT NULL DEFAULT 0,
	speed       TEXT NOT NULL DEFAULT 'normal',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL,
	started_at  INTEGER NOT NULL DEFAULT 0,
	finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS campaigns_due ON campaigns(status, next_run);
CREATE TABLE IF NOT EXISTS autoreplies (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_id     INTEGER NOT NULL,
	name         TEXT NOT NULL DEFAULT '',
	session      TEXT NOT NULL DEFAULT '',
	trigger_type TEXT NOT NULL,
	keywords     TEXT NOT NULL DEFAULT '[]',
	reply        TEXT NOT NULL DEFAULT '{}',
	ai           INTEGER NOT NULL DEFAULT 0,
	ai_prompt    TEXT NOT NULL DEFAULT '',
	only_dm      INTEGER NOT NULL DEFAULT 1,
	cooldown_min INTEGER NOT NULL DEFAULT 60,
	hours        TEXT NOT NULL DEFAULT '{}',
	active       INTEGER NOT NULL DEFAULT 1,
	priority     INTEGER NOT NULL DEFAULT 10,
	hits         INTEGER NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS autoreply_log (
	rule_id INTEGER NOT NULL,
	chat    TEXT NOT NULL,
	at      INTEGER NOT NULL,
	PRIMARY KEY (rule_id, chat)
);
CREATE TABLE IF NOT EXISTS chat_state (
	session     TEXT NOT NULL,
	chat        TEXT NOT NULL,
	human_until INTEGER NOT NULL DEFAULT 0,   -- auto-replies paused until (human takeover)
	menu        TEXT NOT NULL DEFAULT '',     -- last menu options JSON
	menu_at     INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session, chat)
);
CREATE TABLE IF NOT EXISTS polls (
	session  TEXT NOT NULL,
	msg_id   TEXT NOT NULL,
	chat     TEXT NOT NULL,
	name     TEXT NOT NULL,
	options  TEXT NOT NULL,
	at       INTEGER NOT NULL,
	PRIMARY KEY (session, msg_id)
);
CREATE TABLE IF NOT EXISTS webhook_endpoints (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	url         TEXT NOT NULL,
	secret      TEXT NOT NULL,
	events      TEXT NOT NULL DEFAULT '["*"]',
	active      INTEGER NOT NULL DEFAULT 1,
	last_status INTEGER NOT NULL DEFAULT 0,
	last_at     INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS user_ai (
	user_id  INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	base_url TEXT NOT NULL DEFAULT '',
	model    TEXT NOT NULL DEFAULT '',
	api_key  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS inbox (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	email      TEXT NOT NULL,
	phone      TEXT NOT NULL DEFAULT '',
	message    TEXT NOT NULL,
	ip         TEXT NOT NULL DEFAULT '',
	read       INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL DEFAULT 0,
	action  TEXT NOT NULL,
	detail  TEXT NOT NULL DEFAULT '',
	ip      TEXT NOT NULL DEFAULT '',
	at      INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS stats_daily (
	session  TEXT NOT NULL,
	day      TEXT NOT NULL,
	owner_id INTEGER NOT NULL DEFAULT 0,
	sent     INTEGER NOT NULL DEFAULT 0,
	failed   INTEGER NOT NULL DEFAULT 0,
	received INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session, day)
);
CREATE INDEX IF NOT EXISTS stats_owner ON stats_daily(owner_id, day);
`

func migrate2(db *sql.DB) error {
	if _, err := db.Exec(schema2); err != nil {
		return fmt.Errorf("create v2 tables: %w", err)
	}
	adds := []struct{ table, col, def string }{
		{"gw_sessions", "owner_id", "INTEGER NOT NULL DEFAULT 0"},
		{"gw_sessions", "label", "TEXT NOT NULL DEFAULT ''"},
		{"gw_sessions", "settings", "TEXT NOT NULL DEFAULT '{}'"},
		{"gw_outbox", "campaign_id", "INTEGER NOT NULL DEFAULT 0"},
		{"gw_outbox", "speed_factor", "INTEGER NOT NULL DEFAULT 1"},
		{"gw_outbox", "owner_id", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, a := range adds {
		if err := addColumn(db, a.table, a.col, a.def); err != nil {
			return err
		}
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS gw_outbox_cid ON gw_outbox(campaign_id, status)`)
	return err
}

func addColumn(db *sql.DB, table, col, def string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, col).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, def)); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", table, col, err)
		}
	}
	return nil
}

// ---------- settings (site-wide key/value) ----------

var defaultSettings = map[string]string{
	"site_name":       "Vivid Relay",
	"tagline":         "WhatsApp campaigns, automation and API — safely.",
	"company_name":    "VividBrandify",
	"support_email":   "",
	"support_phone":   "",
	"whatsapp_number": "",
	"address":         "",
	"gstin":           "",
	"signup_open":     "true",
	"trial_days":      "7",
	"trial_plan_id":   "",
	"currency":        "INR",
	"payment_note":    "",
	"announcement":    "",
}

// publicSettingKeys may be shown to anyone.
var publicSettingKeys = []string{"site_name", "tagline", "company_name", "support_email", "support_phone", "whatsapp_number", "address", "gstin", "currency", "payment_note", "announcement", "signup_open"}

func getSettings(ctx context.Context, db *sql.DB) map[string]string {
	out := make(map[string]string, len(defaultSettings))
	for k, v := range defaultSettings {
		out[k] = v
	}
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out[k] = v
		}
	}
	return out
}

func getSetting(ctx context.Context, db *sql.DB, key string) string {
	var v string
	if db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v) == nil {
		return v
	}
	return defaultSettings[key]
}

func setSetting(ctx context.Context, db *sql.DB, key, value string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---------- audit ----------

func audit(ctx context.Context, db *sql.DB, userID int64, ip, action, detail string) {
	_, _ = db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO audit(user_id, action, detail, ip, at) VALUES(?,?,?,?,?)`,
		userID, action, detail, ip, time.Now().Unix())
}

// ---------- stats ----------

func bumpStat(ctx context.Context, db *sql.DB, session string, ownerID int64, field string) {
	switch field {
	case "sent", "failed", "received":
	default:
		return
	}
	day := time.Now().Format("2006-01-02")
	_, _ = db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO stats_daily(session, day, owner_id, `+field+`) VALUES(?,?,?,1)
		ON CONFLICT(session, day) DO UPDATE SET `+field+`=`+field+`+1, owner_id=excluded.owner_id`, session, day, ownerID)
}

// ---------- small JSON helpers for TEXT columns ----------

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func fromJSON[T any](s string) T {
	var v T
	_ = json.Unmarshal([]byte(s), &v)
	return v
}

// tags are stored as ",a,b," so `tags LIKE '%,a,%'` finds them.
func packTags(tags []string) string {
	seen := map[string]bool{}
	var clean []string
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(t, ",", " ")))
		if t != "" && !seen[t] && len(t) <= 40 {
			seen[t] = true
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	return "," + strings.Join(clean, ",") + ","
}

func unpackTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(strings.Trim(s, ","), ",") {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
