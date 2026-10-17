package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Carousel: a horizontal set of cards (image + title + text + buttons),
// sent as an InteractiveMessage with a CarouselMessage, the same shape
// Evolution API 2.4 / evolution-go use (Oct 2026): no ViewOnce wrapper, root
// header without media, empty ContextInfo (iOS), MessageVersion 1 on the
// carousel only, and the native_flow "mixed" biz node. Verified renders on
// Android and WhatsApp Web; on iOS the cards show but buttons may not be
// tappable, so each card's text also says "reply N". If WhatsApp refuses
// the carousel, the gateway sends each card as a normal image instead.

type CarouselCard struct {
	Title   string      `json:"title,omitempty"`
	Body    string      `json:"body"`
	Footer  string      `json:"footer,omitempty"`
	Media   *Media      `json:"media,omitempty"` // image (or video) shown on the card
	Buttons []ButtonReq `json:"buttons,omitempty"`
}

type CarouselReq struct {
	Cards []CarouselCard `json:"cards"`
}

func (c *CarouselReq) validate() error {
	if len(c.Cards) < 2 || len(c.Cards) > 10 {
		return errors.New("carousel: give 2-10 cards (for a single card use buttons with a media header)")
	}
	for i := range c.Cards {
		card := &c.Cards[i]
		card.Body = strings.TrimSpace(card.Body)
		if card.Body == "" && strings.TrimSpace(card.Title) == "" {
			return fmt.Errorf("card %d needs a title or body", i+1)
		}
		if card.Media != nil {
			if card.Media.Type != "image" && card.Media.Type != "video" {
				return fmt.Errorf("card %d: media must be an image or video", i+1)
			}
			n := 0
			for _, set := range []bool{card.Media.URL != "", card.Media.Base64 != "", card.Media.MediaID > 0} {
				if set {
					n++
				}
			}
			if n != 1 {
				return fmt.Errorf("card %d: give exactly one of media.url, media.base64 or media.media_id", i+1)
			}
		}
		if len(card.Buttons) > 0 {
			b := &ButtonsReq{Buttons: card.Buttons}
			if err := b.validate(); err != nil {
				return fmt.Errorf("card %d: %w", i+1, err)
			}
			card.Buttons = b.Buttons
		}
	}
	return nil
}

// menuOptions collects reply buttons from all cards so taps can be matched.
func (c *CarouselReq) menuOptions() []MenuOpt {
	var out []MenuOpt
	for _, card := range c.Cards {
		for _, bt := range card.Buttons {
			if bt.Type == "reply" {
				out = append(out, MenuOpt{ID: bt.ID, Text: bt.Text})
			}
		}
	}
	return out
}

// uploadCardMedia uploads every card's media and returns header protos (nil where no media).
func uploadCardMedia(ctx context.Context, cli *whatsmeow.Client, c *CarouselReq, cfg *Config) ([]*waE2E.InteractiveMessage_Header, [][]byte, error) {
	headers := make([]*waE2E.InteractiveMessage_Header, len(c.Cards))
	raws := make([][]byte, len(c.Cards))
	for i, card := range c.Cards {
		if card.Media == nil {
			headers[i] = &waE2E.InteractiveMessage_Header{Title: proto.String(strings.TrimSpace(card.Title)), HasMediaAttachment: proto.Bool(false)}
			continue
		}
		data, mimetype, _, err := loadMedia(ctx, card.Media, cfg.MaxMediaBytes, cfg.AllowPrivateURLs)
		if err != nil {
			return nil, nil, fmt.Errorf("card %d: %w", i+1, err)
		}
		mt := whatsmeow.MediaImage
		if card.Media.Type == "video" {
			mt = whatsmeow.MediaVideo
		}
		up, err := cli.Upload(ctx, data, mt)
		if err != nil {
			return nil, nil, fmt.Errorf("card %d: media upload failed: %w", i+1, err)
		}
		h := &waE2E.InteractiveMessage_Header{Title: proto.String(strings.TrimSpace(card.Title)), HasMediaAttachment: proto.Bool(true)}
		if card.Media.Type == "video" {
			h.Media = &waE2E.InteractiveMessage_Header_VideoMessage{VideoMessage: &waE2E.VideoMessage{Mimetype: proto.String(mimetype), URL: proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength)}}
		} else {
			thumb := thumbnail(data)
			h.Media = &waE2E.InteractiveMessage_Header_ImageMessage{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String(mimetype), URL: proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
				JPEGThumbnail: thumb}}
		}
		headers[i] = h
		raws[i] = data
	}
	return headers, raws, nil
}

// buildCarousel assembles the interactive carousel and its biz node.
func buildCarousel(c *CarouselReq, headers []*waE2E.InteractiveMessage_Header, body string, ci *waE2E.ContextInfo) (*waE2E.Message, []waBinary.Node) {
	cards := make([]*waE2E.InteractiveMessage, 0, len(c.Cards))
	for i, card := range c.Cards {
		bodyText := card.Body
		if n := len(card.Buttons); n > 0 && card.Buttons[0].Type == "reply" {
			// iPhones show the cards but may not let people tap, so the text
			// also tells them what to type.
			var hints []string
			for _, bt := range card.Buttons {
				hints = append(hints, fmt.Sprintf("%q", bt.Text))
			}
			bodyText = strings.TrimSpace(bodyText + "\n\n" + "Reply " + strings.Join(hints, " / "))
		}
		cm := &waE2E.InteractiveMessage{Header: headers[i], Body: &waE2E.InteractiveMessage_Body{Text: proto.String(bodyText)}}
		if f := strings.TrimSpace(card.Footer); f != "" {
			cm.Footer = &waE2E.InteractiveMessage_Footer{Text: proto.String(f)}
		}
		var btns []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
		for _, bt := range card.Buttons {
			var name, params string
			switch bt.Type {
			case "reply":
				name, params = "quick_reply", jsonStr(map[string]string{"display_text": bt.Text, "id": bt.ID})
			case "url":
				name, params = "cta_url", jsonStr(map[string]any{"display_text": bt.Text, "url": bt.URL, "merchant_url": bt.URL})
			case "call":
				name, params = "cta_call", jsonStr(map[string]string{"display_text": bt.Text, "phone_number": bt.Phone})
			case "copy":
				name, params = "cta_copy", jsonStr(map[string]string{"display_text": bt.Text, "copy_code": bt.Code})
			}
			btns = append(btns, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{Name: proto.String(name), ButtonParamsJSON: proto.String(params)})
		}
		// Per card: no MessageVersion and plain "{}" params (iOS shows an
		// "incompatible" notice otherwise).
		cm.InteractiveMessage = &waE2E.InteractiveMessage_NativeFlowMessage_{NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
			Buttons: btns, MessageParamsJSON: proto.String("{}")}}
		cards = append(cards, cm)
	}
	if ci == nil {
		ci = &waE2E.ContextInfo{} // iOS needs the field present
	}
	im := &waE2E.InteractiveMessage{
		Header:      &waE2E.InteractiveMessage_Header{HasMediaAttachment: proto.Bool(false)},
		Body:        &waE2E.InteractiveMessage_Body{Text: proto.String(strings.TrimSpace(body))},
		ContextInfo: ci,
		InteractiveMessage: &waE2E.InteractiveMessage_CarouselMessage_{CarouselMessage: &waE2E.InteractiveMessage_CarouselMessage{
			Cards: cards, MessageVersion: proto.Int32(1)}},
	}
	biz := []waBinary.Node{{
		Tag: "biz",
		Content: []waBinary.Node{{
			Tag: "interactive", Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
			Content: []waBinary.Node{{Tag: "native_flow", Attrs: waBinary.Attrs{"v": "9", "name": "mixed"}}},
		}},
	}}
	return &waE2E.Message{InteractiveMessage: im}, biz
}

// sendCarouselFallback sends the cards one by one as plain image messages.
func (s *Session) sendCarouselFallback(ctx context.Context, cli *whatsmeow.Client, to types.JID, c *CarouselReq, headers []*waE2E.InteractiveMessage_Header, intro string) (whatsmeow.SendResponse, error) {
	var last whatsmeow.SendResponse
	if intro = strings.TrimSpace(intro); intro != "" {
		r, err := cli.SendMessage(ctx, to, &waE2E.Message{Conversation: proto.String(intro)})
		if err != nil {
			return r, err
		}
		last = r
	}
	for i, card := range c.Cards {
		var sb strings.Builder
		if t := strings.TrimSpace(card.Title); t != "" {
			fmt.Fprintf(&sb, "*%d. %s*\n", i+1, t)
		} else {
			fmt.Fprintf(&sb, "*%d.*\n", i+1)
		}
		if card.Body != "" {
			sb.WriteString(card.Body + "\n")
		}
		if len(card.Buttons) > 0 {
			sb.WriteString("\n" + (&ButtonsReq{Buttons: card.Buttons}).fallbackText("")[1:])
		}
		caption := strings.TrimSpace(sb.String())
		var msg *waE2E.Message
		if h := headers[i]; h != nil && h.GetHasMediaAttachment() {
			if img := h.GetImageMessage(); img != nil {
				im := proto.Clone(img).(*waE2E.ImageMessage)
				im.Caption = proto.String(caption)
				msg = &waE2E.Message{ImageMessage: im}
			} else if vid := h.GetVideoMessage(); vid != nil {
				vm := proto.Clone(vid).(*waE2E.VideoMessage)
				vm.Caption = proto.String(caption)
				msg = &waE2E.Message{VideoMessage: vm}
			}
		}
		if msg == nil {
			msg = &waE2E.Message{Conversation: proto.String(caption)}
		}
		r, err := cli.SendMessage(ctx, to, msg)
		if err != nil {
			return r, err
		}
		last = r
		if i < len(c.Cards)-1 {
			if err := sleepCtx(ctx, s.randDur(1200e6, 2500e6)); err != nil {
				return last, err
			}
		}
	}
	return last, nil
}
