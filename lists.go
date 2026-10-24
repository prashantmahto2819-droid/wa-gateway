package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// ======================= saved send lists =======================
//
// A list is a named set of recipients of any kind — phone numbers, saved
// contacts, groups, community announcement groups and channels — so a
// whole mix can be picked with one click in Send or in a campaign. Lists can
// also be filled with the members of a group or community.

const maxListItems = 50000

type ListItem struct {
	To   string `json:"to"`             // phone, contact:<id>, …@g.us or …@newsletter
	Name string `json:"name,omitempty"` // what to show
}

type SendList struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Items       []ListItem     `json:"items,omitempty"`
	Counts      map[string]int `json:"counts"`
	CreatedAt   int64          `json:"created_at"`
	UpdatedAt   int64          `json:"updated_at"`
}

func itemKind(to string) string {
	switch {
	case strings.HasPrefix(to, "contact:"):
		return "contacts"
	case strings.HasSuffix(to, "@g.us"):
		return "groups"
	case strings.HasSuffix(to, "@newsletter"):
		return "channels"
	}
	return "phones"
}

// cleanItems normalises and de-duplicates items (bad ones are dropped).
func cleanItems(items []ListItem, cc string) []ListItem {
	out := make([]ListItem, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		to := strings.TrimSpace(it.To)
		switch itemKind(to) {
		case "contacts":
			if n, err := strconv.ParseInt(strings.TrimPrefix(to, "contact:"), 10, 64); err != nil || n <= 0 {
				continue
			}
		case "groups", "channels":
			if _, err := types.ParseJID(to); err != nil {
				continue
			}
		default:
			if to = normPhone(to, cc); to == "" {
				continue
			}
		}
		if seen[to] {
			continue
		}
		seen[to] = true
		out = append(out, ListItem{To: to, Name: truncate(strings.TrimSpace(it.Name), 80)})
	}
	return out
}

func listCounts(items []ListItem) map[string]int {
	c := map[string]int{"total": len(items), "phones": 0, "contacts": 0, "groups": 0, "channels": 0}
	for _, it := range items {
		c[itemKind(it.To)]++
	}
	return c
}

func (m *Manager) loadList(ctx context.Context, owner, id int64) (*SendList, error) {
	var l SendList
	var items string
	err := m.db.QueryRowContext(ctx, `SELECT id, name, description, items, created_at, updated_at FROM send_lists WHERE id=? AND owner_id=?`, id, owner).
		Scan(&l.ID, &l.Name, &l.Description, &items, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, err
	}
	l.Items = fromJSON[[]ListItem](items)
	l.Counts = listCounts(l.Items)
	return &l, nil
}

func (a *API) listLists(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT id, name, description, items, created_at, updated_at FROM send_lists WHERE owner_id=? ORDER BY name COLLATE NOCASE`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []SendList{}
	for rows.Next() {
		var l SendList
		var items string
		if rows.Scan(&l.ID, &l.Name, &l.Description, &items, &l.CreatedAt, &l.UpdatedAt) == nil {
			l.Counts = listCounts(fromJSON[[]ListItem](items))
			out = append(out, l)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "lists": out})
}

func (a *API) getList(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	l, err := a.m.loadList(r.Context(), principalOf(r).UserID, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("list not found"))
		return
	} else if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "list": l})
}

// saveList creates a list (POST) or renames / replaces its items (PATCH).
// PATCH also accepts add:[…] and remove:["to", …] to change items in place.
func (a *API) saveList(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var b struct {
		Name        *string    `json:"name"`
		Description *string    `json:"description"`
		Items       []ListItem `json:"items"`
		Add         []ListItem `json:"add"`
		Remove      []string   `json:"remove"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	ctx := r.Context()
	cc := a.cfg.DefaultCountryCode
	now := time.Now().Unix()
	var l *SendList
	if idStr := r.PathValue("id"); idStr != "" {
		id, _ := strconv.ParseInt(idStr, 10, 64)
		var err error
		if l, err = a.m.loadList(ctx, p.UserID, id); err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("list not found"))
			return
		}
	} else {
		l = &SendList{CreatedAt: now}
	}
	if b.Name != nil {
		l.Name = strings.TrimSpace(*b.Name)
	}
	if b.Description != nil {
		l.Description = strings.TrimSpace(*b.Description)
	}
	if l.Name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("give the list a name"))
		return
	}
	if b.Items != nil {
		l.Items = b.Items
	}
	if len(b.Remove) > 0 {
		drop := map[string]bool{}
		for _, x := range b.Remove {
			drop[x] = true
		}
		kept := l.Items[:0]
		for _, it := range l.Items {
			if !drop[it.To] {
				kept = append(kept, it)
			}
		}
		l.Items = kept
	}
	l.Items = cleanItems(append(l.Items, b.Add...), cc)
	if len(l.Items) > maxListItems {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("a list can hold at most 50,000 recipients"))
		return
	}
	if l.ID == 0 {
		if err := a.countCheck(ctx, p.UserID, "lists", "send_lists"); err != nil {
			a.fail(w, err)
			return
		}
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO send_lists(owner_id, name, description, items, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
			p.UserID, l.Name, l.Description, jsonText(l.Items), now, now)
		if err != nil {
			a.fail(w, err)
			return
		}
		l.ID, _ = res.LastInsertId()
	} else if _, err := a.m.db.ExecContext(ctx, `UPDATE send_lists SET name=?, description=?, items=?, updated_at=? WHERE id=? AND owner_id=?`,
		l.Name, l.Description, jsonText(l.Items), now, l.ID, p.UserID); err != nil {
		a.fail(w, err)
		return
	}
	l.UpdatedAt = now
	l.Counts = listCounts(l.Items)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "list": l})
}

func (a *API) deleteList(w http.ResponseWriter, r *http.Request) {
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM send_lists WHERE id=? AND owner_id=?`, r.PathValue("id"), principalOf(r).UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("list not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// groupMembersToList adds the members of groups / communities to a list
// (new or existing): POST /api/sessions/{name}/members-to-list {groups:[jid], list_id?, name?}
func (a *API) groupMembersToList(w http.ResponseWriter, r *http.Request) {
	s, cli, ok := a.groupsAllowed(w, r)
	if !ok {
		return
	}
	var b struct {
		Groups []string `json:"groups"`
		ListID int64    `json:"list_id"`
		Name   string   `json:"name"`
	}
	if err := decode(r, &b); err != nil || len(b.Groups) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("choose at least one group"))
		return
	}
	ctx := r.Context()
	owner := s.owner()
	me := cli.Store.GetJID().ToNonAD()
	var add []ListItem
	hidden := 0
	for _, gs := range b.Groups {
		jid, err := types.ParseJID(gs)
		if err != nil || jid.Server != types.GroupServer {
			continue
		}
		g, err := cli.GetGroupInfo(ctx, jid)
		if err != nil {
			a.fail(w, err)
			return
		}
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
			if pn.IsEmpty() || pn.User == me.User {
				hidden++ // number hidden by WhatsApp (LID only)
				continue
			}
			add = append(add, ListItem{To: pn.User, Name: p.DisplayName})
		}
		if b.Name == "" && b.ListID == 0 {
			b.Name = g.Name + " — members"
		}
	}
	l := &SendList{Name: strings.TrimSpace(b.Name)}
	if b.ListID > 0 {
		var err error
		if l, err = a.m.loadList(ctx, owner, b.ListID); err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("list not found"))
			return
		}
	}
	before := len(l.Items)
	l.Items = cleanItems(append(l.Items, add...), a.cfg.DefaultCountryCode)
	if len(l.Items) > maxListItems {
		l.Items = l.Items[:maxListItems]
	}
	now := time.Now().Unix()
	if l.ID == 0 {
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO send_lists(owner_id, name, description, items, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
			owner, l.Name, "", jsonText(l.Items), now, now)
		if err != nil {
			a.fail(w, err)
			return
		}
		l.ID, _ = res.LastInsertId()
	} else if _, err := a.m.db.ExecContext(ctx, `UPDATE send_lists SET items=?, updated_at=? WHERE id=?`, jsonText(l.Items), now, l.ID); err != nil {
		a.fail(w, err)
		return
	}
	l.Counts = listCounts(l.Items)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "list": SendList{ID: l.ID, Name: l.Name, Counts: l.Counts},
		"added": len(l.Items) - before, "hidden": hidden})
}
