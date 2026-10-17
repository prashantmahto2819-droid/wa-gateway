package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// ---------- group management ----------

func (a *API) groupJID(w http.ResponseWriter, r *http.Request) (types.JID, bool) {
	jid, err := types.ParseJID(r.PathValue("jid"))
	if err != nil || jid.Server != types.GroupServer {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("not a group id (…@g.us)"))
		return types.JID{}, false
	}
	return jid, true
}

func (a *API) groupsAllowed(w http.ResponseWriter, r *http.Request) (*Session, *whatsmeow.Client, bool) {
	s, cli, ok := a.client(w, r)
	if !ok {
		return nil, nil, false
	}
	if err := a.lic.require(r.Context(), s.owner(), "groups"); err != nil {
		a.fail(w, err)
		return nil, nil, false
	}
	return s, cli, true
}

func (a *API) groupInfo(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	jid, ok := a.groupJID(w, r)
	if !ok {
		return
	}
	g, err := cli.GetGroupInfo(r.Context(), jid)
	if err != nil {
		a.fail(w, err)
		return
	}
	parts := []map[string]any{}
	for _, p := range g.Participants {
		pn := p.PhoneNumber.ToNonAD()
		if pn.IsEmpty() && p.JID.Server == types.DefaultUserServer {
			pn = p.JID.ToNonAD()
		}
		if pn.IsEmpty() {
			if alt, err := cli.Store.LIDs.GetPNForLID(r.Context(), p.JID.ToNonAD()); err == nil {
				pn = alt
			}
		}
		parts = append(parts, map[string]any{"jid": p.JID.ToNonAD().String(), "phone": phoneOf(pn), "name": p.DisplayName,
			"is_admin": p.IsAdmin, "is_super_admin": p.IsSuperAdmin})
	}
	out := map[string]any{"jid": g.JID.String(), "name": g.Name, "topic": g.Topic, "announce_only": g.IsAnnounce, "locked": g.IsLocked,
		"is_community": g.IsParent, "community": "", "created_at": g.GroupCreated.Unix(), "participants": parts}
	if !g.LinkedParentJID.IsEmpty() {
		out["community"] = g.LinkedParentJID.String()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": out})
}

func parseJIDList(list []string, cc string) ([]types.JID, error) {
	var out []types.JID
	for _, p := range list {
		j, err := parseTarget(p, cc)
		if err != nil || (j.Server != types.DefaultUserServer && j.Server != types.HiddenUserServer) {
			return nil, errors.New("invalid participant: " + p)
		}
		out = append(out, j)
	}
	return out, nil
}

func (a *API) createGroup(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.groupsAllowed(w, r)
	if !ok {
		return
	}
	var b struct {
		Name         string   `json:"name"`
		Participants []string `json:"participants"`
		Community    bool     `json:"community"`
	}
	if err := decode(r, &b); err != nil || strings.TrimSpace(b.Name) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("name is required"))
		return
	}
	if len([]rune(b.Name)) > 25 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("group names can be at most 25 characters"))
		return
	}
	parts, err := parseJIDList(b.Participants, a.cfg.DefaultCountryCode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if len(parts) > 50 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("add at most 50 people at creation; share the invite link for more"))
		return
	}
	req := whatsmeow.ReqCreateGroup{Name: strings.TrimSpace(b.Name), Participants: parts}
	req.IsParent = b.Community
	g, err := cli.CreateGroup(r.Context(), req)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": map[string]any{"jid": g.JID.String(), "name": g.Name}})
}

func (a *API) groupParticipants(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.groupsAllowed(w, r)
	if !ok {
		return
	}
	jid, ok := a.groupJID(w, r)
	if !ok {
		return
	}
	var b struct {
		Action       string   `json:"action"`
		Participants []string `json:"participants"`
	}
	if err := decode(r, &b); err != nil || len(b.Participants) == 0 || len(b.Participants) > 50 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("action and 1-50 participants are required"))
		return
	}
	actions := map[string]whatsmeow.ParticipantChange{"add": whatsmeow.ParticipantChangeAdd, "remove": whatsmeow.ParticipantChangeRemove,
		"promote": whatsmeow.ParticipantChangePromote, "demote": whatsmeow.ParticipantChangeDemote}
	act, okA := actions[b.Action]
	if !okA {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("action must be add, remove, promote or demote"))
		return
	}
	parts, err := parseJIDList(b.Participants, a.cfg.DefaultCountryCode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	res, err := cli.UpdateGroupParticipants(r.Context(), jid, parts, act)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []map[string]any{}
	for _, p := range res {
		item := map[string]any{"jid": p.JID.ToNonAD().String(), "error": p.Error}
		if p.Error == 403 && p.AddRequest != nil {
			item["note"] = "privacy settings: they must join with the invite link"
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": out})
}

func (a *API) groupInvite(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.groupsAllowed(w, r)
	if !ok {
		return
	}
	jid, ok := a.groupJID(w, r)
	if !ok {
		return
	}
	link, err := cli.GetGroupInviteLink(r.Context(), jid, r.URL.Query().Get("reset") == "1")
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "link": link})
}

func (a *API) groupSettings(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.groupsAllowed(w, r)
	if !ok {
		return
	}
	jid, ok := a.groupJID(w, r)
	if !ok {
		return
	}
	var b struct {
		Name         *string `json:"name"`
		Topic        *string `json:"topic"`
		AnnounceOnly *bool   `json:"announce_only"`
		Locked       *bool   `json:"locked"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx := r.Context()
	if b.Name != nil {
		if err := cli.SetGroupName(ctx, jid, strings.TrimSpace(*b.Name)); err != nil {
			a.fail(w, err)
			return
		}
	}
	if b.Topic != nil {
		if err := cli.SetGroupTopic(ctx, jid, "", "", *b.Topic); err != nil {
			a.fail(w, err)
			return
		}
	}
	if b.AnnounceOnly != nil {
		if err := cli.SetGroupAnnounce(ctx, jid, *b.AnnounceOnly); err != nil {
			a.fail(w, err)
			return
		}
	}
	if b.Locked != nil {
		if err := cli.SetGroupLocked(ctx, jid, *b.Locked); err != nil {
			a.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) leaveGroup(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.groupsAllowed(w, r)
	if !ok {
		return
	}
	jid, ok := a.groupJID(w, r)
	if !ok {
		return
	}
	if err := cli.LeaveGroup(r.Context(), jid); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) joinGroup(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.groupsAllowed(w, r)
	if !ok {
		return
	}
	var b struct {
		Link string `json:"link"`
	}
	if err := decode(r, &b); err != nil || b.Link == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("link is required"))
		return
	}
	code := b.Link
	if i := strings.LastIndex(code, "/"); i >= 0 {
		code = code[i+1:]
	}
	jid, err := cli.JoinGroupWithLink(r.Context(), strings.TrimSpace(code))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "jid": jid.String()})
}

// saveGroupContacts stores every member's phone as a contact with a tag.
func (a *API) saveGroupContacts(w http.ResponseWriter, r *http.Request) {
	s, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	jid, ok := a.groupJID(w, r)
	if !ok {
		return
	}
	var b struct {
		Tag string `json:"tag"`
	}
	_ = decode(r, &b)
	ctx := r.Context()
	g, err := cli.GetGroupInfo(ctx, jid)
	if err != nil {
		a.fail(w, err)
		return
	}
	owner := s.owner()
	tag := strings.TrimSpace(b.Tag)
	if tag == "" {
		tag = "group-" + strings.ToLower(unsafeName.ReplaceAllString(g.Name, "-"))
	}
	saved, skipped := 0, 0
	me := cli.Store.GetJID().ToNonAD()
	for _, p := range g.Participants {
		pn := p.PhoneNumber.ToNonAD()
		if pn.IsEmpty() && p.JID.Server == types.DefaultUserServer {
			pn = p.JID.ToNonAD()
		}
		if pn.IsEmpty() {
			if alt, err := cli.Store.LIDs.GetPNForLID(ctx, p.JID.ToNonAD()); err == nil {
				pn = alt
			}
		}
		if pn.IsEmpty() || pn == me {
			skipped++ // hidden number (LID only)
			continue
		}
		var exists int
		_ = a.m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE owner_id=? AND phone=?`, owner, pn.User).Scan(&exists)
		if exists == 0 && a.lic.checkLimit(ctx, owner, "contacts", 1) != nil {
			break
		}
		if _, err := upsertContact(ctx, a.m.db, owner, &Contact{Phone: pn.User, Name: p.DisplayName, Tags: []string{tag}, Source: "group"}, true); err == nil {
			saved++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": saved, "hidden_numbers": skipped, "tag": tag})
}

func (a *API) createChannel(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := a.groupsAllowed(w, r)
	if !ok {
		return
	}
	var b struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decode(r, &b); err != nil || strings.TrimSpace(b.Name) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("name is required"))
		return
	}
	n, err := cli.CreateNewsletter(r.Context(), whatsmeow.CreateNewsletterParams{Name: strings.TrimSpace(b.Name), Description: b.Description})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": map[string]any{"jid": n.ID.String(), "name": n.ThreadMeta.Name.Text}})
}

// ---------- message actions ----------

type msgRef struct {
	Chat   string `json:"chat"`
	ID     string `json:"id"`
	Sender string `json:"sender"`
	Emoji  string `json:"emoji"`
	Text   string `json:"text"`
}

func (a *API) msgAction(w http.ResponseWriter, r *http.Request, kind string) {
	s, cli, ok := a.client(w, r)
	if !ok {
		return
	}
	var b msgRef
	if err := decode(r, &b); err != nil || b.Chat == "" || b.ID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("chat and id are required"))
		return
	}
	chat, err := parseTarget(b.Chat, a.cfg.DefaultCountryCode)
	if err != nil {
		a.fail(w, err)
		return
	}
	sender := types.EmptyJID
	if b.Sender != "" {
		if sender, err = parseTarget(b.Sender, a.cfg.DefaultCountryCode); err != nil {
			a.fail(w, err)
			return
		}
	}
	if err := a.lic.require(r.Context(), s.owner(), ""); err != nil {
		a.fail(w, err)
		return
	}
	var msg = cli.BuildRevoke(chat, sender, b.ID)
	switch kind {
	case "react":
		if sender.IsEmpty() {
			sender = chat
		}
		msg = cli.BuildReaction(chat, sender, b.ID, b.Emoji)
	case "edit":
		if strings.TrimSpace(b.Text) == "" {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("text is required"))
			return
		}
		msg = cli.BuildEdit(chat, b.ID, &waE2E.Message{Conversation: proto.String(b.Text)})
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	resp, err := cli.SendMessage(ctx, chat, msg)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": resp.ID})
}

func (a *API) postStatus(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if s == nil {
		return
	}
	var req SendRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	req.To = "status"
	req.Poll, req.Menu, req.Location, req.Contact = nil, nil, nil, nil
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, err := s.send(ctx, &req, sendOpts{direct: true})
	s.recordDirect(context.Background(), &req, res, err)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": res})
}
