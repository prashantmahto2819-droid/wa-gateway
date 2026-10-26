package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// ======================= Chatbot flows =======================
//
// A flow is a small graph (ported from BizCRT's flow builder): Start →
// messages, questions that save an answer into a variable, conditions,
// tags, an API call, hand-over to a person, End. Nodes are joined by
// edges whose "port" is next / a button id / else / yes / no / ok / fail.
// A chat is in at most one flow at a time; the position and the collected
// variables are kept in flow_state so a reply hours later still continues.
// Flows run before auto-replies; when a flow handles a message the
// auto-replies stay quiet.

type FlowNode struct {
	ID   string         `json:"id"`
	Type string         `json:"type"` // start, message, question, condition, tag, api, assign, end
	Data map[string]any `json:"data"`
}

type FlowEdge struct {
	Source string `json:"source"`
	Port   string `json:"port"` // "" = next
	Target string `json:"target"`
}

type FlowGraph struct {
	Nodes []FlowNode `json:"nodes"`
	Edges []FlowEdge `json:"edges"`
}

type Flow struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Trigger    string    `json:"trigger"` // keyword, contains, welcome, any, manual
	Keywords   []string  `json:"keywords"`
	Session    string    `json:"session"`
	Graph      FlowGraph `json:"graph"`
	Active     bool      `json:"active"`
	TestMode   bool      `json:"test_mode"`   // only test_phones can trigger it
	TestPhones []string  `json:"test_phones"` //
	Priority   int       `json:"priority"`
	Runs       int       `json:"runs"`
	Problems   []string  `json:"problems"`
	CreatedAt  int64     `json:"created_at"`
	UpdatedAt  int64     `json:"updated_at"`
}

const flowCols = `id, name, trigger_type, keywords, session, graph, active, test_mode, test_phones, priority, runs, created_at, updated_at`

func scanFlow(row interface{ Scan(...any) error }) (*Flow, error) {
	var f Flow
	var kw, graph, tp string
	var act, tm int
	if err := row.Scan(&f.ID, &f.Name, &f.Trigger, &kw, &f.Session, &graph, &act, &tm, &tp, &f.Priority, &f.Runs, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, err
	}
	f.Keywords = fromJSON[[]string](kw)
	f.TestPhones = fromJSON[[]string](tp)
	f.Graph = fromJSON[FlowGraph](graph)
	if f.Keywords == nil {
		f.Keywords = []string{}
	}
	if f.TestPhones == nil {
		f.TestPhones = []string{}
	}
	if f.Graph.Nodes == nil {
		f.Graph.Nodes = []FlowNode{}
	}
	if f.Graph.Edges == nil {
		f.Graph.Edges = []FlowEdge{}
	}
	f.Active, f.TestMode = act == 1, tm == 1
	return &f, nil
}

// ---------- graph helpers (same semantics as BizCRT flow-core) ----------

func (g *FlowGraph) node(id string) *FlowNode {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

func (g *FlowGraph) start() *FlowNode {
	for i := range g.Nodes {
		if g.Nodes[i].Type == "start" {
			return &g.Nodes[i]
		}
	}
	return nil
}

func (g *FlowGraph) follow(from, port string) *FlowNode {
	if port == "" {
		port = "next"
	}
	for _, e := range g.Edges {
		p := e.Port
		if p == "" {
			p = "next"
		}
		if e.Source == from && p == port {
			return g.node(e.Target)
		}
	}
	return nil
}

func (g *FlowGraph) hasPort(from, port string) bool {
	for _, e := range g.Edges {
		if e.Source == from && e.Port == port {
			return true
		}
	}
	return false
}

// truncRunes keeps at most n characters (WhatsApp counts characters, not bytes).
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	switch v := m[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func sub(m map[string]any, k string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[k].(map[string]any)
	return v
}

func list(m map[string]any, k string) []map[string]any {
	if m == nil {
		return nil
	}
	raw, _ := m[k].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		if mm, ok := x.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func strList(m map[string]any, k string) []string {
	if m == nil {
		return nil
	}
	raw, _ := m[k].([]any)
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

type flowChoice struct{ id, title string }

func (n *FlowNode) msgType() string {
	if m := sub(n.Data, "message"); m != nil {
		return str(m, "type")
	}
	return "text"
}

// waits says whether the node stops and waits for the customer.
func (n *FlowNode) waits() bool {
	switch n.Type {
	case "question":
		return true
	case "message":
		t := n.msgType()
		return t == "buttons" || t == "list"
	}
	return false
}

func (n *FlowNode) choices() []flowChoice {
	m := sub(n.Data, "message")
	var out []flowChoice
	switch str(m, "type") {
	case "buttons":
		for _, b := range list(m, "buttons") {
			out = append(out, flowChoice{str(b, "id"), str(b, "title")})
		}
	case "list":
		for _, s := range list(m, "sections") {
			for _, r := range list(s, "rows") {
				out = append(out, flowChoice{str(r, "id"), str(r, "title")})
			}
		}
	}
	return out
}

// pickPort maps the customer's answer to a port of a waiting message node.
func (g *FlowGraph) pickPort(n *FlowNode, text, id string) string {
	opts := n.choices()
	if id != "" {
		for _, o := range opts {
			if o.id == id {
				return o.id
			}
		}
	}
	t := normKeyword(text)
	for _, o := range opts {
		if normKeyword(o.title) == t {
			return o.id
		}
	}
	if k, err := strconv.Atoi(strings.Trim(t, ".)( ")); err == nil && k >= 1 && k <= len(opts) {
		return opts[k-1].id
	}
	if g.hasPort(n.ID, "else") {
		return "else"
	}
	return ""
}

var (
	emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	dateRe  = regexp.MustCompile(`^(\d{1,2})[-/.](\d{1,2})[-/.](\d{2,4})$`)
	numRe   = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
)

// checkAnswer validates and normalises a typed answer ("" = invalid).
func checkAnswer(kind, raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	switch kind {
	case "number":
		n := strings.NewReplacer(",", "", " ", "", "₹", "").Replace(v)
		if numRe.MatchString(n) {
			return n
		}
		return ""
	case "email":
		if emailRe.MatchString(v) {
			return strings.ToLower(v)
		}
		return ""
	case "phone":
		d := regexp.MustCompile(`\D`).ReplaceAllString(v, "")
		if len(d) == 10 {
			return "91" + d
		}
		if len(d) >= 11 && len(d) <= 15 {
			return d
		}
		return ""
	case "date":
		if m := dateRe.FindStringSubmatch(v); m != nil {
			y := m[3]
			if len(y) == 2 {
				y = "20" + y
			}
			if t, err := time.Parse("2006-01-02", fmt.Sprintf("%s-%02s-%02s", y, m[2], m[1])); err == nil {
				return t.Format("2006-01-02")
			}
			return ""
		}
		if _, err := time.Parse("2006-01-02", v); err == nil {
			return v
		}
		return ""
	}
	return truncate(v, 500)
}

func evalCondition(d map[string]any, vars map[string]string, tags []string) bool {
	left := vars[strings.ToLower(strings.TrimSpace(str(d, "var")))]
	right := str(d, "value")
	switch str(d, "op") {
	case "exists":
		return strings.TrimSpace(left) != ""
	case "equals":
		return normKeyword(left) == normKeyword(right)
	case "contains":
		return strings.Contains(normKeyword(left), normKeyword(right))
	case "gt", "lt":
		l, err1 := strconv.ParseFloat(strings.TrimSpace(left), 64)
		r, err2 := strconv.ParseFloat(strings.TrimSpace(right), 64)
		if err1 != nil || err2 != nil {
			return false
		}
		if str(d, "op") == "gt" {
			return l > r
		}
		return l < r
	case "tag":
		want := strings.ToLower(strings.TrimSpace(right))
		for _, t := range tags {
			if strings.ToLower(t) == want {
				return true
			}
		}
	}
	return false
}

// pickPath reads "a.b.0.c" from decoded JSON.
func pickPath(v any, path string) string {
	for _, k := range strings.Split(path, ".") {
		if k == "" {
			continue
		}
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(x) {
				return ""
			}
			v = x[i]
		default:
			return ""
		}
	}
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// lint lists the problems that keep a flow from going live.
func (g *FlowGraph) lint() []string {
	var out []string
	starts := 0
	for _, n := range g.Nodes {
		if n.Type == "start" {
			starts++
		}
	}
	if starts != 1 {
		out = append(out, "the flow needs exactly one Start")
	} else if g.follow(g.start().ID, "next") == nil {
		out = append(out, "add a step after Start")
	}
	for _, n := range g.Nodes {
		label := str(n.Data, "label")
		if label == "" {
			label = n.Type
		}
		switch n.Type {
		case "message":
			m := sub(n.Data, "message")
			body := str(m, "text")
			if str(m, "type") != "text" && str(m, "type") != "" {
				body = str(m, "body")
			}
			if strings.TrimSpace(body) == "" {
				out = append(out, fmt.Sprintf("“%s” has no text", label))
			}
			if n.waits() {
				linked := false
				for _, c := range n.choices() {
					if g.follow(n.ID, c.id) != nil {
						linked = true
					}
				}
				if !linked && g.follow(n.ID, "else") == nil {
					out = append(out, fmt.Sprintf("“%s”: none of its buttons leads anywhere", label))
				}
			}
		case "question":
			if strings.TrimSpace(str(n.Data, "text")) == "" || strings.TrimSpace(str(n.Data, "var")) == "" {
				out = append(out, fmt.Sprintf("“%s”: a question needs the text and a variable name", label))
			}
		case "condition":
			if str(n.Data, "op") == "" {
				out = append(out, fmt.Sprintf("“%s”: condition is incomplete", label))
			}
		case "api":
			if !strings.HasPrefix(str(n.Data, "url"), "https://") {
				out = append(out, fmt.Sprintf("“%s”: the API step needs an https:// URL", label))
			}
		case "start", "end", "tag", "assign":
		default:
			out = append(out, fmt.Sprintf("“%s”: unknown step type %q", label, n.Type))
		}
	}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for _, e := range g.Edges {
		if !ids[e.Source] || !ids[e.Target] {
			out = append(out, "some connections point to steps that no longer exist")
			break
		}
	}
	return out
}

// ---------- running ----------

type flowRun struct {
	m       *Manager
	owner   int64
	session string // sending number
	chat    string // chat JID
	phone   string
	flow    *Flow
	vars    map[string]string
	emit    func(req *SendRequest) error // how messages leave (real send or test capture)
	log     func(node, event, detail string)
	noAPI   bool // test mode: don't call external APIs
	noSide  bool // test mode: don't tag contacts / hand over
	steps   int
	nowFunc func() time.Time
}

// message builds the SendRequest for a message node.
func (r *flowRun) message(n *FlowNode) *SendRequest {
	m := sub(n.Data, "message")
	req := &SendRequest{To: r.chat, Vars: r.vars}
	switch str(m, "type") {
	case "buttons":
		b := &ButtonsReq{Title: str(m, "title"), Footer: str(m, "footer")}
		for _, x := range list(m, "buttons") {
			b.Buttons = append(b.Buttons, ButtonReq{Type: "reply", ID: str(x, "id"), Text: truncRunes(str(x, "title"), 20)})
		}
		req.Text = str(m, "body")
		req.Buttons = b
	case "list":
		l := &ListReq{Title: str(m, "title"), ButtonText: str(m, "button_text"), Footer: str(m, "footer")}
		if l.ButtonText == "" {
			l.ButtonText = "चुनें"
		}
		for _, s := range list(m, "sections") {
			sec := ListSection{Title: str(s, "title")}
			for _, x := range list(s, "rows") {
				sec.Rows = append(sec.Rows, ListRow{ID: str(x, "id"), Title: truncRunes(str(x, "title"), 24), Description: truncRunes(str(x, "description"), 72)})
			}
			l.Sections = append(l.Sections, sec)
		}
		req.Text = str(m, "body")
		req.List = l
	default:
		req.Text = str(m, "text")
		if u := str(m, "media_url"); u != "" {
			req.Media = &Media{Type: "image", URL: u}
		}
	}
	return req
}

// advance runs from node n until the flow waits or ends. Returns the node
// the chat is now waiting at ("" when the flow finished).
func (r *flowRun) advance(ctx context.Context, n *FlowNode) string {
	g := &r.flow.Graph
	for n != nil {
		r.steps++
		if r.steps > 40 {
			r.log(n.ID, "error", "stopped: more than 40 steps in one go (loop?)")
			return ""
		}
		switch n.Type {
		case "start":
			n = g.follow(n.ID, "next")
		case "message":
			req := r.message(n)
			if err := r.emit(req); err != nil {
				r.log(n.ID, "error", "send failed: "+err.Error())
				return ""
			}
			r.log(n.ID, "message", truncate(req.Text, 200))
			if n.waits() {
				return n.ID
			}
			n = g.follow(n.ID, "next")
		case "question":
			if err := r.emit(&SendRequest{To: r.chat, Text: str(n.Data, "text"), Vars: r.vars}); err != nil {
				r.log(n.ID, "error", "send failed: "+err.Error())
				return ""
			}
			r.log(n.ID, "question", str(n.Data, "var"))
			return n.ID
		case "condition":
			tags := []string{}
			if r.phone != "" && r.m != nil {
				if c, err := r.m.contactByPhoneFull(ctx, r.owner, r.phone); err == nil {
					tags = c.Tags
				}
			}
			port := "no"
			if evalCondition(n.Data, r.vars, tags) {
				port = "yes"
			}
			r.log(n.ID, "condition", port)
			n = g.follow(n.ID, port)
		case "tag":
			add, rm := strList(n.Data, "add"), strList(n.Data, "remove")
			r.log(n.ID, "tag", fmt.Sprintf("+%v -%v", add, rm))
			if !r.noSide && r.phone != "" && r.m != nil {
				r.m.retag(ctx, r.owner, r.phone, add, rm)
			}
			n = g.follow(n.ID, "next")
		case "assign":
			minutes, _ := strconv.Atoi(str(n.Data, "minutes"))
			if minutes <= 0 {
				minutes = 120
			}
			r.log(n.ID, "assign", fmt.Sprintf("handed to a person for %d min", minutes))
			if !r.noSide && r.m != nil {
				until := r.nowFunc().Add(time.Duration(minutes) * time.Minute).Unix()
				_, _ = r.m.db.ExecContext(ctx, `INSERT INTO chat_state(session, chat, human_until) VALUES(?,?,?) ON CONFLICT(session, chat) DO UPDATE SET human_until=excluded.human_until`, r.session, r.chat, until)
				r.m.dispatchOwner(r.owner, "flow.handover", map[string]any{"flow": r.flow.Name, "chat": r.chat, "phone": r.phone, "note": str(n.Data, "note"), "vars": r.vars})
			}
			n = g.follow(n.ID, "next")
		case "api":
			port := "ok"
			if r.noAPI {
				r.log(n.ID, "api", "skipped in test")
			} else if err := r.callAPI(ctx, n); err != nil {
				r.log(n.ID, "api", "failed: "+err.Error())
				port = "fail"
			} else {
				r.log(n.ID, "api", "ok")
			}
			n = g.follow(n.ID, port)
		case "end":
			r.log(n.ID, "end", "")
			return ""
		default:
			r.log(n.ID, "error", "unknown step "+n.Type)
			return ""
		}
	}
	r.log("", "end", "no next step")
	return ""
}

func (r *flowRun) callAPI(ctx context.Context, n *FlowNode) error {
	u := renderText(str(n.Data, "url"), r.vars, nil)
	if !strings.HasPrefix(u, "https://") {
		return errors.New("only https:// URLs")
	}
	method := strings.ToUpper(str(n.Data, "method"))
	if method == "" {
		method = "POST"
	}
	var body io.Reader
	if method != "GET" {
		b := strings.TrimSpace(str(n.Data, "body"))
		if b == "" {
			raw, _ := json.Marshal(map[string]any{"phone": r.phone, "chat": r.chat, "vars": r.vars})
			b = string(raw)
		} else {
			b = renderText(b, r.vars, nil)
		}
		body = strings.NewReader(b)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "VividRelay-Flow/1.0")
	for _, h := range list(n.Data, "headers") {
		if k := str(h, "name"); k != "" {
			req.Header.Set(k, renderText(str(h, "value"), r.vars, nil))
		}
	}
	resp, err := previewClient(r.m != nil && r.m.cfg.AllowPrivateURLs).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var v any
	_ = json.Unmarshal(bytes.TrimSpace(raw), &v)
	for name, path := range sub(n.Data, "save") {
		if p, ok := path.(string); ok {
			r.vars[strings.ToLower(name)] = truncate(pickPath(v, p), 2000)
		}
	}
	return nil
}

// resume feeds the customer's message into the node the chat waits at.
func (r *flowRun) resume(ctx context.Context, at *FlowNode, text, buttonID string) string {
	g := &r.flow.Graph
	switch at.Type {
	case "question":
		v := checkAnswer(str(at.Data, "kind"), text)
		if v == "" {
			retry := str(at.Data, "retry")
			if retry == "" {
				retry = "समझ नहीं आया — कृपया फिर से लिखें।"
			}
			r.log(at.ID, "invalid", truncate(text, 100))
			_ = r.emit(&SendRequest{To: r.chat, Text: retry, Vars: r.vars})
			return at.ID
		}
		r.vars[strings.ToLower(strings.TrimSpace(str(at.Data, "var")))] = v
		r.log(at.ID, "answer", v)
		return r.advance(ctx, g.follow(at.ID, "next"))
	case "message":
		port := g.pickPort(at, text, buttonID)
		if port == "" {
			r.log(at.ID, "nomatch", truncate(text, 100))
			// repeat the choices once more
			_ = r.emit(r.message(at))
			return at.ID
		}
		r.log(at.ID, "choice", port)
		return r.advance(ctx, g.follow(at.ID, port))
	}
	return r.advance(ctx, at)
}

func (m *Manager) contactByPhoneFull(ctx context.Context, owner int64, phone string) (*Contact, error) {
	row := m.db.QueryRowContext(ctx, `SELECT `+contactCols+` FROM contacts c WHERE c.owner_id=? AND c.phone=?`, owner, phone)
	return scanContact(row)
}

// retag adds / removes tags on the contact for this phone (creating it if needed).
func (m *Manager) retag(ctx context.Context, owner int64, phone string, add, remove []string) {
	c, err := m.contactByPhoneFull(ctx, owner, phone)
	if err != nil {
		c = &Contact{Phone: phone, Source: "flow"}
	}
	drop := map[string]bool{}
	for _, t := range remove {
		drop[strings.ToLower(t)] = true
	}
	var tags []string
	for _, t := range append(c.Tags, add...) {
		if !drop[strings.ToLower(t)] {
			tags = append(tags, t)
		}
	}
	if c.ID == 0 {
		c.Tags = tags
		_, _ = upsertContact(ctx, m.db, owner, c, false)
		return
	}
	_, _ = m.db.ExecContext(ctx, `UPDATE contacts SET tags=?, updated_at=? WHERE id=?`, packTags(tags), time.Now().Unix(), c.ID)
	for _, t := range add {
		m.enrollByTrigger(ctx, owner, "tag", strings.ToLower(t), phone, c.Name, c.ID)
	}
}

// runFlows is called for every incoming direct message. True = a flow took it.
func (s *Session) runFlows(chat types.JID, firstTime bool, text, buttonID, pushName string) bool {
	ctx, cancel := context.WithTimeout(s.m.ctx, 2*time.Minute)
	defer cancel()
	owner := s.owner()
	if s.m.lic.require(ctx, owner, "autoreply") != nil {
		return false
	}
	chatS := chat.String()
	phone := phoneOf(chat)
	var humanUntil int64
	_ = s.m.db.QueryRowContext(ctx, `SELECT human_until FROM chat_state WHERE session=? AND chat=?`, s.name, chatS).Scan(&humanUntil)
	if humanUntil > time.Now().Unix() {
		return false
	}
	// 1. a flow already in progress for this chat?
	var flowID int64
	var nodeID, varsRaw string
	var updated int64
	err := s.m.db.QueryRowContext(ctx, `SELECT flow_id, node_id, vars, updated_at FROM flow_state WHERE session=? AND chat=?`, s.name, chatS).Scan(&flowID, &nodeID, &varsRaw, &updated)
	var flow *Flow
	var at *FlowNode
	vars := map[string]string{}
	if err == nil && nodeID != "" && time.Since(time.Unix(updated, 0)) < 24*time.Hour {
		if flow, _ = scanFlow(s.m.db.QueryRowContext(ctx, `SELECT `+flowCols+` FROM flows WHERE id=? AND owner_id=?`, flowID, owner)); flow != nil && flow.Active {
			at = flow.Graph.node(nodeID)
			vars = fromJSON[map[string]string](varsRaw)
			if vars == nil {
				vars = map[string]string{}
			}
		}
	}
	if at == nil {
		// 2. does a flow start on this message?
		flow = s.m.matchFlow(ctx, owner, s.name, phone, firstTime, text)
		if flow == nil {
			return false
		}
		_, _ = s.m.db.ExecContext(ctx, `UPDATE flows SET runs=runs+1 WHERE id=?`, flow.ID)
	}
	name := pushName
	if name == "" {
		name = "there"
	}
	if vars["name"] == "" {
		vars["name"] = name
	}
	vars["phone"] = phone
	for k, v := range s.m.businessVars(ctx, owner) {
		if _, ok := vars[k]; !ok {
			vars[k] = v
		}
	}
	run := &flowRun{m: s.m, owner: owner, session: s.name, chat: chatS, phone: phone, flow: flow, vars: vars, nowFunc: time.Now,
		emit: func(req *SendRequest) error {
			res, err := s.send(ctx, req, sendOpts{direct: true, reply: true})
			s.recordDirect(ctx, req, res, err)
			return err
		},
		log: func(node, event, detail string) {
			_, _ = s.m.db.ExecContext(ctx, `INSERT INTO flow_log(owner_id, flow_id, chat, node_id, event, detail, at) VALUES(?,?,?,?,?,?,?)`, owner, flow.ID, chatS, node, event, truncate(detail, 500), time.Now().Unix())
		},
	}
	var waitAt string
	if at != nil {
		waitAt = run.resume(ctx, at, text, buttonID)
	} else {
		run.log("", "start", truncate(text, 100))
		waitAt = run.advance(ctx, flow.Graph.start())
	}
	if waitAt == "" {
		_, _ = s.m.db.ExecContext(ctx, `DELETE FROM flow_state WHERE session=? AND chat=?`, s.name, chatS)
		if run.phone != "" {
			s.m.saveFlowAnswers(ctx, owner, run.phone, run.vars)
		}
	} else {
		_, _ = s.m.db.ExecContext(ctx, `INSERT INTO flow_state(session, chat, flow_id, node_id, vars, updated_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(session, chat) DO UPDATE SET flow_id=excluded.flow_id, node_id=excluded.node_id, vars=excluded.vars, updated_at=excluded.updated_at`,
			s.name, chatS, flow.ID, waitAt, jsonText(run.vars), time.Now().Unix())
	}
	return true
}

// saveFlowAnswers keeps the variables a flow collected on the contact (custom fields).
func (m *Manager) saveFlowAnswers(ctx context.Context, owner int64, phone string, vars map[string]string) {
	custom := map[string]string{}
	for k, v := range vars {
		switch k {
		case "name", "phone", "business", "business_phone", "city":
			continue
		}
		if v != "" {
			custom[k] = v
		}
	}
	if len(custom) == 0 {
		return
	}
	c := &Contact{Phone: phone, Custom: custom, Source: "flow"}
	if n := vars["name"]; n != "" && n != "there" {
		c.Name = n
	}
	_, _ = upsertContact(ctx, m.db, owner, c, true)
}

// matchFlow finds the first active flow whose trigger fits this message.
func (m *Manager) matchFlow(ctx context.Context, owner int64, session, phone string, firstTime bool, text string) *Flow {
	rows, err := m.db.QueryContext(ctx, `SELECT `+flowCols+` FROM flows WHERE owner_id=? AND active=1 AND (session='' OR session=?) ORDER BY priority DESC, id`, owner, session)
	if err != nil {
		return nil
	}
	defer rows.Close()
	norm := normKeyword(text)
	for rows.Next() {
		f, err := scanFlow(rows)
		if err != nil {
			continue
		}
		if f.TestMode {
			ok := false
			for _, p := range f.TestPhones {
				if normPhone(p, "91") == phone {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		if len(f.Graph.lint()) > 0 {
			continue
		}
		switch f.Trigger {
		case "welcome":
			if firstTime {
				return f
			}
		case "keyword":
			for _, k := range f.Keywords {
				if nk := normKeyword(k); nk != "" && nk == norm {
					return f
				}
			}
		case "contains":
			for _, k := range f.Keywords {
				if nk := normKeyword(k); nk != "" && strings.Contains(norm, nk) {
					return f
				}
			}
		case "any":
			if norm != "" {
				return f
			}
		}
	}
	return nil
}

// ---------- API ----------

func (a *API) listFlows(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT `+flowCols+` FROM flows WHERE owner_id=? ORDER BY priority DESC, id`, p.UserID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []*Flow{}
	for rows.Next() {
		if f, err := scanFlow(rows); err == nil {
			f.Problems = f.Graph.lint()
			if f.Problems == nil {
				f.Problems = []string{}
			}
			out = append(out, f)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "flows": out})
}

func (a *API) saveFlow(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	if err := a.lic.require(ctx, p.UserID, "autoreply"); err != nil {
		a.fail(w, err)
		return
	}
	f := &Flow{Trigger: "keyword", Active: false, Priority: 10}
	if id := r.PathValue("id"); id != "" {
		old, err := scanFlow(a.m.db.QueryRowContext(ctx, `SELECT `+flowCols+` FROM flows WHERE id=? AND owner_id=?`, id, p.UserID))
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("flow not found"))
			return
		}
		f = old
	}
	if err := decode(r, f); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	f.Name = truncate(strings.TrimSpace(f.Name), 100)
	if f.Name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("give the flow a name"))
		return
	}
	switch f.Trigger {
	case "keyword", "contains":
		if len(f.Keywords) == 0 {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("add at least one keyword"))
			return
		}
	case "welcome", "any", "manual":
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("trigger must be keyword, contains, welcome, any or manual"))
		return
	}
	if f.Session != "" {
		if s, err := a.m.get(f.Session); err != nil || !p.canUse(s) {
			writeErr(w, http.StatusBadRequest, "bad_request", errors.New("number not found"))
			return
		}
	}
	if f.ID == 0 {
		if err := a.countCheck(ctx, p.UserID, "flows", "flows"); err != nil {
			a.fail(w, err)
			return
		}
	}
	if len(f.Graph.Nodes) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("at most 200 steps"))
		return
	}
	for i := range f.TestPhones {
		f.TestPhones[i] = normPhone(f.TestPhones[i], a.cfg.DefaultCountryCode)
	}
	f.Problems = f.Graph.lint()
	if f.Active && len(f.Problems) > 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("fix these before turning the flow on: "+strings.Join(f.Problems, "; ")))
		return
	}
	now := time.Now().Unix()
	if f.ID > 0 {
		_, err := a.m.db.ExecContext(ctx, `UPDATE flows SET name=?, trigger_type=?, keywords=?, session=?, graph=?, active=?, test_mode=?, test_phones=?, priority=?, updated_at=? WHERE id=? AND owner_id=?`,
			f.Name, f.Trigger, jsonText(f.Keywords), f.Session, jsonText(f.Graph), b2i(f.Active), b2i(f.TestMode), jsonText(f.TestPhones), f.Priority, now, f.ID, p.UserID)
		if err != nil {
			a.fail(w, err)
			return
		}
		if !f.Active {
			_, _ = a.m.db.ExecContext(ctx, `DELETE FROM flow_state WHERE flow_id=?`, f.ID)
		}
	} else {
		res, err := a.m.db.ExecContext(ctx, `INSERT INTO flows(owner_id, name, trigger_type, keywords, session, graph, active, test_mode, test_phones, priority, runs, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,0,?,?)`, p.UserID, f.Name, f.Trigger, jsonText(f.Keywords), f.Session, jsonText(f.Graph), b2i(f.Active), b2i(f.TestMode), jsonText(f.TestPhones), f.Priority, now, now)
		if err != nil {
			a.fail(w, err)
			return
		}
		f.ID, _ = res.LastInsertId()
		f.CreatedAt = now
	}
	f.UpdatedAt = now
	if f.Problems == nil {
		f.Problems = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "flow": f})
}

func (a *API) deleteFlow(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	res, err := a.m.db.ExecContext(r.Context(), `DELETE FROM flows WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID)
	if n, _ := rowsAffected(res, err); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("flow not found"))
		return
	}
	_, _ = a.m.db.ExecContext(r.Context(), `DELETE FROM flow_state WHERE flow_id=?`, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /api/flows/{id}/logs?chat=
func (a *API) flowLogs(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	where, args := `owner_id=? AND flow_id=?`, []any{p.UserID, r.PathValue("id")}
	if c := r.URL.Query().Get("chat"); c != "" {
		where += ` AND chat=?`
		args = append(args, c)
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT chat, node_id, event, detail, at FROM flow_log WHERE `+where+` ORDER BY id DESC LIMIT 300`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var chat, node, event, detail string
		var at int64
		if rows.Scan(&chat, &node, &event, &detail, &at) == nil {
			out = append(out, map[string]any{"chat": chat, "phone": strings.TrimSuffix(chat, "@s.whatsapp.net"), "node": node, "event": event, "detail": detail, "at": at})
		}
	}
	var active int
	_ = a.m.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM flow_state WHERE flow_id=?`, r.PathValue("id")).Scan(&active)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "logs": out, "in_progress": active})
}

// POST /api/flows/{id}/test {text, button_id, reset, graph?} — runs the flow
// in a sandbox for this user (nothing is sent, no API calls, no tags) and
// returns the bot's replies. The sandbox state lives in flow_state under a
// "test:" chat so the conversation can go on across calls.
func (a *API) testFlow(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	f, err := scanFlow(a.m.db.QueryRowContext(ctx, `SELECT `+flowCols+` FROM flows WHERE id=? AND owner_id=?`, r.PathValue("id"), p.UserID))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("flow not found"))
		return
	}
	var in struct {
		Text     string     `json:"text"`
		ButtonID string     `json:"button_id"`
		Reset    bool       `json:"reset"`
		Graph    *FlowGraph `json:"graph"` // unsaved editor state
	}
	_ = decode(r, &in)
	if in.Graph != nil {
		f.Graph = *in.Graph
	}
	if probs := f.Graph.lint(); len(probs) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "problems": probs, "replies": []any{}, "events": []any{}})
		return
	}
	chat := fmt.Sprintf("test:%d:%d", p.ActorID, f.ID)
	session := "test"
	if in.Reset {
		_, _ = a.m.db.ExecContext(ctx, `DELETE FROM flow_state WHERE session=? AND chat=?`, session, chat)
	}
	var nodeID, varsRaw string
	_ = a.m.db.QueryRowContext(ctx, `SELECT node_id, vars FROM flow_state WHERE session=? AND chat=?`, session, chat).Scan(&nodeID, &varsRaw)
	vars := fromJSON[map[string]string](varsRaw)
	if vars == nil {
		vars = map[string]string{"name": p.Name, "phone": "919800000000"}
		for k, v := range a.m.businessVars(ctx, p.UserID) {
			vars[k] = v
		}
	}
	var replies []*SendRequest
	var events []map[string]string
	run := &flowRun{m: a.m, owner: p.UserID, session: session, chat: chat, phone: "", flow: f, vars: vars, noAPI: true, noSide: true, nowFunc: time.Now,
		emit: func(req *SendRequest) error { replies = append(replies, req); return nil },
		log: func(node, event, detail string) {
			events = append(events, map[string]string{"node": node, "event": event, "detail": detail})
		},
	}
	var waitAt string
	if at := f.Graph.node(nodeID); at != nil {
		waitAt = run.resume(ctx, at, in.Text, in.ButtonID)
	} else {
		waitAt = run.advance(ctx, f.Graph.start())
	}
	if waitAt == "" {
		_, _ = a.m.db.ExecContext(ctx, `DELETE FROM flow_state WHERE session=? AND chat=?`, session, chat)
	} else {
		_, _ = a.m.db.ExecContext(ctx, `INSERT INTO flow_state(session, chat, flow_id, node_id, vars, updated_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(session, chat) DO UPDATE SET flow_id=excluded.flow_id, node_id=excluded.node_id, vars=excluded.vars, updated_at=excluded.updated_at`,
			session, chat, f.ID, waitAt, jsonText(run.vars), time.Now().Unix())
	}
	for _, rp := range replies {
		rp.Text = renderText(rp.Text, run.vars, nil)
	}
	if replies == nil {
		replies = []*SendRequest{}
	}
	if events == nil {
		events = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "replies": replies, "events": events, "waiting_at": waitAt, "vars": run.vars, "done": waitAt == ""})
}
