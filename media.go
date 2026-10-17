package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The media library keeps the user's own files (offers, brochures, videos)
// on the server disk so campaigns can reuse them without re-uploading.
// Incoming WhatsApp media is never stored.

type MediaItem struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"created_at"`
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func mediaKind(mimetype string) string {
	switch {
	case strings.HasPrefix(mimetype, "image/"):
		return "image"
	case strings.HasPrefix(mimetype, "video/"):
		return "video"
	case strings.HasPrefix(mimetype, "audio/"):
		return "audio"
	}
	return "document"
}

func (m *Manager) mediaDir(owner int64) string {
	return filepath.Join(m.cfg.DataDir, "media", strconv.FormatInt(owner, 10))
}

// readMedia loads a library file that belongs to owner (admins' sessions may use any file).
func (m *Manager) readMedia(ctx context.Context, owner, id int64) ([]byte, string, string, error) {
	var path, mimetype, name string
	var fileOwner int64
	if m.db.QueryRowContext(ctx, `SELECT owner_id, path, mime, name FROM media WHERE id=?`, id).Scan(&fileOwner, &path, &mimetype, &name) != nil {
		return nil, "", "", errors.New("media not found in library")
	}
	if fileOwner != owner && m.lic.roleOf(ctx, owner) != "superadmin" {
		return nil, "", "", errors.New("media not found in library")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", "", errors.New("media file is missing on disk")
	}
	return data, mimetype, name, nil
}

func (a *API) listMedia(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := `SELECT id, name, mime, kind, size, created_at FROM media WHERE owner_id=?`
	args := []any{p.UserID}
	if k := r.URL.Query().Get("kind"); k != "" {
		q += ` AND kind=?`
		args = append(args, k)
	}
	rows, err := a.m.db.QueryContext(r.Context(), q+` ORDER BY id DESC LIMIT 1000`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []MediaItem{}
	for rows.Next() {
		var it MediaItem
		if rows.Scan(&it.ID, &it.Name, &it.Mime, &it.Kind, &it.Size, &it.CreatedAt) == nil {
			it.URL = fmt.Sprintf("/api/media/%d/file", it.ID)
			out = append(out, it)
		}
	}
	eff := a.lic.effective(r.Context(), p.UserID)
	limit := int64(0)
	if eff.Plan != nil && !eff.Plan.Unlimited {
		limit = eff.Plan.StorageMB
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "media": out, "used_mb": a.lic.usage(r.Context(), p.UserID).StorageMB, "limit_mb": limit})
}

func (a *API) uploadMedia(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	max := a.cfg.MaxMediaBytes
	r.Body = http.MaxBytesReader(w, r.Body, max+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", fmt.Errorf("upload failed or file larger than %d MB", max>>20))
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("form field 'file' is required"))
		return
	}
	defer f.Close()
	if hdr.Size > max {
		writeErr(w, http.StatusBadRequest, "bad_request", fmt.Errorf("file is larger than %d MB", max>>20))
		return
	}
	if err := a.lic.checkLimit(r.Context(), p.UserID, "storage", float64(hdr.Size)/1048576); err != nil {
		a.fail(w, err)
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	mimetype := http.DetectContentType(head[:n])
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(hdr.Filename))); byExt != "" &&
		(mimetype == "application/octet-stream" || strings.HasPrefix(mimetype, "text/plain") || strings.HasPrefix(mimetype, "application/zip")) {
		mimetype = byExt // e.g. .docx/.xlsx detect as zip
	}
	if i := strings.Index(mimetype, ";"); i > 0 && !strings.HasPrefix(mimetype, "audio/ogg") {
		mimetype = mimetype[:i]
	}
	if strings.Contains(mimetype, "html") || strings.Contains(mimetype, "javascript") {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("this file type is not allowed"))
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		a.fail(w, err)
		return
	}
	dir := a.m.mediaDir(p.UserID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		a.fail(w, err)
		return
	}
	name := unsafeName.ReplaceAllString(filepath.Base(hdr.Filename), "_")
	if name == "" || name == "." || name == "_" {
		name = "file"
	}
	if len(name) > 120 {
		name = name[len(name)-120:]
	}
	path := filepath.Join(dir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		a.fail(w, err)
		return
	}
	size, err := io.Copy(out, io.LimitReader(f, max+1))
	out.Close()
	if err != nil || size > max {
		os.Remove(path)
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("upload failed"))
		return
	}
	res, err := a.m.db.ExecContext(r.Context(), `INSERT INTO media(owner_id, name, mime, kind, size, path, created_at) VALUES(?,?,?,?,?,?,?)`,
		p.UserID, name, mimetype, mediaKind(mimetype), size, path, time.Now().Unix())
	if err != nil {
		os.Remove(path)
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "media": MediaItem{ID: id, Name: name, Mime: mimetype, Kind: mediaKind(mimetype),
		Size: size, URL: fmt.Sprintf("/api/media/%d/file", id), CreatedAt: time.Now().Unix()}})
}

func (a *API) mediaFile(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var path, mimetype, name string
	var owner int64
	if a.m.db.QueryRowContext(r.Context(), `SELECT owner_id, path, mime, name FROM media WHERE id=?`, r.PathValue("id")).Scan(&owner, &path, &mimetype, &name) != nil ||
		(owner != p.UserID && !p.Admin()) {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("media not found"))
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("file missing"))
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", mimetype)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	disp := "inline"
	if mediaKind(mimetype) == "document" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (a *API) deleteMedia(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var path string
	var owner int64
	if a.m.db.QueryRowContext(r.Context(), `SELECT owner_id, path FROM media WHERE id=?`, r.PathValue("id")).Scan(&owner, &path) != nil ||
		(owner != p.UserID && !p.Admin()) {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("media not found"))
		return
	}
	_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM media WHERE id=?`, r.PathValue("id"))
	_ = os.Remove(path)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
