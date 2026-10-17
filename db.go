package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
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
	created_at     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS gw_outbox (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	session     TEXT NOT NULL,
	campaign    TEXT NOT NULL DEFAULT '',
	to_addr     TEXT NOT NULL,
	payload     TEXT NOT NULL,
	status      TEXT NOT NULL,            -- queued | sending | sent | failed | cancelled
	error       TEXT NOT NULL DEFAULT '',
	wa_id       TEXT NOT NULL DEFAULT '',
	direct      INTEGER NOT NULL DEFAULT 0,
	new_contact INTEGER NOT NULL DEFAULT 0,
	attempts    INTEGER NOT NULL DEFAULT 0,
	not_before  INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	sent_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS gw_outbox_queue ON gw_outbox(session, status, id);
CREATE INDEX IF NOT EXISTS gw_outbox_sent ON gw_outbox(session, sent_at);
CREATE INDEX IF NOT EXISTS gw_outbox_campaign ON gw_outbox(campaign);
CREATE TABLE IF NOT EXISTS gw_contacts (
	session TEXT NOT NULL,
	jid     TEXT NOT NULL,
	last_in INTEGER NOT NULL,
	PRIMARY KEY (session, jid)
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
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=10000&_synchronous=NORMAL", path)
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
	_, _ = db.ExecContext(ctx, `DELETE FROM gw_outbox WHERE status IN ('sent','failed','cancelled') AND created_at<?`, cut)
	_, _ = db.ExecContext(ctx, `DELETE FROM gw_numbers WHERE checked_at<?`, time.Now().AddDate(0, 0, -30).Unix())
}
