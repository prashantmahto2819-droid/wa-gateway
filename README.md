# Vivid Relay

_VividBrandify का WhatsApp gateway — repo: wa-gateway_

अपना छोटा, हल्का WhatsApp gateway — एक या ज़्यादा WhatsApp नंबर QR से जोड़िए, website से मैसेज भेजिए, और आने वाले मैसेज सीधे website पर पाइए।

- **Engine:** [whatsmeow](https://github.com/tulir/whatsmeow) (Go) — Beeper की टीम इसे maintain करती है; WAHA (GOWS), GOWA, WuzAPI, Evolution Go भी इसी पर चलते हैं।
- **हल्का:** एक ~17 MB की file, लगभग 20–60 MB RAM। फ़ोटो/document कभी download या save नहीं होते, chat history save नहीं होती।
- **सुरक्षित भेजना:** रोज़ की सीमा, नए नंबर का warm-up, random अंतर, typing, batch break, तय घंटे, नए contacts की अलग सीमा, और WhatsApp की 463 "reach-out timelock" पर अपने-आप रुकना।
- **Install:** VPS पर एक command (Docker + मुफ़्त HTTPS + crash पर अपने-आप restart)।

> ⚠️ यह WhatsApp की official API नहीं है। अनजान/ठंडे नंबरों पर bulk भेजने से नंबर बैन हो सकता है। सिर्फ़ उन्हीं लोगों को भेजें जिन्होंने अनुमति दी है या आपसे बात की है। OTP जैसे ज़रूरी मैसेज के लिए official Cloud API बेहतर है।

---

## 1. Install (Ubuntu/Debian VPS, 1–2 GB RAM)

1. Domain का A record VPS के IP पर करें (जैसे `wa.example.com`)।
2. VPS पर:

```bash
git clone https://github.com/prashantmahto2819-droid/wa-gateway.git
cd wa-gateway
sudo ./install.sh wa.example.com
```

Script Docker लगाता है, कम RAM हो तो swap बनाता है, `.env` में API key बनाता है, और HTTPS के साथ चालू करता है। अंत में Dashboard link और API key दिखेगी।

3. `https://wa.example.com/` खोलें → API key डालें → session `main` जोड़ें → फ़ोन में **WhatsApp → Linked devices → Link a device** से QR scan करें (या phone number से pairing code लें)।

**Update:** `sudo ./update.sh` · WhatsApp ने कुछ बदला हो तो `sudo ./update.sh --whatsmeow`
**Logs:** `docker compose logs -f gateway` · **Settings:** `.env` बदलें, फिर `docker compose up -d`

> फ़ोन को कम-से-कम हर 14 दिन में एक बार इस्तेमाल करें, नहीं तो WhatsApp linked device हटा देता है। एक ही session को दो जगह (दो gateway) मत चलाइए।

---

## 2. API

हर request में header: `Authorization: Bearer <API_KEY>` (या `X-API-Key`)। Body JSON है। Base: `https://wa.example.com`

### Session (नंबर)

| Method | Path | काम |
|---|---|---|
| GET | `/api/sessions` | सब नंबर + status + आज का हिसाब |
| POST | `/api/sessions` | `{"name":"main","webhook_url":""}` नया session |
| GET | `/api/sessions/{name}` | एक session |
| PATCH | `/api/sessions/{name}` | `{"paused":true}` queue रोकें / `{"webhook_url":"..."}` |
| POST | `/api/sessions/{name}/qr` | QR शुरू करें; जवाब में `qr` और `qr_image` (PNG data URL) |
| GET | `/api/sessions/{name}/qr` | ताज़ा QR (हर 3 सेकंड पूछें; QR बदलता रहता है) |
| POST | `/api/sessions/{name}/pair-code` | `{"phone":"9876543210"}` → 8 अक्षर का code |
| POST | `/api/sessions/{name}/logout` | नंबर हटाएँ |
| POST | `/api/sessions/{name}/reconnect` | दोबारा जोड़ें |
| DELETE | `/api/sessions/{name}` | session पूरी तरह हटाएँ |

Status: `new`, `qr`, `qr_expired`, `connecting`, `connected`, `disconnected` (अपने-आप जुड़ेगा), `logged_out` (फिर से QR), `replaced`, `temp_banned`.

### मैसेज भेजना — तुरंत (OTP-जैसे notice, desk reply)

`POST /api/sessions/main/send`

```json
{ "to": "9876543210", "text": "नमस्ते {{name}}, आपका टोकन {{code}} है", "vars": {"name":"Asha","code":"4821"} }
```

`to` में चल सकता है: `9876543210` (10 अंक पर `91` अपने-आप), `+91 98765-43210`, group `1203…@g.us`, channel `1203…@newsletter`, community के लिए उसका **announcements** group।

फ़ोटो / video / document / audio:

```json
{ "to": "1203630xxxx@g.us", "text": "caption", "media": { "type": "image", "url": "https://site.com/a.jpg" } }
```

`media.base64` भी दे सकते हैं। Document में `filename`। फ़ाइल सिर्फ़ memory में आती है, भेजकर भूल जाते हैं।

Desk reply (quote करके, और पहले मैसेज "read" हो जाता है):

```json
{ "to": "919876543210", "text": "जी, हो जाएगा", "reply_to": { "id": "3EB0ABC…", "text": "मेरा काम कब होगा?" } }
```

Group में mention: `"text": "@919876543210 कृपया देखें", "mentions": ["919876543210"]`

जवाब: `{"ok":true,"message":{"id":"3EB0…","to":"919876543210@s.whatsapp.net","timestamp":…,"new_contact":false}}`

### Campaign / bulk — queue से (सुरक्षित रफ़्तार में)

`POST /api/sessions/main/queue`

```json
{
  "campaign": "blood-camp-oct",
  "template": { "text": "{नमस्ते|प्रणाम} {{name}} जी, रविवार को रक्तदान शिविर है। आना हो तो YES लिखें।" },
  "recipients": [ { "to": "9876543210", "vars": { "name": "राहुल" } }, { "to": "9876543211", "vars": { "name": "सीमा" } } ],
  "send_after": 0
}
```

या `"messages": [ {…}, {…} ]` (हर मैसेज अलग)। `{a|b|c}` में से हर बार एक शब्द random चुना जाता है ताकि सबको एक-सा मैसेज न जाए।

| Method | Path | काम |
|---|---|---|
| GET | `/api/sessions/{name}/queue` | queued/sent/failed गिनती, आज की सीमा |
| GET | `/api/campaigns/{campaign}` | एक campaign का हिसाब |
| POST | `/api/campaigns/{campaign}/cancel` | बाकी queued मैसेज रद्द |
| GET | `/api/queue/{id}` | एक मैसेज की स्थिति |

### और

| Method | Path | काम |
|---|---|---|
| GET | `/api/sessions/{name}/groups` | groups, communities, `community_announcements` group, आप admin हैं या नहीं |
| GET | `/api/sessions/{name}/channels` | आपके channels; `can_post` = आप owner/admin हैं |
| GET | `/api/sessions/{name}/channels/resolve?invite=<channel link>` | link से channel JID |
| POST | `/api/sessions/{name}/read` | `{"chat":"…","ids":["…"],"sender":"…"}` read mark |
| POST | `/api/sessions/{name}/typing` | `{"to":"…","state":"composing"\|"paused"}` |
| POST | `/api/sessions/{name}/check` | `{"numbers":["…"]}` (1–50, घंटे की सीमा) — WhatsApp पर है या नहीं |
| GET | `/health` | बिना key — uptime/monitoring के लिए |

Error जवाब: `{"ok":false,"code":"…","error":"…"}` — मुख्य codes: `not_connected` (503), `not_on_whatsapp` (422), `timelocked` / `daily_limit` / `new_contact_limit` (429), `session_not_found` (404), `bad_request` (400).

---

## 3. Webhook (website पर आने वाले events)

`.env` में `WEBHOOK_URL` (या हर session का `webhook_url`)। हर POST:

```json
{ "event": "message", "session": "main", "timestamp": 1792214875, "data": { … } }
```

| event | data |
|---|---|
| `message` | `id, chat, chat_phone, chat_type (user/group/channel), from, from_phone, push_name, from_me, type, text, caption, mimetype, file_name, file_size, quoted_id, is_edit` |
| `message.receipt` | `ids, status (delivered/read/played), chat, from` |
| `message.deleted` | `id, chat` |
| `queue.status` | `queue_id, campaign, status (sent/failed), wa_id, to, error` |
| `session.status` | `status, error` |
| `session.paired` | `jid, lid, business_name` |
| `session.timelock` | `active, until` — WhatsApp ने नए contacts पर रोक लगाई |

`type`: `text, image, video, audio, voice, document, sticker, location, contact, reaction, poll, button_reply, list_reply`. Media का सिर्फ़ विवरण आता है, file नहीं।

**Signature जाँचें** (headers `X-Timestamp`, `X-Signature: sha256=<hex>`; HMAC-SHA256 of `timestamp + "." + raw body` with `WEBHOOK_SECRET`):

```php
$raw = file_get_contents('php://input');
$ts  = $_SERVER['HTTP_X_TIMESTAMP'] ?? '';
$ok  = hash_equals('sha256=' . hash_hmac('sha256', $ts . '.' . $raw, getenv('WEBHOOK_SECRET')), $_SERVER['HTTP_X_SIGNATURE'] ?? '')
       && abs(time() - (int)$ts) < 300;
```

```js
// Node / Cloudflare Worker
const mac = await crypto.subtle.importKey('raw', new TextEncoder().encode(SECRET), {name:'HMAC',hash:'SHA-256'}, false, ['sign']);
const sig = await crypto.subtle.sign('HMAC', mac, new TextEncoder().encode(ts + '.' + rawBody));
const ok = req.headers.get('x-signature') === 'sha256=' + [...new Uint8Array(sig)].map(b=>b.toString(16).padStart(2,'0')).join('');
```

Website 2xx न लौटाए तो 3s, 15s, 1m, 5m पर दोबारा भेजा जाता है।

---

## 4. बैन से बचाव — gateway क्या करता है, आप क्या करें

Gateway अपने-आप:
- नया जुड़ा नंबर पहले दिन 20 मैसेज, फिर रोज़ 1.6x बढ़कर `DAILY_LIMIT` (250) तक।
- जिन्होंने कभी मैसेज नहीं किया उन्हें रोज़ अधिकतम `NEW_CONTACT_DAILY_LIMIT` (40)।
- Queue: 30–60 सेकंड random अंतर, हर 20 के बाद 10–15 मिनट break, सिर्फ़ सुबह 9 से रात 9।
- भेजने से पहले online + typing, reply से पहले read; बाद में offline ताकि फ़ोन पर notification आते रहें।
- WhatsApp पर न होने वाले नंबर पहले ही छाँट देता है (30 दिन cache)।
- Error 463/475 या timelock सूचना आते ही नए contacts को भेजना रोकता है; पुराने contacts को भेजना चलता रहता है।

आप:
- सिर्फ़ उन्हें भेजें जिन्होंने अनुमति दी/बात की। लोगों को पहले मैसेज करने दें (`wa.me/91XXXXXXXXXX?text=JOIN` link)।
- नंबर पर profile photo, नाम, about रखें। नया SIM हो तो कुछ दिन फ़ोन पर सामान्य इस्तेमाल के बाद जोड़ें।
- मैसेज में जवाब माँगें ("YES लिखें"), "STOP लिखें तो नहीं भेजेंगे" जोड़ें।
- बड़ी सूचना के लिए Channel / Community announcements बेहतर हैं — एक post सब तक।

---

## 5. Data और backup

सब कुछ Docker volume `gw_data` में `gateway.db` (SQLite) में है: login keys, queue, contacts सूची। Backup:

```bash
docker compose exec gateway sh -c 'cp /data/gateway.db /data/backup.db' && docker compose cp gateway:/data/backup.db ./backup.db
```

---

## 6. Development

```bash
go test ./...
API_KEY=$(openssl rand -hex 24) DATA_DIR=./data go run .
```

whatsmeow की license MPL-2.0 है — उसकी files बदले बिना इस्तेमाल करने पर आपके अपने कोड पर कोई शर्त नहीं लगती।
