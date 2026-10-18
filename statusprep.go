package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"go.mau.fi/libsignal/session"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// ======================= Status preparation =======================
//
// A phone posting a Status to 25,000 contacts already knows every contact's
// phones and already has an encryption session with each. A linked device
// starts with nothing, and asking WhatsApp for all of that at the moment of
// posting is slow, very large and looks like mass number-checking.
//
// So the gateway prepares the audience slowly in the background, the way a
// phone builds it up over time: in small batches with long pauses it looks
// up each contact's phones (by number, or by LID for contacts WhatsApp has
// moved to LID) and sets up the encryption sessions. It starts 10 minutes
// after the number connects and repeats once a day; at post time only new
// contacts still need a lookup.

const (
	prepBatch     = 200              // contacts per step
	prepPause     = 20 * time.Second // pause between steps
	prepKeyBatch  = 80               // devices per prekey request
	prepKeyPause  = 4 * time.Second
	prepRefresh   = 24 * time.Hour
	prepFirstWait = 10 * time.Minute
)

type prepTarget struct {
	jid types.JID // who to address (phone number, or LID)
	ok  bool      // has WhatsApp devices
	at  time.Time
}

type statusPrep struct {
	mu                    sync.Mutex
	running               bool
	total, checked, reach int
	sessions              int // encryption sessions set up in this run
	started, finished     time.Time
	lastErr               string
	targets               map[string]prepTarget // phone number → target
	safeSize              int                   // largest audience WhatsApp accepted in one Status (0 = not known yet)
	timer                 *time.Timer
}

// statusMax is the most people one Status goes to: the admin's setting, else .env.
func (m *Manager) statusMax(ctx context.Context) int {
	if a := m.adminPacing(ctx); a.StatusMax > 0 {
		return a.StatusMax
	}
	return m.cfg.StatusMax
}

// schedulePrep (re)arms the background preparation after a connect.
func (s *Session) schedulePrep(after time.Duration) {
	s.prep.mu.Lock()
	defer s.prep.mu.Unlock()
	if s.prep.timer != nil {
		s.prep.timer.Stop()
	}
	s.prep.timer = time.AfterFunc(after, func() {
		s.prepareStatus(s.m.ctx)
		s.schedulePrep(prepRefresh)
	})
}

func (s *Session) prepState() map[string]any {
	s.prep.mu.Lock()
	defer s.prep.mu.Unlock()
	known, ok := 0, 0
	for _, t := range s.prep.targets {
		if time.Since(t.at) < prepRefresh {
			known++
			if t.ok {
				ok++
			}
		}
	}
	out := map[string]any{"running": s.prep.running, "total": s.prep.total, "checked": s.prep.checked, "reachable": s.prep.reach,
		"sessions_set_up": s.prep.sessions, "known": known, "known_reachable": ok, "safe_size": s.prep.safeSize, "error": s.prep.lastErr}
	if !s.prep.started.IsZero() {
		out["started_at"] = s.prep.started.Unix()
	}
	if !s.prep.finished.IsZero() {
		out["finished_at"] = s.prep.finished.Unix()
	}
	return out
}

// prepareStatus runs one slow pass over the whole "Everyone" audience.
func (s *Session) prepareStatus(ctx context.Context) {
	s.prep.mu.Lock()
	if s.prep.running {
		s.prep.mu.Unlock()
		return
	}
	s.prep.running, s.prep.started, s.prep.checked, s.prep.reach, s.prep.sessions, s.prep.lastErr = true, time.Now(), 0, 0, 0, ""
	s.prep.mu.Unlock()
	defer func() {
		s.prep.mu.Lock()
		s.prep.running, s.prep.finished = false, time.Now()
		s.prep.mu.Unlock()
	}()
	fail := func(err error) {
		s.prep.mu.Lock()
		s.prep.lastErr = err.Error()
		s.prep.mu.Unlock()
		s.m.log.Warn("status preparation stopped", "session", s.name, "err", err)
	}
	cli, err := s.ready()
	if err != nil {
		fail(err)
		return
	}
	limit := s.m.statusMax(ctx)
	cands, err := s.statusCandidates(ctx, cli, s.owner(), &StatusAudience{}, limit+limit/4+20)
	if err != nil {
		fail(err)
		return
	}
	s.prep.mu.Lock()
	s.prep.total = len(cands)
	s.prep.mu.Unlock()
	s.m.log.Info("status preparation started", "session", s.name, "contacts", len(cands))
	for i := 0; i < len(cands); i += prepBatch {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.ready(); err != nil {
			fail(err)
			return
		}
		chunk := cands[i:min(i+prepBatch, len(cands))]
		reach, _ := s.reachable(ctx, cli, chunk)
		made := s.ensureSessions(ctx, cli, reach)
		s.prep.mu.Lock()
		s.prep.checked += len(chunk)
		s.prep.reach += len(reach)
		s.prep.sessions += made
		s.prep.mu.Unlock()
		if i+prepBatch < len(cands) {
			if sleepCtx(ctx, prepPause) != nil {
				return
			}
		}
	}
	st := s.prepState()
	s.m.log.Info("status preparation done", "session", s.name, "checked", st["checked"], "reachable", st["reachable"], "sessions_set_up", st["sessions_set_up"])
}

// ensureSessions sets up encryption sessions with the phones of these people
// (only where none exists yet), a few dozen devices per request.
func (s *Session) ensureSessions(ctx context.Context, cli *whatsmeow.Client, people []types.JID) int {
	if len(people) == 0 {
		return 0
	}
	devs, err := cli.GetUserDevices(ctx, people) // cached by the lookup just before
	if err != nil {
		return 0
	}
	own := cli.Store.GetJID().User
	ownLID := cli.Store.GetLID().User
	var pn []types.JID
	for _, d := range devs {
		if d.Server == types.DefaultUserServer {
			pn = append(pn, d)
		}
	}
	lids, _ := cli.Store.LIDs.GetManyLIDsForPNs(ctx, pn)
	identity := map[types.JID]types.JID{}
	var missing []types.JID
	for _, d := range devs {
		if d.User == own || d.User == ownLID {
			continue
		}
		id := d
		if l, ok := lids[d]; ok && !l.IsEmpty() {
			id = l // WhatsApp encrypts to the LID identity when one is known
		}
		has, err := cli.Store.ContainsSession(ctx, id.SignalAddress())
		if err != nil || has {
			continue
		}
		identity[d] = id
		missing = append(missing, d)
	}
	made := 0
	for i := 0; i < len(missing); i += prepKeyBatch {
		chunk := missing[i:min(i+prepKeyBatch, len(missing))]
		bundles := cli.DangerousInternals().FetchPreKeysNoError(ctx, chunk)
		s.sendMu.Lock() // don't race a message being encrypted right now
		for d, b := range bundles {
			if b == nil {
				continue
			}
			id, ok := identity[d]
			if !ok {
				id = d
			}
			builder := session.NewBuilderFromSignal(cli.Store, id.SignalAddress(), store.SignalProtobufSerializer)
			if builder.ProcessBundle(ctx, b) == nil {
				made++
			}
		}
		s.sendMu.Unlock()
		if i+prepKeyBatch < len(missing) && sleepCtx(ctx, prepKeyPause) != nil {
			break
		}
	}
	return made
}

// ---------- API ----------

func (a *API) statusPrepInfo(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	out := s.prepState()
	out["ok"] = true
	out["status_max"] = a.m.statusMax(r.Context())
	writeJSON(w, http.StatusOK, out)
}

func (a *API) statusPrepStart(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	if _, err := s.ready(); err != nil {
		writeErr(w, http.StatusConflict, "not_connected", errors.New("connect this number first"))
		return
	}
	go s.prepareStatus(context.WithoutCancel(s.m.ctx))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true})
}
