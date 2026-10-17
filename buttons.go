package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// Interactive messages on a linked device (unofficial client), as of late 2026:
//
//   - Reply / URL / call / copy buttons: an unwrapped InteractiveMessage with a
//     NativeFlowMessage plus a <biz><interactive type="native_flow" v="1">
//     <native_flow name="mixed" v="9"/></interactive></biz> node. Renders on
//     Android, iOS and WhatsApp Web (not Windows desktop).
//   - List menus: the legacy ListMessage (SINGLE_SELECT); whatsmeow adds the
//     <biz><list/></biz> node itself. The newer single_select flow is blank on
//     iOS and Web, so we never use it.
//   - Both can be refused by WhatsApp (error 405) for some accounts, so every
//     button message carries a plain-text fallback (numbered menu).
//
// Rule from WhatsApp: reply buttons and CTA buttons can't be mixed; at most
// 3 reply buttons or 3 CTA buttons per message.

type ButtonReq struct {
	Type  string `json:"type"`            // reply | url | call | copy
	ID    string `json:"id,omitempty"`    // reply: value returned when tapped
	Text  string `json:"text"`            // label shown on the button
	URL   string `json:"url,omitempty"`   // url
	Phone string `json:"phone,omitempty"` // call
	Code  string `json:"code,omitempty"`  // copy
}

type ButtonsReq struct {
	Title   string      `json:"title,omitempty"`  // bold header line
	Footer  string      `json:"footer,omitempty"` // small grey line
	Buttons []ButtonReq `json:"buttons"`
}

type ListReq struct {
	Title      string        `json:"title,omitempty"`
	ButtonText string        `json:"button_text,omitempty"` // the button that opens the list
	Footer     string        `json:"footer,omitempty"`
	Sections   []ListSection `json:"sections"`
}

type ListSection struct {
	Title string    `json:"title,omitempty"`
	Rows  []ListRow `json:"rows"`
}

type ListRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

func (b *ButtonsReq) validate() error {
	if len(b.Buttons) == 0 || len(b.Buttons) > 3 {
		return errors.New("buttons: give 1-3 buttons")
	}
	reply, cta := 0, 0
	seen := map[string]bool{}
	for i := range b.Buttons {
		bt := &b.Buttons[i]
		bt.Text = strings.TrimSpace(bt.Text)
		if bt.Text == "" || len([]rune(bt.Text)) > 25 {
			return errors.New("each button needs text of up to 25 characters")
		}
		switch bt.Type {
		case "", "reply":
			bt.Type = "reply"
			reply++
			if bt.ID == "" {
				bt.ID = fmt.Sprintf("btn_%d", i+1)
			}
			if seen[bt.ID] {
				return errors.New("button ids must be different")
			}
			seen[bt.ID] = true
		case "url":
			cta++
			u, err := url.Parse(strings.TrimSpace(bt.URL))
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return errors.New("url button needs a valid http(s) link")
			}
			bt.URL = u.String()
		case "call":
			cta++
			d := nonDigits.ReplaceAllString(bt.Phone, "")
			if len(d) < 8 {
				return errors.New("call button needs a phone number")
			}
			bt.Phone = "+" + d
		case "copy":
			cta++
			if strings.TrimSpace(bt.Code) == "" {
				return errors.New("copy button needs a code")
			}
		default:
			return errors.New("button type must be reply, url, call or copy")
		}
	}
	if reply > 0 && cta > 0 {
		return errors.New("reply buttons can't be mixed with url/call/copy buttons in one message")
	}
	return nil
}

func (l *ListReq) validate() error {
	if len(l.Sections) == 0 || len(l.Sections) > 10 {
		return errors.New("list: give 1-10 sections")
	}
	total := 0
	seen := map[string]bool{}
	for si := range l.Sections {
		sec := &l.Sections[si]
		if len(sec.Rows) == 0 {
			return errors.New("every list section needs at least one row")
		}
		for ri := range sec.Rows {
			r := &sec.Rows[ri]
			r.Title = strings.TrimSpace(r.Title)
			if r.Title == "" || len([]rune(r.Title)) > 24 {
				return errors.New("each list row needs a title of up to 24 characters")
			}
			if r.ID == "" {
				r.ID = fmt.Sprintf("row_%d_%d", si+1, ri+1)
			}
			if seen[r.ID] {
				return errors.New("list row ids must be different")
			}
			seen[r.ID] = true
			total++
		}
	}
	if total > 10 {
		return errors.New("a list can have at most 10 rows in total")
	}
	if strings.TrimSpace(l.ButtonText) == "" {
		l.ButtonText = "Choose"
	}
	return nil
}

// menuOptions is what the reply-matching code needs (same as a numbered menu).
func (b *ButtonsReq) menuOptions() []MenuOpt {
	var out []MenuOpt
	for _, bt := range b.Buttons {
		if bt.Type == "reply" {
			out = append(out, MenuOpt{ID: bt.ID, Text: bt.Text})
		}
	}
	return out
}

func (l *ListReq) menuOptions() []MenuOpt {
	var out []MenuOpt
	for _, s := range l.Sections {
		for _, r := range s.Rows {
			out = append(out, MenuOpt{ID: r.ID, Text: r.Title})
		}
	}
	return out
}

// fallbackText renders buttons as plain text when WhatsApp refuses the interactive form.
func (b *ButtonsReq) fallbackText(body string) string {
	var sb strings.Builder
	if t := strings.TrimSpace(b.Title); t != "" {
		sb.WriteString("*" + t + "*\n")
	}
	if body = strings.TrimSpace(body); body != "" {
		sb.WriteString(body + "\n")
	}
	sb.WriteString("\n")
	n := 0
	for _, bt := range b.Buttons {
		switch bt.Type {
		case "reply":
			n++
			fmt.Fprintf(&sb, "%d. %s\n", n, bt.Text)
		case "url":
			fmt.Fprintf(&sb, "%s: %s\n", bt.Text, bt.URL)
		case "call":
			fmt.Fprintf(&sb, "%s: %s\n", bt.Text, bt.Phone)
		case "copy":
			fmt.Fprintf(&sb, "%s: %s\n", bt.Text, bt.Code)
		}
	}
	if n > 0 {
		sb.WriteString("\n_Reply with the number of your choice._")
	}
	if f := strings.TrimSpace(b.Footer); f != "" {
		sb.WriteString("\n_" + f + "_")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (l *ListReq) fallbackText(body string) string {
	m := &MenuReq{Title: l.Title, Footer: l.Footer, Options: l.menuOptions()}
	return renderMenu(m, body)
}

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// buildButtons makes the interactive message and the biz node it needs.
func buildButtons(b *ButtonsReq, body string, header *waE2E.InteractiveMessage_Header, ci *waE2E.ContextInfo) (*waE2E.Message, []waBinary.Node) {
	var btns []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
	for _, bt := range b.Buttons {
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
	if header == nil {
		header = &waE2E.InteractiveMessage_Header{HasMediaAttachment: proto.Bool(false)}
	}
	if t := strings.TrimSpace(b.Title); t != "" && header.Title == nil {
		header.Title = proto.String(t)
	}
	im := &waE2E.InteractiveMessage{
		Header:      header,
		Body:        &waE2E.InteractiveMessage_Body{Text: proto.String(strings.TrimSpace(body))},
		ContextInfo: ci,
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
			Buttons:           btns,
			MessageParamsJSON: proto.String(jsonStr(map[string]string{"from": "api", "templateId": uuid.NewString()})),
		}},
	}
	if f := strings.TrimSpace(b.Footer); f != "" {
		im.Footer = &waE2E.InteractiveMessage_Footer{Text: proto.String(f)}
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

// buildList makes the legacy list message that still renders everywhere.
func buildList(l *ListReq, body string, ci *waE2E.ContextInfo) *waE2E.Message {
	lm := &waE2E.ListMessage{
		Description: proto.String(strings.TrimSpace(body)),
		ButtonText:  proto.String(strings.TrimSpace(l.ButtonText)),
		ListType:    waE2E.ListMessage_SINGLE_SELECT.Enum(),
		ContextInfo: ci,
	}
	if t := strings.TrimSpace(l.Title); t != "" {
		lm.Title = proto.String(t)
	}
	if f := strings.TrimSpace(l.Footer); f != "" {
		lm.FooterText = proto.String(f)
	}
	for _, s := range l.Sections {
		sec := &waE2E.ListMessage_Section{}
		if t := strings.TrimSpace(s.Title); t != "" {
			sec.Title = proto.String(t)
		}
		for _, r := range s.Rows {
			row := &waE2E.ListMessage_Row{Title: proto.String(r.Title), RowID: proto.String(r.ID)}
			if d := strings.TrimSpace(r.Description); d != "" {
				row.Description = proto.String(d)
			}
			sec.Rows = append(sec.Rows, row)
		}
		lm.Sections = append(lm.Sections, sec)
	}
	return &waE2E.Message{ListMessage: lm}
}

// extractInteractiveReply decodes a tap on a button / list row.
func extractInteractiveReply(m *waE2E.Message) (id, text string, ok bool) {
	switch {
	case m.GetInteractiveResponseMessage() != nil:
		ir := m.GetInteractiveResponseMessage()
		if nf := ir.GetNativeFlowResponseMessage(); nf != nil {
			var p struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal([]byte(nf.GetParamsJSON()), &p)
			return p.ID, ir.GetBody().GetText(), true
		}
	case m.GetButtonsResponseMessage() != nil:
		b := m.GetButtonsResponseMessage()
		return b.GetSelectedButtonID(), b.GetSelectedDisplayText(), true
	case m.GetListResponseMessage() != nil:
		l := m.GetListResponseMessage()
		return l.GetSingleSelectReply().GetSelectedRowID(), l.GetTitle(), true
	case m.GetTemplateButtonReplyMessage() != nil:
		t := m.GetTemplateButtonReplyMessage()
		return t.GetSelectedID(), t.GetSelectedDisplayText(), true
	}
	return "", "", false
}
