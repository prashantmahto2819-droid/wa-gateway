# Vivid Relay

_VividBrandify का WhatsApp gateway और marketing panel — repo: wa-gateway_

QR से WhatsApp नंबर जोड़िए। उसके बाद:
- सुरक्षित रफ़्तार में campaign भेजिए और group, community, channel व Status पर पोस्ट schedule कीजिए।
- Auto-reply और AI जवाब चलाइए।
- अपनी हर app (website, BizCRT, n8n…) को API + webhook से जोड़िए।

इसे दो तरह से चलाया जा सकता है:
- **SaaS:** आपके server पर, ग्राहक signup करें और आप licence दें।
- **Self-hosted:** ग्राहक के अपने server पर, licence key से चालू हो।

- **Engine:** [whatsmeow](https://github.com/tulir/whatsmeow) (Go) — वही library जिस पर Beeper, WAHA (GOWS), GOWA, WuzAPI चलते हैं।
- **हल्का:** एक ~34 MB file, लगभग 30–80 MB RAM। आने वाली फ़ोटो/फ़ाइलें save नहीं होतीं, chat history save नहीं होती।
- **एक command install:** Docker + मुफ़्त HTTPS (Caddy) + crash पर अपने-आप restart।

> ⚠️ यह WhatsApp की official API नहीं है। अनजान लोगों को bulk भेजने से नंबर बैन हो सकता है — gateway जोखिम घटाता है, ख़त्म नहीं करता। OTP जैसे ज़रूरी मैसेज के लिए official Cloud API बेहतर है।

---

## 1. Install (Ubuntu/Debian VPS, 2 vCPU / 2–4 GB RAM सलाह)

1. Domain का A record VPS के IP पर करें (Cloudflare में **DNS only / grey cloud**)।
2. VPS पर:

```bash
apt update && apt install -y git
git clone https://<GITHUB_TOKEN>@github.com/prashantmahto2819-droid/wa-gateway.git
cd wa-gateway
./install.sh relay.example.com
```

3. अंत में दिखा **master key** सँभालकर रखें → `https://relay.example.com/setup` खोलें → master key डालकर अपना **owner account** बनाएँ।
4. Dashboard → **Numbers → Add number** → फ़ोन में WhatsApp → Linked devices → QR scan करें।

| काम | Command |
|---|---|
| Update | `sudo ./update.sh` (WhatsApp ने कुछ बदला हो तो `sudo ./update.sh --whatsmeow`) |
| Backup | `sudo ./backup.sh` → `backups/` में (आख़िरी 14 रखे जाते हैं) |
| Logs | `docker compose logs -f gateway` |
| Settings | `.env` बदलें → `docker compose up -d` |

> फ़ोन को हर 14 दिन में कम-से-कम एक बार इस्तेमाल करें, नहीं तो WhatsApp linked device हटा देता है। एक ही नंबर को दो gateway पर न चलाएँ।

---

## 2. क्या-क्या है

| हिस्सा | सुविधाएँ |
|---|---|
| **Numbers** | QR या pairing code से कई नंबर • हर नंबर का अलग proxy (test के साथ) • call अपने-आप काटना + मैसेज • groups/channels अनदेखा करना • always-online • health (fail rate, reply rate) |
| **Send** | Text (link हो तो **preview card** अपने-आप — title, description, image), फ़ोटो, वीडियो, document, audio/voice, **poll** (vote का हिसाब), location, contact card, **numbered menu** (जवाब "2" → option), **buttons** (reply / link / call / copy — फ़ोटो header के साथ भी), **list menu** (tap करके चुनें), **carousel** (फ़ोटो + buttons वाले 2–10 cards), mention-all, reply/quote, react, edit, delete, **WhatsApp Status** (अभी या schedule — रोज़/हफ़्ते के दिन; कौन देखे: automatic / tag / चुने हुए लोग) • भेजने के लिए **WhatsApp contacts** (saved + unsaved, A–Z, search, letter filter) और **groups/communities/channels** (search, "सिर्फ़ जहाँ मैं post कर सकूँ") में से tick करके चुनें — कई चुनें तो एक-एक करके सुरक्षित अंतर से जाते हैं |
| **Campaigns** | Audience: tags (include/exclude), चुने contacts, पेस्ट किए नंबर, groups, channels, Status • `{{name}}`, `{{city}}` जैसे variables + `{नमस्ते|प्रणाम}` spintax • एक बार / रोज़ / हफ़्ते के दिन / महीने का schedule, "until" तारीख • speed: safe/normal/slow • कई नंबरों में rotation — smart (हर व्यक्ति एक ही नंबर से) या **random नंबर** • **मैसेज के कई versions** (हर व्यक्ति को कोई एक random) • **हर मैसेज के नीचे अलग code** (Ref: 7KQ3XP या #482913; text में `{{ref}}` / `{{random}}` भी) • pause/resume/cancel/duplicate/run-now • CSV report |
| **Contacts (CRM)** | Tags, custom fields, CSV import (Excel CSV, `;` या tab भी) / export, bulk actions, अपने-आप save (जो मैसेज करे), STOP opt-out सूची, group के members को contacts में save |
| **Automation** | Auto-reply: welcome (पहला मैसेज), exact keyword, contains, regex, menu choice, कोई भी मैसेज, away (कामकाजी घंटों के बाहर) • cooldown • **AI जवाब** (OpenAI / Gemini / Groq / OpenRouter — ग्राहक की अपनी key) • human takeover (फ़ोन से जवाब देने पर bot उस chat में चुप) |
| **Groups** | Group/community बनाना, members जोड़ना/हटाना/admin बनाना, invite link, name/description/announce-only/lock, join by link, leave • Channel बनाना और पोस्ट करना • Community announcement group में पोस्ट |
| **Developer** | REST API (`/openapi.json` — Postman/n8n में import) • हर ग्राहक की अपनी API keys • signed webhooks, event चुनकर • `/docs` पर quickstart |
| **Accounts & licence** | Signup + free trial, login, plans (नंबर, रोज़/महीने के मैसेज, contacts, storage, features), licence keys (बेचें/redeem/extend/suspend), admin panel (users, plans, keys, settings, contact-form inbox, audit log) |
| **Website** | Animated home, features, pricing (plans अपने-आप), docs, about, contact form, privacy/terms/refund (वकील से जँचवाएँ) |

**Buttons के बारे में:** gateway WhatsApp के "native flow" तरीके से buttons भेजता है (Evolution API, neonize आदि यही करते हैं) — Android, iPhone और WhatsApp Web पर दिखते हैं (Windows desktop app पर नहीं)। List के लिए पुराना ListMessage इस्तेमाल होता है, जो हर जगह दिखता है। WhatsApp किसी नंबर के लिए buttons मना कर दे (error 405) तो वही मैसेज अपने-आप numbered text menu बनकर जाता है, और जवाब का हिसाब दोनों में एक-सा रहता है — इसलिए automation कभी नहीं अटकती। सबसे भरोसेमंद अब भी **poll** और **numbered menu** हैं।

**Carousel:** Android और WhatsApp Web पर पूरा चलता है; iPhone पर cards दिखते हैं पर button tap न हो सके, इसलिए हर card के text में भी लिखा जाता है कि क्या reply करें, और typed जवाब भी उसी option से मिल जाता है। WhatsApp मना करे तो हर card एक सामान्य फ़ोटो मैसेज बनकर जाता है।

**Link preview:** मैसेज में link हो तो gateway उस page का title, description और image लाकर card बनाता है (जैसे फ़ोन से भेजने पर दिखता है)। एक link एक घंटे तक cache रहता है, इसलिए 1,000 लोगों के campaign में page एक बार ही खुलता है। `"preview": false` से बंद। Image बड़ी साफ़ दिखे इसलिए gateway फ़ोन की तरह एक HQ thumbnail WhatsApp पर upload करता है; WebP image वाली sites भी चलती हैं, और जो site सिर्फ़ Facebook crawler को image देती है उसके लिए दूसरी कोशिश होती है।

**Status:** linked device को हर contact के हर device के लिए Status अलग से encrypt करना पड़ता है, और हज़ारों लोगों की list WhatsApp मना कर देता है ("limit exceeded")। इसलिए हर post ज़्यादा से ज़्यादा `STATUS_MAX_RECIPIENTS` लोगों (default 5000) तक जाती है — पहले वे जो आपसे chat करते हैं, फिर saved contacts, फिर फ़ोन की list। WhatsApp list बड़ी कहकर मना करे तो gateway आधी list से अपने-आप दोबारा भेजता है। नए "LID" वाले contacts (जिनके phone नंबर से नहीं मिलते) भी अब LID से ढूँढे जाते हैं, इसलिए Status ज़्यादा लोगों तक पहुँचता है। Status उन्हीं को दिखता है जिन्होंने आपका नंबर save किया है।

**"Failed" दिखा पर मैसेज चला गया?** अब ऐसा नहीं होगा: मैसेज WhatsApp तक पहुँच जाए पर उसकी पुष्टि देर से आए, तो gateway उसे "sent" मानता है (दोबारा नहीं भेजता, ताकि duplicate न जाए)।

---

## 3. अपनी apps जोड़ना

1. Dashboard → **Developer → API key** बनाएँ (`vr_…`)।
2. हर request में header: `Authorization: Bearer vr_…`

```bash
# तुरंत मैसेज
curl -X POST https://relay.example.com/api/sessions/NUMBER_NAME/send \
  -H "Authorization: Bearer vr_xxx" -H "Content-Type: application/json" \
  -d '{"to":"9876543210","text":"नमस्ते {{name}}, आपका टोकन {{code}} है","vars":{"name":"Asha","code":"4821"}}'

# Campaign (tag वालों को, कल सुबह 10 बजे, हर सोमवार)
curl -X POST https://relay.example.com/api/campaigns -H "Authorization: Bearer vr_xxx" -H "Content-Type: application/json" \
  -d '{"name":"Weekly","sessions":["NUMBER_NAME"],"message":{"text":"Hi {{name}}"},"audience":{"tags":["vip"]},
       "schedule":{"at":1792300000,"repeat":"weekly","days":[1]},"speed":"safe"}'
```

3. **Webhooks:** Developer → Webhook → URL और events चुनें। हर POST पर `X-Signature: sha256=HMAC(secret, X-Timestamp + "." + body)` होता है — जाँचने का PHP/Node कोड `/docs` पर है।
4. **n8n / Make / Zapier:** `https://relay.example.com/openapi.json` import करें; incoming के लिए उनका webhook URL ऊपर जोड़ें।

पूरा API contract: [`docs/API.md`](docs/API.md)। पुराने v1 endpoints (master key से) भी चलते हैं।

---

## 4. Licence

**आपका main server (SaaS + licence server):**
- Admin → **Plans:** कीमत, सीमाएँ और features बदलें। शुरू के plans सिर्फ़ उदाहरण हैं।
- Admin → **Licences → Generate:** key बनाकर ग्राहक को दें, या सीधे किसी user को plan दें।
- ग्राहक **Licence → Redeem** में key डालता है। Renewal पुराने licence के बाद जुड़ता है।
- Settings में signup चालू/बंद, trial के दिन और trial plan, कंपनी का नाम/पता/GSTIN और announcement bar सेट करें।

**Self-hosted ग्राहक:**
1. Admin → Licences → kind **selfhosted** की key बनाएँ। उसमें कितने server चल सकें, वह भी तय करें।
2. Admin → **Licence server** से public key copy करें।
3. ग्राहक के server पर `.env` में ये तीन लाइनें जोड़ें:
   ```
   LICENCE_SERVER=https://relay.vividbrandify.com
   LICENCE_KEY=VRS-…
   LICENCE_PUBLIC_KEY=…
   ```
4. ग्राहक को source code नहीं देना हो तो GitHub Actions से बनी image `ghcr.io/prashantmahto2819-droid/vivid-relay` और `docker-compose.image.yml` इस्तेमाल करें।

Self-hosted server हर 12 घंटे में licence server से जाँच करता है। Server तक न पहुँच पाए तो भी 7 दिन तक चलता रहता है। आप key suspend करें तो अगली जाँच पर बंद हो जाता है। Licence token Ed25519 से signed है, इसलिए नकली token नहीं चलेगा।

⚠️ आपकी `licence_ed25519.seed` (gw_data volume में) ही असली चाबी है। `./backup.sh` नियमित चलाएँ। यह फ़ाइल खो गई तो सारे self-hosted ग्राहकों को नया public key देना पड़ेगा।

---

## 5. बैन से बचाव

**Gateway अपने-आप करता है:**
- **Warm-up:** नया नंबर पहले दिन 20 मैसेज भेजता है, फिर रोज़ 1.6x बढ़ते हुए plan/`DAILY_LIMIT` तक।
- **नए लोगों की सीमा:** जिन्होंने कभी मैसेज नहीं किया, उन्हें रोज़ अलग सीमा (40)।
- **एक व्यक्ति की सीमा:** हफ़्ते में अधिकतम 3 campaign मैसेज।
- **इंसान जैसी रफ़्तार:** मैसेज के बीच 30–60 सेकंड का random अंतर (safe ×2, slow ×4), 20 के बाद लंबा break, सिर्फ़ 9–21 बजे।
- **इंसान जैसा व्यवहार:** typing दिखाना, reply से पहले read, भेजने के बाद offline।
- **गलत नंबर पहले ही छाँट लेता है:** WhatsApp पर न होने वाले नंबरों पर भेजता ही नहीं।
- **रोक लगते ही रुक जाता है:** WhatsApp की 463/475 (नए contacts पर रोक) आते ही नए लोगों को भेजना रोकता है।
- **सेहत खराब होने पर:** मैसेज ज़्यादा fail हों तो queue रोकता है, जवाब कम आएँ तो धीमा हो जाता है।
- **STOP / "बंद करो":** लिखने वाले को फिर कभी campaign नहीं भेजता।
- **जवाब देने वाले पहले:** जिन्होंने जवाब दिया है, उन्हें पहले भेजता है।

**आप करें:**
- सिर्फ़ अनुमति देने वालों को भेजें।
- लोगों को पहले मैसेज करने दें — `wa.me` link या QR दें।
- नंबर पर profile photo और नाम रखें।
- बड़ी सूचनाओं के लिए Channel या Community का इस्तेमाल करें।

---

## 6. Development

```bash
go test ./...
API_KEY=$(openssl rand -hex 24) DATA_DIR=./data go run .
# http://localhost:8080/setup
```

कोड: `session.go` (WhatsApp कनेक्शन), `sender.go` (भेजना + queue), `campaign.go`, `autoreply.go`, `licence.go`, `auth.go`, `contacts.go`, `media.go`, `groups.go`, `admin.go`, `site.go` + `web/` (website और dashboard)।

whatsmeow की license MPL-2.0 है — उसकी files बदले बिना इस्तेमाल करने पर आपके अपने कोड पर कोई शर्त नहीं लगती।
