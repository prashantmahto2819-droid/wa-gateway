import { api, html, raw, ic, t, fmtNum, fmtDate, relTime, pill, campaignTone, skel, empty, toast, confirmBox, getSessions, sessionName, fmtPhone, parsePhones, debounce, toLocalInput, fromLocalInput, fmtDuration, sessionStatus, $$ } from '/static/app/core.js';
import { createComposer, summarize } from '/static/app/composer.js';
import { loadTemplate } from '/static/app/send.js';

const STATUSES = ['all', 'draft', 'scheduled', 'running', 'paused', 'done', 'cancelled'];
const STATUS_LABEL = { draft: 'Draft', scheduled: 'Scheduled', running: 'Running', paused: 'Paused', done: 'Done', cancelled: 'Cancelled' };
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
export const SPEED = { safe: 2, normal: 1, slow: 4 };

export async function render(ctx) {
  if (ctx.params.view === 'new') return wizard(ctx);
  if (ctx.params.view === 'detail') return detail(ctx);
  return list(ctx);
}

export function progressBar(s = {}) {
  const tot = s.total || 0;
  const p = (n) => (tot ? (n / tot) * 100 : 0);
  return html`<div class="progress" role="progressbar" aria-label="${t('Progress')}" aria-valuemin="0" aria-valuemax="${tot}" aria-valuenow="${(s.sent || 0) + (s.failed || 0) + (s.skipped || 0)}">
    <span class="p-sent" style="width:${p(s.sent || 0)}%"></span><span class="p-fail" style="width:${p(s.failed || 0)}%"></span><span class="p-skip" style="width:${p((s.skipped || 0) + (s.cancelled || 0))}%"></span></div>`;
}
export const campaignPill = (c) => pill(t(STATUS_LABEL[c.status] || c.status), campaignTone(c.status));

// ------------------------------------------------------------------ list
async function list(ctx) {
  const { root } = ctx;
  let filter = ctx.query.get('status') || 'all', q = '';
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Campaigns')}</h1><p>${t('Bulk messages that go out slowly and safely, one by one, with random gaps.')}</p></div>
      <div class="page-actions"><a class="btn primary" href="/app/campaigns/new" data-link>${ic('plus', 16)}${t('New campaign')}</a></div></div>
    <div class="row mb"><div class="seg" role="tablist" aria-label="${t('Filter by status')}">${STATUSES.map((s) => html`<button role="tab" data-f="${s}" aria-selected="${s === filter}">${s === 'all' ? t('All') : t(STATUS_LABEL[s])}</button>`)}</div>
      <div class="search grow" style="max-width:280px;margin-left:auto">${ic('search', 16)}<input type="search" data-q placeholder="${t('Search campaigns')}" aria-label="${t('Search campaigns')}"></div></div>
    <section class="panel" data-list>${raw('<div class="panel-body">' + skel(5).s + '</div>')}</section></div>`.s;
  let items = [];
  async function load() {
    const j = await api('GET', '/api/campaigns', undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    items = j.ok ? j.campaigns || [] : []; draw();
  }
  function draw() {
    const box = root.querySelector('[data-list]');
    const l = items.filter((c) => (filter === 'all' || c.status === filter) && (!q || (c.name || '').toLowerCase().includes(q)));
    if (!items.length) { box.innerHTML = empty('megaphone', t('No campaigns yet'), t('Create a campaign to message a tag, a list of numbers, or your groups — safely, over time.'), html`<a class="btn primary" href="/app/campaigns/new" data-link>${ic('plus', 16)}${t('New campaign')}</a>`).s; return; }
    if (!l.length) { box.innerHTML = empty('filter', t('Nothing matches this filter')).s; return; }
    box.innerHTML = html`<div class="table-wrap"><table class="t stack-sm"><thead><tr><th>${t('Campaign')}</th><th>${t('Status')}</th><th style="min-width:200px">${t('Progress')}</th><th class="hide-sm">${t('Numbers')}</th><th>${t('When')}</th></tr></thead><tbody>
      ${l.map((c) => { const s = c.stats || {}; return html`<tr>
        <td><a href="/app/campaigns/${c.id}" data-link class="cell-main">${c.name || t('Untitled')}</a><div class="cell-sub ellipsis" style="max-width:320px">${summarize(c.message)}</div></td>
        <td>${campaignPill(c)}</td>
        <td>${progressBar(s)}<div class="cell-sub mt-s tnum">${t('{a} of {b} sent', { a: fmtNum(s.sent || 0), b: fmtNum(s.total || 0) })}${s.failed ? ' · ' + t('{n} failed', { n: fmtNum(s.failed) }) : ''}</div></td>
        <td class="hide-sm"><span class="cell-sub">${(c.sessions || []).length}</span></td>
        <td class="nowrap cell-sub">${c.status === 'scheduled' && c.schedule?.next_run ? html`${ic('clock', 13)} ${fmtDate(c.schedule.next_run)}` : relTime(c.started_at || c.created_at)}</td></tr>`; })}
      </tbody></table></div>`.s;
  }
  root.addEventListener('click', (e) => { const b = e.target.closest('[data-f]'); if (b) { filter = b.dataset.f; root.querySelectorAll('[data-f]').forEach((x) => x.setAttribute('aria-selected', String(x === b))); draw(); } });
  root.querySelector('[data-q]').addEventListener('input', debounce((e) => { q = e.target.value.toLowerCase(); draw(); }, 150));
  ctx.every(10000, () => { if (items.some((c) => c.status === 'running')) load(); });
  await load();
}

// ------------------------------------------------------------------ wizard
const STEPS = ['Name & numbers', 'Message', 'Audience', 'Schedule & speed', 'Review'];

async function wizard(ctx) {
  const { root } = ctx;
  ctx.setTitle(t('New campaign'), html`<a href="/app/campaigns" data-link>${t('Campaigns')}</a> ${ic('chevron', 14)} <span>${t('New campaign')}</span>`);
  const [sessions, tagsJ] = await Promise.all([getSessions(true), api('GET', '/api/contacts/tags', undefined, { silent: true })]);
  if (!ctx.isCurrent()) return;
  const tags = tagsJ.ok ? tagsJ.tags || [] : [];
  const dup = sessionStorage.getItem('vr_campaign_draft');
  sessionStorage.removeItem('vr_campaign_draft');
  let base = {};
  try { base = dup ? JSON.parse(dup) : {}; } catch { /* */ }
  const startStep = Number(ctx.query.get('step')) || 1;
  const w = {
    step: Math.min(5, Math.max(1, startStep)),
    name: base.name || '', sessions: base.sessions || sessions.filter((s) => s.status === 'connected').slice(0, 1).map((s) => s.name),
    message: base.message || { text: '' },
    aud: { contacts: [], tags: [], exclude_tags: [], phones: [], groups: [], channels: [], statuses: false, ...(base.audience || {}) },
    contactObjs: [],
    schedule: { mode: 'now', at: 0, repeat: 'none', days: [1, 2, 3, 4, 5], until: 0, ...(base.schedule || {}) },
    speed: base.speed || 'safe',
    groupsList: null,
  };
  if (w.schedule.at) w.schedule.mode = 'later';
  let comp = null;

  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('New campaign')}</h1><p>${t('Five short steps. You can review everything before it starts.')}</p></div></div>
    <div class="wizard">
      <ol class="wsteps" aria-label="${t('Steps')}">${STEPS.map((s, i) => html`<li data-si="${i + 1}"><button type="button" data-go="${i + 1}"><span class="n">${i + 1}</span>${t(s)}</button></li>`)}</ol>
      <div><section class="panel"><div class="panel-body" data-body></div></section>
        <div class="wfoot"><button class="btn" data-a="back">${t('Back')}</button><span class="grow"></span><button class="btn ghost" data-a="draft">${t('Save as draft')}</button><button class="btn primary" data-a="next">${t('Continue')}</button></div></div>
    </div></div>`.s;
  const body = root.querySelector('[data-body]');

  function stepper() {
    $$('.wsteps li', root).forEach((li) => {
      const i = +li.dataset.si;
      li.className = i === w.step ? 'cur' : i < w.step ? 'done' : '';
      li.querySelector('button').setAttribute('aria-current', i === w.step ? 'step' : 'false');
      li.querySelector('.n').innerHTML = i < w.step ? raw(ic('check', 14)).s : String(i);
    });
    root.querySelector('[data-a="back"]').style.visibility = w.step === 1 ? 'hidden' : '';
    const nx = root.querySelector('[data-a="next"]');
    nx.innerHTML = w.step === 5 ? html`${ic('send', 16)}${w.schedule.mode === 'later' ? t('Schedule campaign') : t('Start campaign')}`.s : t('Continue');
  }

  function draw() {
    collect();
    stepper();
    if (w.step === 1) {
      body.innerHTML = html`<div class="stack">
        <label class="field" style="max-width:520px"><span>${t('Campaign name')}</span><input name="cname" required value="${w.name}" placeholder="${t('Diwali offer — VIP customers')}"></label>
        <fieldset><legend>${t('Send from')}</legend>
          <p class="hint" style="margin:-6px 0 10px">${t('Pick more than one number to share the load (number pool). Each number keeps its own daily limit.')}</p>
          ${sessions.length ? html`<div class="choice-grid">${sessions.map((s) => { const st = sessionStatus(s.status); return html`<label class="choice"><input type="checkbox" name="sess" value="${s.name}" ${w.sessions.includes(s.name) ? 'checked' : ''}>
            <b>${ic('phone', 16)}${sessionName(s)}</b><small>${s.phone ? fmtPhone(s.phone) : t('Not linked')}</small><span>${pill(st.label, st.tone)}</span></label>`; })}</div>`
          : empty('phone', t('No numbers yet'), '', html`<a class="btn" href="/app/numbers" data-link>${t('Add number')}</a>`)}
        </fieldset></div>`.s;
      body.querySelector('[name=cname]').focus();
    } else if (w.step === 2) {
      body.innerHTML = html`<div class="row between mb"><h2>${t('Message')}</h2><button class="btn sm ghost" data-a="tpl">${ic('template', 15)}${t('Load a template')}</button></div><div data-comp></div>`.s;
      comp = createComposer(body.querySelector('[data-comp]'), { message: w.message, groupMode: w.aud.groups.length > 0, customVars: ['city'] });
    } else if (w.step === 3) {
      drawAudience();
    } else if (w.step === 4) {
      const sc = w.schedule;
      body.innerHTML = html`<div class="stack" style="gap:22px">
        <fieldset><legend>${t('When')}</legend><div class="choice-grid">
          <label class="choice"><input type="radio" name="when" value="now" ${sc.mode === 'now' ? 'checked' : ''}><b>${ic('bolt', 16)}${t('Start now')}</b><small>${t('Begins as soon as you confirm.')}</small></label>
          <label class="choice"><input type="radio" name="when" value="later" ${sc.mode === 'later' ? 'checked' : ''}><b>${ic('calendar', 16)}${t('Pick date and time')}</b><small>${t('Runs once or repeats.')}</small></label></div></fieldset>
        <div data-later class="stack" ${sc.mode === 'later' ? '' : 'hidden'}>
          <div class="form-grid">
            <label class="field"><span>${t('First run')}</span><input type="datetime-local" name="at" value="${toLocalInput(sc.at)}"></label>
            <label class="field"><span>${t('Repeat')}</span><select name="repeat">${['none', 'daily', 'weekly', 'monthly'].map((r) => html`<option value="${r}" ${sc.repeat === r ? 'selected' : ''}>${t({ none: 'Does not repeat', daily: 'Every day', weekly: 'Every week on…', monthly: 'Every month (same date)' }[r])}</option>`)}</select></label></div>
          <div data-days ${sc.repeat === 'weekly' ? '' : 'hidden'}><span class="field-label">${t('On these days')}</span>
            <div class="days-pick mt-s">${DAYS.map((d, i) => html`<label><input type="checkbox" name="days" value="${i}" ${sc.days.includes(i) ? 'checked' : ''}><span>${t(d)}</span></label>`)}</div></div>
          <label class="field" data-until ${sc.repeat !== 'none' ? '' : 'hidden'} style="max-width:260px"><span>${t('Stop repeating after')} <small>(${t('optional')})</small></span><input type="date" name="until" value="${sc.until ? new Date(sc.until * 1000).toISOString().slice(0, 10) : ''}"></label>
        </div>
        <fieldset><legend>${t('Speed')}</legend><div class="choice-grid">
          ${[['safe', 'shield', 'Safe', 'Recommended. Waits about twice the normal gap between messages.'], ['normal', 'send', 'Normal', 'Uses the server’s standard gaps. For warm, engaged lists.'], ['slow', 'clock', 'Very slow', 'Four times the gap. Best for new numbers and cold lists.']]
            .map(([k, i, l, d]) => html`<label class="choice"><input type="radio" name="speed" value="${k}" ${w.speed === k ? 'checked' : ''}><b>${ic(i, 16)}${t(l)}</b><small>${t(d)}</small></label>`)}</div></fieldset></div>`.s;
    } else if (w.step === 5) {
      drawReview();
    }
    body.closest('.panel').scrollIntoView({ block: 'nearest' });
  }

  async function drawAudience() {
    const a = w.aud;
    body.innerHTML = html`<div class="stack" style="gap:22px">
      <fieldset><legend>${t('Contacts by tag')}</legend>
        <p class="hint" style="margin:-6px 0 10px">${t('Click once to include, again to exclude, a third time to clear. Opted-out contacts are always skipped.')}</p>
        ${tags.length ? html`<div class="tag-pick" data-tags>${tags.map((x) => html`<button type="button" data-tag="${x.tag}" class="${a.tags.includes(x.tag) ? 'inc' : a.exclude_tags.includes(x.tag) ? 'exc' : ''}" aria-pressed="${a.tags.includes(x.tag) ? 'true' : a.exclude_tags.includes(x.tag) ? 'mixed' : 'false'}">${x.tag}<span class="badge">${fmtNum(x.count)}</span></button>`)}</div>`
          : html`<p class="muted">${t('No tags yet. Import contacts with a tag in Contacts.')}</p>`}</fieldset>
      <fieldset><legend>${t('Specific contacts')}</legend>
        <div class="search" style="max-width:480px">${ic('search', 16)}<input type="search" data-cs placeholder="${t('Search by name or phone')}" aria-label="${t('Search contacts')}"></div>
        <div data-cres class="mt-s"></div><div class="row mt-s" data-cpicked></div></fieldset>
      <fieldset><legend>${t('Phone numbers')}</legend>
        <textarea name="phones" rows="3" placeholder="919876543210&#10;919812345678">${a.phones.join('\n')}</textarea>
        <p class="hint">${t('Paste numbers with country code, one per line or separated by commas.')} <span data-pc></span></p></fieldset>
      <fieldset><legend>${t('Groups and channels')}</legend><div data-gl>${skel(2)}</div></fieldset>
      <fieldset><legend>${t('Your status')}</legend>${raw(`<label class="switch"><input type="checkbox" name="statuses" ${a.statuses ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${t('Also post this message to my Status')}</span><small>${t('Text and photo messages only.')}</small></span></label>`)}</fieldset>
      <div class="note info" data-aud-sum></div></div>`.s;
    drawPicked(); audSummary();
    body.querySelector('[name=phones]').addEventListener('input', audSummary);
    const s = w.sessions[0];
    if (!s) { body.querySelector('[data-gl]').innerHTML = html`<p class="muted">${t('Choose a number in step 1 to see its groups.')}</p>`.s; return; }
    if (!w.groupsList) {
      const [g, c] = await Promise.all([api('GET', `/api/sessions/${encodeURIComponent(s)}/groups`, undefined, { silent: true }), api('GET', `/api/sessions/${encodeURIComponent(s)}/channels`, undefined, { silent: true })]);
      w.groupsList = [...(g.ok ? g.groups || [] : []).filter((x) => x.type !== 'community').map((x) => ({ ...x, kind: 'group' })), ...(c.ok ? c.channels || [] : []).filter((x) => x.can_post).map((x) => ({ ...x, kind: 'channel' }))];
    }
    if (!ctx.isCurrent() || w.step !== 3) return;
    const gl = body.querySelector('[data-gl]');
    gl.innerHTML = w.groupsList.length ? html`<div class="target-list">${w.groupsList.map((x) => html`<label><input type="checkbox" name="${x.kind === 'channel' ? 'chan' : 'grp'}" value="${x.jid}" ${(x.kind === 'channel' ? a.channels : a.groups).includes(x.jid) ? 'checked' : ''}>
      ${ic(x.kind === 'channel' ? 'channel' : 'group', 16)}<span class="grow"><b class="ellipsis" style="display:block">${x.name}</b><small class="muted">${x.kind === 'channel' ? t('Channel') + ' · ' + t('{n} followers', { n: x.subscribers ?? 0 }) : (x.type === 'community_announcements' ? t('Community announcements') : t('Group')) + ' · ' + t('{n} members', { n: x.participants ?? 0 })}</small></span></label>`)}</div>`.s
      : html`<p class="muted">${t('This number is not in any groups or channels you can post to.')}</p>`.s;
  }
  function drawPicked() {
    const box = body.querySelector('[data-cpicked]'); if (!box) return;
    box.innerHTML = w.contactObjs.map((c) => html`<span class="chip">${c.name || fmtPhone(c.phone)}<button type="button" data-rmc="${c.id}" aria-label="${t('Remove')}">${ic('x')}</button></span>`.s).join('');
  }
  function audSummary() {
    collect();
    const a = w.aud; const tagCount = tags.filter((x) => a.tags.includes(x.tag)).reduce((s, x) => s + x.count, 0);
    const el = body.querySelector('[data-aud-sum]'); if (!el) return;
    el.innerHTML = html`${ic('users', 16)}<span>${t('About {n} recipients', { n: fmtNum(estimateTotal()) })} — ${t('{a} from tags, {b} contacts, {c} numbers, {d} groups/channels', { a: fmtNum(tagCount), b: a.contacts.length, c: a.phones.length, d: a.groups.length + a.channels.length })}${a.exclude_tags.length ? ' · ' + t('excluding {t}', { t: a.exclude_tags.join(', ') }) : ''}</span>`.s;
    const pc = body.querySelector('[data-pc]'); if (pc) pc.textContent = a.phones.length ? t('{n} valid numbers', { n: a.phones.length }) : '';
  }
  function estimateTotal() {
    const a = w.aud;
    return tags.filter((x) => a.tags.includes(x.tag)).reduce((s, x) => s + x.count, 0) + a.contacts.length + a.phones.length + a.groups.length + a.channels.length + (a.statuses ? 1 : 0);
  }

  function drawReview() {
    const a = w.aud, sc = w.schedule;
    const n = estimateTotal();
    const pool = Math.max(1, w.sessions.length);
    const secs = (n * 45 * SPEED[w.speed]) / pool;
    const whenTxt = sc.mode === 'now' ? t('Starts right away') : fmtDate(sc.at) + (sc.repeat !== 'none' ? ' · ' + t({ daily: 'repeats daily', weekly: 'repeats weekly', monthly: 'repeats monthly' }[sc.repeat]) + (sc.repeat === 'weekly' ? ' (' + sc.days.map((d) => t(DAYS[d])).join(', ') + ')' : '') + (sc.until ? ' ' + t('until {d}', { d: fmtDate(sc.until, false) }) : '') : '');
    body.innerHTML = html`<div class="stack" style="gap:20px">
      <div class="estimate"><div><small>${t('Recipients')}</small><br><b class="tnum">${fmtNum(n)}</b></div><div><small>${t('Estimated time')}</small><br><b>${fmtDuration(secs)}</b></div><div><small>${t('Numbers sharing the load')}</small><br><b>${pool}</b></div>
        <small style="flex-basis:100%">${t('Based on 30–60 seconds between messages × speed, split across your numbers. Daily limits and quiet hours can make it longer.')}</small></div>
      <div class="grid g2" style="align-items:start">
        <dl class="summary">
          <div><dt>${t('Name')}</dt><dd>${w.name}</dd></div>
          <div><dt>${t('From')}</dt><dd>${w.sessions.map((s) => sessionName(sessions.find((x) => x.name === s) || { name: s })).join(', ')}</dd></div>
          <div><dt>${t('Audience')}</dt><dd>${[a.tags.length && t('Tags') + ': ' + a.tags.join(', '), a.exclude_tags.length && t('Excluding') + ': ' + a.exclude_tags.join(', '), a.contacts.length && t('{n} contacts', { n: a.contacts.length }), a.phones.length && t('{n} numbers', { n: a.phones.length }), a.groups.length && t('{n} groups', { n: a.groups.length }), a.channels.length && t('{n} channels', { n: a.channels.length }), a.statuses && t('My status')].filter(Boolean).join(' · ') || '—'}</dd></div>
          <div><dt>${t('When')}</dt><dd>${whenTxt}</dd></div>
          <div><dt>${t('Speed')}</dt><dd>${t({ safe: 'Safe', normal: 'Normal', slow: 'Very slow' }[w.speed])}</dd></div>
        </dl>
        <div><p class="preview-label">${ic('eye', 14)}${t('Message preview')}</p><div class="chat"><div class="chat-body" data-prev></div></div></div>
      </div>
      <div class="panel panel-pad" style="background:var(--card-2)"><h3 class="mb">${t('Before you start')}</h3><ul class="tips">
        <li>${t('Send only to people who know you or asked to hear from you. Reports from strangers get numbers banned.')}</li>
        <li>${t('Personalise with {{name}} and spintax so messages are not identical.')}</li>
        <li>${t('New numbers: start with 20–40 messages a day and grow slowly over 2–3 weeks.')}</li>
        <li>${t('Replies protect your number. Ask a question people will answer.')}</li>
        <li>${t('People who reply STOP are opted out automatically.')}</li></ul></div></div>`.s;
    import('/static/app/composer.js').then(({ bubbleHTML }) => { const p = body.querySelector('[data-prev]'); if (p) p.innerHTML = bubbleHTML(w.message).s; });
  }

  function collect() {
    const f = (n) => body.querySelector(`[name="${n}"]`);
    if (f('cname')) w.name = f('cname').value.trim();
    if (body.querySelector('[name=sess]')) w.sessions = $$('[name=sess]:checked', body).map((x) => x.value);
    if (comp && w.step === 2) w.message = comp.get();
    if (f('phones')) w.aud.phones = parsePhones(f('phones').value);
    if (body.querySelector('[data-gl] input')) { w.aud.groups = $$('[name=grp]:checked', body).map((x) => x.value); w.aud.channels = $$('[name=chan]:checked', body).map((x) => x.value); }
    if (f('statuses')) w.aud.statuses = f('statuses').checked;
    if (body.querySelector('[name=when]')) {
      w.schedule.mode = body.querySelector('[name=when]:checked')?.value || 'now';
      w.schedule.at = fromLocalInput(f('at').value);
      w.schedule.repeat = f('repeat').value;
      w.schedule.days = $$('[name=days]:checked', body).map((x) => +x.value);
      w.schedule.until = f('until').value ? Math.floor(new Date(f('until').value + 'T23:59:59').getTime() / 1000) : 0;
      w.speed = body.querySelector('[name=speed]:checked')?.value || 'safe';
    }
  }
  function check(step) {
    if (step === 1) { if (!w.name) return t('Give the campaign a name.'); if (!w.sessions.length) return t('Choose at least one number.'); }
    if (step === 2 && comp) { const e = comp.validate(); if (e) return e; }
    if (step === 3 && !estimateTotal()) return t('Add at least one recipient.');
    if (step === 4 && w.schedule.mode === 'later') {
      if (!w.schedule.at) return t('Pick a date and time.');
      if (w.schedule.at < Date.now() / 1000 - 60) return t('That time is in the past.');
      if (w.schedule.repeat === 'weekly' && !w.schedule.days.length) return t('Pick at least one day.');
    }
    return '';
  }
  function payload(draft) {
    const sc = w.schedule;
    const p = { name: w.name || t('Untitled'), sessions: w.sessions, message: w.message, audience: { ...w.aud }, speed: w.speed, draft: !!draft };
    if (sc.mode === 'later') p.schedule = { at: sc.at, repeat: sc.repeat, days: sc.repeat === 'weekly' ? sc.days : [], until: sc.until || 0 };
    return p;
  }

  body.addEventListener('click', async (e) => {
    const tg = e.target.closest('[data-tag]');
    if (tg) {
      const k = tg.dataset.tag, a = w.aud;
      if (a.tags.includes(k)) { a.tags = a.tags.filter((x) => x !== k); a.exclude_tags.push(k); tg.className = 'exc'; tg.setAttribute('aria-pressed', 'mixed'); }
      else if (a.exclude_tags.includes(k)) { a.exclude_tags = a.exclude_tags.filter((x) => x !== k); tg.className = ''; tg.setAttribute('aria-pressed', 'false'); }
      else { a.tags.push(k); tg.className = 'inc'; tg.setAttribute('aria-pressed', 'true'); }
      audSummary(); return;
    }
    const rm = e.target.closest('[data-rmc]');
    if (rm) { const id = +rm.dataset.rmc; w.aud.contacts = w.aud.contacts.filter((x) => x !== id); w.contactObjs = w.contactObjs.filter((x) => x.id !== id); drawPicked(); audSummary(); return; }
    const add = e.target.closest('[data-addc]');
    if (add) { const c = body.querySelector('[data-cres]')._list.find((x) => String(x.id) === add.dataset.addc); if (c && !w.aud.contacts.includes(c.id)) { w.aud.contacts.push(c.id); w.contactObjs.push(c); drawPicked(); audSummary(); } return; }
    if (e.target.closest('[data-a="tpl"]')) loadTemplate(comp);
  });
  body.addEventListener('change', (e) => {
    if (e.target.name === 'when') body.querySelector('[data-later]').hidden = e.target.value !== 'later';
    if (e.target.name === 'repeat') { body.querySelector('[data-days]').hidden = e.target.value !== 'weekly'; body.querySelector('[data-until]').hidden = e.target.value === 'none'; }
    if (['grp', 'chan', 'statuses'].includes(e.target.name)) audSummary();
  });
  body.addEventListener('input', debounce(async (e) => {
    if (!e.target.matches('[data-cs]')) return;
    const q = e.target.value.trim(); const r = body.querySelector('[data-cres]');
    if (!q) { r.innerHTML = ''; return; }
    const j = await api('GET', `/api/contacts?limit=8&q=${encodeURIComponent(q)}`, undefined, { silent: true });
    r._list = j.ok ? j.contacts || [] : [];
    r.innerHTML = r._list.length ? html`<div class="target-list">${r._list.map((c) => html`<label><span class="grow"><b>${c.name || fmtPhone(c.phone)}</b> <small class="muted">${fmtPhone(c.phone)}</small></span><button type="button" class="btn sm" data-addc="${c.id}">${ic('plus', 14)}${t('Add')}</button></label>`)}</div>`.s : html`<p class="muted">${t('No contacts match.')}</p>`.s;
  }, 250));

  root.querySelector('.wizard').addEventListener('click', async (e) => {
    const go = e.target.closest('[data-go]');
    if (go) { collect(); const to = +go.dataset.go; if (to > w.step) { for (let s = w.step; s < to; s++) { const err = check(s); if (err) { w.step = s; draw(); return toast(err, 'warn'); } } } w.step = to; draw(); return; }
    const b = e.target.closest('[data-a]'); if (!b || b.closest('[data-body]')) return;
    if (b.dataset.a === 'back') { collect(); w.step = Math.max(1, w.step - 1); draw(); }
    if (b.dataset.a === 'next') {
      collect(); const err = check(w.step); if (err) return toast(err, 'warn');
      if (w.step < 5) { w.step++; draw(); return; }
      b.classList.add('busy');
      const j = await api('POST', '/api/campaigns', payload(false));
      b.classList.remove('busy');
      if (j.ok) { toast(w.schedule.mode === 'later' ? t('Campaign scheduled') : t('Campaign started')); ctx.navigate('/app/campaigns/' + (j.campaign?.id ?? '')); }
    }
    if (b.dataset.a === 'draft') {
      collect(); if (!w.name) return toast(t('Give the campaign a name.'), 'warn');
      const j = await api('POST', '/api/campaigns', payload(true));
      if (j.ok) { toast(t('Draft saved')); ctx.navigate('/app/campaigns/' + (j.campaign?.id ?? '')); }
    }
  });
  draw();
}

// ------------------------------------------------------------------ detail
async function detail(ctx) {
  const { root, params } = ctx;
  const id = params.id;
  root.innerHTML = html`<div class="page">${skel(6)}</div>`.s;
  let c = null;
  async function load(first) {
    const j = await api('GET', `/api/campaigns/${id}`, undefined, { silent: !first });
    if (!ctx.isCurrent()) return;
    if (!j.ok) { if (first) root.innerHTML = html`<div class="page">${empty('megaphone', t('Campaign not found'), '', html`<a class="btn" href="/app/campaigns" data-link>${t('Back to campaigns')}</a>`)}</div>`.s; return; }
    c = j.campaign; draw(j.recent || []);
  }
  function draw(recent) {
    ctx.setTitle(c.name || t('Campaign'), html`<a href="/app/campaigns" data-link>${t('Campaigns')}</a> ${ic('chevron', 14)} <span>${c.name}</span>`);
    const s = c.stats || {}, sc = c.schedule || {}, a = c.audience || {};
    const done = (s.sent || 0) + (s.failed || 0) + (s.skipped || 0) + (s.cancelled || 0);
    const remain = Math.max(0, (s.total || 0) - done);
    const actions = [];
    if (c.status === 'running') actions.push(['pause', 'pause', 'Pause']);
    if (c.status === 'paused') actions.push(['resume', 'play', 'Resume']);
    if (['scheduled', 'draft'].includes(c.status)) actions.push(['run-now', 'bolt', 'Run now']);
    actions.push(['duplicate', 'copy', 'Duplicate']);
    if (['running', 'paused', 'scheduled', 'draft'].includes(c.status)) actions.push(['cancel', 'x', 'Cancel']);
    root.innerHTML = html`<div class="page">
      <div class="page-head"><div><div class="row" style="gap:12px"><h1>${c.name || t('Untitled')}</h1>${campaignPill(c)}</div>
        <p>${t('Created {d}', { d: fmtDate(c.created_at) })}${c.started_at ? ' · ' + t('started {d}', { d: fmtDate(c.started_at) }) : ''}${c.finished_at ? ' · ' + t('finished {d}', { d: fmtDate(c.finished_at) }) : ''}</p></div>
        <div class="page-actions">${actions.map(([k, i, l]) => html`<button class="btn ${k === 'cancel' ? 'danger' : ''}" data-a="${k}">${ic(i, 16)}${t(l)}</button>`)}
          <a class="btn" href="/api/campaigns/${c.id}/report.csv" download>${ic('download', 16)}${t('Report CSV')}</a></div></div>
      ${c.status === 'running' ? html`<div class="note ok mb">${ic('refresh', 16)}<span>${t('Live — updates every 5 seconds.')} ${remain ? t('About {d} left.', { d: fmtDuration((remain * 45 * (SPEED[c.speed] || 1)) / Math.max(1, (c.sessions || []).length)) }) : ''}</span></div>` : ''}
      <div class="grid g4">${[['Total', s.total, null], ['Sent', s.sent, 'var(--c-sent)'], ['Failed', s.failed, 'var(--c-fail)'], ['Waiting', (s.queued || 0) + (s.sending || 0), 'var(--line-strong)']].map(([l, v, col]) => html`<div class="panel"><div class="kpi"><span class="k-label">${col ? raw(`<span class="swatch" style="background:${col}"></span>`) : ''}${t(l)}</span><span class="k-val">${fmtNum(v || 0)}</span></div></div>`)}</div>
      <div class="panel panel-pad mt">${progressBar(s)}<div class="row between mt-s cell-sub"><span>${t('{p}% done', { p: s.total ? Math.round((done / s.total) * 100) : 0 })}</span><span>${s.skipped ? t('{n} skipped (opted out or invalid)', { n: s.skipped }) : ''}</span></div></div>
      <div class="split mt">
        <section class="panel"><div class="panel-head"><h2>${t('Recent messages')}</h2></div>
          ${recent.length ? html`<div class="table-wrap"><table class="t"><thead><tr><th>${t('To')}</th><th>${t('Status')}</th><th>${t('Time')}</th></tr></thead><tbody>${recent.map((r) => html`<tr><td class="mono">${String(r.to).includes('@') ? r.to : fmtPhone(r.to)}</td>
            <td>${pill(t(r.status), r.status === 'sent' ? 'ok' : r.status === 'failed' ? 'bad' : r.status === 'skipped' ? 'muted' : 'info')}${r.error ? html`<div class="cell-sub">${r.error}</div>` : ''}</td><td class="cell-sub nowrap">${r.sent_at ? relTime(r.sent_at) : '—'}</td></tr>`)}</tbody></table></div>`
          : empty('send', t('Nothing sent yet'), c.status === 'scheduled' ? t('Messages appear here once the campaign starts.') : '')}</section>
        <div class="stack">
          <section class="panel"><div class="panel-head"><h2>${t('Details')}</h2></div><div class="panel-body"><dl class="kv">
            <dt>${t('Numbers')}</dt><dd>${(c.sessions || []).join(', ')}</dd>
            <dt>${t('Speed')}</dt><dd>${t({ safe: 'Safe', normal: 'Normal', slow: 'Very slow' }[c.speed] || c.speed || '—')}</dd>
            <dt>${t('Schedule')}</dt><dd>${sc.at ? fmtDate(sc.at) : t('Immediately')}${sc.repeat && sc.repeat !== 'none' ? ' · ' + t(sc.repeat) : ''}</dd>
            ${sc.next_run ? html`<dt>${t('Next run')}</dt><dd>${fmtDate(sc.next_run)}</dd>` : ''}
            ${sc.runs ? html`<dt>${t('Runs so far')}</dt><dd>${sc.runs}</dd>` : ''}
            <dt>${t('Audience')}</dt><dd>${[a.tags?.length && t('Tags') + ': ' + a.tags.join(', '), a.exclude_tags?.length && t('Excluding') + ': ' + a.exclude_tags.join(', '), a.contacts?.length && t('{n} contacts', { n: a.contacts.length }), a.phones?.length && t('{n} numbers', { n: a.phones.length }), a.groups?.length && t('{n} groups', { n: a.groups.length }), a.channels?.length && t('{n} channels', { n: a.channels.length }), a.statuses && t('My status')].filter(Boolean).join(' · ') || '—'}</dd>
          </dl></div></section>
          <section class="panel"><div class="panel-head"><h2>${t('Message')}</h2></div><div class="chat" style="border:0;border-radius:0 0 var(--r-lg) var(--r-lg)"><div class="chat-body" data-prev style="min-height:120px"></div></div></section>
        </div></div></div>`.s;
    import('/static/app/composer.js').then(({ bubbleHTML }) => { const p = root.querySelector('[data-prev]'); if (p) p.innerHTML = bubbleHTML(c.message || {}).s; });
  }
  root.addEventListener('click', async (e) => {
    const b = e.target.closest('[data-a]'); if (!b) return;
    const a = b.dataset.a;
    if (a === 'cancel' && !(await confirmBox(t('Cancel this campaign? Messages not yet sent will be dropped.'), { okText: t('Cancel campaign'), danger: true }))) return;
    b.classList.add('busy');
    const j = await api('POST', `/api/campaigns/${id}/${a}`, {});
    b.classList.remove('busy');
    if (!j.ok) return;
    if (a === 'duplicate') {
      toast(t('Campaign duplicated'));
      if (j.campaign?.id) return ctx.navigate('/app/campaigns/' + j.campaign.id);
      sessionStorage.setItem('vr_campaign_draft', JSON.stringify({ ...c, name: c.name + ' (copy)' }));
      return ctx.navigate('/app/campaigns/new');
    }
    toast({ pause: t('Paused'), resume: t('Resumed'), cancel: t('Cancelled'), 'run-now': t('Started') }[a] || t('Done'));
    load();
  });
  ctx.every(5000, () => { if (c && c.status === 'running') load(); });
  await load(true);
}
