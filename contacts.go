package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Contact struct {
	ID        int64             `json:"id"`
	Phone     string            `json:"phone"`
	Name      string            `json:"name"`
	Email     string            `json:"email"`
	Tags      []string          `json:"tags"`
	Notes     string            `json:"notes"`
	Custom    map[string]string `json:"custom"`
	OptedOut  bool              `json:"opted_out"`
	Source    string            `json:"source"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
	LastSeen  int64             `json:"last_seen"`
}

const contactCols = `c.id, c.phone, c.name, c.email, c.tags, c.notes, c.custom, c.source, c.created_at, c.updated_at, c.last_seen,
	EXISTS(SELECT 1 FROM optouts o WHERE o.owner_id=c.owner_id AND o.phone=c.phone)`

func scanContact(row interface{ Scan(...any) error }) (*Contact, error) {
	var c Contact
	var tags, custom string
	if err := row.Scan(&c.ID, &c.Phone, &c.Name, &c.Email, &tags, &c.Notes, &custom, &c.Source, &c.CreatedAt, &c.UpdatedAt, &c.LastSeen, &c.OptedOut); err != nil {
		return nil, err
	}
	c.Tags = unpackTags(tags)
	c.Custom = map[string]string{}
	_ = json.Unmarshal([]byte(custom), &c.Custom)
	return &c, nil
}

// normPhone returns digits with country code, or "" if invalid.
func normPhone(raw, cc string) string {
	j, err := parseTarget(raw, cc)
	if err != nil || j.Server != "s.whatsapp.net" {
		return ""
	}
	return j.User
}

// upsertContact creates or updates a contact by phone. Empty fields don't overwrite.
func upsertContact(ctx context.Context, db *sql.DB, owner int64, c *Contact, mergeTags bool) (created bool, err error) {
	now := time.Now().Unix()
	if c.Custom == nil {
		c.Custom = map[string]string{}
	}
	if c.Source == "" {
		c.Source = "manual"
	}
	var id int64
	var oldTags, oldCustom string
	err = db.QueryRowContext(ctx, `SELECT id, tags, custom FROM contacts WHERE owner_id=? AND phone=?`, owner, c.Phone).Scan(&id, &oldTags, &oldCustom)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.ExecContext(ctx, `INSERT INTO contacts(owner_id, phone, name, email, tags, notes, custom, source, created_at, updated_at, last_seen)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, owner, c.Phone, truncate(c.Name, 120), truncate(c.Email, 200), packTags(c.Tags),
			truncate(c.Notes, 2000), jsonText(c.Custom), c.Source, now, now, c.LastSeen)
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	tags := c.Tags
	if mergeTags {
		tags = append(unpackTags(oldTags), c.Tags...)
	}
	custom := fromJSON[map[string]string](oldCustom)
	if custom == nil {
		custom = map[string]string{}
	}
	for k, v := range c.Custom {
		custom[k] = v
	}
	_, err = db.ExecContext(ctx, `UPDATE contacts SET name=COALESCE(NULLIF(?,''),name), email=COALESCE(NULLIF(?,''),email), tags=?,
		notes=COALESCE(NULLIF(?,''),notes), custom=?, updated_at=?, last_seen=MAX(last_seen, ?) WHERE id=?`,
		truncate(c.Name, 120), truncate(c.Email, 200), packTags(tags), truncate(c.Notes, 2000), jsonText(custom), now, c.LastSeen, id)
	return false, err
}

// contactVars gives the {{placeholders}} for a contact.
func contactVars(c *Contact) map[string]string {
	v := map[string]string{"phone": c.Phone, "name": c.Name, "email": c.Email}
	if v["name"] == "" {
		v["name"] = "there" // "Hi {{name}}" still reads well
	}
	for k, val := range c.Custom {
		v[strings.ToLower(k)] = val
	}
	return v
}

func (a *API) listContacts(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	page = max(page, 1)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	where := []string{"c.owner_id=?"}
	args := []any{p.UserID}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		like := "%" + strings.ReplaceAll(strings.ReplaceAll(s, "%", ""), "_", "") + "%"
		where = append(where, "(c.phone LIKE ? OR c.name LIKE ? OR c.email LIKE ? OR c.notes LIKE ?)")
		args = append(args, like, like, like, like)
	}
	if t := strings.ToLower(strings.TrimSpace(q.Get("tag"))); t != "" {
		where = append(where, "c.tags LIKE ?")
		args = append(args, "%,"+t+",%")
	}
	switch q.Get("opted_out") {
	case "1", "true":
		where = append(where, "EXISTS(SELECT 1 FROM optouts o WHERE o.owner_id=c.owner_id AND o.phone=c.phone)")
	case "0", "false":
		where = append(where, "NOT EXISTS(SELECT 1 FROM optouts o WHERE o.owner_id=c.owner_id AND o.phone=c.phone)")
	}
	w2 := strings.Join(where, " AND ")
	var total int
	_ = a.m.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM contacts c WHERE `+w2, args...).Scan(&total)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT `+contactCols+` FROM contacts c WHERE `+w2+` ORDER BY c.updated_at DESC, c.id DESC LIMIT ? OFFSET ?`,
		append(args, limit, (page-1)*limit)...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []*Contact{}
	for rows.Next() {
		if c, err := scanContact(rows); err == nil {
			out = append(out, c)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "contacts": out, "total": total, "page": page, "limit": limit})
}

func (a *API) getContactByID(ctx context.Context, owner int64, id string) (*Contact, error) {
	return scanContact(a.m.db.QueryRowContext(ctx, `SELECT `+contactCols+` FROM contacts c WHERE c.id=? AND c.owner_id=?`, id, owner))
}

func (a *API) saveContact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var c Contact
	if err := decode(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	isNew := true
	if id := r.PathValue("id"); id != "" {
		old, err := a.getContactByID(r.Context(), p.UserID, id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("contact not found"))
			return
		}
		isNew = false
		if c.Phone == "" {
			c.Phone = old.Phone
		}
		if c.Tags == nil {
			c.Tags = old.Tags
		}
		// A PATCH replaces name/email/notes/custom exactly as sent.
		_, err = a.m.db.ExecContext(r.Context(), `UPDATE contacts SET name=?, email=?, tags=?, notes=?, custom=?, updated_at=? WHERE id=?`,
			truncate(c.Name, 120), truncate(c.Email, 200), packTags(c.Tags), truncate(c.Notes, 2000), jsonText(orEmpty(c.Custom)), time.Now().Unix(), old.ID)
		if err != nil {
			a.fail(w, err)
			return
		}
		c2, _ := a.getContactByID(r.Context(), p.UserID, id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "contact": c2})
		return
	}
	c.Phone = normPhone(c.Phone, a.cfg.DefaultCountryCode)
	if c.Phone == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("a valid phone number is required"))
		return
	}
	var exists int
	_ = a.m.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM contacts WHERE owner_id=? AND phone=?`, p.UserID, c.Phone).Scan(&exists)
	if exists == 0 && isNew {
		if err := a.lic.checkLimit(r.Context(), p.UserID, "contacts", 1); err != nil {
			a.fail(w, err)
			return
		}
	}
	if _, err := upsertContact(r.Context(), a.m.db, p.UserID, &c, false); err != nil {
		a.fail(w, err)
		return
	}
	saved, _ := scanContact(a.m.db.QueryRowContext(r.Context(), `SELECT `+contactCols+` FROM contacts c WHERE c.owner_id=? AND c.phone=?`, p.UserID, c.Phone))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "contact": saved})
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (a *API) deleteContact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM contacts WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("contact not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) bulkContacts(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var b struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
		Tag    string  `json:"tag"`
	}
	if err := decode(r, &b); err != nil || len(b.IDs) == 0 || len(b.IDs) > 10000 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("ids (1-10000) and action are required"))
		return
	}
	tag := strings.ToLower(strings.TrimSpace(b.Tag))
	ctx := r.Context()
	tx, err := a.m.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	done := 0
	now := time.Now().Unix()
	for _, id := range b.IDs {
		var phone, tags string
		if tx.QueryRowContext(ctx, `SELECT phone, tags FROM contacts WHERE id=? AND owner_id=?`, id, p.UserID).Scan(&phone, &tags) != nil {
			continue
		}
		var err error
		switch b.Action {
		case "add_tag", "remove_tag":
			if tag == "" {
				writeErr(w, http.StatusBadRequest, "bad_request", errors.New("tag is required"))
				return
			}
			list := unpackTags(tags)
			if b.Action == "add_tag" {
				list = append(list, tag)
			} else {
				keep := list[:0]
				for _, t := range list {
					if t != tag {
						keep = append(keep, t)
					}
				}
				list = keep
			}
			_, err = tx.ExecContext(ctx, `UPDATE contacts SET tags=?, updated_at=? WHERE id=?`, packTags(list), now, id)
		case "delete":
			_, err = tx.ExecContext(ctx, `DELETE FROM contacts WHERE id=?`, id)
		case "opt_out":
			_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO optouts(owner_id, phone, keyword, session, at) VALUES(?, ?, 'manual', '', ?)`, p.UserID, phone, now)
		case "opt_in":
			_, err = tx.ExecContext(ctx, `DELETE FROM optouts WHERE owner_id=? AND phone=?`, p.UserID, phone)
		default:
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("unknown action"))
			return
		}
		if err == nil {
			done++
		}
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": done})
}

func (a *API) contactTags(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT tags FROM contacts WHERE owner_id=? AND tags<>''`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil {
			for _, tag := range unpackTags(t) {
				counts[tag]++
			}
		}
	}
	type tc struct {
		Tag   string `json:"tag"`
		Count int    `json:"count"`
	}
	out := []tc{}
	for t, n := range counts {
		out = append(out, tc{t, n})
	}
	sortSlice(out, func(x, y tc) bool { return x.Count > y.Count || (x.Count == y.Count && x.Tag < y.Tag) })
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tags": out})
}

// importContacts accepts a CSV upload or JSON rows.
func (a *API) importContacts(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var rows []map[string]string
	extraTag := ""
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("CSV upload failed (max 20 MB)"))
			return
		}
		defer r.MultipartForm.RemoveAll()
		extraTag = r.FormValue("tag")
		f, _, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("form field 'file' is required"))
			return
		}
		defer f.Close()
		rows, err = readCSV(f)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err)
			return
		}
	} else {
		var b struct {
			Rows []map[string]any `json:"rows"`
			Tag  string           `json:"tag"`
		}
		if err := decode(r, &b); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err)
			return
		}
		extraTag = b.Tag
		for _, row := range b.Rows {
			m := map[string]string{}
			for k, v := range row {
				switch x := v.(type) {
				case string:
					m[strings.ToLower(strings.TrimSpace(k))] = x
				case []any:
					var parts []string
					for _, e := range x {
						if s, ok := e.(string); ok {
							parts = append(parts, s)
						}
					}
					m[strings.ToLower(strings.TrimSpace(k))] = strings.Join(parts, ",")
				case float64:
					m[strings.ToLower(strings.TrimSpace(k))] = strconv.FormatFloat(x, 'f', -1, 64)
				}
			}
			rows = append(rows, m)
		}
	}
	if len(rows) > 100000 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("at most 100000 rows per import"))
		return
	}
	created, updated, invalid := 0, 0, 0
	ctx := r.Context()
	for _, row := range rows {
		phone := normPhone(firstOf(row, "phone", "mobile", "number", "whatsapp", "contact"), a.cfg.DefaultCountryCode)
		if phone == "" {
			invalid++
			continue
		}
		c := &Contact{Phone: phone, Name: firstOf(row, "name", "full name", "fullname"), Email: row["email"], Notes: row["notes"], Source: "import", Custom: map[string]string{}}
		c.Tags = strings.FieldsFunc(row["tags"], func(r rune) bool { return r == ',' || r == ';' || r == '|' })
		if extraTag != "" {
			c.Tags = append(c.Tags, extraTag)
		}
		for k, v := range row {
			switch k {
			case "phone", "mobile", "number", "whatsapp", "contact", "name", "full name", "fullname", "email", "notes", "tags":
			default:
				if k != "" && v != "" && len(c.Custom) < 30 {
					c.Custom[truncate(k, 40)] = truncate(v, 500)
				}
			}
		}
		var exists int
		_ = a.m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE owner_id=? AND phone=?`, p.UserID, phone).Scan(&exists)
		if exists == 0 {
			if err := a.lic.checkLimit(ctx, p.UserID, "contacts", 1); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": created, "updated": updated, "invalid": invalid,
					"stopped": "contact limit of your plan reached"})
				return
			}
		}
		isNew, err := upsertContact(ctx, a.m.db, p.UserID, c, true)
		switch {
		case err != nil:
			invalid++
		case isNew:
			created++
		default:
			updated++
		}
	}
	audit(ctx, a.m.db, p.ActorID, clientIP(r), "contacts.import", strconv.Itoa(created+updated)+" rows")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": created, "updated": updated, "invalid": invalid})
}

func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(m[k]); v != "" {
			return v
		}
	}
	return ""
}

// readCSV reads a CSV with a header row (comma, semicolon or tab separated; Excel BOM ok).
func readCSV(f io.Reader) ([]map[string]string, error) {
	data, err := io.ReadAll(io.LimitReader(f, 20<<20))
	if err != nil {
		return nil, err
	}
	s := strings.TrimPrefix(string(data), "\ufeff")
	if !utf8.ValidString(s) {
		return nil, errors.New("CSV must be UTF-8 (in Excel: Save As → CSV UTF-8)")
	}
	first := strings.SplitN(s, "\n", 2)[0]
	cr := csv.NewReader(strings.NewReader(s))
	switch {
	case strings.Count(first, ";") > strings.Count(first, ","):
		cr.Comma = ';'
	case strings.Count(first, "\t") > strings.Count(first, ","):
		cr.Comma = '\t'
	}
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, errors.New("could not read CSV: " + err.Error())
	}
	if len(recs) < 2 {
		return nil, errors.New("CSV needs a header row and at least one contact")
	}
	head := make([]string, len(recs[0]))
	for i, h := range recs[0] {
		head[i] = strings.ToLower(strings.TrimSpace(h))
	}
	var out []map[string]string
	for _, rec := range recs[1:] {
		m := map[string]string{}
		for i, v := range rec {
			if i < len(head) {
				m[head[i]] = strings.TrimSpace(v)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func (a *API) exportContacts(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := `SELECT ` + contactCols + ` FROM contacts c WHERE c.owner_id=?`
	args := []any{p.UserID}
	if t := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tag"))); t != "" {
		q += ` AND c.tags LIKE ?`
		args = append(args, "%,"+t+",%")
	}
	rows, err := a.m.db.QueryContext(r.Context(), q+` ORDER BY c.id`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	var list []*Contact
	keys := map[string]bool{}
	for rows.Next() {
		if c, err := scanContact(rows); err == nil {
			list = append(list, c)
			for k := range c.Custom {
				keys[k] = true
			}
		}
	}
	var custom []string
	for k := range keys {
		custom = append(custom, k)
	}
	sortSlice(custom, func(x, y string) bool { return x < y })
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="contacts.csv"`)
	_, _ = w.Write([]byte("\ufeff")) // Excel-friendly
	cw := csv.NewWriter(w)
	_ = cw.Write(append([]string{"phone", "name", "email", "tags", "notes", "opted_out", "source"}, custom...))
	for _, c := range list {
		row := []string{c.Phone, csvSafe(c.Name), csvSafe(c.Email), strings.Join(c.Tags, ","), csvSafe(c.Notes), strconv.FormatBool(c.OptedOut), c.Source}
		for _, k := range custom {
			row = append(row, csvSafe(c.Custom[k]))
		}
		_ = cw.Write(row)
	}
	cw.Flush()
}

// csvSafe stops spreadsheet formula injection.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// autoSaveContact records people who message a number (if enabled for the number).
func (s *Session) autoSaveContact(ctx context.Context, phone, pushName string) {
	if phone == "" {
		return
	}
	owner := s.owner()
	var exists int
	_ = s.m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE owner_id=? AND phone=?`, owner, phone).Scan(&exists)
	if exists == 0 && s.m.lic.checkLimit(ctx, owner, "contacts", 1) != nil {
		return
	}
	if exists > 0 {
		_, _ = s.m.db.ExecContext(ctx, `UPDATE contacts SET last_seen=?, name=CASE WHEN name='' THEN ? ELSE name END WHERE owner_id=? AND phone=?`,
			time.Now().Unix(), truncate(pushName, 120), owner, phone)
		return
	}
	_, _ = upsertContact(ctx, s.m.db, owner, &Contact{Phone: phone, Name: pushName, Source: "whatsapp", LastSeen: time.Now().Unix()}, true)
}
