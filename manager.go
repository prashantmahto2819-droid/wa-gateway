package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type whatsmeowDevice = store.Device

var sessionNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

var (
	errNoSession     = errors.New("session not found")
	errSessionExists = errors.New("session already exists")
	errBadName       = errors.New("session name must be 1-32 characters: a-z, 0-9, _ or -")
)

// Manager owns all sessions (one per WhatsApp number).
type Manager struct {
	ctx       context.Context
	cfg       *Config
	db        *sql.DB
	container *sqlstore.Container
	hooks     *Webhooks
	lic       *Licensing
	schedWake chan struct{}
	log       *slog.Logger
	waLog     waLog.Logger

	mu       sync.RWMutex
	sessions map[string]*Session
}

func newManager(ctx context.Context, cfg *Config, db *sql.DB, hooks *Webhooks, lic *Licensing, log *slog.Logger, waLogLevel string) (*Manager, error) {
	previewLog = log
	wl := waLog.Stdout("WA", waLogLevel, false)
	container := sqlstore.NewWithDB(db, "sqlite3", wl.Sub("DB"))
	if err := container.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("prepare WhatsApp store: %w", err)
	}
	// How the gateway appears in the phone's "Linked devices" list.
	store.SetOSInfo(cfg.DeviceName, [3]uint32{2, 0, 0})
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()

	return &Manager{
		ctx: ctx, cfg: cfg, db: db, container: container, hooks: hooks, lic: lic, log: log, waLog: wl,
		sessions: map[string]*Session{}, schedWake: make(chan struct{}, 1),
	}, nil
}

// loadAll restores sessions from the database and reconnects linked ones.
func (m *Manager) loadAll() error {
	_, _ = m.db.ExecContext(m.ctx, `UPDATE gw_outbox SET status='queued' WHERE status='sending'`)
	rows, err := m.db.QueryContext(m.ctx, `SELECT name, jid, owner_id FROM gw_sessions ORDER BY name`)
	if err != nil {
		return err
	}
	type pair struct {
		name, jid string
		owner     int64
	}
	var list []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.name, &p.jid, &p.owner); err == nil {
			list = append(list, p)
		}
	}
	rows.Close()
	for _, p := range list {
		s := m.add(p.name)
		s.ownerID.Store(p.owner)
		if p.jid == "" {
			continue
		}
		jid, err := types.ParseJID(p.jid)
		if err != nil {
			continue
		}
		go func() {
			if err := s.connectExisting(m.ctx, jid); err != nil {
				m.log.Warn("reconnect failed, will retry", "session", s.name, "err", err)
			}
		}()
	}
	go m.watchdog()
	go m.runScheduler()
	return nil
}

func (m *Manager) add(name string) *Session {
	s := newSession(m, name)
	qctx, cancel := context.WithCancel(m.ctx)
	s.stopQueue = cancel
	m.mu.Lock()
	m.sessions[name] = s
	m.mu.Unlock()
	go s.runQueue(qctx)
	return s
}

func (m *Manager) create(name, label string, owner int64, webhookURL string) (*Session, error) {
	if !sessionNameRe.MatchString(name) {
		return nil, errBadName
	}
	m.mu.RLock()
	_, exists := m.sessions[name]
	m.mu.RUnlock()
	if exists {
		return nil, errSessionExists
	}
	if label == "" {
		label = name
	}
	if _, err := m.db.ExecContext(m.ctx, `INSERT INTO gw_sessions(name, label, owner_id, webhook_url, created_at) VALUES(?,?,?,?,?)`,
		name, label, owner, webhookURL, time.Now().Unix()); err != nil {
		return nil, errSessionExists
	}
	s := m.add(name)
	s.ownerID.Store(owner)
	return s, nil
}

// reloadOwners refreshes session owners after accounts change (first setup).
func (m *Manager) reloadOwners() {
	for _, s := range m.list() {
		var owner int64
		if m.db.QueryRow(`SELECT owner_id FROM gw_sessions WHERE name=?`, s.name).Scan(&owner) == nil {
			s.ownerID.Store(owner)
		}
	}
}

// listFor returns the sessions a user may see.
func (m *Manager) listFor(p *Principal) []*Session {
	all := m.list()
	if p.Admin() {
		return all
	}
	out := all[:0:0]
	for _, s := range all {
		if s.owner() == p.UserID {
			out = append(out, s)
		}
	}
	return out
}

func (m *Manager) get(name string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[name]
	if !ok {
		return nil, errNoSession
	}
	return s, nil
}

func (m *Manager) list() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (m *Manager) remove(ctx context.Context, name string) error {
	s, err := m.get(name)
	if err != nil {
		return err
	}
	s.logout(ctx)
	if s.stopQueue != nil {
		s.stopQueue()
	}
	m.mu.Lock()
	delete(m.sessions, name)
	m.mu.Unlock()
	_, _ = m.db.ExecContext(ctx, `DELETE FROM gw_outbox WHERE session=? AND status IN ('queued','sending')`, name)
	_, _ = m.db.ExecContext(ctx, `DELETE FROM gw_contacts WHERE session=?`, name)
	_, err = m.db.ExecContext(ctx, `DELETE FROM gw_sessions WHERE name=?`, name)
	return err
}

// watchdog reconnects linked sessions that stayed offline and cleans old data.
func (m *Manager) watchdog() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	lastClean := time.Time{}
	offlineSince := map[string]time.Time{}
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(lastClean) > 6*time.Hour {
			cleanup(m.ctx, m.db, m.cfg.RetentionDays)
			lastClean = time.Now()
		}
		for _, s := range m.list() {
			s.mu.Lock()
			cli, st := s.client, s.status
			s.mu.Unlock()
			linked := cli != nil && cli.Store.ID != nil
			s.mu.Lock()
			banned := s.banUntil.After(time.Now())
			s.mu.Unlock()
			if !linked || st == StReplaced || st == StLoggedOut || st == StOutdated || banned || cli.IsConnected() {
				delete(offlineSince, s.name)
				continue
			}
			since, seen := offlineSince[s.name]
			if !seen {
				offlineSince[s.name] = time.Now()
				continue
			}
			// whatsmeow retries by itself; step in only if it gave up for 5+ minutes.
			if time.Since(since) > 5*time.Minute {
				m.log.Warn("session offline too long, reconnecting", "session", s.name)
				go s.kick()
				offlineSince[s.name] = time.Now()
			}
		}
	}
}

func (m *Manager) closeAll() {
	for _, s := range m.list() {
		s.close()
	}
}
