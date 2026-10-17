package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// One small SQLite file holds everything: whatsmeow's login keys and our
// own few tables. No chat history and no media are ever stored.
const schema = `
CREATE TABLE IF NOT EXISTS gw_sessions (
	name           TEXT PRIMARY KEY,
	jid            TEXT NOT NULL DEFAULT '',
	webhook_url    TEXT NOT NULL DEFAULT '',
	paused         INTEGER NOT NULL DEFAULT 0,
	paired_at      INTEGER NOT NULL DEFAULT 0,
	timelock_until INTEGER NOT NULL DEFAULT 0,
	proxy          TEXT NOT NULL DEFAULT '',
	pause_reason   TEXT NOT NULL DEFAULT '',
	health_since   INTEGER NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS gw_outbox (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	session     TEXT NOT NULL,
	campaign    TEXT NOT NULL DEFAULT '',
	to_addr     TEXT NOT NULL,
	payload     TEXT NOT NULL,
	status      TEXT NOT NULL,            -- queued | sending | sent | failed | skipped | cancelled
	error       TEXT NOT NULL DEFAULT '',
	wa_id       TEXT NOT NULL DEFAULT '',
	direct      INTEGER NOT NULL DEFAULT 0,
	new_contact INTEGER NOT NULL DEFAULT 0,
	attempts    INTEGER NOT NULL DEFAULT 0,
	not_before  INTEGER NOT NULL DEFAULT 0,
	priority    INTEGER NOT NULL DEFAULT 0, -- 2 recent repliers, 1 known, 0 never talked
	pool        TEXT NOT NULL DEFAULT '',   -- comma list of sessions this row may move between
	created_at  INTEGER NOT NULL,
	sent_at     INTEGER NOT NULL DEFAULT 0,
	finished_at INTEGER NOT NULL DEFAULT 0,
	wa_error    INTEGER NOT NULL DEFAULT 0  -- 1 = WhatsApp itself refused (counts for health)
);
CREATE INDEX IF NOT EXISTS gw_outbox_queue ON gw_outbox(session, status, id);
CREATE INDEX IF NOT EXISTS gw_outbox_to ON gw_outbox(to_addr, sent_at);
CREATE INDEX IF NOT EXISTS gw_outbox_sent ON gw_outbox(session, sent_at);
CREATE INDEX IF NOT EXISTS gw_outbox_campaign ON gw_outbox(campaign);
CREATE TABLE IF NOT EXISTS gw_contacts (
	session TEXT NOT NULL,
	jid     TEXT NOT NULL,
	last_in INTEGER NOT NULL,
	PRIMARY KEY (session, jid)
);
CREATE INDEX IF NOT EXISTS gw_contacts_jid ON gw_contacts(jid);
CREATE TABLE IF NOT EXISTS gw_optouts (
	phone   TEXT PRIMARY KEY,   -- legacy (v1) table, migrated into optouts
	keyword TEXT NOT NULL DEFAULT '',
	session TEXT NOT NULL DEFAULT '',
	at      INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS optouts (
	owner_id INTEGER NOT NULL,   -- each customer has their own STOP list
	phone    TEXT NOT NULL,      -- digits only
	keyword  TEXT NOT NULL DEFAULT '',
	session  TEXT NOT NULL DEFAULT '',
	at       INTEGER NOT NULL,
	PRIMARY KEY (owner_id, phone)
);
CREATE TABLE IF NOT EXISTS gw_numbers (
	phone      TEXT PRIMARY KEY,
	jid        TEXT NOT NULL DEFAULT '',
	is_in      INTEGER NOT NULL,
	checked_at INTEGER NOT NULL
);
`

func openDB(dataDir string) (*sql.DB, error) {
	path := filepath.Join(dataDir, "gateway.db")
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=10000&_synchronous=NORMAL&_txlock=immediate", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite works best with a small pool; WAL lets readers run alongside the writer.
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}
	if err = migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrate2(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// dayStart is local midnight (set TZ=Asia/Kolkata in the container).
func dayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func countSentSince(ctx context.Context, db *sql.DB, session string, since time.Time, onlyNew bool) (int, error) {
	q := `SELECT COUNT(*) FROM gw_outbox WHERE session=? AND status='sent' AND sent_at>=?`
	if onlyNew {
		q += ` AND new_contact=1`
	}
	var n int
	err := db.QueryRowContext(ctx, q, session, since.Unix()).Scan(&n)
	return n, err
}

func rememberContact(ctx context.Context, db *sql.DB, session, jid string) {
	_, _ = db.ExecContext(ctx, `INSERT INTO gw_contacts(session,jid,last_in) VALUES(?,?,?)
		ON CONFLICT(session,jid) DO UPDATE SET last_in=excluded.last_in`, session, jid, time.Now().Unix())
}

func isKnownContact(ctx context.Context, db *sql.DB, session string, jids ...string) bool {
	for _, j := range jids {
		if j == "" {
			continue
		}
		var x int
		if db.QueryRowContext(ctx, `SELECT 1 FROM gw_contacts WHERE session=? AND jid=?`, session, j).Scan(&x) == nil {
			return true
		}
	}
	return false
}

// cleanup removes old finished queue rows and stale number checks.
func cleanup(ctx context.Context, db *sql.DB, retentionDays int) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	cut := time.Now().AddDate(0, 0, -retentionDays).Unix()
	_, _ = db.ExecContext(ctx, `DELETE FROM gw_outbox WHERE status IN ('sent','failed','skipped','cancelled') AND created_at<?`, cut)
	_, _ = db.ExecContext(ctx, `DELETE FROM gw_numbers WHERE checked_at<?`, time.Now().AddDate(0, 0, -30).Unix())
	_, _ = db.ExecContext(ctx, `DELETE FROM autoreply_burst WHERE at<?`, time.Now().Add(-time.Hour).Unix())
	_, _ = db.ExecContext(ctx, `DELETE FROM auth_tokens WHERE expires_at<?`, time.Now().Unix())
}

// migrate adds columns introduced after the first release to older databases.
func migrate(db *sql.DB) error {
	adds := []struct{ table, col, def string }{
		{"gw_sessions", "proxy", "TEXT NOT NULL DEFAULT ''"},
		{"gw_sessions", "pause_reason", "TEXT NOT NULL DEFAULT ''"},
		{"gw_sessions", "health_since", "INTEGER NOT NULL DEFAULT 0"},
		{"gw_outbox", "priority", "INTEGER NOT NULL DEFAULT 0"},
		{"gw_outbox", "pool", "TEXT NOT NULL DEFAULT ''"},
		{"gw_outbox", "finished_at", "INTEGER NOT NULL DEFAULT 0"},
		{"gw_outbox", "wa_error", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, a := range adds {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, a.table, a.col).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", a.table, a.col, a.def)); err != nil {
				return fmt.Errorf("migrate %s.%s: %w", a.table, a.col, err)
			}
		}
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS gw_outbox_queue2 ON gw_outbox(session, status, priority, id)`)
	return err
}

// digitsOf returns the user part of a JID / number for opt-out lookups.
func digitsOf(addr string) string {
	if i := strings.IndexAny(addr, "@:"); i >= 0 {
		addr = addr[:i]
	}
	return nonDigits.ReplaceAllString(addr, "")
}

func isOptedOut(ctx context.Context, db *sql.DB, owner int64, addr string) bool {
	d := digitsOf(addr)
	if d == "" {
		return false
	}
	var x int
	return db.QueryRowContext(ctx, `SELECT 1 FROM optouts WHERE owner_id=? AND phone=?`, owner, d).Scan(&x) == nil
}

// addOptout records a STOP for one customer; returns true if it was new.
func addOptout(ctx context.Context, db *sql.DB, owner int64, phone, keyword, session string) bool {
	res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO optouts(owner_id, phone, keyword, session, at) VALUES(?,?,?,?,?)`,
		owner, phone, keyword, session, time.Now().Unix())
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	if n > 0 { // nothing queued by this customer should still go out
		_, _ = db.ExecContext(ctx, `UPDATE gw_outbox SET status='skipped', error='opted_out' WHERE status='queued' AND owner_id=? AND to_addr LIKE ?`, owner, phone+"@%")
	}
	return n > 0
}
