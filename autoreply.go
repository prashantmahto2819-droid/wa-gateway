package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type Hours struct {
	From int   `json:"from"`
	To   int   `json:"to"`
	Days []int `json:"days"`
}

type AutoReply struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	Session     string      `json:"session"`
	Trigger     string      `json:"trigger"`
	Keywords    []string    `json:"keywords"`
	Reply       SendRequest `json:"reply"`
	AI          bool        `json:"ai"`
	AIPrompt    string      `json:"ai_prompt"`
	OnlyDM      bool        `json:"only_dm"`
	CooldownMin int         `json:"cooldown_min"`
	Hours       Hours       `json:"hours"`
	Active      bool        `json:"active"`
	Priority    int         `json:"priority"`
	Hits        int         `json:"hits"`
	CreatedAt   int64       `json:"created_at"`
}

const arCols = `id, name, session, trigger_type, keywords, reply, ai, ai_prompt, only_dm, cooldown_min, hours, active, priority, hits, created_at`

func scanAR(row interface{ Scan(...any) error }) (*AutoReply, error) {
	var r AutoReply
	var kw, reply, hours string
	var ai, dm, active int
	if err := row.Scan(&r.ID, &r.Name, &r.Session, &r.Trigger, &kw, &reply, &ai, &r.AIPrompt, &dm, &r.CooldownMin, &hours, &active, &r.Priority, &r.Hits, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.Keywords = fromJSON[[]string](kw)
	if r.Keywords == nil {
		r.Keywords = []string{}
	}
	r.Reply = fromJSON[SendRequest](reply)
	r.Hours = fromJSON[Hours](hours)
	r.AI, r.OnlyDM, r.Active = ai == 1, dm == 1, active == 1
	return &r, nil
}

var regexCache sync.Map // pattern -> *regexp.Regexp (nil if invalid)

func cachedRegex(p string) *regexp.Regexp {
	if v, ok := regexCache.Load(p); ok {
		re, _ := v.(*regexp.Regexp)
		return re
	}
	re, err := regexp.Compile("(?i)" + p)
	if err != nil {
		re = nil
	}
	regexCache.Store(p, re)
	return re
}

// matches says whether an incoming message triggers this rule.
func (r *AutoReply) matches(firstTime bool, text string, choice *MenuOpt, now time.Time) bool {
	norm := normKeyword(text)
	switch r.Trigger {
	case "welcome":
		return firstTime
	case "keyword":
		for _, k := range r.Keywords {
			if nk := normKeyword(k); nk != "" && nk == norm {
				return true
			}
		}
	case "contains":
		for _, k := range r.Keywords {
			if nk := normKeyword(k); nk != "" && strings.Contains(norm, nk) {
				return true
			}
		}
	case "regex":
		for _, k := range r.Keywords {
			if re := cachedRegex(k); re != nil && re.MatchString(text) {
				return true
			}
		}
	case "menu":
		if choice == nil {
			return false
		}
		if len(r.Keywords) == 0 {
			return true
		}
		for _, k := range r.Keywords {
			if strings.EqualFold(strings.TrimSpace(k), choice.ID) || normKeyword(k) == normKeyword(choice.Text) {
				return true
			}
		}
	case "any":
		return true
	case "away":
		h := r.Hours
		if h.From == 0 && h.To == 0 {
			h.From, h.To = 9, 21
		}
		inDay := len(h.Days) == 0 || slices.Contains(h.Days, int(now.Weekday()))
		hr := now.Hour()
		open := hr >= h.From && hr < h.To
		if h.From > h.To { // overnight
			open = hr >= h.From || hr < h.To
		}
		return !(inDay && open)
	}
	return false
}

// runAutoReplies fires the first matching rule for an incoming message.
func (s *Session) runAutoReplies(chat types.JID, isGroup, firstTime bool, text string, choice *MenuOpt, pushName string, msgID types.MessageID, sender types.JID) {
	ctx, cancel := context.WithTimeout(s.m.ctx, 2*time.Minute)
	defer cancel()
	owner := s.owner()
	if s.m.lic.require(ctx, owner, "autoreply") != nil {
		return
	}
	var humanUntil int64
	_ = s.m.db.QueryRowContext(ctx, `SELECT human_until FROM chat_state WHERE session=? AND chat=?`, s.name, chat.String()).Scan(&humanUntil)
	if humanUntil > time.Now().Unix() {
		return // a person is handling this chat
	}
	rows, err := s.m.db.QueryContext(ctx, `SELECT `+arCols+` FROM autoreplies WHERE owner_id=? AND active=1 AND (session='' OR session=?) ORDER BY priority DESC, id`, owner, s.name)
	if err != nil {
		return
	}
	var rules []*AutoReply
	for rows.Next() {
		if r, err := scanAR(rows); err == nil {
			rules = append(rules, r)
		}
	}
	rows.Close()
	now := time.Now()
	for _, rule := range rules {
		if isGroup && rule.OnlyDM {
			continue
		}
		if !rule.matches(firstTime, text, choice, now) {
			continue
		}
		var last int64
		_ = s.m.db.QueryRowContext(ctx, `SELECT at FROM autoreply_log WHERE rule_id=? AND chat=?`, rule.ID, chat.String()).Scan(&last)
		if rule.CooldownMin > 0 && now.Unix()-last < int64(rule.CooldownMin)*60 {
			continue
		}
		req := rule.Reply
		if rule.AI {
			if s.m.lic.require(ctx, owner, "ai") != nil || strings.TrimSpace(text) == "" {
				continue
			}
			answer, err := s.m.askAI(ctx, owner, rule.AIPrompt, text)
			if err != nil {
				s.m.log.Warn("AI auto-reply failed", "session", s.name, "err", err)
				continue
			}
			req = SendRequest{Text: answer}
		}
		req.To = chat.String()
		name := pushName
		if name == "" {
			name = "there"
		}
		vars := map[string]string{"name": name, "phone": phoneOf(chat)}
		for k, v := range req.Vars {
			vars[k] = v
		}
		req.Vars = vars
		if isGroup {
			req.ReplyTo = &ReplyRef{ID: msgID, Sender: sender.String(), Text: truncate(text, 200)}
		}
		res, err := s.send(ctx, &req, sendOpts{direct: true})
		s.recordDirect(ctx, &req, res, err)
		if err != nil {
			s.m.log.Warn("auto-reply not sent", "session", s.name, "rule", rule.ID, "err", err)
			return
		}
		_, _ = s.m.db.ExecContext(ctx, `INSERT INTO autoreply_log(rule_id, chat, at) VALUES(?,?,?) ON CONFLICT(rule_id, chat) DO UPDATE SET at=excluded.at`,
			rule.ID, chat.String(), now.Unix())
		_, _ = s.m.db.ExecContext(ctx, `UPDATE autoreplies SET hits=hits+1 WHERE id=?`, rule.ID)
		return // one reply per message
	}
}

// menuChoice maps "2" (or the option text) to the menu we last sent this chat (24h).
func (s *Session) menuChoice(ctx context.Context, chat types.JID, text string) *MenuOpt {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 60 {
		return nil
	}
	var raw string
	var at int64
	if s.m.db.QueryRowContext(ctx, `SELECT menu, menu_at FROM chat_state WHERE session=? AND chat=?`, s.name, chat.String()).Scan(&raw, &at) != nil ||
		raw == "" || time.Since(time.Unix(at, 0)) > 24*time.Hour {
		return nil
	}
	opts := fromJSON[[]MenuOpt](raw)
	if n, err := strconv.Atoi(strings.Trim(text, ".)( ")); err == nil && n >= 1 && n <= len(opts) {
		return &opts[n-1]
	}
	for i := range opts {
		if normKeyword(opts[i].Text) == normKeyword(text) {
			return &opts[i]
		}
	}
	return nil
}

func (s *Session) onPollVote(ctx context.Context, cli *whatsmeow.Client, e *events.Message, voter types.JID) {
	if cli == nil {
		return
	}
	upd := e.Message.GetPollUpdateMessage()
	pollID := upd.GetPollCreationMessageKey().GetID()
	var raw, name string
	if s.m.db.QueryRowContext(ctx, `SELECT options, name FROM polls WHERE session=? AND msg_id=?`, s.name, pollID).Scan(&raw, &name) != nil {
		return // not one of our polls
	}
	vote, err := cli.DecryptPollVote(ctx, e)
	if err != nil {
		s.m.log.Warn("could not decrypt poll vote", "session", s.name, "err", err)
		return
	}
	options := fromJSON[[]string](raw)
	byHash := map[string]string{}
	for _, o := range options {
		h := sha256.Sum256([]byte(o))
		byHash[string(h[:])] = o
	}
	chosen := []string{}
	for _, h := range vote.GetSelectedOptions() {
		if o, ok := byHash[string(h)]; ok {
			chosen = append(chosen, o)
		}
	}
	s.hook("poll.vote", map[string]any{"poll_id": pollID, "poll": name, "chat": e.Info.Chat.ToNonAD().String(),
		"voter": voter.String(), "voter_phone": phoneOf(voter), "options": chosen, "timestamp": e.Info.Timestamp.Unix()})
}

func (s *Session) onCall(e *events.CallOffer) {
	ctx := s.m.ctx
	st := s.settings(ctx)
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()
	from := s.resolvePN(ctx, cli, e.From, types.JID{})
	rejected := false
	if st.RejectCalls && cli != nil {
		if err := cli.RejectCall(ctx, e.From, e.CallID); err == nil {
			rejected = true
		}
	}
	s.hook("call", map[string]any{"from": from.String(), "from_phone": phoneOf(from), "call_id": e.CallID, "rejected": rejected, "timestamp": e.Timestamp.Unix()})
	if rejected && strings.TrimSpace(st.RejectMessage) != "" && from.Server == types.DefaultUserServer {
		go func() {
			ctx, cancel := context.WithTimeout(s.m.ctx, time.Minute)
			defer cancel()
			req := &SendRequest{To: from.String(), Text: st.RejectMessage}
			res, err := s.send(ctx, req, sendOpts{direct: true})
			s.recordDirect(ctx, req, res, err)
		}()
	}
}

// ---------- AI (any OpenAI-compatible chat API, user's own key) ----------

func (m *Manager) askAI(ctx context.Context, owner int64, prompt, question string) (string, error) {
	var base, model, key string
	if m.db.QueryRowContext(ctx, `SELECT base_url, model, api_key FROM user_ai WHERE user_id=?`, owner).Scan(&base, &model, &key) != nil || key == "" || base == "" {
		return "", errors.New("AI is not set up (Settings → AI assistant)")
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	system := strings.TrimSpace(prompt)
	if system == "" {
		system = "You are a helpful assistant for a business on WhatsApp."
	}
	system += "\nReply in the same language as the customer (Hindi, Hinglish or English), briefly (under 80 words), in plain text without markdown. If you don't know something, say a team member will reply soon."
	body, _ := json.Marshal(map[string]any{
		"model": model, "temperature": 0.4, "max_tokens": 400,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": truncate(question, 2000)}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 45 * time.Second, Transport: m.hooks.guarded.Transport}
	if m.cfg.AllowPrivateURLs {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("AI answered HTTP %d", resp.StatusCode)
	}
	if out.Error != nil {
		return "", errors.New(out.Error.Message)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", errors.New("AI returned an empty answer")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// ---------- API ----------

func (a *API) listAutoReplies(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT `+arCols+` FROM autoreplies WHERE owner_id=? ORDER BY priority DESC, id`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []*AutoReply{}
	for rows.Next() {
		if ar, err := scanAR(rows); err == nil {
			out = append(out, ar)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "autoreplies": out})
}

func (a *API) saveAutoReply(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if err := a.lic.require(r.Context(), p.UserID, "autoreply"); err != nil {
		a.fail(w, err)
		return
	}
	ar := AutoReply{OnlyDM: true, CooldownMin: 60, Active: true, Priority: 10}
	if id := r.PathValue("id"); id != "" {
		old, err := scanAR(a.m.db.QueryRowContext(r.Context(), `SELECT `+arCols+` FROM autoreplies WHERE id=? AND owner_id=?`, id, p.UserID))
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("auto-reply not found"))
			return
		}
		ar = *old
	}
	if err := decode(r, &ar); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	switch ar.Trigger {
	case "welcome", "any", "away", "menu":
	case "keyword", "contains", "regex":
		if len(ar.Keywords) == 0 {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("add at least one keyword"))
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("trigger must be welcome, keyword, contains, regex, menu, any or away"))
		return
	}
	if ar.Trigger == "regex" {
		for _, k := range ar.Keywords {
			if _, err := regexp.Compile(k); err != nil {
				writeErr(w, http.StatusBadRequest, "bad_request", fmt.Errorf("invalid pattern %q", k))
				return
			}
		}
	}
	if ar.AI {
		if err := a.lic.require(r.Context(), p.UserID, "ai"); err != nil {
			a.fail(w, err)
			return
		}
	} else {
		ar.Reply.To = "x"
		if err := ar.Reply.validate(); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", fmt.Errorf("reply: %w", err))
			return
		}
		ar.Reply.To = ""
		if ar.Reply.Media != nil && ar.Reply.Media.Base64 != "" {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("reply media must be media.url or media.media_id"))
			return
		}
	}
	if ar.Session != "" {
		s, err := a.m.get(ar.Session)
		if err != nil || (!p.Admin() && s.owner() != p.UserID) {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("number not found"))
			return
		}
	}
	if ar.Keywords == nil {
		ar.Keywords = []string{}
	}
	args := []any{truncate(ar.Name, 100), ar.Session, ar.Trigger, jsonText(ar.Keywords), jsonText(ar.Reply), b2i(ar.AI), truncate(ar.AIPrompt, 4000),
		b2i(ar.OnlyDM), max(ar.CooldownMin, 0), jsonText(ar.Hours), b2i(ar.Active), ar.Priority}
	if ar.ID > 0 {
		_, err := a.m.db.ExecContext(r.Context(), `UPDATE autoreplies SET name=?, session=?, trigger_type=?, keywords=?, reply=?, ai=?, ai_prompt=?, only_dm=?, cooldown_min=?, hours=?, active=?, priority=? WHERE id=?`,
			append(args, ar.ID)...)
		if err != nil {
			a.fail(w, err)
			return
		}
	} else {
		res, err := a.m.db.ExecContext(r.Context(), `INSERT INTO autoreplies(name, session, trigger_type, keywords, reply, ai, ai_prompt, only_dm, cooldown_min, hours, active, priority, owner_id, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(args, p.UserID, time.Now().Unix())...)
		if err != nil {
			a.fail(w, err)
			return
		}
		ar.ID, _ = res.LastInsertId()
	}
	saved, _ := scanAR(a.m.db.QueryRowContext(r.Context(), `SELECT `+arCols+` FROM autoreplies WHERE id=?`, ar.ID))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "autoreply": saved})
}

func (a *API) deleteAutoReply(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM autoreplies WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("auto-reply not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) getAI(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var base, model, key string
	_ = a.m.db.QueryRowContext(r.Context(), `SELECT base_url, model, api_key FROM user_ai WHERE user_id=?`, p.UserID).Scan(&base, &model, &key)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "base_url": base, "model": model, "has_key": key != ""})
}

func (a *API) setAI(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var b struct {
		BaseURL *string `json:"base_url"`
		Model   *string `json:"model"`
		APIKey  *string `json:"api_key"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx := r.Context()
	_, _ = a.m.db.ExecContext(ctx, `INSERT OR IGNORE INTO user_ai(user_id) VALUES(?)`, p.UserID)
	if b.BaseURL != nil {
		u, err := url.Parse(strings.TrimSpace(*b.BaseURL))
		if *b.BaseURL != "" && (err != nil || u.Scheme != "https" || u.Host == "") {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("base_url must be an https:// address"))
			return
		}
		_, _ = a.m.db.ExecContext(ctx, `UPDATE user_ai SET base_url=? WHERE user_id=?`, strings.TrimRight(strings.TrimSpace(*b.BaseURL), "/"), p.UserID)
	}
	if b.Model != nil {
		_, _ = a.m.db.ExecContext(ctx, `UPDATE user_ai SET model=? WHERE user_id=?`, truncate(strings.TrimSpace(*b.Model), 100), p.UserID)
	}
	if b.APIKey != nil {
		_, _ = a.m.db.ExecContext(ctx, `UPDATE user_ai SET api_key=? WHERE user_id=?`, strings.TrimSpace(*b.APIKey), p.UserID)
	}
	a.getAI(w, r)
}

func (a *API) testAI(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var b struct {
		Prompt string `json:"prompt"`
	}
	_ = decode(r, &b)
	if b.Prompt == "" {
		b.Prompt = "Say hello in one short sentence."
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	reply, err := a.m.askAI(ctx, p.UserID, "", b.Prompt)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "ai_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reply": reply})
}
