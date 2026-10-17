package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	errLicenceRequired = errors.New("no active licence: redeem a licence key or contact us to upgrade")
	errLicenceExpired  = errors.New("your licence has expired: please renew to continue sending")
	errFeatureLocked   = errors.New("this feature is not included in your plan")
	errLimitReached    = errors.New("plan limit reached")
	errBadLicenceKey   = errors.New("this licence key is not valid or already used")
)

// limitError names which limit was hit (sent to the website as "limit").
type limitError struct{ name string }

func (e *limitError) Error() string { return "plan limit reached: " + e.name }
func (e *limitError) Is(t error) bool {
	return t == errLimitReached
}

var allFeatures = []string{"campaigns", "scheduler", "autoreply", "pool", "api", "webhooks", "groups", "status", "polls", "ai"}

type Plan struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Price           float64         `json:"price"`
	Currency        string          `json:"currency"`
	PeriodDays      int64           `json:"period_days"`
	MaxNumbers      int64           `json:"max_numbers"`      // 0 = unlimited
	DailyLimit      int64           `json:"daily_limit"`      // per number, 0 = gateway default
	MonthlyMessages int64           `json:"monthly_messages"` // 0 = unlimited
	MaxContacts     int64           `json:"max_contacts"`     // 0 = unlimited
	StorageMB       int64           `json:"storage_mb"`       // 0 = unlimited
	Features        map[string]bool `json:"features"`
	Public          bool            `json:"public"`
	Trial           bool            `json:"trial"`
	Highlight       bool            `json:"highlight"`
	Sort            int64           `json:"sort"`
	Unlimited       bool            `json:"unlimited,omitempty"`
}

func (p *Plan) has(feature string) bool {
	if p == nil {
		return false
	}
	if p.Unlimited {
		return true
	}
	return p.Features[feature]
}

const planCols = `id, name, description, price, currency, period_days, max_numbers, daily_limit, monthly_messages, max_contacts, storage_mb, features, public, trial, highlight, sort`

func scanPlan(row interface{ Scan(...any) error }) (*Plan, error) {
	var p Plan
	var feats string
	var pub, trial, hl int
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Price, &p.Currency, &p.PeriodDays, &p.MaxNumbers, &p.DailyLimit,
		&p.MonthlyMessages, &p.MaxContacts, &p.StorageMB, &feats, &pub, &trial, &hl, &p.Sort); err != nil {
		return nil, err
	}
	p.Features = map[string]bool{}
	_ = json.Unmarshal([]byte(feats), &p.Features)
	for _, f := range allFeatures {
		if _, ok := p.Features[f]; !ok {
			p.Features[f] = false
		}
	}
	p.Public, p.Trial, p.Highlight = pub == 1, trial == 1, hl == 1
	return &p, nil
}

func getPlan(ctx context.Context, db *sql.DB, id int64) (*Plan, error) {
	return scanPlan(db.QueryRowContext(ctx, `SELECT `+planCols+` FROM plans WHERE id=?`, id))
}

func listPlans(ctx context.Context, db *sql.DB, onlyPublic bool) []*Plan {
	q := `SELECT ` + planCols + ` FROM plans`
	if onlyPublic {
		q += ` WHERE public=1`
	}
	rows, err := db.QueryContext(ctx, q+` ORDER BY sort, price, id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Plan
	for rows.Next() {
		if p, err := scanPlan(rows); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func savePlan(ctx context.Context, db *sql.DB, p *Plan) (int64, error) {
	if strings.TrimSpace(p.Name) == "" {
		return 0, errors.New("plan name is required")
	}
	if p.Currency == "" {
		p.Currency = "INR"
	}
	if p.PeriodDays <= 0 {
		p.PeriodDays = 30
	}
	feats := map[string]bool{}
	for _, f := range allFeatures {
		feats[f] = p.Features[f]
	}
	args := []any{p.Name, p.Description, p.Price, p.Currency, p.PeriodDays, p.MaxNumbers, p.DailyLimit, p.MonthlyMessages,
		p.MaxContacts, p.StorageMB, jsonText(feats), b2i(p.Public), b2i(p.Trial), b2i(p.Highlight), p.Sort}
	if p.ID > 0 {
		_, err := db.ExecContext(ctx, `UPDATE plans SET name=?, description=?, price=?, currency=?, period_days=?, max_numbers=?, daily_limit=?,
			monthly_messages=?, max_contacts=?, storage_mb=?, features=?, public=?, trial=?, highlight=?, sort=? WHERE id=?`, append(args, p.ID)...)
		return p.ID, err
	}
	res, err := db.ExecContext(ctx, `INSERT INTO plans(name, description, price, currency, period_days, max_numbers, daily_limit, monthly_messages,
		max_contacts, storage_mb, features, public, trial, highlight, sort, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(args, time.Now().Unix())...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ensureDefaultPlans seeds editable example plans on a fresh install.
func ensureDefaultPlans(ctx context.Context, db *sql.DB) {
	var n int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plans`).Scan(&n)
	if n > 0 {
		return
	}
	all := func(except ...string) map[string]bool {
		m := map[string]bool{}
		for _, f := range allFeatures {
			m[f] = true
		}
		for _, e := range except {
			m[e] = false
		}
		return m
	}
	seed := []*Plan{
		{Name: "Free trial", Description: "Try everything on one number.", Price: 0, PeriodDays: 7, MaxNumbers: 1, DailyLimit: 100, MonthlyMessages: 1000, MaxContacts: 1000, StorageMB: 100, Features: all("pool"), Public: false, Trial: true, Sort: 0},
		{Name: "Starter", Description: "One number for a small business.", Price: 499, PeriodDays: 30, MaxNumbers: 1, DailyLimit: 250, MonthlyMessages: 7500, MaxContacts: 5000, StorageMB: 500, Features: all("pool"), Public: true, Sort: 1},
		{Name: "Growth", Description: "Several numbers with rotation and automation.", Price: 1299, PeriodDays: 30, MaxNumbers: 3, DailyLimit: 300, MonthlyMessages: 30000, MaxContacts: 25000, StorageMB: 2000, Features: all(), Public: true, Highlight: true, Sort: 2},
		{Name: "Business", Description: "For agencies and larger teams.", Price: 2999, PeriodDays: 30, MaxNumbers: 10, DailyLimit: 400, MonthlyMessages: 100000, MaxContacts: 100000, StorageMB: 10000, Features: all(), Public: true, Sort: 3},
	}
	for _, p := range seed {
		id, err := savePlan(ctx, db, p)
		if err == nil && p.Trial {
			_ = setSetting(ctx, db, "trial_plan_id", fmt.Sprint(id))
		}
	}
}

// ---------- effective licence ----------

type EffLicence struct {
	Source    string `json:"source"` // licence | trial | admin | selfhosted | none
	Status    string `json:"status"` // active | expired | suspended | none
	Plan      *Plan  `json:"plan"`
	Key       string `json:"key,omitempty"`
	StartsAt  int64  `json:"starts_at"`
	ExpiresAt int64  `json:"expires_at"`
	DaysLeft  int    `json:"days_left"`
	ID        int64  `json:"-"`
}

func (l *EffLicence) active() bool { return l != nil && l.Status == "active" }

func maskKey(k string) string {
	if len(k) <= 8 {
		return k
	}
	return k[:3] + strings.Repeat("•", 6) + k[len(k)-4:]
}

func daysLeft(exp int64) int {
	if exp == 0 {
		return 0
	}
	return int(math.Ceil(time.Until(time.Unix(exp, 0)).Hours() / 24))
}

var ownerPlan = &Plan{Name: "Owner (unlimited)", Unlimited: true, Features: map[string]bool{}}

type Licensing struct {
	db  *sql.DB
	cfg *Config
	log *slog.Logger

	priv ed25519.PrivateKey // licence server signing key (SaaS mode)

	mu       sync.Mutex
	client   *clientToken // self-hosted mode: last verified token
	clientEr string
}

func newLicensing(ctx context.Context, db *sql.DB, cfg *Config, log *slog.Logger) (*Licensing, error) {
	l := &Licensing{db: db, cfg: cfg, log: log}
	if !l.clientMode() {
		ensureDefaultPlans(ctx, db)
		priv, err := loadSigningKey(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		l.priv = priv
	} else {
		l.loadStoredToken(ctx)
	}
	return l, nil
}

func (l *Licensing) clientMode() bool { return l.cfg.LicenceServer != "" }

func (l *Licensing) roleOf(ctx context.Context, userID int64) string {
	var role string
	_ = l.db.QueryRowContext(ctx, `SELECT role FROM users WHERE id=?`, userID).Scan(&role)
	return role
}

// effective returns the licence that applies to a user right now.
func (l *Licensing) effective(ctx context.Context, userID int64) *EffLicence {
	if l.clientMode() {
		return l.clientLicence()
	}
	if userID == 0 || l.roleOf(ctx, userID) == "superadmin" {
		return &EffLicence{Source: "admin", Status: "active", Plan: ownerPlan}
	}
	now := time.Now().Unix()
	var id, planID, start, exp int64
	var key, status string
	err := l.db.QueryRowContext(ctx, `SELECT id, lkey, status, plan_id, activated_at, expires_at FROM licences
		WHERE user_id=? AND kind='saas' AND status IN ('active','suspended') AND activated_at<=?
		ORDER BY (status='active' AND expires_at>?) DESC, expires_at DESC LIMIT 1`, userID, now, now).Scan(&id, &key, &status, &planID, &start, &exp)
	if err != nil {
		return &EffLicence{Source: "none", Status: "none"}
	}
	plan, err := getPlan(ctx, l.db, planID)
	if err != nil {
		return &EffLicence{Source: "none", Status: "none"}
	}
	src := "licence"
	if strings.HasPrefix(key, "TRIAL-") {
		src = "trial"
	} else if strings.HasPrefix(key, "GRANT-") {
		src = "admin"
	}
	eff := &EffLicence{Source: src, Status: status, Plan: plan, Key: maskKey(key), StartsAt: start, ExpiresAt: exp, DaysLeft: daysLeft(exp), ID: id}
	if status == "active" && exp > 0 && exp <= now {
		eff.Status = "expired"
		_, _ = l.db.ExecContext(ctx, `UPDATE licences SET status='expired' WHERE id=?`, id)
	}
	return eff
}

func licenceErr(eff *EffLicence) error {
	switch {
	case eff == nil || eff.Status == "none" || eff.Plan == nil:
		return errLicenceRequired
	case eff.Status != "active":
		return errLicenceExpired
	}
	return nil
}

// require checks the licence is active and includes a feature ("" = just active).
func (l *Licensing) require(ctx context.Context, userID int64, feature string) error {
	eff := l.effective(ctx, userID)
	if err := licenceErr(eff); err != nil {
		return err
	}
	if feature != "" && !eff.Plan.has(feature) {
		return fmt.Errorf("%w (%s)", errFeatureLocked, feature)
	}
	return nil
}

// scope returns the owner filter for usage counting (client mode counts the whole install).
func (l *Licensing) scope(userID int64) (string, []any) {
	if l.clientMode() {
		return "1=1", nil
	}
	return "owner_id=?", []any{userID}
}

type Usage struct {
	Numbers       int     `json:"numbers"`
	MessagesMonth int     `json:"messages_month"`
	Contacts      int     `json:"contacts"`
	StorageMB     float64 `json:"storage_mb"`
}

func (l *Licensing) usage(ctx context.Context, userID int64) Usage {
	w, args := l.scope(userID)
	var u Usage
	_ = l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gw_sessions WHERE `+w, args...).Scan(&u.Numbers)
	month := time.Now().Format("2006-01") + "-01"
	_ = l.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(sent),0) FROM stats_daily WHERE `+w+` AND day>=?`, append(args, month)...).Scan(&u.MessagesMonth)
	_ = l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE `+w, args...).Scan(&u.Contacts)
	var bytes int64
	_ = l.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0) FROM media WHERE `+w, args...).Scan(&bytes)
	u.StorageMB = math.Round(float64(bytes)/1048576*10) / 10
	return u
}

// checkLimit verifies one more unit of a limit fits ("numbers", "messages", "contacts", "storage").
func (l *Licensing) checkLimit(ctx context.Context, userID int64, limit string, adding float64) error {
	eff := l.effective(ctx, userID)
	if err := licenceErr(eff); err != nil {
		return err
	}
	if eff.Plan.Unlimited {
		return nil
	}
	u := l.usage(ctx, userID)
	var max, cur float64
	switch limit {
	case "numbers":
		max, cur = float64(eff.Plan.MaxNumbers), float64(u.Numbers)
	case "messages":
		max, cur = float64(eff.Plan.MonthlyMessages), float64(u.MessagesMonth)
	case "contacts":
		max, cur = float64(eff.Plan.MaxContacts), float64(u.Contacts)
	case "storage":
		max, cur = float64(eff.Plan.StorageMB), u.StorageMB
	}
	if max > 0 && cur+adding > max {
		return &limitError{name: limit}
	}
	return nil
}

// planDailyLimit is the per-number daily cap from the plan (0 = none).
func (l *Licensing) planDailyLimit(ctx context.Context, userID int64) int {
	eff := l.effective(ctx, userID)
	if eff.Plan == nil || eff.Plan.Unlimited {
		return 0
	}
	return int(eff.Plan.DailyLimit)
}

// ---------- keys ----------

func newLicenceKey(kind string) string {
	prefix := "VR"
	if kind == "selfhosted" {
		prefix = "VRS"
	}
	t := strings.ToUpper(randToken(15)) // 24 chars
	return fmt.Sprintf("%s-%s-%s-%s-%s", prefix, t[0:5], t[5:10], t[10:15], t[15:20])
}

func (l *Licensing) startTrial(ctx context.Context, userID int64) {
	var planID int64
	fmt.Sscan(getSetting(ctx, l.db, "trial_plan_id"), &planID)
	var days int64
	fmt.Sscan(getSetting(ctx, l.db, "trial_days"), &days)
	if planID == 0 || days <= 0 {
		return
	}
	if _, err := getPlan(ctx, l.db, planID); err != nil {
		return
	}
	now := time.Now()
	_, _ = l.db.ExecContext(ctx, `INSERT INTO licences(lkey, kind, status, plan_id, user_id, days, created_at, activated_at, expires_at)
		VALUES(?, 'saas', 'active', ?, ?, ?, ?, ?, ?)`, "TRIAL-"+strings.ToUpper(randToken(10)), planID, userID, days,
		now.Unix(), now.Unix(), now.Add(time.Duration(days)*24*time.Hour).Unix())
}

// grant creates and activates a licence for a user, starting after any current one.
func (l *Licensing) grant(ctx context.Context, userID, planID, days int64, prefix, key string) (int64, error) {
	if days <= 0 {
		return 0, errors.New("days must be positive")
	}
	if _, err := getPlan(ctx, l.db, planID); err != nil {
		return 0, errors.New("plan not found")
	}
	start := time.Now().Unix()
	var latest int64
	_ = l.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(expires_at),0) FROM licences WHERE user_id=? AND kind='saas' AND status='active' AND lkey NOT LIKE 'TRIAL-%'`, userID).Scan(&latest)
	if latest > start {
		start = latest // renewals stack
	}
	exp := start + days*86400
	if key == "" {
		key = prefix + strings.ToUpper(randToken(10))
		res, err := l.db.ExecContext(ctx, `INSERT INTO licences(lkey, kind, status, plan_id, user_id, days, created_at, activated_at, expires_at)
			VALUES(?, 'saas', 'active', ?, ?, ?, ?, ?, ?)`, key, planID, userID, days, time.Now().Unix(), start, exp)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := l.db.ExecContext(ctx, `UPDATE licences SET status='active', user_id=?, activated_at=?, expires_at=? WHERE lkey=? AND kind='saas' AND status='unused'`,
		userID, start, exp, key)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, errBadLicenceKey
	}
	// End the free trial once a paid licence starts.
	_, _ = l.db.ExecContext(ctx, `UPDATE licences SET status='expired', expires_at=? WHERE user_id=? AND lkey LIKE 'TRIAL-%' AND status='active'`, time.Now().Unix(), userID)
	var id int64
	_ = l.db.QueryRowContext(ctx, `SELECT id FROM licences WHERE lkey=?`, key).Scan(&id)
	return id, nil
}

func (l *Licensing) redeem(ctx context.Context, userID int64, key string) error {
	key = strings.ToUpper(strings.TrimSpace(key))
	var planID, days int64
	if l.db.QueryRowContext(ctx, `SELECT plan_id, days FROM licences WHERE lkey=? AND kind='saas' AND status='unused'`, key).Scan(&planID, &days) != nil {
		return errBadLicenceKey
	}
	_, err := l.grant(ctx, userID, planID, days, "", key)
	return err
}

// ======================= self-hosted licences =======================

func loadSigningKey(dataDir string) (ed25519.PrivateKey, error) {
	path := filepath.Join(dataDir, "licence_ed25519.seed")
	if b, err := os.ReadFile(path); err == nil && len(b) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(b), nil
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		return nil, fmt.Errorf("save licence signing key: %w", err)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func (l *Licensing) publicKey() string {
	if l.priv == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(l.priv.Public().(ed25519.PublicKey))
}

// tokenPayload is what the licence server signs for a self-hosted install.
type tokenPayload struct {
	Key        string `json:"k"`
	InstanceID string `json:"iid"`
	Plan       *Plan  `json:"plan"`
	ExpiresAt  int64  `json:"exp"` // licence end
	IssuedAt   int64  `json:"iat"`
	ValidUntil int64  `json:"vu"` // works offline until this time (then must re-check)
}

func (l *Licensing) sign(p tokenPayload) string {
	body, _ := json.Marshal(p)
	sig := ed25519.Sign(l.priv, body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func verifyToken(token, pubB64 string) (*tokenPayload, error) {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("LICENCE_PUBLIC_KEY is not valid")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, errors.New("bad licence token")
	}
	body, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	if err1 != nil || err2 != nil || !ed25519.Verify(pub, body, sig) {
		return nil, errors.New("licence token signature is invalid")
	}
	var p tokenPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if p.Plan == nil {
		return nil, errors.New("licence token has no plan")
	}
	return &p, nil
}

// activateHandler is the licence-server endpoint self-hosted installs call.
func (a *API) licenceActivate(w http.ResponseWriter, r *http.Request) {
	if a.lic.clientMode() {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("not a licence server"))
		return
	}
	if !loginLimiter.allow("activate:"+clientIP(r), 60, time.Hour) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", errTooMany)
		return
	}
	var b struct {
		Key        string `json:"key"`
		InstanceID string `json:"instance_id"`
		Domain     string `json:"domain"`
		Version    string `json:"version"`
	}
	if err := decode(r, &b); err != nil || b.Key == "" || len(b.InstanceID) < 8 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("key and instance_id are required"))
		return
	}
	ctx := r.Context()
	key := strings.ToUpper(strings.TrimSpace(b.Key))
	var id, planID, days, maxInst, exp int64
	var status string
	if a.m.db.QueryRowContext(ctx, `SELECT id, plan_id, days, max_instances, status, expires_at FROM licences WHERE lkey=? AND kind='selfhosted'`, key).
		Scan(&id, &planID, &days, &maxInst, &status, &exp) != nil {
		writeErr(w, http.StatusForbidden, "bad_licence", errBadLicenceKey)
		return
	}
	now := time.Now()
	switch status {
	case "suspended":
		writeErr(w, http.StatusForbidden, "licence_suspended", errors.New("this licence is suspended"))
		return
	case "unused":
		exp = now.Add(time.Duration(days) * 24 * time.Hour).Unix()
		_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET status='active', activated_at=?, expires_at=? WHERE id=?`, now.Unix(), exp, id)
	}
	if exp <= now.Unix() {
		_, _ = a.m.db.ExecContext(ctx, `UPDATE licences SET status='expired' WHERE id=? AND status='active'`, id)
		writeErr(w, http.StatusForbidden, "licence_expired", errLicenceExpired)
		return
	}
	var others int
	_ = a.m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM licence_activations WHERE licence_id=? AND instance_id<>?`, id, b.InstanceID).Scan(&others)
	if maxInst > 0 && others >= int(maxInst) {
		writeErr(w, http.StatusForbidden, "instance_limit", fmt.Errorf("this licence is already used on %d server(s)", others))
		return
	}
	_, _ = a.m.db.ExecContext(ctx, `INSERT INTO licence_activations(licence_id, instance_id, domain, version, ip, first_seen, last_seen) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(licence_id, instance_id) DO UPDATE SET domain=excluded.domain, version=excluded.version, ip=excluded.ip, last_seen=excluded.last_seen`,
		id, b.InstanceID, truncate(b.Domain, 200), truncate(b.Version, 40), clientIP(r), now.Unix(), now.Unix())
	plan, err := getPlan(ctx, a.m.db, planID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "plan_missing", errors.New("plan missing on licence server"))
		return
	}
	vu := min(now.Add(7*24*time.Hour).Unix(), exp)
	tok := a.lic.sign(tokenPayload{Key: key, InstanceID: b.InstanceID, Plan: plan, ExpiresAt: exp, IssuedAt: now.Unix(), ValidUntil: vu})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok, "expires_at": exp})
}

// ---------- client side (self-hosted install) ----------

type clientToken struct {
	tokenPayload
	raw string
}

func (l *Licensing) instanceID(ctx context.Context) string {
	id := getSetting(ctx, l.db, "instance_id")
	if id == "" {
		id = randToken(16)
		_ = setSetting(ctx, l.db, "instance_id", id)
	}
	return id
}

func (l *Licensing) loadStoredToken(ctx context.Context) {
	raw := getSetting(ctx, l.db, "licence_token")
	if raw == "" {
		return
	}
	if p, err := verifyToken(raw, l.cfg.LicencePublicKey); err == nil && p.InstanceID == l.instanceID(ctx) {
		l.mu.Lock()
		l.client = &clientToken{tokenPayload: *p, raw: raw}
		l.mu.Unlock()
	}
}

func (l *Licensing) clientLicence() *EffLicence {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client == nil {
		return &EffLicence{Source: "selfhosted", Status: "none"}
	}
	t := l.client
	eff := &EffLicence{Source: "selfhosted", Status: "active", Plan: t.Plan, Key: maskKey(t.Key), StartsAt: t.IssuedAt, ExpiresAt: t.ExpiresAt, DaysLeft: daysLeft(t.ExpiresAt)}
	now := time.Now().Unix()
	if now >= t.ExpiresAt || now >= t.ValidUntil {
		eff.Status = "expired"
	}
	return eff
}

// runClient checks the licence with the licence server at start and every 12 hours.
func (l *Licensing) runClient(ctx context.Context) {
	if !l.clientMode() {
		return
	}
	for {
		if err := l.checkIn(ctx); err != nil {
			l.log.Warn("licence check failed (install keeps working until the last token expires)", "err", err)
			l.mu.Lock()
			l.clientEr = err.Error()
			l.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(12 * time.Hour):
		}
	}
}

func (l *Licensing) checkIn(ctx context.Context) error {
	if l.cfg.LicenceKey == "" {
		return errors.New("LICENCE_KEY is not set")
	}
	body, _ := json.Marshal(map[string]string{"key": l.cfg.LicenceKey, "instance_id": l.instanceID(ctx), "domain": l.cfg.PublicURL, "version": version})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(l.cfg.LicenceServer, "/")+"/licence/v1/activate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("licence server answered HTTP %d", resp.StatusCode)
	}
	if !out.OK {
		if resp.StatusCode == http.StatusForbidden { // suspended / expired / not valid: stop now
			l.mu.Lock()
			l.client = nil
			l.mu.Unlock()
			_ = setSetting(ctx, l.db, "licence_token", "")
		}
		return errors.New(out.Error)
	}
	p, err := verifyToken(out.Token, l.cfg.LicencePublicKey)
	if err != nil {
		return err
	}
	if p.InstanceID != l.instanceID(ctx) {
		return errors.New("licence token is for another server")
	}
	_ = setSetting(ctx, l.db, "licence_token", out.Token)
	l.mu.Lock()
	l.client, l.clientEr = &clientToken{tokenPayload: *p, raw: out.Token}, ""
	l.mu.Unlock()
	return nil
}

func (l *Licensing) clientError() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.clientEr
}
