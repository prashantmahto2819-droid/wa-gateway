package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Session statuses shown to the website / dashboard.
const (
	StNew          = "new"          // created, never linked
	StQR           = "qr"           // waiting for QR scan / pair code
	StQRExpired    = "qr_expired"   // QR not scanned in time, ask again
	StConnecting   = "connecting"   // linked, connecting to WhatsApp
	StConnected    = "connected"    // ready to send and receive
	StDisconnected = "disconnected" // temporary network drop, reconnecting by itself
	StLoggedOut    = "logged_out"   // removed from phone's Linked Devices; link again
	StReplaced     = "replaced"     // same login opened elsewhere (two gateways running?)
	StBanned       = "temp_banned"  // WhatsApp temporary ban
)

var (
	errNotConnected  = errors.New("session is not connected to WhatsApp")
	errNotOnWA       = errors.New("this number is not on WhatsApp")
	errTimelocked    = errors.New("WhatsApp is temporarily blocking messages to new contacts (error 463); only people who already chatted with this number can be messaged right now")
	errDailyLimit    = errors.New("daily sending limit reached for this number")
	errNewLimit      = errors.New("daily limit for new contacts reached")
	errAlreadyLinked = errors.New("session is already linked; log out first")
)

type Session struct {
	name string
	m    *Manager

	mu         sync.Mutex
	client     *whatsmeow.Client
	status     string
	qrCode     string
	pairCancel context.CancelFunc
	lastErr    string
	banUntil   time.Time
	pushName   string

	sendMu     sync.Mutex // one WhatsApp send at a time per number
	lastSend   time.Time
	onlineStop *time.Timer

	checkMu     sync.Mutex
	checkCount  int
	checkWindow time.Time

	wake  chan struct{}
	rnd   *rand.Rand
	rndMu sync.Mutex
}

func newSession(m *Manager, name string) *Session {
	return &Session{
		name:   name,
		m:      m,
		status: StNew,
		wake:   make(chan struct{}, 1),
		rnd:    rand.New(rand.NewPCG(rand.Uint64(), uint64(time.Now().UnixNano()))),
	}
}

func (s *Session) randDur(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	s.rndMu.Lock()
	defer s.rndMu.Unlock()
	return lo + time.Duration(s.rnd.Int64N(int64(hi-lo)))
}

func (s *Session) setStatus(st, errMsg string) {
	s.mu.Lock()
	changed := s.status != st
	s.status = st
	if errMsg != "" {
		s.lastErr = errMsg
	}
	s.mu.Unlock()
	if changed {
		s.m.log.Info("session status", "session", s.name, "status", st)
		s.hook("session.status", map[string]any{"status": st, "error": errMsg})
	}
	if st == StConnected {
		s.poke()
	}
}

func (s *Session) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Session) hook(event string, data any) {
	s.m.hooks.send(s.webhookURL(), s.name, event, data)
}

func (s *Session) webhookURL() string {
	var u string
	_ = s.m.db.QueryRow(`SELECT webhook_url FROM gw_sessions WHERE name=?`, s.name).Scan(&u)
	if u != "" {
		return u
	}
	return s.m.cfg.WebhookURL
}

// ready returns the client only if it is linked and connected.
func (s *Session) ready() (*whatsmeow.Client, error) {
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()
	if cli == nil || !cli.IsLoggedIn() || !cli.IsConnected() {
		return nil, errNotConnected
	}
	return cli, nil
}

func (s *Session) newClient(dev *whatsmeowDevice) *whatsmeow.Client {
	cli := whatsmeow.NewClient(dev, s.m.waLog.Sub(s.name))
	cli.EnableAutoReconnect = true
	cli.AutomaticMessageRerequestFromPhone = true // fixes most "waiting for this message"
	cli.AddEventHandler(s.onEvent)
	return cli
}

// connectExisting reconnects a session that was linked before (startup / manual reconnect).
func (s *Session) connectExisting(ctx context.Context, jid types.JID) error {
	dev, err := s.m.container.GetDevice(ctx, jid)
	if err != nil {
		return err
	}
	if dev == nil {
		s.forgetLogin(ctx)
		s.setStatus(StLoggedOut, "login data not found, please link again")
		return nil
	}
	s.mu.Lock()
	if s.client != nil {
		s.client.Disconnect()
	}
	cli := s.newClient(dev)
	s.client = cli
	s.mu.Unlock()
	s.setStatus(StConnecting, "")
	if err := cli.Connect(); err != nil {
		s.setStatus(StDisconnected, err.Error())
		return err
	}
	return nil
}

// startPairing begins QR linking. Calling it again while a QR is active is harmless.
func (s *Session) startPairing() error {
	s.mu.Lock()
	if s.client != nil && s.client.Store.ID != nil {
		s.mu.Unlock()
		return errAlreadyLinked
	}
	if s.pairCancel != nil { // already showing a QR
		s.mu.Unlock()
		return nil
	}
	if s.client != nil {
		s.client.Disconnect()
	}
	cli := s.newClient(s.m.container.NewDevice())
	ctx, cancel := context.WithTimeout(s.m.ctx, 4*time.Minute)
	qrChan, err := cli.GetQRChannel(ctx)
	if err != nil {
		cancel()
		s.mu.Unlock()
		return err
	}
	s.client, s.pairCancel, s.qrCode = cli, cancel, ""
	s.mu.Unlock()
	s.setStatus(StQR, "")

	if err := cli.Connect(); err != nil {
		s.endPairing(cli)
		s.setStatus(StQRExpired, err.Error())
		return err
	}
	go func() {
		defer s.endPairing(cli)
		for item := range qrChan {
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				s.mu.Lock()
				s.qrCode = item.Code
				s.mu.Unlock()
			case whatsmeow.QRChannelSuccess.Event:
				s.setStatus(StConnecting, "")
				return
			default:
				msg := item.Event
				if item.Error != nil {
					msg += ": " + item.Error.Error()
				}
				cli.Disconnect()
				s.mu.Lock()
				if s.client == cli {
					s.client = nil
				}
				s.mu.Unlock()
				s.setStatus(StQRExpired, msg)
				return
			}
		}
	}()
	return nil
}

func (s *Session) endPairing(cli *whatsmeow.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == cli && s.pairCancel != nil {
		s.pairCancel()
		s.pairCancel = nil
	}
	s.qrCode = ""
}

// pairCode links with an 8-character code typed on the phone instead of scanning.
func (s *Session) pairCode(ctx context.Context, phone string) (string, error) {
	digits := nonDigits.ReplaceAllString(phone, "")
	if len(digits) == 10 {
		digits = s.m.cfg.DefaultCountryCode + digits
	}
	if len(digits) < 8 {
		return "", errors.New("invalid phone number")
	}
	if err := s.startPairing(); err != nil {
		return "", err
	}
	// PairPhone needs the socket open; wait until the first QR arrives.
	deadline := time.Now().Add(20 * time.Second)
	for {
		s.mu.Lock()
		cli, ready := s.client, s.qrCode != ""
		s.mu.Unlock()
		if ready && cli != nil {
			return cli.PairPhone(ctx, digits, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
		}
		if time.Now().After(deadline) {
			return "", errors.New("could not reach WhatsApp to request a pairing code, try again")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (s *Session) forgetLogin(ctx context.Context) {
	_, _ = s.m.db.ExecContext(ctx, `UPDATE gw_sessions SET jid='', paired_at=0, timelock_until=0 WHERE name=?`, s.name)
}

// logout unlinks the device from the phone. Safe to call more than once.
func (s *Session) logout(ctx context.Context) {
	s.mu.Lock()
	cli := s.client
	s.client = nil
	if s.pairCancel != nil {
		s.pairCancel()
		s.pairCancel = nil
	}
	s.mu.Unlock()
	if cli != nil {
		if cli.Store.ID != nil {
			if err := cli.Logout(ctx); err != nil {
				// Not connected: remove the local keys anyway.
				_ = cli.Store.Delete(ctx)
			}
		}
		cli.Disconnect()
	}
	s.forgetLogin(ctx)
	s.setStatus(StLoggedOut, "")
}

func (s *Session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pairCancel != nil {
		s.pairCancel()
		s.pairCancel = nil
	}
	if s.client != nil {
		s.client.Disconnect()
	}
}

// ---------- WhatsApp events ----------

func (s *Session) onEvent(evt any) {
	ctx := s.m.ctx
	switch e := evt.(type) {
	case *events.PairSuccess:
		_, _ = s.m.db.ExecContext(ctx, `UPDATE gw_sessions SET jid=?, paired_at=?, timelock_until=0 WHERE name=?`,
			e.ID.String(), time.Now().Unix(), s.name)
		s.hook("session.paired", map[string]any{"jid": e.ID.String(), "lid": e.LID.String(), "business_name": e.BusinessName, "platform": e.Platform})
	case *events.Connected:
		s.mu.Lock()
		s.lastErr = ""
		cli := s.client
		s.mu.Unlock()
		if cli != nil {
			// "unavailable" keeps notifications ringing on the owner's phone.
			_ = cli.SendPresence(ctx, types.PresenceUnavailable)
			if cli.Store.PushName != "" {
				s.mu.Lock()
				s.pushName = cli.Store.PushName
				s.mu.Unlock()
			}
		}
		s.setStatus(StConnected, "")
	case *events.Disconnected:
		s.mu.Lock()
		st := s.status
		s.mu.Unlock()
		if st == StConnected || st == StConnecting {
			s.setStatus(StDisconnected, "")
		}
	case *events.KeepAliveTimeout:
		if e.ErrorCount >= 4 {
			go s.kick()
		}
	case *events.LoggedOut:
		s.mu.Lock()
		s.client = nil
		s.mu.Unlock()
		s.forgetLogin(ctx)
		s.setStatus(StLoggedOut, fmt.Sprintf("logged out by WhatsApp/phone (%v)", e.Reason))
	case *events.StreamReplaced:
		s.setStatus(StReplaced, "this login was opened somewhere else; make sure only one gateway runs")
	case *events.TemporaryBan:
		s.mu.Lock()
		s.banUntil = time.Now().Add(e.Expire)
		s.mu.Unlock()
		s.setStatus(StBanned, e.String())
	case *events.ConnectFailure:
		s.setStatus(StDisconnected, fmt.Sprintf("connect failure: %v %s", e.Reason, e.Message))
	case *events.ClientOutdated:
		s.setStatus(StDisconnected, "WhatsApp says this gateway version is outdated: update the gateway (docker compose pull / rebuild)")
	case *events.NotifyAccountReachoutTimelock:
		until := int64(0)
		if e.IsActive {
			until = time.Now().Add(s.m.cfg.TimelockPause).Unix()
			if t := e.TimeEnforcementEnds.Time; t.After(time.Now()) {
				until = t.Unix()
			}
		}
		_, _ = s.m.db.ExecContext(ctx, `UPDATE gw_sessions SET timelock_until=? WHERE name=?`, until, s.name)
		s.hook("session.timelock", map[string]any{"active": e.IsActive, "until": until, "type": e.EnforcementType})
		s.poke()
	case *events.Message:
		s.onMessage(ctx, e)
	case *events.Receipt:
		s.onReceipt(e)
	}
}

// kick forces a fresh connection when keep-alives keep failing (zombie socket).
func (s *Session) kick() {
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()
	if cli == nil || cli.Store.ID == nil {
		return
	}
	s.m.log.Warn("keep-alive failing, reconnecting", "session", s.name)
	cli.Disconnect()
	time.Sleep(2 * time.Second)
	if err := cli.Connect(); err != nil {
		s.setStatus(StDisconnected, err.Error())
	}
}

// resolvePN turns a hidden @lid address into the phone-number address when known.
func (s *Session) resolvePN(ctx context.Context, cli *whatsmeow.Client, jid, alt types.JID) types.JID {
	jid = jid.ToNonAD()
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if alt.Server == types.DefaultUserServer {
		return alt.ToNonAD()
	}
	if cli != nil {
		if pn, err := cli.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return jid
}

func phoneOf(j types.JID) string {
	if j.Server == types.DefaultUserServer {
		return j.User
	}
	return ""
}

func chatType(j types.JID) string {
	switch j.Server {
	case types.GroupServer:
		return "group"
	case types.NewsletterServer:
		return "channel"
	default:
		return "user"
	}
}

func (s *Session) onMessage(ctx context.Context, e *events.Message) {
	info := e.Info
	if info.Chat.Server == types.BroadcastServer { // statuses and broadcast lists
		return
	}
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()

	sender := s.resolvePN(ctx, cli, info.Sender, info.SenderAlt)
	chat := info.Chat.ToNonAD()
	chatPN := chat
	if chat.Server == types.HiddenUserServer {
		alt := info.SenderAlt
		if info.IsFromMe {
			alt = info.RecipientAlt
		}
		chatPN = s.resolvePN(ctx, cli, chat, alt)
	}

	// Anyone we have a 1:1 conversation with counts as a known contact
	// (sending to them is low-risk and allowed even during a timelock).
	if !info.IsGroup && chat.Server != types.NewsletterServer {
		rememberContact(ctx, s.m.db, s.name, chat.String())
		if chatPN != chat {
			rememberContact(ctx, s.m.db, s.name, chatPN.String())
		}
		if alt := info.SenderAlt; !info.IsFromMe && !alt.IsEmpty() {
			rememberContact(ctx, s.m.db, s.name, alt.ToNonAD().String())
		}
	}

	if info.IsFromMe && !s.m.cfg.WebhookIncludeSelf {
		return
	}
	content := extractContent(e.Message)
	if content == nil {
		return
	}
	if content["type"] == "revoke" {
		s.hook("message.deleted", map[string]any{"id": content["id"], "chat": chat.String(), "chat_phone": phoneOf(chatPN), "from_me": info.IsFromMe})
		return
	}
	data := map[string]any{
		"id":         info.ID,
		"chat":       chat.String(),
		"chat_phone": phoneOf(chatPN),
		"chat_type":  chatType(chat),
		"from":       sender.String(),
		"from_phone": phoneOf(sender),
		"push_name":  info.PushName,
		"from_me":    info.IsFromMe,
		"timestamp":  info.Timestamp.Unix(),
		"is_edit":    e.IsEdit,
	}
	for k, v := range content {
		data[k] = v
	}
	s.hook("message", data)
}

func (s *Session) onReceipt(e *events.Receipt) {
	var t string
	switch e.Type {
	case types.ReceiptTypeDelivered:
		t = "delivered"
	case types.ReceiptTypeRead:
		t = "read"
	case types.ReceiptTypePlayed:
		t = "played"
	default:
		return
	}
	if e.IsFromMe { // our own other devices reading things
		return
	}
	s.hook("message.receipt", map[string]any{
		"ids": e.MessageIDs, "status": t, "chat": e.Chat.ToNonAD().String(),
		"from": e.Sender.ToNonAD().String(), "timestamp": e.Timestamp.Unix(),
	})
}

// extractContent describes a message without downloading any media.
func extractContent(m *waE2E.Message) map[string]any {
	if m == nil {
		return nil
	}
	out := map[string]any{}
	ctxInfo := func(ci *waE2E.ContextInfo) {
		if ci != nil && ci.GetStanzaID() != "" {
			out["quoted_id"] = ci.GetStanzaID()
		}
	}
	switch {
	case m.Conversation != nil:
		out["type"], out["text"] = "text", m.GetConversation()
	case m.ExtendedTextMessage != nil:
		x := m.ExtendedTextMessage
		out["type"], out["text"] = "text", x.GetText()
		ctxInfo(x.ContextInfo)
	case m.ImageMessage != nil:
		x := m.ImageMessage
		out["type"], out["caption"], out["mimetype"], out["file_size"] = "image", x.GetCaption(), x.GetMimetype(), x.GetFileLength()
		ctxInfo(x.ContextInfo)
	case m.VideoMessage != nil:
		x := m.VideoMessage
		out["type"], out["caption"], out["mimetype"], out["file_size"] = "video", x.GetCaption(), x.GetMimetype(), x.GetFileLength()
		ctxInfo(x.ContextInfo)
	case m.AudioMessage != nil:
		x := m.AudioMessage
		out["type"] = "audio"
		if x.GetPTT() {
			out["type"] = "voice"
		}
		out["mimetype"], out["file_size"], out["seconds"] = x.GetMimetype(), x.GetFileLength(), x.GetSeconds()
		ctxInfo(x.ContextInfo)
	case m.DocumentMessage != nil:
		x := m.DocumentMessage
		out["type"], out["caption"], out["mimetype"], out["file_size"], out["file_name"] = "document", x.GetCaption(), x.GetMimetype(), x.GetFileLength(), x.GetFileName()
		ctxInfo(x.ContextInfo)
	case m.DocumentWithCaptionMessage != nil && m.DocumentWithCaptionMessage.GetMessage().GetDocumentMessage() != nil:
		x := m.DocumentWithCaptionMessage.GetMessage().GetDocumentMessage()
		out["type"], out["caption"], out["mimetype"], out["file_size"], out["file_name"] = "document", x.GetCaption(), x.GetMimetype(), x.GetFileLength(), x.GetFileName()
		ctxInfo(x.ContextInfo)
	case m.StickerMessage != nil:
		out["type"] = "sticker"
	case m.LocationMessage != nil:
		x := m.LocationMessage
		out["type"], out["latitude"], out["longitude"], out["text"] = "location", x.GetDegreesLatitude(), x.GetDegreesLongitude(), strings.TrimSpace(x.GetName()+" "+x.GetAddress())
	case m.LiveLocationMessage != nil:
		x := m.LiveLocationMessage
		out["type"], out["latitude"], out["longitude"] = "location", x.GetDegreesLatitude(), x.GetDegreesLongitude()
	case m.ContactMessage != nil:
		out["type"], out["text"], out["vcard"] = "contact", m.ContactMessage.GetDisplayName(), m.ContactMessage.GetVcard()
	case m.ReactionMessage != nil:
		out["type"], out["text"], out["quoted_id"] = "reaction", m.ReactionMessage.GetText(), m.ReactionMessage.GetKey().GetID()
	case m.ButtonsResponseMessage != nil:
		out["type"], out["text"], out["button_id"] = "button_reply", m.ButtonsResponseMessage.GetSelectedDisplayText(), m.ButtonsResponseMessage.GetSelectedButtonID()
	case m.ListResponseMessage != nil:
		out["type"], out["text"], out["button_id"] = "list_reply", m.ListResponseMessage.GetTitle(), m.ListResponseMessage.GetSingleSelectReply().GetSelectedRowID()
	case m.TemplateButtonReplyMessage != nil:
		out["type"], out["text"], out["button_id"] = "button_reply", m.TemplateButtonReplyMessage.GetSelectedDisplayText(), m.TemplateButtonReplyMessage.GetSelectedID()
	case m.PollCreationMessage != nil || m.PollCreationMessageV3 != nil:
		p := m.PollCreationMessage
		if p == nil {
			p = m.PollCreationMessageV3
		}
		out["type"], out["text"] = "poll", p.GetName()
	case m.ProtocolMessage != nil:
		if m.ProtocolMessage.GetType() == waE2E.ProtocolMessage_REVOKE {
			return map[string]any{"type": "revoke", "id": m.ProtocolMessage.GetKey().GetID()}
		}
		return nil // edits arrive as normal messages with is_edit; other protocol messages are internal
	default:
		return nil // reactions to polls, key distribution and other internal messages
	}
	return out
}
