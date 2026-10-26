# Vivid Relay — API contract (v2)

This file is the single source of truth for the backend, the dashboard (`/app`) and the public site.
All JSON. Success: `{"ok": true, ...}`. Error: `{"ok": false, "code": "<stable_code>", "error": "<human text>"}` with a matching HTTP status.

## Authentication

Three ways, checked in this order:

1. **Master key** — `Authorization: Bearer <API_KEY from .env>` (or `X-API-Key`). The server's own key: it reaches every account's numbers, so automation and support scripts keep working.
2. **User API key** — `Authorization: Bearer vr_<...>` created in *Developer → API keys*. Acts as that user, and only on the numbers the key was tied to (all of the account's numbers when none were chosen).
3. **Browser session** — cookie `vr_session` set by `/auth/login`. Mutating requests (POST/PATCH/PUT/DELETE) from the browser must send header `X-Requested-With: vivid` (CSRF guard). The SPA always sends it.

Roles: `superadmin` (`/api/admin/*`, plus every account's data in the admin pages), `user` (only own data).

**Numbers belong to one account.** Every logged-in account — the super admin included — can list, send from and configure only its own numbers; anything else answers 404, so another account's number is never even revealed. A customer's number is reachable for support in `/api/admin/numbers` (reconnect, pause / resume — no sending, no chats). Only the `.env` master key crosses accounts.
Unauthenticated → 401 `unauthorized`. Forbidden → 403 `forbidden`. Licence problems → 402 `licence_required` / `licence_expired` / 403 `feature_locked` / 429 `limit_reached` (body has `limit` name).

### Auth endpoints (no auth needed)

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/auth/state` | – | `{ok, needs_setup, signup_open, mode, logged_in, user?}` |
| POST | `/auth/setup` | `{api_key, name, email, password}` | first super-admin; only when no users exist; sets cookie; `{ok, user}` |
| POST | `/auth/signup` | `{name, email, phone, password}` | only if signup_open; starts trial; sets cookie; `{ok, user}` |
| POST | `/auth/login` | `{email, password}` | sets cookie; `{ok, user}`; 401 `bad_credentials`; 429 `too_many_attempts` |
| POST | `/auth/logout` | – | clears cookie |

Passwords: min 8 chars. Sessions last 30 days.

## Objects

```jsonc
User      { "id":1, "email":"a@b.com", "name":"Asha", "phone":"9198…", "role":"superadmin|user", "status":"active|disabled", "created_at":0, "last_login":0 }
Plan      { "id":1, "name":"Starter", "price":499, "currency":"INR", "period_days":30,
            "max_numbers":1, "daily_limit":250, "monthly_messages":5000, "max_contacts":5000, "storage_mb":200,
            "features":{"campaigns":true,"scheduler":true,"autoreply":true,"pool":false,"api":true,"webhooks":true,"groups":true,"status":true,"polls":true},
            "public":true, "trial":false, "sort":1, "description":"..." , "highlight":false }
Licence   { "source":"licence|trial|admin|selfhosted|none", "status":"active|expired|suspended|none",
            "plan":Plan|null, "key":"VR-XXXX-…-1A2B"(masked), "starts_at":0, "expires_at":0, "days_left":12 }
Usage     { "numbers":1, "messages_month":120, "contacts":340, "storage_mb":12.5 }
Session   { "name":"u3-main", "label":"Main", "owner_id":3, "status":"connected|connecting|disconnected|qr|qr_expired|logged_out|replaced|temp_banned|outdated|new",
            "error":"", "jid":"9198…@s.whatsapp.net", "phone":"9198…", "push_name":"", "paused":false, "pause_reason":"",
            "proxy":"socks5://user@host:port", "linked_at":0, "timelock_until":0, "banned_until":0,
            "today":{"sent":3,"limit":250,"new_contacts":1,"new_contact_limit":40},
            "health":{"state":"ok|slow|paused","fail_rate":0,"reply_rate":0,"campaign_sent":0,"campaign_failed":0,"new_contact_sent":0,"new_contact_replied":0,"reason":""} }
Message   { // used by send, templates, campaigns, auto-replies (exactly one main kind)
            "text":"Hi {{name}}", "media":{"type":"image|video|document|audio","url":"https://…" | "media_id":12, "filename":"a.pdf"},
            "poll":{"name":"Q?","options":["A","B"],"multiple":false},
            "location":{"lat":21.1,"lng":81.6,"name":"","address":""},
            "contact":{"name":"Asha","phone":"9198…"},
            "menu":{"title":"Choose","footer":"Reply with a number","options":[{"id":"price","text":"Price list"}]},
            "buttons":{"title":"Diwali offer","footer":"Valid till Friday","buttons":[{"type":"reply","id":"yes","text":"Interested"},{"type":"reply","id":"no","text":"Not now"}]},
            //   button types: reply {id,text} | url {text,url} | call {text,phone} | copy {text,code}; max 3; reply and url/call/copy can't be mixed;
            //   buttons may be combined with "media" (image/video/document header). If WhatsApp refuses buttons (405) the gateway sends a numbered text menu instead → response has fallback:true
            "list":{"title":"Menu","button_text":"Choose","footer":"","sections":[{"title":"Food","rows":[{"id":"thali","title":"Thali","description":"₹120"}]}]},  // max 10 rows total, needs "text"
            "carousel":{"cards":[{"title":"Thali","body":"₹120","footer":"","media":{"type":"image","media_id":12},"buttons":[{"type":"reply","id":"thali","text":"Order"}]}, {"title":"Dosa","body":"₹80","media":{"type":"image","url":"https://…/dosa.jpg"},"buttons":[{"type":"url","text":"Menu","url":"https://…"}]}]},
            //   carousel: 2–10 cards, each: title/body/footer, image (or video), up to 3 buttons (same rules as "buttons"); "text" is the intro above the cards.
            //   Renders on Android + WhatsApp Web; iPhone shows cards but may not allow taps (card text also says what to reply). Refused → each card sent as a photo message (fallback:true).
            "preview":true,  // text messages: fetch title/description/image of the first link and attach a preview card (default true)
            "mentions":["9198…"], "mention_all":false, "reply_to":{"id":"","sender":"","text":""}, "vars":{}, "typing":true }
Contact   { "id":1, "phone":"919876543210", "name":"", "email":"", "tags":["vip"], "notes":"", "custom":{"city":"Korba"},
            "opted_out":false, "source":"manual|import|whatsapp|group", "created_at":0, "updated_at":0, "last_seen":0 }
Media     { "id":1, "name":"offer.jpg", "mime":"image/jpeg", "size":12345, "kind":"image|video|document|audio", "url":"/api/media/1/file", "created_at":0 }
Template  { "id":1, "name":"Diwali offer", "message":Message, "created_at":0, "updated_at":0 }
Campaign  { "id":1, "name":"", "status":"draft|scheduled|running|paused|done|cancelled", "sessions":["u3-main"],
            "message":Message, "audience":{"contacts":[1,2],"tags":["vip"],"exclude_tags":[],"phones":["98…"],"groups":["…@g.us"],"channels":["…@newsletter"],"statuses":false},
            "schedule":{"at":0,"repeat":"none|daily|weekly|monthly","days":[1,3,5],"until":0,"next_run":0,"runs":0},
            "speed":"safe|normal|slow", "created_at":0, "started_at":0, "finished_at":0,
            "stats":{"total":0,"queued":0,"sending":0,"sent":0,"failed":0,"skipped":0,"cancelled":0} }
AutoReply { "id":1, "name":"", "session":"" /* "" = all my numbers */, "trigger":"welcome|keyword|contains|regex|menu|any|away",
            "keywords":["price"], "reply":Message, "ai":false, "ai_prompt":"You are the assistant of …" /* if ai=true the reply text is generated by the user's AI settings using this prompt; reply is then ignored */,
            "only_dm":true, "cooldown_min":60,
            "hours":{"from":9,"to":21,"days":[1,2,3,4,5,6]} /* for away: outside these hours */, "active":true, "priority":10, "hits":0, "created_at":0 }
SessionSettings { "reject_calls":false, "reject_message":"Sorry, calls are not answered on this number.", "ignore_groups":false, "ignore_channels":true,
            "auto_save_contacts":true, "always_online":false, "takeover_minutes":30 /* auto-replies pause in a chat for N min after a human replies from the phone */ }
Webhook   { "id":1, "url":"https://…", "events":["message","message.receipt", "*"], "secret":"whsec_…(masked)", "active":true, "last_status":200, "last_at":0, "created_at":0 }
ApiKey    { "id":1, "name":"BizCRT", "prefix":"vr_ab12", "created_at":0, "last_used":0 }
```

## Account

| Method | Path | Notes |
|---|---|---|
| GET | `/api/me` | `{ok, user, licence:Licence, usage:Usage, mode:"saas|client"}` |
| PATCH | `/api/me` | `{name?, phone?}` |
| POST | `/api/me/password` | `{current, new}` |
| GET | `/api/licence` | `{ok, licence, usage}` |
| POST | `/api/licence/redeem` | `{key}` → activates a licence key for me; `{ok, licence}` |
| GET/POST | `/api/keys` | list / create `{name, sessions?:["number-name"]}` → `{ok, key:ApiKey, secret:"vr_…"}` (shown once). `sessions` ties the key to those numbers; left out or empty it may use every number of the account, including ones linked later |
| DELETE | `/api/keys/{id}` | |
| GET/POST | `/api/webhooks` | list / create `{url, events[]}` → includes full `secret` once |
| PATCH/DELETE | `/api/webhooks/{id}` | `{url?, events?, active?}` |
| POST | `/api/webhooks/{id}/test` | sends `{"event":"test"}` |
| GET/PUT | `/api/me/pacing` | the account's sending pace: `{gap_min, gap_max, batch_size, batch_pause_min, api_gap_min, api_gap_max, typing, reply_typing, reply_delay_min, reply_delay_max}` (seconds / minutes; 0 = inherit; `batch_size:-1` = no breaks). GET also returns `effective`, `defaults`, `floor_gap`, `floor_api_gap`, `can_change` |
| GET/PATCH | `/api/me/ai` | AI settings for AI auto-replies: GET `{ok, base_url, model, has_key}`; PATCH `{base_url?, model?, api_key?}` (any OpenAI-compatible endpoint: OpenAI, Gemini, Groq, OpenRouter…). `POST /api/me/ai/test {prompt}` → `{ok, reply}` |
| GET | `/api/stats?days=14` | `{ok, days:[{day:"2026-10-17",sent,failed,received}], totals:{sent,failed,received}}` |

## WhatsApp numbers (sessions)

Users see only their own sessions. `name` is generated for users (`u<id>-<slug>`); `label` is the display name.

| Method | Path | Notes |
|---|---|---|
| GET | `/api/sessions` | `{ok, sessions:[Session]}` |
| POST | `/api/sessions` | `{label, name?, webhook_url?}` → `{ok, session}` (licence: max_numbers) |
| GET/PATCH/DELETE | `/api/sessions/{name}` | PATCH `{label?, webhook_url?, paused?, proxy?, settings?:SessionSettings}`; Session objects also include `settings` |
| POST/GET | `/api/sessions/{name}/qr` | `{ok, status, qr?, qr_image?}` |
| POST | `/api/sessions/{name}/pair-code` | `{phone}` → `{ok, code}` |
| POST | `/api/sessions/{name}/logout` · `/reconnect` · `/proxy-test` | |

## Sending

| Method | Path | Body |
|---|---|---|
| POST | `/api/sessions/{name}/send` | `Message + {to}` → `{ok, message:{id,to,timestamp,new_contact,unconfirmed?}}` — `unconfirmed:true` means the message left the gateway but WhatsApp's acknowledgement was late; it is not resent (no duplicates) |
| POST | `/api/sessions/{name}/react` | `{chat, id, sender?, emoji}` (`""` removes) |
| POST | `/api/sessions/{name}/edit` | `{chat, id, text}` |
| POST | `/api/sessions/{name}/delete` | `{chat, id, sender?}` (delete for everyone) |
| POST | `/api/sessions/{name}/status` | `{text?, media?, background?:"#25D366", status_audience?}` → posts a WhatsApp Status (story); response `message.recipients` = how many people it was shared with |
| POST | `/api/sessions/{name}/read` · `/typing` · `/check` | as v1 |
| POST | `/api/sessions/{name}/queue`, `/api/queue` | v1 bulk queue (kept for compatibility); body `{campaign:"label", messages[] | template+recipients[], pool?[], random?:false, send_after?}` (`random:true` = random number from the pool per person) |
| GET / POST | `/api/queue/campaigns/{label}` · `/api/queue/campaigns/{label}/cancel` | counts / cancel for a v1 queue label |
| GET | `/api/queue/{id}` | one queued message |
| GET | `/api/link-preview?url=&session=&fresh=1` | what the link's preview card will contain: `{title, description, image, has_image, thumb (data URL), upload:"ok|failed", advice?, error?}` |

`to` may be a phone, `…@s.whatsapp.net`, group `…@g.us`, channel `…@newsletter`, or `contact:<id>`.

**Message variants:** any Message may carry `variants:[Message,…]` (up to 9). Each recipient gets the main message or one variant at random; `to`, `vars` and `reply_to` always come from the main message. Spintax `{Hi|Hello}` still works inside each version.

**Unique code:** `unique_code:"ref"` adds a line like `Ref: 7KQ3XP` under the text (or caption / button / list body), `"number"` adds `#482913`; `unique_prefix` changes the label. Every recipient gets a different code. `{{ref}}` and `{{random}}` can also be used anywhere in the text. Not added to polls, locations, contact cards or Status.

**Status audience:** a linked device must encrypt a Status separately for every contact device, and WhatsApp refuses very large lists, so each post goes to at most `STATUS_MAX_RECIPIENTS` people (default 30000, max 50000; also `status_max` in admin pacing). The audience is prepared slowly in the background (device lookups and encryption sessions in batches of 200 contacts every 20 s, starting 10 minutes after connect, then daily; `GET/POST /api/sessions/{name}/status-prep` shows progress / starts it now). If WhatsApp refuses the size, the gateway retries with the first half of the list; with `status_audience.split:true` the rest gets the same Status in further parts (response `message.parts`). Large posts that take longer than 150 s answer `202 {pending:true}` and finish in the background. Every contact's devices are looked up in batches, and contacts on WhatsApp's newer LID identity (whose devices don't show up by phone number) are addressed by LID, so they are reached too; people with no WhatsApp devices are left out. `status_audience:{mode, tags?, phones?, limit?}` — `mode:"auto"` (default: people who wrote to this number most recently, then your saved contacts, then the phone's named contacts A–Z), `"tags"` (contacts with any of `tags`) or `"phones"` (exactly `phones`). If the phone's Status privacy is "Only share with…", WhatsApp's own list is used instead.
`menu` is sent as a numbered list ("1. Price list…"); when the person replies with the number, the `message` webhook includes `menu_choice:{id,text}` and `menu`-trigger auto-replies fire.
`buttons` and `list` are real interactive messages (native flow / list). A tap arrives as a `message` webhook with `type:"button_reply"|"list_reply"`, `button_id` and `menu_choice:{id,text}`, so the same `menu`-trigger auto-replies work for menus, buttons and lists. Not available for channels or Status.

### Files that follow a message

`then` on any message (Send, API, campaigns, templates, auto-replies) adds up to 5 files that go to the same person right after it, each its own WhatsApp message with its own caption:

```json
{"to":"919876543210","text":"आज का ऑफ़र: https://shop.example.com/sale","unique_code":"ref",
 "then":[{"media":{"type":"document","media_id":12,"filename":"rates.pdf"},"text":"रेट लिस्ट"},
         {"media":{"type":"image","media_id":13}}]}
```

They are sent by the same number, in order, with the account's API gap between them, and each gets its own unique code (a follow-up inherits `unique_code` / `unique_prefix` unless it sets its own). A follow-up leaves `to` empty and cannot carry `then` or `variants` of its own. In the queue a recipient still gets one row — so the per-person weekly limit, opt-outs and duplicate checks count one delivery, not several.

The send result carries `then: [{id} | {error}]`. If the main message was sent, the row is never retried even when a file failed — a retry would deliver the message twice. Queued files must use `media.url` or `media.media_id`, not base64.

## Groups, communities, channels

| Method | Path | Notes |
|---|---|---|
| GET | `/api/sessions/{name}/groups` | `{ok, groups:[{jid,name,type:"group|community|community_announcements",participants,announce_only,i_am_admin,can_post,community?}]}` — `can_post` is false where only admins may send and you aren't one |
| POST | `/api/sessions/{name}/groups` | `{name, participants[], community?:false}` |
| GET | `/api/sessions/{name}/groups/{jid}` | `{ok, group:{jid,name,topic,participants:[{jid,phone,is_admin,is_super_admin}],…}}` |
| POST | `/api/sessions/{name}/groups/{jid}/participants` | `{action:"add|remove|promote|demote", participants[]}` |
| GET | `/api/sessions/{name}/groups/{jid}/invite?reset=0` | `{ok, link}` |
| POST | `/api/sessions/{name}/groups/{jid}/settings` | `{name?, topic?, announce_only?, locked?}` |
| POST | `/api/sessions/{name}/groups/{jid}/leave` | |
| POST | `/api/sessions/{name}/groups/join` | `{link}` |
| POST | `/api/sessions/{name}/groups/{jid}/save-contacts` | `{tag}` → members saved as contacts `{ok, saved}` |
| GET | `/api/sessions/{name}/channels` | `{ok, channels:[{jid,name,role,can_post,subscribers,invite}]}` |
| POST | `/api/sessions/{name}/channels` | `{name, description}` creates a channel |
| GET | `/api/sessions/{name}/channels/resolve?invite=` | |
| GET | `/api/sessions/{name}/wa-contacts` | the linked phone's address book: `{ok, contacts:[{jid,phone,name,push_name,business?,saved}], total, saved}` sorted A–Z (saved name, else the person's own WhatsApp name). Appears a few minutes after linking, once the phone has synced |

## Contacts

| Method | Path | Notes |
|---|---|---|
| GET | `/api/contacts?q=&tag=&opted_out=&source=&filter=&page=1&limit=50` | `{ok, contacts:[Contact], total, page, limit}`. `filter`: `new` (7 days), `active` (wrote in 30 days), `inactive` (silent 90+ days), `noname`, `duplicates` (same name on several numbers), `untagged`, `noemail` |
| POST | `/api/contacts` | Contact fields (phone required) → upsert by phone |
| PATCH/DELETE | `/api/contacts/{id}` | |
| POST | `/api/contacts/bulk` | `{ids[], action:"add_tag|remove_tag|delete|opt_out|opt_in", tag?}` |
| POST | `/api/contacts/import` | multipart `file` (CSV) + optional `tag`, or JSON `{rows:[{phone,name,email,tags,…}], tag}` → `{ok, created, updated, invalid}` |
| GET | `/api/contacts/export.csv?tag=` | CSV download |
| GET | `/api/contacts/tags` | `{ok, tags:[{tag,count}]}` |
| GET | `/api/contacts/stats` | counts for every smart filter and per source |
| POST | `/api/contacts/merge` | `{keep:id, remove:[ids]}` — tags, notes, custom fields, follow-ups, renewals and tasks move to `keep`; other numbers kept as `other_phone` |
| GET | `/api/contacts/export.vcf?tag=&ids=` | vCard 3.0 (UTF-8) for phones / Google Contacts |

CSV columns: `phone,name,email,tags,notes` + any extra column becomes a custom field (`{{city}}` in messages).

## Saved send lists

| Method | Path | Notes |
|---|---|---|
| GET/POST | `/api/lists` | POST `{name, description?, items:[{to, name?}]}` — `to` is a phone, `contact:<id>`, `…@g.us` (group / community announcements) or `…@newsletter`; duplicates and invalid entries are dropped; up to 50,000 items |
| GET/PATCH/DELETE | `/api/lists/{id}` | PATCH `{name?, items?}` replaces, or `{add:[…], remove:["to",…]}` changes in place |
| POST | `/api/sessions/{name}/members-to-list` | `{groups:[jid], list_id? \| name?}` adds the members' numbers (hidden LID-only members are counted in `hidden`) |

Campaign `audience.lists:[id]` sends to everyone in the lists; lists are read when each run starts, so repeating campaigns pick up changes. A phone item's `name` fills `{{name}}`.

## Media library

| Method | Path | Notes |
|---|---|---|
| GET | `/api/media` | `{ok, media:[Media], used_mb, limit_mb}` |
| POST | `/api/media` | multipart `file` (≤ MAX_MEDIA_MB) → `{ok, media}` |
| GET | `/api/media/{id}/file` | the file (auth) |
| DELETE | `/api/media/{id}` | |

## Templates, campaigns, auto-replies

| Method | Path | Notes |
|---|---|---|
| GET/POST | `/api/templates` | `{name, message}` |
| PATCH/DELETE | `/api/templates/{id}` | |
| GET/POST | `/api/campaigns` | POST `{name, sessions[], message, audience, schedule?, speed?, rotation?:"smart|random", draft?:false}`; without `schedule.at` it starts now. `audience.statuses:true` with a repeating `schedule` = scheduled / daily Status posts |
| GET | `/api/campaigns/{id}` | `{ok, campaign, recent:[{id,to,status,error,sent_at}]}` |
| PATCH/DELETE | `/api/campaigns/{id}` | edit only while draft/scheduled |
| POST | `/api/campaigns/{id}/pause` · `/resume` · `/cancel` · `/duplicate` · `/run-now` | |
| GET | `/api/campaigns/{id}/report.csv` | |
| GET/POST | `/api/autoreplies` | |
| PATCH/DELETE | `/api/autoreplies/{id}` | |

Rotation (several `sessions`): `smart` keeps each person on one number (the one they chatted with, else the least busy); `random` picks a random number per person; `turns` sends one message at a time across the numbers in order (1 → 2 → 3 → 1 …) with the campaign gap between consecutive messages — a number that misses its turn for 45 s (offline, at its limit, quiet hours) is skipped for 10 minutes. `gap_min`/`gap_max` (seconds) set the campaign's own gap and replace `speed`; they can't be below the admin floor. Groups, channels and Status are always posted by the first number.

Speed: `safe` = delays ×2, `normal` = configured delays, `slow` = ×4. Repeat: `daily`, `weekly` (`days`: 0=Sun…6=Sat), `monthly` (same date), stops after `until`.

## Business Kit

A kit is a ready-made setup for one kind of business (30 kits: insurance, clinic, school, salon, news portal …). Applying one writes auto-replies (welcome menu + one answer per question), follow-up sequences, renewal types, checklist tasks and public forms into the workspace; everything can be edited afterwards. Re-applying refreshes what the kit made and keeps hand-made items.

| Method | Path | Notes |
|---|---|---|
| GET | `/api/kits` · `/api/kits/{id}` | catalogue (groups, counts) and one full kit |
| GET/PATCH | `/api/business` | profile `{kit, subs[], name, phone, city, code, fields[], forms[], stages}` + `counts`; `form_base` is the public form URL prefix |
| POST | `/api/business/apply` | `{kit, subs[], name?, phone?, city?, session?}` → `{created:{autoreplies, sequences, renewal_types, tasks}}` |
| GET | `/api/business/entries?form=` | filled forms |
| GET | `/f/{code}/{key}` | public enquiry form (no auth; POST creates a contact tagged `form:<key>`, a task, starts `form` / `lead_new` sequences, fires `form.entry`) |

### Follow-up sequences

| Method | Path | Notes |
|---|---|---|
| GET/POST | `/api/sequences` | `{name, trigger:"lead_new|form|tag|stage|manual", trigger_value?, business_days, stop_on_reply, session?, active, steps:[{day, action:"auto_whatsapp|whatsapp|call|task", title, message?}]}` |
| PATCH/DELETE | `/api/sequences/{id}` | |
| POST | `/api/sequences/{id}/enroll` | `{contacts:[ids], phones:[…], list_id?}` → `{started, people}` (one active run per person per sequence) |
| GET | `/api/sequences/runs?status=active|done|stopped|replied|all&sequence=` | |
| DELETE | `/api/sequences/runs/{id}` | stop a run |

`auto_whatsapp` steps go through the normal outbox (pacing, turns and daily limits apply); the other kinds become tasks. Day-0 steps run at once, later steps at 10:30 local; `business_days` skips Sundays. `{{name}} {{phone}} {{business}} {{business_phone}} {{city}}` plus contact fields.

### Renewals

| Method | Path | Notes |
|---|---|---|
| GET/POST | `/api/renewal-types` · PATCH/DELETE `/api/renewal-types/{id}` | `{label, icon, frequency:"yearly|half_yearly|quarterly|monthly|weekly|once", remind_before:[30,7,1], remind_after:[7], staff_task, message, fields[], session?}` |
| GET | `/api/renewals?status=open|done|lost|all&type=&q=&due=overdue|week|month` | |
| POST/PATCH/DELETE | `/api/renewals` · `/api/renewals/{id}` | `{type_id, contact_id? | phone, name?, title, amount?, due (unix), fields{}, status, note}`; marking a recurring one `done` returns `next` |

Reminders go out from 9 am local on the configured days with `{{name}} {{title}} {{amount}} {{due}} {{business}}`; `staff_task` also creates a task on the first reminder day.

### Tasks

| Method | Path | Notes |
|---|---|---|
| GET | `/api/tasks?view=today|upcoming|done|all` | agents see unassigned tasks and their own |
| POST/PATCH/DELETE | `/api/tasks` · `/api/tasks/{id}` | `{title, due, repeat:"|daily|weekly|monthly", at_time, done, assignee_id, phone, note}`; a repeating task marked done rolls to its next occurrence |

## Chatbot flows

| Method | Path | Notes |
|---|---|---|
| GET/POST | `/api/flows` · PATCH/DELETE `/api/flows/{id}` | `{name, trigger:"keyword|contains|welcome|any|manual", keywords[], session?, priority, active, test_mode, test_phones[], graph:{nodes:[{id,type,data}], edges:[{source,port,target}]}}`; saving returns `problems[]` and refuses `active:true` while problems remain |
| POST | `/api/flows/{id}/test` | `{text?, button_id?, reset?, graph?}` sandbox: nothing is sent, API steps and tags are skipped → `{replies:[SendRequest], events, waiting_at, vars, done}` |
| GET | `/api/flows/{id}/logs?chat=` | execution log + `in_progress` |

Node types: `start`; `message` (`data.message.type` = `text` | `buttons` ≤3 | `list` ≤10, waits for a tap when it has choices); `question` (`text, var, kind:"text|number|phone|email|date", retry`); `condition` (`var, op:"exists|equals|contains|gt|lt|tag", value` → ports `yes`/`no`); `tag` (`add[], remove[]`); `api` (`url` https, `method`, `body` template, `headers[]`, `save:{var:"json.path"}` → ports `ok`/`fail`); `assign` (`minutes, note` — pauses the bot in that chat and fires `flow.handover`); `end`. Ports: `next`, a button/row id, `else`, `yes`, `no`, `ok`, `fail`. A chat is in at most one flow; flows run before auto-replies; collected variables are saved on the contact when the flow ends.

## Link Publisher & websites

| Method | Path | Notes |
|---|---|---|
| GET/POST | `/api/websites` · PATCH/DELETE `/api/websites/{id}` | `{name, url, text (default template), active, routes:[{category, session, targets:[{to,name}], text, auto}]}`; POST (and `PATCH {rotate_secret:true}`) returns `key` and `secret` once |
| POST | `/api/inbound` | **signed, no login**: headers `X-Relay-Key`, `X-Relay-Timestamp` (unix s, ±5 min), `X-Relay-Signature: sha256=HMAC_SHA256(secret, timestamp + "." + rawBody)`; body `{id, event, title, summary, text?, url, image?, category?, audience?, auto?}` → `{ok, id, status:"pending|queued|duplicate"}` |
| GET | `/api/inbound/ping` | same headers → `{ok, website, routes}` |
| GET | `/api/posts?status=pending|queued|rejected|all&q=` | `pending` count included |
| POST | `/api/posts` | bulk `{urls:[…], text?, session?, targets?, category?, fetch:true, approve:false}` (≤100 links; metadata fetched when `fetch`) |
| PATCH | `/api/posts/{id}` | `{title, summary, text, image, url, session, category, targets, refetch}` |
| POST | `/api/posts/{id}/approve` · `/reject` | approve `{targets?, session?, text?}` queues through the outbox (label `post<id>`); stats per post come from the outbox |
| DELETE | `/api/posts/{id}` | |

Targets: a phone, `contact:<id>`, `list:<id>`, `…@g.us`, `…@newsletter` or `status`. Templates: `{{title}} {{summary}} {{url}} {{image}} {{site}} {{category}}`. A route's `auto` only skips approval when the website also sends `auto:true`. Events: `post.pending`, `post.queued`, `form.entry`, `flow.handover`.

## Proxies and quotas

| Method | Path | Notes |
|---|---|---|
| GET | `/api/proxies` | pool proxies this workspace may attach (`masked` only) + `own_allowed` |
| PATCH | `/api/sessions/{name}` | `{proxy_id}` attaches a pool proxy (0 = none); `{proxy:"socks5://…"}` an own URL when allowed; `settings.daily_limit` = the number's own cap |
| GET/POST | `/api/admin/proxies` · PATCH/DELETE `/api/admin/proxies/{id}` · POST `/{id}/test` | `{label, url, kind:"shared|dedicated", assigned_to, country, note, active}`; `POST /api/admin/proxies/settings {own_allowed}` |

Quota engine (`PUT /api/admin/pacing`): `warmup_start` (day-1 allowance), `warmup_growth_pct` (per day), `daily_limit` (ceiling), `new_contact_limit`; a plan's `daily_limit` and the number's own cap can only lower it. Every session carries `quota:{day, warmup_limit, effective_limit, tomorrow, full_in_days, …}`.

Plan `limits` (entitlements, 0 = unlimited): `staff`, `websites`, `flows`, `sequences`, `lists`.

## Admin (super-admin only)

`GET/PUT /api/admin/pacing` — default pace for every account plus hard floors: the pacing fields above and `floor_gap`, `floor_api_gap` (seconds nobody may go below) and `users_can_change`.


| Method | Path | Notes |
|---|---|---|
| GET | `/api/admin/overview` | `{ok, users, active_licences, sessions, connected, sent_today, sent_month, messages_unread}` |
| GET | `/api/admin/users?q=` | `{ok, users:[User & {licence, sessions}]}` |
| PATCH | `/api/admin/users/{id}` | `{role?, status?, name?}` |
| POST | `/api/admin/users/{id}/reset-password` | `{ok, password}` (temporary) |
| POST | `/api/admin/users/{id}/grant` | `{plan_id, days}` → creates & activates a licence for the user |
| DELETE | `/api/admin/users/{id}` | |
| GET/POST | `/api/admin/plans` · PATCH/DELETE `/api/admin/plans/{id}` | Plan fields |
| GET | `/api/admin/licences?status=&kind=` | `{ok, licences:[{id,key,kind,status,plan_id,plan_name,user_id,user_email,days,max_instances,activations,created_at,activated_at,expires_at,notes}]}` |
| POST | `/api/admin/licences` | `{plan_id, kind:"saas|selfhosted", days, count:1, max_instances:1, user_email?, notes?}` → `{ok, licences:[…with full key]}` |
| PATCH | `/api/admin/licences/{id}` | `{status?:"active|suspended", extend_days?, expires_at?, user_email?, notes?}` |
| DELETE | `/api/admin/licences/{id}` | |
| GET | `/api/admin/activations` | self-hosted installs `{ok, activations:[{licence_id,key,instance_id,domain,version,ip,first_seen,last_seen}]}` |
| GET/PATCH | `/api/admin/settings` | Settings |
| GET | `/api/admin/inbox` · DELETE `/api/admin/inbox/{id}` · POST `/api/admin/inbox/{id}/read` | contact-form messages |
| GET | `/api/admin/audit?limit=200` | `{ok, entries:[{id,user_id,user_email,action,detail,ip,at}]}` (logins, key/licence/plan changes, deletions) |
| GET | `/api/admin/licence-server` | `{ok, public_key, server_url}` (give to self-hosted buyers) |

Settings: `{site_name, tagline, company_name, support_email, support_phone, whatsapp_number, address, gstin, signup_open, trial_days, trial_plan_id, currency, payment_note, announcement}`

## Public

| Method | Path | Notes |
|---|---|---|
| GET | `/public/plans` | public plans |
| GET | `/public/settings` | public subset of settings |
| POST | `/public/contact` | `{name, email, phone, message}` (rate-limited) |
| POST | `/licence/v1/activate` | self-hosted install → `{key, instance_id, domain, version}` → `{ok, token}` (Ed25519-signed) |
| GET | `/openapi.json` | machine-readable API |

## Webhook events

Payload `{event, session, owner_id, timestamp, data}`; headers `X-Event`, `X-Timestamp`, `X-Signature: sha256=HMAC(secret, timestamp + "." + body)`.
`message` data also carries `menu_choice:{id,text}` when the text answers a menu we sent. `poll.vote` data: `{poll_id, chat, voter, voter_phone, options:["A"]}`. `call` data: `{from, from_phone, video, rejected}`.
Events: `call`, `message`, `message.receipt`, `message.deleted`, `poll.vote`, `queue.status`, `campaign.status`, `session.status`, `session.paired`, `session.timelock`, `session.health`, `contact.optout`, `contact.optin`, `group.update`, `form.entry`, `flow.handover`, `post.pending`, `post.queued`, `test`.
User webhooks get only events of their own sessions; the global `.env` WEBHOOK_URL gets everything.
