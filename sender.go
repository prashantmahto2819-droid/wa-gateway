package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type SendResult struct {
	ID         string `json:"id"` // WhatsApp message id (use it for reply_to / receipts)
	To         string `json:"to"` // final JID it went to
	Timestamp  int64  `json:"timestamp"`
	NewContact bool   `json:"new_contact"` // true if this person never wrote to us before
}

// permanent errors are not retried by the queue.
func isPermanent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errNotOnWA) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"invalid", "is required", "too large", "must be", "not a valid", "unsupported", "media.url", "links are not", "not admin", "HTTP 4"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func serverErrCode(err error) string {
	if err != nil && errors.Is(err, whatsmeow.ErrServerReturnedError) {
		f := strings.Fields(err.Error())
		return f[len(f)-1]
	}
	return ""
}

// ---------- limits ----------

type sessionRow struct {
	jid           string
	paused        bool
	pairedAt      int64
	timelockUntil int64
	proxy         string
	pauseReason   string
	healthSince   int64
}

func (s *Session) row(ctx context.Context) sessionRow {
	var r sessionRow
	var paused int
	_ = s.m.db.QueryRowContext(ctx, `SELECT jid, paused, paired_at, timelock_until, proxy, pause_reason, health_since FROM gw_sessions WHERE name=?`, s.name).
		Scan(&r.jid, &paused, &r.pairedAt, &r.timelockUntil, &r.proxy, &r.pauseReason, &r.healthSince)
	r.paused = paused == 1
	return r
}

// dailyLimit grows from WARMUP_START by 1.6x per day after linking,
// up to DAILY_LIMIT, so a freshly linked number doesn't send a flood.
func (s *Session) dailyLimit(pairedAt int64) int {
	cfg := s.m.cfg
	if cfg.WarmupStart <= 0 || pairedAt == 0 {
		return cfg.DailyLimit
	}
	days := time.Since(time.Unix(pairedAt, 0)).Hours() / 24
	l := float64(cfg.WarmupStart) * math.Pow(1.6, math.Floor(max(days, 0)))
	return int(min(l, float64(cfg.DailyLimit)))
}

func (s *Session) timelocked(r sessionRow) bool {
	return r.timelockUntil > time.Now().Unix()
}

// checkLimits says whether one more message is allowed right now.
func (s *Session) checkLimits(ctx context.Context, r sessionRow, newContact, slow bool) error {
	now := time.Now()
	sent, err := countSentSince(ctx, s.m.db, s.name, dayStart(now), false)
	if err != nil {
		return err
	}
	if sent >= s.dailyLimit(r.pairedAt) {
		return errDailyLimit
	}
	if newContact {
		if s.timelocked(r) {
			return errTimelocked
		}
		n, err := countSentSince(ctx, s.m.db, s.name, dayStart(now), true)
		if err != nil {
			return err
		}
		limit := s.m.cfg.NewContactLimit
		if slow {
			limit = max(limit/2, 1)
		}
		if n >= limit {
			return errNewLimit
		}
	}
	return nil
}

// ---------- recipient resolution ----------

// classify parses the target and says whether this person is new to us
// (never wrote to this number). It does not touch the network.
func (s *Session) classify(ctx context.Context, cli *whatsmeow.Client, raw string) (types.JID, bool, error) {
	to, err := parseTarget(raw, s.m.cfg.DefaultCountryCode)
	if err != nil {
		return to, false, err
	}
	if to.Server != types.DefaultUserServer && to.Server != types.HiddenUserServer {
		return to, false, nil // groups and channels
	}
	if isKnownContact(ctx, s.m.db, s.name, to.String()) {
		return to, false, nil
	}
	if alt, err := cli.Store.GetAltJID(ctx, to); err == nil && !alt.IsEmpty() &&
		isKnownContact(ctx, s.m.db, s.name, alt.ToNonAD().String()) {
		return to, false, nil
	}
	return to, true, nil
}

// verifyNumber checks a new phone number is on WhatsApp before the first
// message (cached 30 days). It runs only after the limits allow sending,
// because mass number lookups are themselves a ban signal.
func (s *Session) verifyNumber(ctx context.Context, cli *whatsmeow.Client, to types.JID) (types.JID, error) {
	if to.Server != types.DefaultUserServer {
		return to, nil
	}
	var isIn int
	var jidStr string
	err := s.m.db.QueryRowContext(ctx, `SELECT is_in, jid FROM gw_numbers WHERE phone=? AND checked_at>?`,
		to.User, time.Now().AddDate(0, 0, -30).Unix()).Scan(&isIn, &jidStr)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := cli.IsOnWhatsApp(ctx, []string{"+" + to.User})
		if err != nil {
			return to, err
		}
		isIn = 0
		if len(res) > 0 && res[0].IsIn {
			isIn, jidStr = 1, res[0].JID.ToNonAD().String()
		}
		_, _ = s.m.db.ExecContext(ctx, `INSERT OR REPLACE INTO gw_numbers(phone,jid,is_in,checked_at) VALUES(?,?,?,?)`,
			to.User, jidStr, isIn, time.Now().Unix())
	} else if err != nil {
		return to, err
	}
	if isIn == 0 {
		return to, errNotOnWA
	}
	if j, err := types.ParseJID(jidStr); err == nil && !j.IsEmpty() {
		to = j.ToNonAD()
	}
	return to, nil
}

// ---------- the actual send ----------

type sendOpts struct {
	direct bool // API call (not queue): enforce DIRECT_GAP and record a row
	slow   bool // health guard: half the new-contact allowance
}

func (s *Session) send(ctx context.Context, req *SendRequest, opts sendOpts) (*SendResult, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	to, newContact, err := s.classify(ctx, cli, req.To)
	if err != nil {
		return &SendResult{To: to.String(), NewContact: newContact}, err
	}
	r := s.row(ctx)
	if err := s.checkLimits(ctx, r, newContact, opts.slow); err != nil {
		return &SendResult{To: to.String(), NewContact: newContact}, err
	}
	if newContact {
		if to, err = s.verifyNumber(ctx, cli, to); err != nil {
			return &SendResult{To: to.String(), NewContact: newContact}, err
		}
	}

	s.rndMu.Lock()
	text := renderText(req.Text, req.Vars, s.rnd)
	s.rndMu.Unlock()

	// Upload media before taking the send lock so a slow download
	// doesn't hold up other messages.
	msg, handle, err := buildMessage(ctx, cli, to, req, text, s.m.cfg)
	if err != nil {
		return &SendResult{To: to.String(), NewContact: newContact}, err
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if opts.direct {
		if wait := s.m.cfg.DirectGap - time.Since(s.lastSend); wait > 0 {
			if err := sleepCtx(ctx, wait); err != nil {
				return nil, err
			}
		}
	}

	isChat := to.Server != types.NewsletterServer
	typing := s.m.cfg.Typing
	if req.Typing != nil {
		typing = *req.Typing
	}
	if isChat {
		s.goOnline(ctx, cli)
		// Mark the message we're replying to as read, like a person would.
		if req.ReplyTo != nil && req.ReplyTo.ID != "" {
			sender := to
			if p, err := types.ParseJID(req.ReplyTo.Sender); err == nil && req.ReplyTo.Sender != "" {
				sender = p
			}
			_ = cli.MarkRead(ctx, []types.MessageID{req.ReplyTo.ID}, time.Now(), to, sender)
		}
		if typing {
			_ = cli.SendChatPresence(ctx, to, types.ChatPresenceComposing, types.ChatPresenceMediaText)
			s.rndMu.Lock()
			d := typingDelay(text, s.rnd)
			s.rndMu.Unlock()
			if err := sleepCtx(ctx, d); err != nil {
				return nil, err
			}
			_ = cli.SendChatPresence(ctx, to, types.ChatPresencePaused, types.ChatPresenceMediaText)
		}
	}

	resp, err := cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{MediaHandle: handle})
	s.lastSend = time.Now()
	res := &SendResult{To: to.String(), NewContact: newContact}
	if err != nil {
		switch serverErrCode(err) {
		case "463", "475": // reach-out timelock / new-contact cap
			until := time.Now().Add(s.m.cfg.TimelockPause).Unix()
			_, _ = s.m.db.ExecContext(ctx, `UPDATE gw_sessions SET timelock_until=MAX(timelock_until, ?) WHERE name=?`, until, s.name)
			s.hook("session.timelock", map[string]any{"active": true, "until": until, "type": "send-error-" + serverErrCode(err)})
			return res, errTimelocked
		}
		return res, err
	}
	res.ID, res.Timestamp = resp.ID, resp.Timestamp.Unix()
	return res, nil
}

// goOnline shows the number as online while sending and goes back to
// "unavailable" after a minute of quiet (so the phone keeps notifying).
func (s *Session) goOnline(ctx context.Context, cli *whatsmeow.Client) {
	s.mu.Lock()
	s.onlineGen++
	gen := s.onlineGen
	first := s.onlineStop == nil
	if !first {
		s.onlineStop.Stop()
	}
	s.onlineStop = time.AfterFunc(time.Minute, func() {
		s.mu.Lock()
		if s.onlineGen != gen { // a newer send extended the online time
			s.mu.Unlock()
			return
		}
		s.onlineStop = nil
		c := s.client
		s.mu.Unlock()
		if c != nil && c.IsConnected() {
			_ = c.SendPresence(context.Background(), types.PresenceUnavailable)
		}
	})
	s.mu.Unlock()
	if first { // network call outside the lock
		_ = cli.SendPresence(ctx, types.PresenceAvailable)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// recordDirect stores direct sends so daily limits count them too.
func (s *Session) recordDirect(ctx context.Context, req *SendRequest, res *SendResult, sendErr error) int64 {
	logged := *req
	if req.Media != nil && req.Media.Base64 != "" { // never keep file contents
		m := *req.Media
		m.Base64 = "(omitted)"
		logged.Media = &m
	}
	payload, _ := json.Marshal(&logged)
	status, errMsg, waID, to, nc := "sent", "", "", req.To, 0
	if res != nil {
		waID, nc = res.ID, b2i(res.NewContact)
		if res.To != "" {
			to = res.To
		}
	}
	sentAt := time.Now().Unix()
	if sendErr != nil {
		status, errMsg, sentAt = "failed", sendErr.Error(), 0
	}
	r, err := s.m.db.ExecContext(ctx, `INSERT INTO gw_outbox(session,to_addr,payload,status,error,wa_id,direct,new_contact,attempts,created_at,sent_at)
		VALUES(?,?,?,?,?,?,1,?,1,?,?)`, s.name, to, string(payload), status, errMsg, waID, nc, time.Now().Unix(), sentAt)
	if err != nil {
		return 0
	}
	id, _ := r.LastInsertId()
	return id
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- the queue (campaigns / bulk) ----------

func (s *Session) inSendingHours(now time.Time) bool {
	from, to := s.m.cfg.SendHourFrom, s.m.cfg.SendHourTo
	if from == 0 && to == 24 {
		return true
	}
	h := now.Hour()
	if from <= to {
		return h >= from && h < to
	}
	return h >= from || h < to // e.g. 22 -> 6
}

// runQueue sends queued messages one by one with human-like gaps.
func (s *Session) runQueue(ctx context.Context) {
	batch := 0
	for {
		wait := s.queueStep(ctx, &batch)
		if wait <= 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(wait):
		}
	}
}

// queueStep handles at most one message and returns how long to wait before the next try.
func (s *Session) queueStep(ctx context.Context, batch *int) time.Duration {
	if ctx.Err() != nil {
		return time.Second
	}
	cfg := s.m.cfg
	now := time.Now()
	r := s.row(ctx)
	if r.paused || r.jid == "" {
		return 5 * time.Minute
	}
	if _, err := s.ready(); err != nil {
		return time.Minute
	}
	s.mu.Lock()
	banned := s.banUntil.After(now)
	s.mu.Unlock()
	if banned || !s.inSendingHours(now) {
		return 5 * time.Minute
	}
	sent, _ := countSentSince(ctx, s.m.db, s.name, dayStart(now), false)
	if sent >= s.dailyLimit(r.pairedAt) {
		return time.Until(dayStart(now).Add(24*time.Hour)) + time.Minute
	}
	h := s.health(ctx, r)
	s.applyHealth(ctx, h)
	if h.State == "paused" {
		return 5 * time.Minute
	}
	slow := h.State == "slow"

	var id int64
	var attempts int
	var payload, campaign, toAddr string
	err := s.m.db.QueryRowContext(ctx, `SELECT id, payload, campaign, attempts, to_addr FROM gw_outbox
		WHERE session=? AND status='queued' AND not_before<=? ORDER BY priority DESC, id LIMIT 1`, s.name, now.Unix()).
		Scan(&id, &payload, &campaign, &attempts, &toAddr)
	if errors.Is(err, sql.ErrNoRows) {
		*batch = 0
		return 5 * time.Minute // woken early by new messages
	}
	if err != nil {
		s.m.log.Error("queue read failed", "err", err)
		return time.Minute
	}
	var req SendRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		s.finish(ctx, id, campaign, "failed", "bad payload", nil)
		return 0
	}
	// Claim the row atomically: a cancel, STOP or pool move may have happened meanwhile.
	claim, err := s.m.db.ExecContext(ctx, `UPDATE gw_outbox SET status='sending', attempts=attempts+1 WHERE id=? AND session=? AND status='queued'`, id, s.name)
	if err != nil {
		return time.Minute
	}
	if n, _ := claim.RowsAffected(); n == 0 {
		return 0
	}
	// People who said STOP are skipped; people who got enough campaign messages this week wait.
	if isOptedOut(ctx, s.m.db, toAddr) {
		s.finish(context.WithoutCancel(ctx), id, campaign, "skipped", "opted_out", nil)
		return 0
	}
	if cfg.PerContactWeekly > 0 && weeklyCount(ctx, s.m.db, toAddr) >= cfg.PerContactWeekly {
		_, _ = s.m.db.ExecContext(context.WithoutCancel(ctx), `UPDATE gw_outbox SET status='queued', attempts=attempts-1, error='weekly_limit', not_before=? WHERE id=?`,
			now.Add(24*time.Hour).Unix(), id)
		return 0
	}

	sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	res, err := s.send(sctx, &req, sendOpts{slow: slow})
	cancel()

	// Bookkeeping must survive a shutdown, or a sent message could be sent twice.
	bctx := context.WithoutCancel(ctx)
	switch {
	case err == nil:
		s.finish(bctx, id, campaign, "sent", "", res)
	case errors.Is(err, errTimelocked), errors.Is(err, errNewLimit):
		// Hold new-contact messages back (this one and every queued
		// "never talked" row); known contacts keep going.
		nb := dayStart(now).Add(24 * time.Hour)
		if errors.Is(err, errTimelocked) {
			nb = time.Unix(max(s.row(bctx).timelockUntil, now.Add(time.Hour).Unix()), 0)
		}
		_, _ = s.m.db.ExecContext(bctx, `UPDATE gw_outbox SET status='queued', attempts=attempts-1, not_before=?, error=? WHERE id=?`, nb.Unix(), err.Error(), id)
		_, _ = s.m.db.ExecContext(bctx, `UPDATE gw_outbox SET not_before=? WHERE session=? AND status='queued' AND priority=0 AND not_before<?`,
			nb.Unix(), s.name, nb.Unix())
		return time.Second
	case errors.Is(err, errDailyLimit), errors.Is(err, errNotConnected), errors.Is(err, context.Canceled):
		_, _ = s.m.db.ExecContext(bctx, `UPDATE gw_outbox SET status='queued', attempts=attempts-1 WHERE id=?`, id)
		return time.Minute
	case isPermanent(err) || attempts+1 >= 3:
		s.finish(bctx, id, campaign, "failed", err.Error(), res)
	default:
		_, _ = s.m.db.ExecContext(bctx, `UPDATE gw_outbox SET status='queued', not_before=?, error=? WHERE id=?`,
			now.Add(3*time.Minute).Unix(), err.Error(), id)
	}

	factor := time.Duration(1)
	if slow {
		factor = 2
	}
	*batch++
	if cfg.BatchSize > 0 && *batch >= cfg.BatchSize {
		*batch = 0
		return factor * s.randDur(cfg.BatchPause, cfg.BatchPause+cfg.BatchPause/2)
	}
	return factor * s.randDur(cfg.MinDelay, cfg.MaxDelay)
}

func (s *Session) finish(ctx context.Context, id int64, campaign, status, errMsg string, res *SendResult) {
	waID, to, nc := "", "", 0
	if res != nil {
		waID, to, nc = res.ID, res.To, b2i(res.NewContact)
	}
	now := time.Now().Unix()
	sentAt := int64(0)
	if status == "sent" {
		sentAt = now
	}
	// Bad numbers / bad media are list problems, not a sign WhatsApp dislikes this number.
	waErr := b2i(status == "failed" && !isPermanent(errors.New(errMsg)))
	_, _ = s.m.db.ExecContext(ctx, `UPDATE gw_outbox SET status=?, error=?, wa_id=?, new_contact=?, sent_at=?, finished_at=?, wa_error=?,
		to_addr=CASE WHEN ?<>'' THEN ? ELSE to_addr END WHERE id=?`, status, errMsg, waID, nc, sentAt, now, waErr, to, to, id)
	s.hook("queue.status", map[string]any{"queue_id": id, "campaign": campaign, "status": status, "error": errMsg, "wa_id": waID, "to": to})
}
