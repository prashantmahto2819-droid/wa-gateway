package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// Principal is who is making a request.
type Principal struct {
	UserID        int64  // the workspace (owner) everything is keyed on
	ActorID       int64  // the person actually logged in (same as UserID for owners)
	WorkspaceRole string // owner | admin | manager | supervisor | agent | analyst
	Role          string // account role: superadmin | user
	Email         string
	Name          string
	ViaKey        bool // user API key
	Master        bool // .env API_KEY
}

// asWorkspace fills the workspace fields for a logged-in user.
func (p *Principal) asWorkspace(ctx context.Context, db *sql.DB) *Principal {
	p.ActorID = p.UserID
	p.WorkspaceRole = "owner"
	if p.Role != "superadmin" {
		p.UserID, p.WorkspaceRole = workspaceOf(ctx, db, p.UserID)
	}
	return p
}

func (p *Principal) Admin() bool { return p != nil && p.Role == "superadmin" }

type ctxKey int

const principalKey ctxKey = 1

func principalOf(r *http.Request) *Principal {
	p, _ := r.Context().Value(principalKey).(*Principal)
	return p
}

const sessionCookie = "vr_session"

var (
	errBadCreds      = errors.New("wrong email or password")
	errTooMany       = errors.New("too many attempts, try again in a few minutes")
	errEmailTaken    = errors.New("an account with this email already exists")
	errWeakPassword  = errors.New("password must be at least 8 characters")
	errBadEmail      = errors.New("please enter a valid email")
	errSignupClosed  = errors.New("sign-ups are closed; please contact us")
	errForbidden     = errors.New("you don't have access to this")
	errAlreadySetup  = errors.New("setup is already done")
	errUserNotActive = errors.New("this account is disabled")
)

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(b), "="))
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func clientIP(r *http.Request) string {
	// Caddy in front sets X-Forwarded-For; the gateway port itself is not public.
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// ---------- users ----------

type User struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	LastLogin int64  `json:"last_login"`
}

const userCols = `id, email, name, phone, role, status, created_at, last_login`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Phone, &u.Role, &u.Status, &u.CreatedAt, &u.LastLogin); err != nil {
		return nil, err
	}
	return &u, nil
}

func getUser(ctx context.Context, db *sql.DB, id int64) (*User, error) {
	return scanUser(db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func countUsers(ctx context.Context, db *sql.DB) int {
	var n int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n
}

// firstAdminID is used for the master API key and for sessions created before accounts existed.
func firstAdminID(ctx context.Context, db *sql.DB) int64 {
	var id int64
	_ = db.QueryRowContext(ctx, `SELECT id FROM users WHERE role='superadmin' AND status='active' ORDER BY id LIMIT 1`).Scan(&id)
	return id
}

func validateNewUser(email, password string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || !strings.Contains(email, ".") {
		return "", errBadEmail
	}
	if len(password) < 8 || len(password) > 200 {
		return "", errWeakPassword
	}
	return email, nil
}

func createUser(ctx context.Context, db *sql.DB, name, email, phone, password, role string) (*User, error) {
	email, err := validateNewUser(email, password)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 11)
	if err != nil {
		return nil, err
	}
	res, err := db.ExecContext(ctx, `INSERT INTO users(email, name, phone, pass_hash, role, created_at) VALUES(?,?,?,?,?,?)`,
		email, strings.TrimSpace(name), strings.TrimSpace(phone), string(hash), role, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errEmailTaken
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getUser(ctx, db, id)
}

func setPassword(ctx context.Context, db *sql.DB, userID int64, password string) error {
	if len(password) < 8 || len(password) > 200 {
		return errWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 11)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE users SET pass_hash=? WHERE id=?`, string(hash), userID)
	if err == nil { // log out other browsers
		_, _ = db.ExecContext(ctx, `DELETE FROM auth_tokens WHERE user_id=?`, userID)
	}
	return err
}

func checkPassword(ctx context.Context, db *sql.DB, email, password string) (*User, error) {
	var id int64
	var hash, status string
	err := db.QueryRowContext(ctx, `SELECT id, pass_hash, status FROM users WHERE email=?`, strings.ToLower(strings.TrimSpace(email))).Scan(&id, &hash, &status)
	if err != nil {
		// Spend similar time so timing doesn't reveal which emails exist.
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return nil, errBadCreds
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, errBadCreds
	}
	if status != "active" {
		return nil, errUserNotActive
	}
	return getUser(ctx, db, id)
}

var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), 11)
	return h
})

// ---------- browser sessions ----------

func (a *API) startLogin(w http.ResponseWriter, r *http.Request, u *User) {
	tok := randToken(32)
	now := time.Now()
	_, _ = a.m.db.ExecContext(r.Context(), `INSERT INTO auth_tokens(token_hash, user_id, created_at, expires_at, ip, ua) VALUES(?,?,?,?,?,?)`,
		sha(tok), u.ID, now.Unix(), now.Add(30*24*time.Hour).Unix(), clientIP(r), truncate(r.UserAgent(), 200))
	_, _ = a.m.db.ExecContext(r.Context(), `UPDATE users SET last_login=? WHERE id=?`, now.Unix(), u.ID)
	_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM auth_tokens WHERE expires_at<?`, now.Unix())
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: isHTTPS(r), Expires: now.Add(30 * 24 * time.Hour),
	})
	audit(r.Context(), a.m.db, u.ID, clientIP(r), "login", u.Email)
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// truncate cuts s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// resolvePrincipal works out who is calling (nil = nobody).
func (a *API) resolvePrincipal(r *http.Request) (*Principal, error) {
	ctx := r.Context()
	key := r.Header.Get("X-API-Key")
	if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		key = strings.TrimSpace(b)
	}
	if key != "" {
		if subtle.ConstantTimeCompare([]byte(key), []byte(a.cfg.APIKey)) == 1 {
			p := &Principal{UserID: firstAdminID(ctx, a.m.db), Role: "superadmin", Master: true, Name: "master key"}
			return p.asWorkspace(ctx, a.m.db), nil
		}
		if strings.HasPrefix(key, "vr_") {
			var uid, kid int64
			if a.m.db.QueryRowContext(ctx, `SELECT id, user_id FROM api_keys WHERE key_hash=?`, sha(key)).Scan(&kid, &uid) == nil {
				u, err := getUser(ctx, a.m.db, uid)
				if err == nil && u.Status == "active" {
					_, _ = a.m.db.ExecContext(ctx, `UPDATE api_keys SET last_used=? WHERE id=?`, time.Now().Unix(), kid)
					return (&Principal{UserID: u.ID, Role: u.Role, Email: u.Email, Name: u.Name, ViaKey: true}).asWorkspace(ctx, a.m.db), nil
				}
			}
		}
		return nil, nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	var uid int64
	if a.m.db.QueryRowContext(ctx, `SELECT user_id FROM auth_tokens WHERE token_hash=? AND expires_at>?`, sha(c.Value), time.Now().Unix()).Scan(&uid) != nil {
		return nil, nil
	}
	u, err := getUser(ctx, a.m.db, uid)
	if err != nil || u.Status != "active" {
		return nil, nil
	}
	// CSRF guard for cookie-authenticated changes.
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Requested-With") != "vivid" {
		return nil, errors.New("missing X-Requested-With header")
	}
	return (&Principal{UserID: u.ID, Role: u.Role, Email: u.Email, Name: u.Name}).asWorkspace(ctx, a.m.db), nil
}

// ---------- login rate limiting ----------

type attemptLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

var loginLimiter = &attemptLimiter{hits: map[string][]time.Time{}}

func (l *attemptLimiter) allow(key string, max int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= max {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	if len(l.hits) > 10000 { // keep memory bounded
		l.hits = map[string][]time.Time{}
	}
	return true
}

// ---------- auth endpoints ----------

func (a *API) authState(w http.ResponseWriter, r *http.Request) {
	p, _ := a.resolvePrincipal(r)
	out := map[string]any{"ok": true, "needs_setup": countUsers(r.Context(), a.m.db) == 0,
		"signup_open": a.signupOpen(r.Context()), "mode": a.mode(), "logged_in": p != nil}
	if p != nil && p.UserID > 0 {
		if u, err := getUser(r.Context(), a.m.db, p.UserID); err == nil {
			out["user"] = u
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) signupOpen(ctx context.Context) bool {
	if a.lic.clientMode() {
		return false
	}
	return getSetting(ctx, a.m.db, "signup_open") == "true"
}

func (a *API) mode() string {
	if a.lic.clientMode() {
		return "client"
	}
	return "saas"
}

func (a *API) authSetup(w http.ResponseWriter, r *http.Request) {
	if !requireHeader(w, r) {
		return
	}
	var b struct {
		APIKey   string `json:"api_key"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if !loginLimiter.allow("setup:"+clientIP(r), 10, 15*time.Minute) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", errTooMany)
		return
	}
	if countUsers(r.Context(), a.m.db) > 0 {
		writeErr(w, http.StatusConflict, "already_setup", errAlreadySetup)
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(b.APIKey)), []byte(a.cfg.APIKey)) != 1 {
		writeErr(w, http.StatusUnauthorized, "bad_api_key", errors.New("API key does not match the one in .env"))
		return
	}
	if _, perr := validateNewUser(b.Email, b.Password); perr != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", perr)
		return
	}
	// Only one request may win the setup, even if two arrive together.
	res, err := a.m.db.ExecContext(r.Context(), `INSERT OR IGNORE INTO settings(key, value) VALUES('setup_done', '1')`)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusConflict, "already_setup", errAlreadySetup)
		return
	}
	u, err := createUser(r.Context(), a.m.db, b.Name, b.Email, "", b.Password, "superadmin")
	if err != nil {
		_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM settings WHERE key='setup_done'`)
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	// Sessions linked before accounts existed now belong to the owner.
	_, _ = a.m.db.ExecContext(r.Context(), `UPDATE gw_sessions SET owner_id=? WHERE owner_id=0`, u.ID)
	a.m.reloadOwners()
	a.startLogin(w, r, u)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
}

func (a *API) authSignup(w http.ResponseWriter, r *http.Request) {
	if !requireHeader(w, r) {
		return
	}
	var b struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	if !a.signupOpen(r.Context()) {
		writeErr(w, http.StatusForbidden, "signup_closed", errSignupClosed)
		return
	}
	if !loginLimiter.allow("signup:"+clientIP(r), 5, time.Hour) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", errTooMany)
		return
	}
	if strings.TrimSpace(b.Name) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("please enter your name"))
		return
	}
	u, err := createUser(r.Context(), a.m.db, b.Name, b.Email, b.Phone, b.Password, "user")
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errEmailTaken) {
			code = http.StatusConflict
		}
		writeErr(w, code, "bad_request", err)
		return
	}
	a.lic.startTrial(r.Context(), u.ID)
	audit(r.Context(), a.m.db, u.ID, clientIP(r), "signup", u.Email)
	a.startLogin(w, r, u)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
}

func (a *API) authLogin(w http.ResponseWriter, r *http.Request) {
	if !requireHeader(w, r) {
		return
	}
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(b.Email))
	if !loginLimiter.allow("login:"+clientIP(r), 20, 15*time.Minute) || !loginLimiter.allow("login:"+email, 8, 15*time.Minute) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", errTooMany)
		return
	}
	u, err := checkPassword(r.Context(), a.m.db, email, b.Password)
	if err != nil {
		code := "bad_credentials"
		if errors.Is(err, errUserNotActive) {
			code = "account_disabled"
		}
		writeErr(w, http.StatusUnauthorized, code, err)
		return
	}
	a.startLogin(w, r, u)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
}

func requireHeader(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Requested-With") != "vivid" {
		writeErr(w, http.StatusForbidden, "csrf", errors.New("missing X-Requested-With header"))
		return false
	}
	return true
}

func (a *API) authLogout(w http.ResponseWriter, r *http.Request) {
	if !requireHeader(w, r) {
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM auth_tokens WHERE token_hash=?`, sha(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
