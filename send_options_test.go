package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	pngenc "image/png"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestMessageVariants(t *testing.T) {
	yes := true
	req := &SendRequest{To: "919876543210", Text: "Hi {{name}}", Vars: map[string]string{"name": "Asha"}, Typing: &yes,
		Variants: []SendRequest{{Text: "Hello {{name}}"}, {Media: &Media{Type: "image", URL: "https://x/y.jpg"}, Text: "Look"}}}
	if err := req.validateContent(); err != nil {
		t.Fatal(err)
	}
	v0 := req.variant(0)
	if v0.Text != "Hi {{name}}" || v0.Variants != nil || v0.To != req.To {
		t.Fatalf("variant 0: %+v", v0)
	}
	v2 := req.variant(2)
	if v2.Media == nil || v2.Text != "Look" || v2.To != req.To || v2.Vars["name"] != "Asha" || v2.Typing == nil || v2.Variants != nil {
		t.Fatalf("variant 2: %+v", v2)
	}
	if len(req.Variants) != 2 || req.Variants[1].To != "" {
		t.Fatal("variant() changed the original")
	}
	bad := &SendRequest{Text: "a", Variants: []SendRequest{{Text: "b", Variants: []SendRequest{{Text: "c"}}}}}
	if bad.validateContent() == nil {
		t.Fatal("nested variants accepted")
	}
	bad = &SendRequest{Text: "a", Variants: []SendRequest{{}}}
	if bad.validateContent() == nil {
		t.Fatal("empty variant accepted")
	}
	bad = &SendRequest{Text: "a", Variants: make([]SendRequest, 10)}
	if bad.validateContent() == nil {
		t.Fatal("too many variants accepted")
	}
	if (&SendRequest{Text: "a", StatusAudience: &StatusAudience{Mode: "everyone"}}).validateContent() == nil {
		t.Fatal("bad status audience mode accepted")
	}
}

type fakeContacts struct {
	store.ContactStore
	all map[types.JID]types.ContactInfo
}

func (f *fakeContacts) GetAllContacts(context.Context) (map[types.JID]types.ContactInfo, error) {
	return f.all, nil
}

func TestStatusContactsWrapper(t *testing.T) {
	phoneBook := map[types.JID]types.ContactInfo{types.NewJID("911111111111", types.DefaultUserServer): {Found: true, FullName: "A"}}
	w := &statusContacts{&fakeContacts{all: phoneBook}}
	got, _ := w.GetAllContacts(context.Background())
	if len(got) != 1 {
		t.Fatalf("pass-through: %v", got)
	}
	chosen := []types.JID{types.NewJID("912222222222", types.DefaultUserServer), types.NewJID("913333333333", types.DefaultUserServer)}
	got, _ = w.GetAllContacts(context.WithValue(context.Background(), statusAudKey{}, chosen))
	if len(got) != 2 || got[chosen[0]].FullName == "" {
		t.Fatalf("audience override: %v", got)
	}
}

func TestLeftGateway(t *testing.T) {
	sent := whatsmeow.SendResponse{}
	sent.DebugTimings.Send = time.Millisecond
	cases := []struct {
		err  error
		resp whatsmeow.SendResponse
		want bool
	}{
		{whatsmeow.ErrMessageTimedOut, whatsmeow.SendResponse{}, true},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), sent, true},
		{context.DeadlineExceeded, whatsmeow.SendResponse{}, false}, // never left: real failure
		{errors.New("server returned error 479"), sent, false},
	}
	for i, c := range cases {
		if got := leftGateway(c.err, c.resp); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func TestCampaignRotationAndStatusSchedule(t *testing.T) {
	env := newTestEnv(t, nil)
	env.browser().ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	c := env.withKey(testKey)
	a := c.ok("POST", "/api/sessions", map[string]any{"name": "a"})["session"].(map[string]any)["name"].(string)
	b := c.ok("POST", "/api/sessions", map[string]any{"name": "b"})["session"].(map[string]any)["name"].(string)

	future := time.Now().Add(24 * time.Hour).Unix()
	camp := c.ok("POST", "/api/campaigns", map[string]any{"name": "Rotate", "sessions": []string{a, b}, "rotation": "random",
		"message":  map[string]any{"text": "Hi", "variants": []map[string]any{{"text": "Hello"}, {"text": "Namaste"}}},
		"audience": map[string]any{"phones": []string{"9000000001", "9000000002"}}, "schedule": map[string]any{"at": future}})["campaign"].(map[string]any)
	if camp["rotation"] != "random" || len(camp["message"].(map[string]any)["variants"].([]any)) != 2 {
		t.Fatalf("campaign: %v", camp)
	}
	got := c.ok("GET", fmt.Sprintf("/api/campaigns/%v", camp["id"]), nil)["campaign"].(map[string]any)
	if got["rotation"] != "random" {
		t.Fatalf("rotation not stored: %v", got)
	}
	if code, _ := c.do("POST", "/api/campaigns", map[string]any{"name": "X", "sessions": []string{a}, "rotation": "chaos",
		"message": map[string]any{"text": "Hi"}, "audience": map[string]any{"phones": []string{"9000000001"}}}, true); code != 400 {
		t.Fatalf("bad rotation accepted: %d", code)
	}

	// A daily Status post with a chosen audience can be scheduled like any campaign.
	st := c.ok("POST", "/api/campaigns", map[string]any{"name": "Daily story", "sessions": []string{a},
		"message":  map[string]any{"text": "Good morning", "background": "#075E54", "status_audience": map[string]any{"mode": "tags", "tags": []string{"vip"}, "limit": 200}},
		"audience": map[string]any{"statuses": true}, "schedule": map[string]any{"at": future, "repeat": "daily"}})["campaign"].(map[string]any)
	if st["status"] != "scheduled" || st["message"].(map[string]any)["status_audience"] == nil {
		t.Fatalf("status campaign: %v", st)
	}

	// enqueue with random rotation spreads people over the pool.
	msgs := make([]SendRequest, 40)
	for i := range msgs {
		msgs[i] = SendRequest{To: fmt.Sprintf("9190000%05d", i), Text: "x"}
	}
	res, err := env.api.m.enqueue(context.Background(), 1, []string{a, b}, 0, "rnd", msgs, time.Now().Add(time.Hour).Unix(), 1, "random")
	if err != nil {
		t.Fatal(err)
	}
	if res.PerSession[a] == 0 || res.PerSession[b] == 0 || res.PerSession[a]+res.PerSession[b] != 40 {
		t.Fatalf("random rotation: %v", res.PerSession)
	}
}

func TestUniqueCode(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		out := withUniqueCode("Diwali offer!\n", "ref", "", rnd)
		if !strings.HasPrefix(out, "Diwali offer!\n\nRef: ") || len(out) != len("Diwali offer!\n\nRef: ")+6 {
			t.Fatalf("ref line: %q", out)
		}
		if seen[out] {
			t.Fatalf("duplicate code after %d messages", i)
		}
		seen[out] = true
	}
	if out := withUniqueCode("Hi", "number", "", rnd); !regexp.MustCompile(`^Hi\n\n#\d{6}$`).MatchString(out) {
		t.Fatalf("number line: %q", out)
	}
	if out := withUniqueCode("Hi", "number", "Code:", rnd); !regexp.MustCompile(`^Hi\n\nCode: \d{6}$`).MatchString(out) {
		t.Fatalf("custom prefix: %q", out)
	}
	if out := withUniqueCode("", "ref", "ID", rnd); !regexp.MustCompile(`^ID [A-Z2-9]{6}$`).MatchString(out) {
		t.Fatalf("empty text: %q", out)
	}
	if withUniqueCode("Hi", "", "", rnd) != "Hi" {
		t.Fatal("code added when off")
	}
	got := renderText("Order {{ref}} / token {{random}} for {{name}}", map[string]string{"name": "Asha"}, rnd)
	if !regexp.MustCompile(`^Order [A-Z2-9]{6} / token \d{6} for Asha$`).MatchString(got) {
		t.Fatalf("built-in vars: %q", got)
	}
	if got := renderText("{{ref}}", map[string]string{"ref": "MINE"}, rnd); got != "MINE" {
		t.Fatalf("own ref var overridden: %q", got)
	}
	if (&SendRequest{Text: "x", UniqueCode: "emoji"}).validateContent() == nil {
		t.Fatal("bad unique_code accepted")
	}
	v := (&SendRequest{Text: "a", UniqueCode: "ref", Variants: []SendRequest{{Text: "b"}}}).variant(1)
	if v.UniqueCode != "ref" {
		t.Fatal("variant lost unique code")
	}
}

func TestNoDoubleStatus(t *testing.T) {
	s := &Session{}
	a := &SendRequest{Text: "Shubh Diwali", Background: "#075E54"}
	b := &SendRequest{Text: "Shubh Diwali", Background: "#075E54", Media: &Media{Type: "image", MediaID: 7}}
	fa, fb := statusFingerprint(a), statusFingerprint(b)
	if fa == fb {
		t.Fatal("photo status has the same fingerprint as text status")
	}
	if !s.claimStatus(fa) || s.claimStatus(fa) {
		t.Fatal("second identical post was not refused")
	}
	if !s.claimStatus(fb) {
		t.Fatal("different status refused")
	}
	s.releaseStatus(fa) // a failed post can be tried again
	if !s.claimStatus(fa) {
		t.Fatal("released status still refused")
	}
	other := &Session{} // every number keeps its own list
	if !other.claimStatus(fa) {
		t.Fatal("another number was blocked")
	}
	if !isPermanent(errDupStatus) {
		t.Fatal("duplicate status would be retried by the queue")
	}
}

func TestLinkPreviewBlockedBotAndEndpoint(t *testing.T) {
	var png bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	_ = pngenc.Encode(&png, img)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Like a Cloudflare site with bot protection: "bots" get 403, browsers get the page.
		if !strings.Contains(r.UserAgent(), "Chrome/") {
			http.Error(w, "blocked", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/p":
			_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="Blocked &amp; found"><meta property="og:image" content="/i.png"></head></html>`))
		case "/i.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png.Bytes())
		case "/noimg":
			_, _ = w.Write([]byte(`<html><head><title>No picture</title></head></html>`))
		}
	}))
	defer site.Close()
	p := getPreview(context.Background(), site.URL+"/p", true, true)
	if p.Error != "" || p.HQ == nil || p.Title != "Blocked & found" {
		t.Fatalf("browser fallback: %+v", p)
	}

	env := newTestEnv(t, nil)
	env.browser().ok("POST", "/auth/setup", map[string]any{"api_key": testKey, "name": "Owner", "email": "o@example.com", "password": "secret123"})
	c := env.withKey(testKey)
	out := c.ok("GET", "/api/link-preview?fresh=1&url="+url.QueryEscape("see "+site.URL+"/p today"), nil)
	if out["has_image"] != true || !strings.HasPrefix(out["thumb"].(string), "data:image/jpeg;base64,") || out["title"] != "Blocked & found" {
		t.Fatalf("endpoint: %v", out)
	}
	out = c.ok("GET", "/api/link-preview?url="+url.QueryEscape(site.URL+"/noimg"), nil)
	if out["has_image"] != false || !strings.Contains(out["advice"].(string), "og:image") {
		t.Fatalf("no-image advice: %v", out)
	}
}

func TestRefusedForSize(t *testing.T) {
	for _, e := range []error{fmt.Errorf("%w 479", whatsmeow.ErrServerReturnedError), errors.New("content limit exceeded"), errors.New("rate-overlimit")} {
		if !refusedForSize(e) {
			t.Errorf("%v should shrink and retry", e)
		}
	}
	if refusedForSize(errors.New("websocket not connected")) {
		t.Error("connection error treated as size refusal")
	}
}
