package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// ======================= message variants =======================

// variant returns version n of a message: 0 is the message itself, 1.. are
// its variants. Recipient, personal variables and reply info always come
// from the main message.
func (r *SendRequest) variant(n int) *SendRequest {
	if n <= 0 || n > len(r.Variants) {
		c := *r
		c.Variants = nil
		return &c
	}
	v := r.Variants[n-1]
	v.To, v.Vars, v.ReplyTo, v.StatusAudience, v.Variants = r.To, r.Vars, r.ReplyTo, r.StatusAudience, nil
	if v.Typing == nil {
		v.Typing = r.Typing
	}
	if v.Preview == nil {
		v.Preview = r.Preview
	}
	if v.Background == "" {
		v.Background = r.Background
	}
	if v.UniqueCode == "" {
		v.UniqueCode, v.UniquePrefix = r.UniqueCode, r.UniquePrefix
	}
	return &v
}

// ======================= no double Status posts =======================

// statusFingerprint identifies a Status by its content (text, media, colour).
func statusFingerprint(req *SendRequest) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", req.Text, req.Background)
	if m := req.Media; m != nil {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00", m.Type, m.URL, m.MediaID)
		h.Write([]byte(m.Base64))
		h.Write(m.data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// claimStatus records a Status about to be posted; false if the same one
// was posted (or is being posted) from this number in the last 30 minutes.
func (s *Session) claimStatus(fp string) bool {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	now := time.Now()
	for k, at := range s.statusSeen {
		if now.Sub(at) > 30*time.Minute {
			delete(s.statusSeen, k)
		}
	}
	if _, dup := s.statusSeen[fp]; dup {
		return false
	}
	if s.statusSeen == nil {
		s.statusSeen = map[string]time.Time{}
	}
	s.statusSeen[fp] = now
	return true
}

func (s *Session) releaseStatus(fp string) {
	s.statusMu.Lock()
	delete(s.statusSeen, fp)
	s.statusMu.Unlock()
}

// ======================= Status audience =======================
//
// A Status post is one message encrypted separately for every device of
// every contact. Linked devices can't do what the phone does (send the key
// only to new devices), so for a phone book of thousands the message grows
// past what WhatsApp accepts. The gateway therefore picks who sees each
// post: by default the people who chat with this number most recently, then
// your saved contacts, then the phone's address book, up to STATUS_MAX_RECIPIENTS.

type StatusAudience struct {
	Mode   string   `json:"mode,omitempty"`   // auto (default) | tags | phones
	Tags   []string `json:"tags,omitempty"`   // mode=tags: contacts with any of these tags
	Phones []string `json:"phones,omitempty"` // mode=phones: exactly these numbers
	Limit  int      `json:"limit,omitempty"`  // at most this many people (capped by STATUS_MAX_RECIPIENTS)
	// Split: if WhatsApp refuses one Status that big, post the same Status in
	// 2–3 parts so everyone still gets it (your own phone shows each part).
	Split bool `json:"split,omitempty"`
}

type statusAudKey struct{}

// statusContacts lets the gateway choose the Status audience: while a Status
// is being sent, whatsmeow asks the contact store for "all contacts" and
// gets the chosen list instead. Everything else passes straight through.
type statusContacts struct{ store.ContactStore }

func (c *statusContacts) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	if list, ok := ctx.Value(statusAudKey{}).([]types.JID); ok {
		out := make(map[types.JID]types.ContactInfo, len(list))
		for _, j := range list {
			out[j] = types.ContactInfo{Found: true, FullName: "status"} // whatsmeow keeps named contacts only
		}
		return out, nil
	}
	return c.ContactStore.GetAllContacts(ctx)
}

func wrapContactStore(cli *whatsmeow.Client) {
	if cli == nil || cli.Store == nil || cli.Store.Contacts == nil {
		return
	}
	if _, done := cli.Store.Contacts.(*statusContacts); !done {
		cli.Store.Contacts = &statusContacts{cli.Store.Contacts}
	}
}

// statusRecipients picks who a Status post goes to (phone-number JIDs, no duplicates).
func (s *Session) statusRecipients(ctx context.Context, cli *whatsmeow.Client, owner int64, aud *StatusAudience) ([]types.JID, error) {
	if aud == nil {
		aud = &StatusAudience{}
	}
	want := s.m.statusMax(ctx)
	if aud.Limit > 0 && aud.Limit < want {
		want = aud.Limit
	}
	// Collect some extra candidates: people no longer on WhatsApp drop out below.
	out, err := s.statusCandidates(ctx, cli, owner, aud, want+want/4+20)
	if err != nil {
		return nil, err
	}
	reach, missing := s.reachable(ctx, cli, out)
	if len(reach) > want {
		reach = reach[:want]
	}
	s.m.log.Info("status audience", "session", s.name, "mode", firstNonEmpty(aud.Mode, "auto"), "candidates", len(out),
		"reachable", len(reach), "no_devices", missing)
	if len(reach) == 0 {
		return nil, errors.New("none of these contacts could be found on WhatsApp right now; try again in a few minutes")
	}
	return reach, nil
}

// statusCandidates lists who a Status could go to, best first (phone-number JIDs, no duplicates).
func (s *Session) statusCandidates(ctx context.Context, cli *whatsmeow.Client, owner int64, aud *StatusAudience, limit int) ([]types.JID, error) {
	me := cli.Store.GetJID().User
	seen := map[string]bool{me: true}
	var out []types.JID
	add := func(raw string) bool {
		if len(out) >= limit {
			return false
		}
		j, err := types.ParseJID(raw)
		if err != nil {
			if p := normPhone(raw, s.m.cfg.DefaultCountryCode); p != "" {
				j = types.NewJID(p, types.DefaultUserServer)
			} else {
				return true
			}
		}
		j = j.ToNonAD()
		if j.Server == types.HiddenUserServer {
			pn, err := cli.Store.LIDs.GetPNForLID(ctx, j)
			if err != nil || pn.IsEmpty() {
				return true
			}
			j = pn.ToNonAD()
		}
		if j.Server != types.DefaultUserServer || seen[j.User] {
			return true
		}
		seen[j.User] = true
		out = append(out, j)
		return true
	}
	query := func(q string, args ...any) {
		rows, err := s.m.db.QueryContext(ctx, q, args...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if rows.Scan(&v) == nil && !add(v) {
				return
			}
		}
	}
	switch aud.Mode {
	case "phones":
		for _, p := range aud.Phones {
			add(p)
		}
	case "tags":
		for _, t := range aud.Tags {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				query(`SELECT phone FROM contacts WHERE owner_id=? AND tags LIKE ? ORDER BY last_seen DESC, id`, owner, "%,"+t+",%")
			}
		}
	default:
		// 1. people who wrote to this number, most recent first
		query(`SELECT jid FROM gw_contacts WHERE session=? ORDER BY last_in DESC`, s.name)
		// 2. your saved contacts
		query(`SELECT phone FROM contacts WHERE owner_id=? AND phone NOT IN (SELECT phone FROM optouts WHERE owner_id=?) ORDER BY last_seen DESC, id`, owner, owner)
		// 3. the phone's address book (named contacts), A to Z
		if len(out) < limit {
			if all, err := underlyingContacts(cli).GetAllContacts(ctx); err == nil {
				type nc struct{ jid, name string }
				var named []nc
				for j, c := range all {
					if c.FullName != "" && (j.Server == types.DefaultUserServer || j.Server == types.HiddenUserServer) {
						named = append(named, nc{j.String(), strings.ToLower(c.FullName)})
					}
				}
				sort.Slice(named, func(a, b int) bool { return named[a].name < named[b].name })
				for _, n := range named {
					if !add(n.jid) {
						break
					}
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no one to show this Status to: save some contacts first, or choose numbers")
	}
	return out, nil
}

// reachable looks up each person's phones (devices) the way WhatsApp Web
// does, in small batches. Contacts moved to WhatsApp's newer "LID" identity
// often return no devices when asked by phone number, which is why a Status
// silently skipped them; for those we ask by LID instead. People with no
// devices either way are left out (not on WhatsApp any more).
func (s *Session) reachable(ctx context.Context, cli *whatsmeow.Client, list []types.JID) ([]types.JID, int) {
	const batch = 300
	hasDevices := func(jids []types.JID) map[string]bool {
		found := map[string]bool{}
		for i := 0; i < len(jids); i += batch {
			chunk := jids[i:min(i+batch, len(jids))]
			devs, err := cli.GetUserDevices(ctx, chunk)
			if err != nil {
				s.m.log.Warn("status device lookup", "session", s.name, "err", err)
				for _, j := range chunk { // unknown: keep them, WhatsApp may still deliver
					found[j.User] = true
				}
			}
			for _, d := range devs {
				found[d.User] = true
			}
			if i+batch < len(jids) {
				_ = sleepCtx(ctx, 400*time.Millisecond) // gentle on WhatsApp's lookup limits
			}
		}
		return found
	}
	// People already looked up by the background preparation (last 24 h) need no new lookup.
	known := map[string]prepTarget{}
	var unknown []types.JID
	s.prep.mu.Lock()
	for _, j := range list {
		if t, ok := s.prep.targets[j.User]; ok && time.Since(t.at) < 24*time.Hour {
			known[j.User] = t
		} else {
			unknown = append(unknown, j)
		}
	}
	s.prep.mu.Unlock()
	list0 := list
	list = unknown
	byPN := hasDevices(list)
	var out, viaLID []types.JID
	lidOf := map[string]types.JID{}
	for _, j := range list {
		if byPN[j.User] {
			continue
		}
		if lid, err := cli.Store.LIDs.GetLIDForPN(ctx, j); err == nil && !lid.IsEmpty() {
			lid = lid.ToNonAD()
			lidOf[j.User] = lid
			viaLID = append(viaLID, lid)
		}
	}
	byLID := map[string]bool{}
	if len(viaLID) > 0 {
		byLID = hasDevices(viaLID)
	}
	missing := 0
	now := time.Now()
	s.prep.mu.Lock()
	if s.prep.targets == nil {
		s.prep.targets = map[string]prepTarget{}
	}
	for _, j := range list0 { // keep the original priority order
		if t, ok := known[j.User]; ok {
			if t.ok {
				out = append(out, t.jid)
			} else {
				missing++
			}
			continue
		}
		switch {
		case byPN[j.User]:
			out = append(out, j)
			s.prep.targets[j.User] = prepTarget{jid: j, ok: true, at: now}
		case lidOf[j.User].User != "" && byLID[lidOf[j.User].User]:
			out = append(out, lidOf[j.User])
			s.prep.targets[j.User] = prepTarget{jid: lidOf[j.User], ok: true, at: now}
		default:
			missing++
			s.prep.targets[j.User] = prepTarget{ok: false, at: now}
		}
	}
	s.prep.mu.Unlock()
	return out, missing
}

func underlyingContacts(cli *whatsmeow.Client) store.ContactStore {
	if w, ok := cli.Store.Contacts.(*statusContacts); ok {
		return w.ContactStore
	}
	return cli.Store.Contacts
}

// ======================= WhatsApp contacts (phone address book) =======================

type waContact struct {
	JID      string `json:"jid"`
	Phone    string `json:"phone"`
	Name     string `json:"name"`      // name saved in the phone ("" if not saved)
	PushName string `json:"push_name"` // name the person set for themselves
	Business string `json:"business,omitempty"`
	Saved    bool   `json:"saved"`
}

// waContacts lists everyone the linked phone knows: saved contacts and
// unsaved people it has chatted with, A to Z (saved names first by name).
func (a *API) waContacts(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	all, err := underlyingContacts(cli).GetAllContacts(ctx)
	if err != nil {
		a.fail(w, err)
		return
	}
	me := cli.Store.GetJID().User
	byPhone := map[string]*waContact{}
	for j, c := range all {
		jid := j.ToNonAD()
		phone := ""
		switch jid.Server {
		case types.DefaultUserServer:
			phone = jid.User
		case types.HiddenUserServer:
			if pn, err := cli.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
				phone = pn.User
				jid = pn.ToNonAD()
			}
		default:
			continue
		}
		if phone == "" || phone == me {
			continue // LIDs without a known number can't be messaged by number
		}
		x := byPhone[phone]
		if x == nil {
			x = &waContact{JID: jid.String(), Phone: phone}
			byPhone[phone] = x
		}
		if c.FullName != "" {
			x.Name, x.Saved = c.FullName, true
		}
		if x.PushName == "" {
			x.PushName = c.PushName
		}
		if x.Business == "" {
			x.Business = c.BusinessName
		}
	}
	out := make([]*waContact, 0, len(byPhone))
	for _, x := range byPhone {
		out = append(out, x)
	}
	display := func(x *waContact) string {
		return strings.ToLower(firstNonEmpty(x.Name, x.PushName, x.Business, "~"+x.Phone))
	}
	slices.SortFunc(out, func(p, q *waContact) int { return strings.Compare(display(p), display(q)) })
	saved := 0
	for _, x := range out {
		if x.Saved {
			saved++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "contacts": out, "total": len(out), "saved": saved,
		"status_max": a.m.cfg.StatusMax, "fetched_at": time.Now().Unix()})
}
