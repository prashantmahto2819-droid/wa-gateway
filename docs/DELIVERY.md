# Vivid Relay — Business Kit release: delivery report

Date: 24 Oct 2026 · Repo `prashantmahto2819-droid/wa-gateway`, branch `main`.
Update a running server with `sudo ./update.sh` (new tables are created automatically on start).

## What was built (this round)

| Area | Where in the dashboard | What it does |
|---|---|---|
| Workspaces, staff, roles | Account → Team | Owner = workspace. Invite by link (no e-mail server needed), roles admin / manager / supervisor / agent / analyst, seat limit from the plan. |
| Business Kit | Business → Business Kit | 30 kits (BizCRT's 29 ported as-is + a new **News portal / media / publisher** kit). Pick a kit + sub-categories → auto-replies (welcome menu + one answer per customer question; "menu / hi / help" shows it again), follow-up sequences, renewal types, checklist tasks, lead fields, public forms. Re-apply refreshes kit items and keeps hand-made ones. |
| Follow-up sequences | Business Kit → Follow-ups | Day-based plans; automatic WhatsApp steps go through the normal outbox (pacing / turns / limits apply), call / task steps become tasks; a reply can stop a run; enrol from phones or a saved list; forms start the "new lead" sequence. |
| Renewals | Business Kit → Renewals | Dated reminders N days before / after (policy premium, subscription, AMC, birthday …); done → next period created; staff task on the first reminder day. |
| Tasks | Business Kit → Today | Checklist items from the kit (daily / weekly / monthly, roll forward when done), sequence calls, renewal calls, form entries, your own. Agents see unassigned + their own. |
| Public enquiry forms | Business Kit → Forms | `/f/<code>/<form>` — plain page, no JS, works everywhere; creates a contact (tagged `form:<key>`), a task, starts the follow-up, fires `form.entry`; thank-you page with a wa.me button. |
| Chatbot flows | Content → Chatbot flows | BizCRT's flow graph engine in Go (message text/buttons/list, question with typed answers, condition, tag, API call, hand-over, end). Structured editor (no canvas needed), lint before going live, sandbox test chat, test mode for chosen numbers, execution logs. Flows run before auto-replies. |
| Link Publisher + website registry | Business → Link Publisher | Register a website → key + secret; signed inbound webhook (`POST /api/inbound`) with timestamp check and de-duplication; category routes (which number, which lists/groups/channels/Status, template, optional auto-send); approval inbox; bulk URL paste with metadata fetch; history with per-post delivery stats. |
| Multi-number routing | Publisher routes · sequence / renewal "send from" · auto-reply "number" | Each automation can be pinned to a number or use the pool's smart pick. |
| Quota engine | Admin → Site settings → Sending safety · Numbers → Settings | Admin-set warm-up start, growth % per day, ceiling, new-contact limit; plan cap and the number's own cap only lower it; every number card shows the warm-up day and tomorrow's allowance. |
| Proxy pool | Admin → Proxy pool · Numbers → Proxy | Admin keeps proxies (shared / dedicated to a user), tests them; users attach without seeing credentials; own URLs can be disabled; re-assignment detaches numbers. |
| Contact intelligence | Audience → Contacts | Smart filters (new, active, silent 90+ d, no name, duplicate names, untagged), source filter, merge, VCF export, CSV (UTF-8 with BOM). |
| Plan entitlements | Admin → Plans | `limits`: staff seats, websites, flows, sequences, saved lists (0 = unlimited). |
| Admin control center | Admin → overview | New tiles (posts awaiting approval, active flows, proxies, paused numbers) and a "Needs attention" list (banned / outdated / auto-paused numbers, licences expiring in 7 days, failed proxies). |

## Decisions taken with you

- **Chat text is not stored** — no Shared Inbox was built. Auto-replies, flows and sequences work on the message as it arrives; only the position in a flow and the collected answers are kept.
- **All 29 BizCRT kits** were ported (data file `kits/kits.json`), plus the news/media kit you asked for.
- **Charameti integration = generic signed webhook** (Cloudflare Workers / custom code). The prompt for Charameti's side was given in chat; the contract is in `docs/API.md` → "Link Publisher & websites".
- **Staff join by invite link** (no SMTP needed).

## Honest notes

- A proxy only changes the IP WhatsApp sees for that number's gateway connection. It does not make an unofficial gateway official; warm-up, pacing and sending to people who know you still matter most.
- Buttons / lists need WhatsApp on a phone (not the Windows desktop app); a numbered text fallback always goes along, and typed answers ("2") work in flows too.
- Status reach depends on who saved your number; the background preparation and "split in parts" help, they cannot force WhatsApp to show it to everyone.
- Sequence and renewal messages are sent by the gateway's queue, so they land only while the number is linked and within its daily allowance.

## Tests

`go test -race ./...` — 2 new suites this round on top of the existing ones: kits / sequences / renewals / tasks / forms, publisher (signature, duplicate, update, auto route, approve, bulk, rotate), flows (sandbox, matching, test mode, helpers), quota + proxy pool, contact filters / merge / VCF. UI checked in a headless browser against mock WhatsApp data (desktop + 390 px mobile).

## Where things live (for the next developer)

| File | Contents |
|---|---|
| `kits.go`, `kits/kits.json` | kit catalogue, business profile, apply |
| `sequences.go` | sequences, runs, renewals, tasks, scheduler hooks (`runSequences`, `runRenewals`) |
| `forms.go` | public forms |
| `flows.go` | flow graph, engine, sandbox, API |
| `publisher.go` | websites, signed inbound, posts, targets expansion |
| `proxies.go` | proxy pool |
| `pacing.go` / `sender.go` | quota engine (`quotaFor`, `quotaInfo`) |
| `team.go` | workspaces, roles, invites, `countCheck` entitlements |
| `web/static/app/{kit,publisher,flows}.js` | the three new pages |
| `docs/API.md`, `/openapi.json` | endpoint reference |

## Not done / next

- Charameti-side code (you are prompting it there with the contract above).
- Shared inbox (by decision), payment gateway, e-mail sending.
- Hindi translations cover the new pages' main labels; long help texts fall back to English.
