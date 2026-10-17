package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Config holds all settings. Everything comes from environment variables
// (see .env.example), so nothing has to be edited inside the code.
type Config struct {
	Port       string
	DataDir    string
	APIKey     string
	DeviceName string

	WebhookURL    string
	WebhookSecret string

	DefaultCountryCode string // added to 10-digit numbers, e.g. "91"

	// Safe-sending policy (applies to the queue; direct sends use the
	// daily limit and a small gap only).
	MinDelay        time.Duration // random gap between queued messages
	MaxDelay        time.Duration
	BatchSize       int           // after this many queued messages...
	BatchPause      time.Duration // ...take a longer break
	DailyLimit      int           // max messages per session per day (all kinds)
	NewContactLimit int           // max messages per day to people who never wrote to us
	WarmupStart     int           // day-1 limit for a freshly linked number (0 = no warm-up)
	SendHourFrom    int           // queue only sends between these local hours
	SendHourTo      int
	DirectGap       time.Duration // minimum gap between direct (API) sends
	Typing          bool          // show "typing..." before text messages
	CheckHourly     int           // max numbers checked via /check per hour per session

	MaxMediaBytes      int64
	AllowPrivateURLs   bool
	TimelockPause      time.Duration // how long to pause new-contact sending after error 463
	RetentionDays      int           // delete finished queue rows older than this
	WebhookIncludeSelf bool          // also forward messages sent from the phone itself

	// Opt-out: people who write STOP never get queued (campaign) messages again.
	OptoutKeywords []string
	OptinKeywords  []string
	OptoutReply    string
	OptinReply     string

	PerContactWeekly int // max campaign (queue) messages to one person in 7 days
	RecentReplyDays  int // people who wrote within this many days are sent first

	// Health guard: auto-slow / auto-pause a number that looks unhealthy.
	HealthMinSample int     // judge only after this many campaign sends in the window
	HealthMaxFail   float64 // pause the queue if more than this share of sends fail
	HealthMinReply  float64 // slow down if fewer than this share of new contacts reply
}

func loadConfig() (*Config, error) {
	c := &Config{
		Port:               env("PORT", "8080"),
		DataDir:            env("DATA_DIR", "./data"),
		APIKey:             os.Getenv("API_KEY"),
		DeviceName:         env("DEVICE_NAME", "Vivid Relay"),
		WebhookURL:         os.Getenv("WEBHOOK_URL"),
		WebhookSecret:      os.Getenv("WEBHOOK_SECRET"),
		DefaultCountryCode: strings.TrimPrefix(env("DEFAULT_COUNTRY_CODE", "91"), "+"),
		MinDelay:           envDur("SEND_MIN_DELAY", 30*time.Second),
		MaxDelay:           envDur("SEND_MAX_DELAY", 60*time.Second),
		BatchSize:          envInt("BATCH_SIZE", 20),
		BatchPause:         envDur("BATCH_PAUSE", 10*time.Minute),
		DailyLimit:         envInt("DAILY_LIMIT", 250),
		NewContactLimit:    envInt("NEW_CONTACT_DAILY_LIMIT", 40),
		WarmupStart:        envInt("WARMUP_START", 20),
		SendHourFrom:       envInt("SEND_HOUR_FROM", 9),
		SendHourTo:         envInt("SEND_HOUR_TO", 21),
		DirectGap:          envDur("DIRECT_GAP", 2*time.Second),
		Typing:             envBool("TYPING", true),
		CheckHourly:        envInt("CHECK_HOURLY_LIMIT", 100),
		MaxMediaBytes:      int64(envInt("MAX_MEDIA_MB", 16)) << 20,
		AllowPrivateURLs:   envBool("ALLOW_PRIVATE_URLS", false),
		TimelockPause:      envDur("TIMELOCK_PAUSE", 6*time.Hour),
		RetentionDays:      envInt("RETENTION_DAYS", 30),
		WebhookIncludeSelf: envBool("WEBHOOK_INCLUDE_SELF", true),
	}
	c.OptoutKeywords = envList("OPTOUT_KEYWORDS", "stop,unsubscribe,stop karo,band karo,बंद,बंद करो,रोकें,मत भेजो")
	c.OptinKeywords = envList("OPTIN_KEYWORDS", "start,subscribe,शुरू")
	c.OptoutReply = env("OPTOUT_REPLY", "ठीक है, अब आपको ये संदेश नहीं भेजे जाएँगे। फिर से पाने के लिए START लिखें।")
	c.OptinReply = env("OPTIN_REPLY", "धन्यवाद! आपको फिर से संदेश मिलेंगे।")
	c.PerContactWeekly = envInt("PER_CONTACT_WEEKLY_LIMIT", 3)
	c.RecentReplyDays = envInt("RECENT_REPLY_DAYS", 30)
	c.HealthMinSample = envInt("HEALTH_MIN_SAMPLE", 30)
	c.HealthMaxFail = envFloat("HEALTH_MAX_FAIL_RATE", 0.25)
	c.HealthMinReply = envFloat("HEALTH_MIN_REPLY_RATE", 0.03)
	if strings.Contains(c.APIKey, "change-me") {
		return nil, fmt.Errorf("API_KEY is still the example value; set a long random key (install.sh does this)")
	}
	if len(c.APIKey) < 24 {
		return nil, fmt.Errorf("API_KEY must be set and at least 24 characters long")
	}
	if c.MaxDelay < c.MinDelay {
		c.MaxDelay = c.MinDelay
	}
	if c.SendHourFrom < 0 || c.SendHourFrom > 23 || c.SendHourTo < 1 || c.SendHourTo > 24 {
		return nil, fmt.Errorf("SEND_HOUR_FROM must be 0-23 and SEND_HOUR_TO 1-24")
	}
	return c, nil
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(k))); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(k))); err == nil {
		return v
	}
	return def
}

// envDur accepts Go durations ("45s", "10m") or plain seconds ("45").
func envDur(k string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(k)), 64); err == nil {
		return v
	}
	return def
}

// envList reads a comma separated list; an explicitly empty value ("-") disables it.
func envList(k, def string) []string {
	v := env(k, def)
	if v == "-" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = normKeyword(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// normKeyword lowercases and strips punctuation/emoji so "STOP!!" == "stop".
func normKeyword(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		default:
			space = true
		}
	}
	return b.String()
}
