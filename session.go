package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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
	StOutdated     = "outdated"     // WhatsApp wants a newer version: run ./update.sh --whatsmeow
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
	name    string
	m       *Manager
	ownerID atomic.Int64

	mu         sync.Mutex
	client     *whatsmeow.Client
	status     string
	qrCode     string
	pairCancel context.CancelFunc
	pairCli    *whatsmeow.Client // client of the running QR flow
	pairStart  time.Time
	lastErr    string
	banUntil   time.Time
	pushName   string

	sendMu     sync.Mutex // one WhatsApp send at a time per number
	lastSend   time.Time
	onlineStop *time.Timer
	onlineGen  int

	checkMu     sync.Mutex
	checkCount  int
	checkWindow time.Time

	lastHealth string

	stopQueue context.CancelFunc
	wake      chan struct{}
	rnd       *rand.Rand
	rndMu     sync.Mutex
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
	if changed && (st == StLoggedOut || st == StReplaced || st == StBanned) {
		go s.m.rebalance(s.name)
	}
}

func (s *Session) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Session) hook(event string, data any) {
	s.m.dispatch(s, event, data)
}

// owner is the user who owns this number (sessions from before accounts belong to the first admin).
func (s *Session) owner() int64 {
	if id := s.ownerID.Load(); id > 0 {
		return id
	}
	return firstAdminID(s.m.ctx, s.m.db)
}

// SessionSettings are per-number options editable in the dashboard.
type SessionSettings struct {
	RejectCalls      bool   `json:"reject_calls"`
	RejectMessage    string `json:"reject_message"`
	IgnoreGroups     bool   `json:"ignore_groups"`
	IgnoreChannels   bool   `json:"ignore_channels"`
	AutoSaveContacts bool   `json:"auto_save_contacts"`
	AlwaysOnline     bool   `json:"always_online"`
	TakeoverMinutes  int    `json:"takeover_minutes"`
}

func (s *Session) settings(ctx context.Context) SessionSettings {
	st := SessionSettings{IgnoreChannels: true, AutoSaveContacts: true, TakeoverMinutes: s.m.cfg.TakeoverDefault,
		RejectMessage: "Sorry, calls are not answered on this number. Please send a message."}
	var raw string
	if s.m.db.QueryRowContext(ctx, `SELECT settings FROM gw_sessions WHERE name=?`, s.name).Scan(&raw) == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	return st
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
	cli.InitialAutoReconnect = true               // keep retrying even if the very first connect fails
	cli.AutomaticMessageRerequestFromPhone = true // fixes most "waiting for this message"
	if p := s.row(s.m.ctx).proxy; p != "" {
		if err := cli.SetProxyAddress(p); err != nil {
			// Never fall back to the server's own IP silently.
			s.m.log.Error("invalid proxy, connection will fail until it is fixed", "session", s.name, "err", err)
			s.lastErr = "invalid proxy: " + err.Error() // callers hold s.mu
			cli.SetProxy(func(*http.Request) (*url.URL, error) { return nil, fmt.Errorf("invalid proxy configured: %w", err) })
		}
	}
	// Events from an old client (after reconnect / re-link) are ignored.
	cli.AddEventHandler(func(evt any) { s.onEvent(cli, evt) })
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
	old := s.client
	cli := s.newClient(s.m.container.NewDevice())
	ctx, cancel := context.WithTimeout(s.m.ctx, 4*time.Minute)
	qrChan, err := cli.GetQRChannel(ctx)
	if err != nil {
		cancel()
		s.mu.Unlock()
		return err
	}
	s.client, s.pairCancel, s.pairCli, s.pairStart, s.qrCode = cli, cancel, cli, time.Now(), ""
	s.mu.Unlock()
	if old != nil {
		old.Disconnect()
	}
	s.setStatus(StQR, "")

	if err := cli.Connect(); err != nil {
		s.failPairing(cli, err.Error())
		return err
	}
	go func() {
		for item := range qrChan {
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				s.mu.Lock()
				if s.pairCli == cli {
					s.qrCode = item.Code
				}
				s.mu.Unlock()
			case whatsmeow.QRChannelSuccess.Event:
				s.endPairing(cli)
				s.setStatus(StConnecting, "")
				return
			case whatsmeow.QRChannelTimeout.Event, whatsmeow.QRChannelEventError,
				whatsmeow.QRChannelErrUnexpectedEvent.Event, whatsmeow.QRChannelClientOutdated.Event:
				msg := item.Event
				if item.Error != nil {
					msg += ": " + item.Error.Error()
				}
				s.failPairing(cli, msg)
				return
			default:
				// e.g. scanned with an old phone app, passkey prompts: not final, keep waiting.
				s.m.log.Info("pairing notice", "session", s.name, "event", item.Event)
			}
		}
		s.failPairing(cli, "QR closed")
	}()
	return nil
}

// endPairing stops the QR flow of this client (no-op if a newer flow started).
func (s *Session) endPairing(cli *whatsmeow.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pairCli != cli {
		return
	}
	if s.pairCancel != nil {
		s.pairCancel()
	}
	s.pairCancel, s.pairCli, s.qrCode = nil, nil, ""
}

func (s *Session) failPairing(cli *whatsmeow.Client, msg string) {
	s.mu.Lock()
	current := s.pairCli == cli
	s.mu.Unlock()
	if !current {
		return
	}
	s.endPairing(cli)
	cli.Disconnect()
	s.mu.Lock()
	if s.client == cli && cli.Store.ID == nil {
		s.client = nil
	}
	s.mu.Unlock()
	s.setStatus(StQRExpired, msg)
}

// pairCode links with an 8-character code typed on the phone instead of scanning.
func (s *Session) pairCode(ctx context.Context, phone string) (string, error) {
	jid, err := parseTarget(phone, s.m.cfg.DefaultCountryCode)
	if err != nil || jid.Server != types.DefaultUserServer {
		return "", errors.New("invalid phone number")
	}
	// A QR flow only lives ~160s; start a fresh one so the code has time to be typed.
	s.mu.Lock()
	stale := s.pairCli != nil && time.Since(s.pairStart) > 45*time.Second
	cli := s.pairCli
	s.mu.Unlock()
	if stale {
		s.failPairing(cli, "restarting for pairing code")
	}
	if err := s.startPairing(); err != nil {
		return "", err
	}
	// PairPhone needs the socket open; wait until the first QR arrives.
	deadline := time.Now().Add(20 * time.Second)
	for {
		s.mu.Lock()
		cli, ready := s.pairCli, s.qrCode != ""
		s.mu.Unlock()
		if ready && cli != nil {
			return cli.PairPhone(ctx, jid.User, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
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
	}
	s.pairCancel, s.pairCli, s.qrCode = nil, nil, ""
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

func (s *Session) onEvent(from *whatsmeow.Client, evt any) {
	ctx := s.m.ctx
	s.mu.Lock()
	current := s.client == from
	s.mu.Unlock()
	if !current {
		return
	}
	switch e := evt.(type) {
	case *events.PairSuccess:
		_, _ = s.m.db.ExecContext(ctx, `UPDATE gw_sessions SET jid=?, paired_at=?, timelock_until=0 WHERE name=?`,
			e.ID.String(), time.Now().Unix(), s.name)
		s.hook("session.paired", map[string]any{"jid": e.ID.String(), "lid": e.LID.String(), "business_name": e.BusinessName, "platform": e.Platform})
	case *events.Connected:
		s.mu.Lock()
		s.lastErr = ""
		s.mu.Unlock()
		if cli := from; cli != nil {
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
		s.setStatus(StOutdated, "WhatsApp says this gateway version is outdated: update the gateway (docker compose pull / rebuild)")
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
	case *events.CallOffer:
		go s.onCall(e)
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
	s.mu.Lock()
	still := s.client == cli
	s.mu.Unlock()
	if !still {
		return // a manual reconnect / re-link replaced it meanwhile
	}
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
	st := s.settings(ctx)
	isChannel := info.Chat.Server == types.NewsletterServer
	if (info.IsGroup && st.IgnoreGroups) || (isChannel && st.IgnoreChannels) {
		return
	}

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
	isDM := !info.IsGroup && !isChannel
	fresh := time.Since(info.Timestamp) < 10*time.Minute // not an old backlog message

	// First message ever from this person? (checked before we remember them)
	firstTime := isDM && !info.IsFromMe && !isKnownContact(ctx, s.m.db, s.name, chat.String(), chatPN.String())

	// Anyone we have a 1:1 conversation with counts as a known contact
	// (sending to them is low-risk and allowed even during a timelock).
	if isDM {
		rememberContact(ctx, s.m.db, s.name, chat.String())
		if chatPN != chat {
			rememberContact(ctx, s.m.db, s.name, chatPN.String())
		}
		if alt := info.SenderAlt; !info.IsFromMe && !alt.IsEmpty() {
			rememberContact(ctx, s.m.db, s.name, alt.ToNonAD().String())
		}
	}
	if isDM && info.IsFromMe && fresh {
		// A person answered from the phone: keep the bot quiet in this chat for a while.
		if st.TakeoverMinutes > 0 {
			until := time.Now().Add(time.Duration(st.TakeoverMinutes) * time.Minute).Unix()
			_, _ = s.m.db.ExecContext(ctx, `INSERT INTO chat_state(session, chat, human_until) VALUES(?,?,?)
				ON CONFLICT(session, chat) DO UPDATE SET human_until=excluded.human_until`, s.name, chatPN.String(), until)
		}
	}
	if !info.IsFromMe {
		bumpStat(ctx, s.m.db, s.name, s.owner(), "received")
		if isDM && st.AutoSaveContacts {
			s.autoSaveContact(ctx, phoneOf(chatPN), info.PushName)
		}
	}

	// Poll votes arrive encrypted; decode them into option names.
	if e.Message.GetPollUpdateMessage() != nil {
		s.onPollVote(ctx, cli, e, sender)
		return
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
	text, _ := content["text"].(string)
	if text == "" {
		text, _ = content["caption"].(string)
	}
	var choice *MenuOpt
	if isDM && !info.IsFromMe {
		if content["type"] == "text" && text != "" {
			s.handleKeyword(ctx, chatPN, text)
		}
		choice = s.menuChoice(ctx, chatPN, text)
		if choice != nil {
			content["menu_choice"] = choice
		}
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

	if !info.IsFromMe && fresh && !e.IsEdit && content["type"] != "reaction" {
		go s.runAutoReplies(chatPN, info.IsGroup, firstTime, text, choice, info.PushName, info.ID, sender)
	}
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
