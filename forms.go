package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ======================= public enquiry forms =======================
//
// Every business has its kit's forms at /f/<code>/<key> — a plain page a
// customer can fill from a website link, a QR code or a WhatsApp message.
// A filled form becomes a contact (tagged with the form), starts the
// "new lead" follow-up sequence, makes a task for staff and fires the
// form.entry webhook. No JavaScript, so it works everywhere.

var formLimiter = &attemptLimiter{hits: map[string][]time.Time{}}

var formTmpl = template.Must(template.New("form").Parse(`<!doctype html>
<html lang="hi"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · {{.Business}}</title>
<meta name="robots" content="noindex">
<style>
:root{color-scheme:light dark;--bg:#f4f6f8;--card:#fff;--ink:#17202a;--mut:#5b6773;--line:#dde3e9;--acc:#128c7e;--accd:#0d6b60}
@media (prefers-color-scheme:dark){:root{--bg:#0f1419;--card:#17202a;--ink:#eef2f6;--mut:#9aa7b4;--line:#2a3542}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,"Noto Sans",sans-serif}
main{max-width:520px;margin:0 auto;padding:24px 16px 48px}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:22px}
h1{font-size:1.35rem;margin:0 0 4px}.biz{color:var(--mut);margin:0 0 6px;font-size:.95rem}.desc{margin:0 0 18px;color:var(--mut)}
label{display:block;font-weight:600;margin:14px 0 6px}label small{font-weight:400;color:var(--mut)}
input,select,textarea{width:100%;padding:11px 12px;border:1px solid var(--line);border-radius:10px;background:transparent;color:inherit;font:inherit}
textarea{min-height:96px}.opts label{font-weight:400;margin:4px 0;display:flex;gap:8px;align-items:center}.opts input{width:auto}
button{margin-top:20px;width:100%;padding:13px;border:0;border-radius:12px;background:var(--acc);color:#fff;font:inherit;font-weight:700;cursor:pointer}button:hover{background:var(--accd)}
.err{background:#fde8e8;color:#9b1c1c;border-radius:10px;padding:10px 12px;margin-bottom:12px}
.ok{text-align:center}.ok .big{font-size:3rem}.wa{display:inline-block;margin-top:16px;padding:12px 18px;border-radius:12px;background:#25d366;color:#fff;text-decoration:none;font-weight:700}
footer{text-align:center;color:var(--mut);font-size:.8rem;margin-top:18px}
</style></head><body><main><div class="card">
{{if .Done}}
<div class="ok"><div class="big">✅</div><h1>धन्यवाद{{if .Name}}, {{.Name}} जी{{end}}!</h1>
<p class="desc">आपकी जानकारी {{.Business}} तक पहुँच गई। हम जल्दी ही WhatsApp पर संपर्क करेंगे।</p>
{{if .WaLink}}<a class="wa" href="{{.WaLink}}">अभी WhatsApp पर बात करें</a>{{end}}</div>
{{else}}
<h1>{{.Title}}</h1><p class="biz">{{.Business}}{{if .City}} · {{.City}}{{end}}</p>
{{if .Desc}}<p class="desc">{{.Desc}}</p>{{end}}
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<form method="post" action="">
<input type="text" name="website" value="" style="display:none" tabindex="-1" autocomplete="off">
<label for="f-name">आपका नाम</label><input id="f-name" name="name" type="text" required value="{{index .Values "name"}}" autocomplete="name">
<label for="f-phone">WhatsApp नंबर</label><input id="f-phone" name="phone" type="tel" required inputmode="tel" value="{{index .Values "phone"}}" autocomplete="tel" placeholder="98xxxxxxxx">
{{range .Fields}}
<label for="f-{{.ID}}">{{.Label}}{{if not .Required}} <small>(optional)</small>{{end}}</label>
{{if eq .Type "textarea"}}<textarea id="f-{{.ID}}" name="{{.ID}}" {{if .Required}}required{{end}}>{{index $.Values .ID}}</textarea>
{{else if eq .Type "select"}}{{$f := .}}<select id="f-{{.ID}}" name="{{.ID}}" {{if .Required}}required{{end}}><option value="">चुनें…</option>{{range .Options}}<option {{if eq . (index $.Values $f.ID)}}selected{{end}}>{{.}}</option>{{end}}</select>
{{else if eq .Type "radio"}}<div class="opts">{{$f := .}}{{range .Options}}<label><input type="radio" name="{{$f.ID}}" value="{{.}}" {{if $f.Required}}required{{end}} {{if eq . (index $.Values $f.ID)}}checked{{end}}>{{.}}</label>{{end}}</div>
{{else if eq .Type "yesno"}}<div class="opts"><label><input type="radio" name="{{.ID}}" value="हाँ">हाँ</label><label><input type="radio" name="{{.ID}}" value="नहीं">नहीं</label></div>
{{else if eq .Type "date"}}<input id="f-{{.ID}}" name="{{.ID}}" type="date" value="{{index $.Values .ID}}" {{if .Required}}required{{end}}>
{{else if eq .Type "number"}}<input id="f-{{.ID}}" name="{{.ID}}" type="number" inputmode="numeric" value="{{index $.Values .ID}}" {{if .Required}}required{{end}}>
{{else}}<input id="f-{{.ID}}" name="{{.ID}}" type="text" value="{{index $.Values .ID}}" {{if .Required}}required{{end}}>{{end}}
{{end}}
<button type="submit">भेजें</button>
</form>
{{end}}
</div><footer>{{.Business}} · Powered by {{.Site}}</footer></main></body></html>`))

type formPage struct {
	Title, Desc, Business, City, Site, Error, Name, WaLink string
	Fields                                                 []KitField
	Values                                                 map[string]string
	Done                                                   bool
}

func (a *API) businessByCode(ctx context.Context, code string) (int64, *Business, error) {
	var owner int64
	if err := a.m.db.QueryRowContext(ctx, `SELECT owner_id FROM business WHERE code=?`, code).Scan(&owner); err != nil {
		return 0, nil, err
	}
	b, err := a.m.business(ctx, owner)
	return owner, b, err
}

func (a *API) renderForm(w http.ResponseWriter, pg *formPage, status int) {
	var buf bytes.Buffer
	if err := formTmpl.Execute(&buf, pg); err != nil {
		http.Error(w, "page error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors *")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (a *API) formFor(w http.ResponseWriter, r *http.Request) (int64, *Business, *KitForm, bool) {
	owner, b, err := a.businessByCode(r.Context(), r.PathValue("code"))
	if err != nil {
		http.NotFound(w, r)
		return 0, nil, nil, false
	}
	for i := range b.Forms {
		if b.Forms[i].Key == r.PathValue("key") {
			return owner, b, &b.Forms[i], true
		}
	}
	http.NotFound(w, r)
	return 0, nil, nil, false
}

func (a *API) formPageFor(b *Business, f *KitForm) *formPage {
	biz := b.Name
	if biz == "" {
		biz = getSettings(context.Background(), a.m.db)["site_name"]
	}
	return &formPage{Title: f.Title, Desc: f.Description, Business: biz, City: b.City, Site: getSettings(context.Background(), a.m.db)["site_name"],
		Fields: f.Fields, Values: map[string]string{}}
}

func (a *API) publicForm(w http.ResponseWriter, r *http.Request) {
	_, b, f, ok := a.formFor(w, r)
	if !ok {
		return
	}
	a.renderForm(w, a.formPageFor(b, f), http.StatusOK)
}

func (a *API) publicFormSubmit(w http.ResponseWriter, r *http.Request) {
	owner, b, f, ok := a.formFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	pg := a.formPageFor(b, f)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("website") != "" { // honeypot
		a.renderForm(w, pg, http.StatusOK)
		return
	}
	for k := range r.PostForm {
		pg.Values[k] = truncate(strings.TrimSpace(r.PostForm.Get(k)), 1000)
	}
	if !formLimiter.allow("form:"+clientIP(r), 20, time.Hour) {
		pg.Error = "बहुत सारी कोशिशें — थोड़ी देर बाद फिर से करें।"
		a.renderForm(w, pg, http.StatusTooManyRequests)
		return
	}
	name := truncate(pg.Values["name"], 120)
	phone := normPhone(pg.Values["phone"], a.cfg.DefaultCountryCode)
	if name == "" || phone == "" {
		pg.Error = "नाम और सही WhatsApp नंबर भरें।"
		a.renderForm(w, pg, http.StatusBadRequest)
		return
	}
	answers := map[string]string{}
	for _, fd := range f.Fields {
		v := pg.Values[fd.ID]
		if fd.Required && v == "" {
			pg.Error = fd.Label + " भरना ज़रूरी है।"
			a.renderForm(w, pg, http.StatusBadRequest)
			return
		}
		if v != "" {
			answers[fd.ID] = v
		}
	}
	// contact (tagged with the form), entry, task, sequence, webhook
	c := &Contact{Phone: phone, Name: name, Tags: []string{"lead", "form:" + f.Key}, Custom: answers, Source: "form"}
	created, err := upsertContact(ctx, a.m.db, owner, c, true)
	if err != nil {
		a.m.log.Warn("form contact", "err", err)
	}
	cc, _ := a.m.contactByPhone(ctx, owner, phone)
	var contactID int64
	if cc != nil {
		contactID = cc.ID
	}
	now := time.Now().Unix()
	_, _ = a.m.db.ExecContext(ctx, `INSERT INTO form_entries(owner_id, form_key, contact_id, phone, name, answers, ip, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		owner, f.Key, contactID, phone, name, jsonText(answers), clientIP(r), now)
	summary := make([]string, 0, len(answers))
	for _, fd := range f.Fields {
		if v, ok := answers[fd.ID]; ok {
			summary = append(summary, fd.Label+": "+v)
		}
	}
	_, _ = a.m.db.ExecContext(ctx, `INSERT INTO tasks(owner_id, title, due, repeat, at_time, done_at, assignee_id, contact_id, phone, source, note, created_at)
		VALUES(?,?,?,'','',0,0,?,?,'form',?,?)`, owner, truncate(fmt.Sprintf("📩 %s — %s (%s)", f.Title, name, phone), 200), now, contactID, phone, truncate(strings.Join(summary, "\n"), 2000), now)
	bg := context.WithoutCancel(ctx)
	a.m.enrollByTrigger(bg, owner, "form", f.Key, phone, name, contactID)
	if created || len(answers) > 0 {
		a.m.enrollByTrigger(bg, owner, "lead_new", "", phone, name, contactID)
	}
	a.m.dispatchOwner(owner, "form.entry", map[string]any{"form": f.Key, "title": f.Title, "phone": phone, "name": name, "answers": answers, "new_contact": created, "timestamp": now})
	pg.Done, pg.Name = true, firstWord(name, "")
	if b.Phone != "" {
		pg.WaLink = "https://wa.me/" + b.Phone + "?text=" + url.QueryEscape("नमस्ते, मैंने "+f.Title+" form भरा है — "+name)
	}
	a.renderForm(w, pg, http.StatusOK)
}

// GET /api/business/entries?form=key — recent form entries.
func (a *API) listFormEntries(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	where, args := `owner_id=?`, []any{p.UserID}
	if k := r.URL.Query().Get("form"); k != "" {
		where += ` AND form_key=?`
		args = append(args, k)
	}
	rows, err := a.m.db.QueryContext(r.Context(), `SELECT id, form_key, contact_id, phone, name, answers, created_at FROM form_entries WHERE `+where+` ORDER BY id DESC LIMIT 300`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, cid, at int64
		var key, phone, name, answers string
		if rows.Scan(&id, &key, &cid, &phone, &name, &answers, &at) == nil {
			out = append(out, map[string]any{"id": id, "form": key, "contact_id": cid, "phone": phone, "name": name, "answers": orEmpty(fromJSON[map[string]string](answers)), "created_at": at})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": out})
}
