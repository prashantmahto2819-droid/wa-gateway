package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// SendRequest is what the website sends to /send and /queue.
type SendRequest struct {
	To       string            `json:"to"`                 // phone number, or a JID (...@g.us, ...@newsletter, ...@s.whatsapp.net)
	Text     string            `json:"text,omitempty"`     // message text, or caption for media
	Media    *Media            `json:"media,omitempty"`    // optional photo / video / document / audio
	Mentions []string          `json:"mentions,omitempty"` // numbers to @mention in a group (also write @number in the text)
	ReplyTo  *ReplyRef         `json:"reply_to,omitempty"` // quote an earlier message (support desk)
	Vars     map[string]string `json:"vars,omitempty"`     // fills {{name}} style placeholders
	Typing   *bool             `json:"typing,omitempty"`   // override the TYPING setting for this message

	Poll       *PollReq     `json:"poll,omitempty"`
	Location   *LocationReq `json:"location,omitempty"`
	Contact    *ContactReq  `json:"contact,omitempty"`
	Menu       *MenuReq     `json:"menu,omitempty"`
	MentionAll bool         `json:"mention_all,omitempty"` // groups: notify every member
	Background string       `json:"background,omitempty"`  // text Status background colour (#RRGGBB)
}

type PollReq struct {
	Name     string   `json:"name"`
	Options  []string `json:"options"`
	Multiple bool     `json:"multiple"`
}

type LocationReq struct {
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	Name    string  `json:"name,omitempty"`
	Address string  `json:"address,omitempty"`
}

type ContactReq struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

// MenuReq is sent as a numbered text list; the reply number maps back to the option id.
type MenuReq struct {
	Title   string    `json:"title,omitempty"`
	Footer  string    `json:"footer,omitempty"`
	Options []MenuOpt `json:"options"`
}

type MenuOpt struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// kind is the main content type of the message.
func (r *SendRequest) kind() string {
	switch {
	case r.Media != nil:
		return "media"
	case r.Poll != nil:
		return "poll"
	case r.Location != nil:
		return "location"
	case r.Contact != nil:
		return "contact"
	case r.Menu != nil:
		return "menu"
	}
	return "text"
}

type Media struct {
	Type     string `json:"type"`               // image | video | document | audio
	URL      string `json:"url,omitempty"`      // fetched into memory, sent, then forgotten
	Base64   string `json:"base64,omitempty"`   // alternative to url (data: prefix allowed)
	FileName string `json:"filename,omitempty"` // for documents
	Mimetype string `json:"mimetype,omitempty"` // detected automatically when empty
	MediaID  int64  `json:"media_id,omitempty"` // file from the media library

	data []byte // filled internally from the media library
}

type ReplyRef struct {
	ID     string `json:"id"`               // WhatsApp message id from the incoming webhook
	Sender string `json:"sender,omitempty"` // sender JID (needed in groups)
	Text   string `json:"text,omitempty"`   // short text shown inside the quote
}

func (r *SendRequest) validate() error {
	if strings.TrimSpace(r.To) == "" {
		return errors.New("'to' is required")
	}
	return r.validateContent()
}

// validateContent checks the message itself (used without "to" by templates/campaigns).
func (r *SendRequest) validateContent() error {
	kinds := 0
	for _, set := range []bool{r.Media != nil, r.Poll != nil, r.Location != nil, r.Contact != nil, r.Menu != nil} {
		if set {
			kinds++
		}
	}
	if kinds > 1 {
		return errors.New("send only one of media, poll, location, contact or menu per message")
	}
	if kinds == 0 && strings.TrimSpace(r.Text) == "" {
		return errors.New("'text' is required")
	}
	if len(r.Text) > 60000 {
		return errors.New("text is too long")
	}
	if r.Media != nil {
		switch r.Media.Type {
		case "image", "video", "document", "audio":
		default:
			return errors.New("media.type must be image, video, document or audio")
		}
		n := 0
		for _, set := range []bool{r.Media.URL != "", r.Media.Base64 != "", r.Media.MediaID > 0} {
			if set {
				n++
			}
		}
		if n != 1 {
			return errors.New("give exactly one of media.url, media.base64 or media.media_id")
		}
	}
	if r.Poll != nil {
		if strings.TrimSpace(r.Poll.Name) == "" || len(r.Poll.Options) < 2 || len(r.Poll.Options) > 12 {
			return errors.New("a poll needs a question and 2-12 options")
		}
		seen := map[string]bool{}
		for _, o := range r.Poll.Options {
			o = strings.TrimSpace(o)
			if o == "" || seen[o] {
				return errors.New("poll options must be non-empty and different")
			}
			seen[o] = true
		}
	}
	if r.Location != nil && (r.Location.Lat < -90 || r.Location.Lat > 90 || r.Location.Lng < -180 || r.Location.Lng > 180) {
		return errors.New("location lat/lng out of range")
	}
	if r.Contact != nil && (strings.TrimSpace(r.Contact.Name) == "" || len(nonDigits.ReplaceAllString(r.Contact.Phone, "")) < 8) {
		return errors.New("contact card needs a name and phone")
	}
	if r.Menu != nil {
		if len(r.Menu.Options) < 1 || len(r.Menu.Options) > 20 {
			return errors.New("a menu needs 1-20 options")
		}
		for i := range r.Menu.Options {
			if strings.TrimSpace(r.Menu.Options[i].Text) == "" {
				return errors.New("menu options need text")
			}
			if r.Menu.Options[i].ID == "" {
				r.Menu.Options[i].ID = strconv.Itoa(i + 1)
			}
		}
	}
	return nil
}

var nonDigits = regexp.MustCompile(`\D`)

// parseTarget turns "98765 43210", "+91-9876543210", "1203...@g.us" etc. into a JID.
func parseTarget(s, countryCode string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.JID{}, errors.New("empty recipient")
	}
	if s == "status" || s == "status@broadcast" {
		return types.StatusBroadcastJID, nil
	}
	if strings.Contains(s, "@") {
		jid, err := types.ParseJID(s)
		if err != nil {
			return types.JID{}, fmt.Errorf("invalid JID %q", s)
		}
		switch jid.Server {
		case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer, types.NewsletterServer:
		default:
			return types.JID{}, fmt.Errorf("unsupported address %q", s)
		}
		if jid.User == "" {
			return types.JID{}, fmt.Errorf("invalid JID %q", s)
		}
		return jid.ToNonAD(), nil
	}
	if strings.Contains(s, "whatsapp.com/") {
		return types.JID{}, errors.New("links are not accepted here; use the group/channel JID from /groups or /channels")
	}
	d := nonDigits.ReplaceAllString(s, "")
	d = strings.TrimPrefix(d, "00")
	if len(d) == 11 && strings.HasPrefix(d, "0") && countryCode != "" { // 09876543210 -> local trunk prefix
		d = d[1:]
	}
	if len(d) == 10 && countryCode != "" {
		d = countryCode + d
	}
	if len(d) < 8 || len(d) > 15 {
		return types.JID{}, fmt.Errorf("invalid phone number %q", s)
	}
	return types.NewJID(d, types.DefaultUserServer), nil
}

var (
	varRe   = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}`)
	spinRe  = regexp.MustCompile(`\{([^{}|]*\|[^{}]*)\}`)
	spinMax = 50
)

// renderText fills {{vars}} and then picks one option from each {a|b|c}
// group, so a campaign doesn't send the exact same words to everyone.
func renderText(t string, vars map[string]string, rnd *rand.Rand) string {
	if t == "" {
		return t
	}
	if len(vars) > 0 {
		low := make(map[string]string, len(vars))
		for k, v := range vars {
			low[strings.ToLower(k)] = v
		}
		t = varRe.ReplaceAllStringFunc(t, func(m string) string {
			k := strings.ToLower(varRe.FindStringSubmatch(m)[1])
			if v, ok := low[k]; ok {
				return v
			}
			return m
		})
	}
	for i := 0; i < spinMax && spinRe.MatchString(t); i++ {
		t = spinRe.ReplaceAllStringFunc(t, func(m string) string {
			opts := strings.Split(m[1:len(m)-1], "|")
			return opts[rnd.IntN(len(opts))]
		})
	}
	return t
}

// ---------- media loading (kept only in memory) ----------

func loadMedia(ctx context.Context, m *Media, maxBytes int64, allowPrivate bool) (data []byte, mimetype, filename string, err error) {
	if m.data != nil {
		data = m.data
	} else if m.Base64 != "" {
		raw := m.Base64
		if i := strings.Index(raw, ","); strings.HasPrefix(raw, "data:") && i > 0 {
			if mimetype == "" {
				mimetype = strings.TrimSuffix(strings.TrimPrefix(raw[:i], "data:"), ";base64")
			}
			raw = raw[i+1:]
		}
		if int64(base64.StdEncoding.DecodedLen(len(raw))) > maxBytes+3 {
			return nil, "", "", errors.New("media is too large")
		}
		data, err = base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, "", "", errors.New("media.base64 is not valid base64")
		}
	} else {
		data, mimetype, filename, err = fetchURL(ctx, m.URL, maxBytes, allowPrivate)
		if err != nil {
			return nil, "", "", err
		}
	}
	if len(data) == 0 {
		return nil, "", "", errors.New("media is empty")
	}
	if m.Mimetype != "" {
		mimetype = m.Mimetype
	}
	if mimetype == "" || mimetype == "application/octet-stream" {
		mimetype = http.DetectContentType(data)
	}
	if i := strings.Index(mimetype, ";"); i > 0 && !strings.HasPrefix(mimetype, "audio/ogg") {
		mimetype = strings.TrimSpace(mimetype[:i])
	}
	if m.FileName != "" {
		filename = m.FileName
	}
	return data, mimetype, filename, nil
}

func fetchURL(ctx context.Context, raw string, maxBytes int64, allowPrivate bool) ([]byte, string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", "", errors.New("media.url must be an http(s) link")
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if !allowPrivate {
		// Refuse to fetch from the server's own network (protects the VPS).
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, _ := net.SplitHostPort(address)
			if !publicIP(net.ParseIP(host)) {
				return errors.New("this address is not allowed (private or internal network)")
			}
			return nil
		}
	}
	client := &http.Client{
		Timeout:   90 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext, Proxy: nil, TLSHandshakeTimeout: 15 * time.Second},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "vivid-relay/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("could not download media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("could not download media: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, "", "", errors.New("media is too large")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("could not download media: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, "", "", errors.New("media is too large")
	}
	name := path.Base(u.Path)
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		name = params["filename"]
	}
	if name == "/" || name == "." {
		name = ""
	}
	return data, resp.Header.Get("Content-Type"), name, nil
}

// ---------- building the WhatsApp message ----------

func buildContext(req *SendRequest, chat types.JID, cc string) (*waE2E.ContextInfo, error) {
	var ci *waE2E.ContextInfo
	if len(req.Mentions) > 0 {
		ci = &waE2E.ContextInfo{}
		for _, m := range req.Mentions {
			j, err := parseTarget(m, cc)
			if err != nil {
				return nil, fmt.Errorf("mention: %w", err)
			}
			ci.MentionedJID = append(ci.MentionedJID, j.String())
		}
	}
	if req.ReplyTo != nil && req.ReplyTo.ID != "" {
		if ci == nil {
			ci = &waE2E.ContextInfo{}
		}
		ci.StanzaID = proto.String(req.ReplyTo.ID)
		participant := chat
		if req.ReplyTo.Sender != "" {
			p, err := types.ParseJID(req.ReplyTo.Sender)
			if err != nil {
				return nil, errors.New("reply_to.sender is not a valid JID")
			}
			participant = p.ToNonAD()
		}
		ci.Participant = proto.String(participant.String())
		ci.QuotedMessage = &waE2E.Message{Conversation: proto.String(req.ReplyTo.Text)}
	}
	return ci, nil
}

// buildMessage prepares the protobuf message, uploading media when needed.
// For channels the upload is unencrypted and the returned handle must be
// passed to SendMessage.
func buildMessage(ctx context.Context, cli *whatsmeow.Client, to types.JID, req *SendRequest, text string, cfg *Config, extraMentions []string) (*waE2E.Message, string, error) {
	ci, err := buildContext(req, to, cfg.DefaultCountryCode)
	if err != nil {
		return nil, "", err
	}
	if len(extraMentions) > 0 {
		if ci == nil {
			ci = &waE2E.ContextInfo{}
		}
		ci.MentionedJID = append(ci.MentionedJID, extraMentions...)
	}
	switch req.kind() {
	case "poll":
		opts := make([]string, len(req.Poll.Options))
		for i, o := range req.Poll.Options {
			opts[i] = strings.TrimSpace(o)
		}
		selectable := 1
		if req.Poll.Multiple {
			selectable = 0 // 0 = any number of options
		}
		msg := cli.BuildPollCreation(strings.TrimSpace(req.Poll.Name), opts, selectable)
		msg.PollCreationMessage.ContextInfo = ci
		return msg, "", nil
	case "location":
		return &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude: proto.Float64(req.Location.Lat), DegreesLongitude: proto.Float64(req.Location.Lng),
			Name: proto.String(req.Location.Name), Address: proto.String(req.Location.Address), ContextInfo: ci}}, "", nil
	case "contact":
		phone := nonDigits.ReplaceAllString(req.Contact.Phone, "")
		if len(phone) == 10 {
			phone = cfg.DefaultCountryCode + phone
		}
		name := strings.NewReplacer("\n", " ", "\r", " ", ";", " ").Replace(req.Contact.Name)
		vcard := fmt.Sprintf("BEGIN:VCARD\nVERSION:3.0\nFN:%s\nTEL;type=CELL;type=VOICE;waid=%s:+%s\nEND:VCARD", name, phone, phone)
		return &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String(name), Vcard: proto.String(vcard), ContextInfo: ci}}, "", nil
	case "menu":
		text = renderMenu(req.Menu, text)
	}
	if req.Media == nil {
		if to == types.StatusBroadcastJID {
			return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text),
				BackgroundArgb: proto.Uint32(argb(req.Background)), TextArgb: proto.Uint32(0xFFFFFFFF),
				Font: waE2E.ExtendedTextMessage_SYSTEM.Enum()}}, "", nil
		}
		if ci == nil {
			return &waE2E.Message{Conversation: proto.String(text)}, "", nil
		}
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text), ContextInfo: ci}}, "", nil
	}

	data, mimetype, filename, err := loadMedia(ctx, req.Media, cfg.MaxMediaBytes, cfg.AllowPrivateURLs)
	if err != nil {
		return nil, "", err
	}
	mediaType := map[string]whatsmeow.MediaType{
		"image": whatsmeow.MediaImage, "video": whatsmeow.MediaVideo,
		"document": whatsmeow.MediaDocument, "audio": whatsmeow.MediaAudio,
	}[req.Media.Type]

	newsletter := to.Server == types.NewsletterServer
	var up whatsmeow.UploadResponse
	if newsletter {
		up, err = cli.UploadNewsletter(ctx, data, mediaType)
	} else {
		up, err = cli.Upload(ctx, data, mediaType)
	}
	if err != nil {
		return nil, "", fmt.Errorf("media upload failed: %w", err)
	}
	data = nil // let the memory go right away

	var mediaKey, encSHA []byte
	if !newsletter {
		mediaKey, encSHA = up.MediaKey, up.FileEncSHA256
	}
	url, dp, length := proto.String(up.URL), proto.String(up.DirectPath), proto.Uint64(up.FileLength)
	var caption *string
	if text != "" {
		caption = proto.String(text)
	}
	msg := &waE2E.Message{}
	switch req.Media.Type {
	case "image":
		msg.ImageMessage = &waE2E.ImageMessage{Caption: caption, Mimetype: proto.String(mimetype), URL: url, DirectPath: dp,
			MediaKey: mediaKey, FileEncSHA256: encSHA, FileSHA256: up.FileSHA256, FileLength: length, ContextInfo: ci}
	case "video":
		msg.VideoMessage = &waE2E.VideoMessage{Caption: caption, Mimetype: proto.String(mimetype), URL: url, DirectPath: dp,
			MediaKey: mediaKey, FileEncSHA256: encSHA, FileSHA256: up.FileSHA256, FileLength: length, ContextInfo: ci}
	case "audio":
		msg.AudioMessage = &waE2E.AudioMessage{Mimetype: proto.String(mimetype), URL: url, DirectPath: dp,
			MediaKey: mediaKey, FileEncSHA256: encSHA, FileSHA256: up.FileSHA256, FileLength: length, ContextInfo: ci,
			PTT: proto.Bool(strings.HasPrefix(mimetype, "audio/ogg"))}
	case "document":
		if filename == "" {
			filename = "file"
		}
		msg.DocumentMessage = &waE2E.DocumentMessage{Caption: caption, Mimetype: proto.String(mimetype), URL: url, DirectPath: dp,
			MediaKey: mediaKey, FileEncSHA256: encSHA, FileSHA256: up.FileSHA256, FileLength: length, ContextInfo: ci,
			FileName: proto.String(filename), Title: proto.String(filename)}
	}
	return msg, up.Handle, nil
}

// typingDelay imitates a person typing: ~45ms per character, 1.5–8s.
func typingDelay(text string, rnd *rand.Rand) time.Duration {
	d := time.Duration(len([]rune(text))) * 45 * time.Millisecond
	d += time.Duration(rnd.IntN(1500)) * time.Millisecond
	return min(max(d, 1500*time.Millisecond), 8*time.Second)
}

var blockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"100.64.0.0/10", "198.18.0.0/15", "192.0.0.0/24", "64:ff9b::/96", "64:ff9b:1::/48", "2001:db8::/32", "240.0.0.0/4"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// publicIP refuses loopback, private, link-local, CGNAT/metadata and similar ranges.
func publicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// renderMenu turns a menu into a numbered list people answer by number.
func renderMenu(m *MenuReq, intro string) string {
	var b strings.Builder
	if t := strings.TrimSpace(m.Title); t != "" {
		b.WriteString("*" + t + "*\n")
	}
	if intro = strings.TrimSpace(intro); intro != "" {
		b.WriteString(intro + "\n")
	}
	b.WriteString("\n")
	for i, o := range m.Options {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(o.Text))
	}
	footer := strings.TrimSpace(m.Footer)
	if footer == "" {
		footer = "Reply with the number of your choice."
	}
	b.WriteString("\n_" + footer + "_")
	return b.String()
}

// argb converts "#RRGGBB" to WhatsApp's ARGB colour (default: dark teal).
func argb(hex string) uint32 {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if v, err := strconv.ParseUint(hex, 16, 32); err == nil && len(hex) == 6 {
		return 0xFF000000 | uint32(v)
	}
	return 0xFF0B4F4A
}
