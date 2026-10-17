package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestParseTarget(t *testing.T) {
	cases := map[string]string{
		"9876543210":                     "919876543210@s.whatsapp.net",
		"+91 98765-43210":                "919876543210@s.whatsapp.net",
		"09876543210":                    "919876543210@s.whatsapp.net",
		"0091 9876543210":                "919876543210@s.whatsapp.net",
		"14155550123":                    "14155550123@s.whatsapp.net",
		"120363012345678901@g.us":        "120363012345678901@g.us",
		"120363098765432109@newsletter":  "120363098765432109@newsletter",
		"919876543210:12@s.whatsapp.net": "919876543210@s.whatsapp.net",
		"123456789012345@lid":            "123456789012345@lid",
	}
	for in, want := range cases {
		got, err := parseTarget(in, "91")
		if err != nil || got.String() != want {
			t.Errorf("parseTarget(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "12345", "abc@broadcast", "https://chat.whatsapp.com/abc", "1234567890123456789"} {
		if _, err := parseTarget(bad, "91"); err == nil {
			t.Errorf("parseTarget(%q) should fail", bad)
		}
	}
}

func TestRenderText(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	got := renderText("{Hi|Hello} {{Name}}, your id is {{ id }}. {{missing}}", map[string]string{"name": "Asha", "ID": "7"}, rnd)
	if !(strings.HasPrefix(got, "Hi Asha") || strings.HasPrefix(got, "Hello Asha")) || !strings.Contains(got, "id is 7") || !strings.Contains(got, "{{missing}}") {
		t.Fatalf("unexpected: %q", got)
	}
	if got := renderText("price {100} and json {\"a\":1}", nil, rnd); got != "price {100} and json {\"a\":1}" {
		t.Fatalf("braces without | must stay: %q", got)
	}
	seen := map[string]bool{}
	for range 200 {
		seen[renderText("{a|b|{c|d}}", nil, rnd)] = true
	}
	if len(seen) != 4 {
		t.Fatalf("nested spintax should give 4 variants, got %v", seen)
	}
}

func TestExtractContent(t *testing.T) {
	c := extractContent(&waE2E.Message{Conversation: proto.String("hello")})
	if c["type"] != "text" || c["text"] != "hello" {
		t.Fatal(c)
	}
	c = extractContent(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("pic"), Mimetype: proto.String("image/jpeg"),
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("Q1")}}})
	if c["type"] != "image" || c["caption"] != "pic" || c["quoted_id"] != "Q1" {
		t.Fatal(c)
	}
	c = extractContent(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key: &waCommon.MessageKey{ID: proto.String("X1")}}})
	if c["type"] != "revoke" || c["id"] != "X1" {
		t.Fatal(c)
	}
	if extractContent(&waE2E.Message{SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{}}) != nil {
		t.Fatal("internal messages must be ignored")
	}
}

func TestErrorsAndLimits(t *testing.T) {
	if code := serverErrCode(fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, 463)); code != "463" {
		t.Fatalf("got %q", code)
	}
	if !isPermanent(errNotOnWA) || isPermanent(errors.New("connection reset")) {
		t.Fatal("isPermanent wrong")
	}
	s := &Session{m: &Manager{cfg: &Config{DailyLimit: 250, WarmupStart: 20, SendHourFrom: 9, SendHourTo: 21}}}
	day := int64(24 * 3600)
	now := time.Now().Unix()
	if l := s.dailyLimit(now); l != 20 {
		t.Fatalf("day 1 limit %d", l)
	}
	if l := s.dailyLimit(now - 2*day - 60); l != 51 {
		t.Fatalf("day 3 limit %d", l)
	}
	if l := s.dailyLimit(now - 30*day); l != 250 {
		t.Fatalf("cap %d", l)
	}
	if l := s.dailyLimit(0); l != 250 {
		t.Fatalf("unknown link time %d", l)
	}
	at := func(h int) time.Time { return time.Date(2026, 1, 1, h, 30, 0, 0, time.Local) }
	if !s.inSendingHours(at(9)) || !s.inSendingHours(at(20)) || s.inSendingHours(at(21)) || s.inSendingHours(at(3)) {
		t.Fatal("sending hours wrong")
	}
	s.m.cfg.SendHourFrom, s.m.cfg.SendHourTo = 22, 6
	if !s.inSendingHours(at(23)) || !s.inSendingHours(at(2)) || s.inSendingHours(at(12)) {
		t.Fatal("overnight hours wrong")
	}
}

func TestValidate(t *testing.T) {
	ok := []SendRequest{{To: "9876543210", Text: "hi"}, {To: "x", Media: &Media{Type: "image", URL: "https://a/b.jpg"}}}
	for _, r := range ok {
		if err := r.validate(); err != nil {
			t.Error(err)
		}
	}
	bad := []SendRequest{{To: "1"}, {Text: "hi"}, {To: "1", Media: &Media{Type: "gif", URL: "u"}}, {To: "1", Media: &Media{Type: "image"}}}
	for _, r := range bad {
		if err := r.validate(); err == nil {
			t.Errorf("should fail: %+v", r)
		}
	}
}

func TestWebhookSignature(t *testing.T) {
	w := newWebhooks("secret", nil)
	sig := w.sign([]byte(`{"a":1}`), "1700000000")
	// echo -n '1700000000.{"a":1}' | openssl dgst -sha256 -hmac secret
	if sig != "sha256=49f24e537407743fa4a0242bb63b94b9a47ee99cbbe071ccd8a22550ae411686" {
		t.Fatal(sig)
	}
	if w.sign([]byte(`{"a":2}`), "1700000000") == sig {
		t.Fatal("signature must depend on body")
	}
}
