import { api, html, raw, ic, t, getSessions, sessionOptions, toast, parsePhones, debounce, modal, promptBox, fmtPhone, fmtNum, skel, empty, toLocalInput, fromLocalInput, $$ } from '/static/app/core.js';
import { createComposer } from '/static/app/composer.js';
import { waContactsPicker, chatsPicker, SPEEDS } from '/static/app/pickers.js';

const TARGETS = [['phone', 'phone', 'Phone numbers'], ['contact', 'users', 'Saved contacts'], ['wa', 'contact', 'WhatsApp contacts'], ['chats', 'group', 'Groups, communities & channels']];
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const DIRECT_MAX = 5; // more people than this, or more than one group/channel, go out as a campaign with gaps

export async function render(ctx) {
  const { root, query } = ctx;
  ctx.setTitle(t('Send'));
  const sessions = await getSessions(true);
  if (!ctx.isCurrent()) return;
  const qTo = query.get('to') || '';
  const st = {
    mode: query.get('mode') === 'status' ? 'status' : 'message',
    session: query.get('session') || sessions.find((s) => s.status === 'connected')?.name || sessions[0]?.name || '',
    target: qTo.endsWith('@g.us') || qTo.endsWith('@newsletter') ? 'chats' : 'phone',
    phones: qTo && !qTo.includes('@') ? qTo : '',
    contacts: [], // saved (gateway) contacts picked
    waSel: new Set(),
    chatSel: new Set(qTo.includes('@') ? [qTo] : []),
    aud: { mode: 'auto', tags: [], limit: '' }, // Status audience
    audSel: new Set(),
    tags: null,
  };
  if (!sessions.length) {
    root.innerHTML = html`<div class="page"><div class="page-head"><div><h1>${t('Send')}</h1></div></div><div class="panel">${empty('phone', t('Link a number first'), t('You need at least one linked WhatsApp number to send messages.'), html`<a class="btn primary" href="/app/numbers" data-link>${t('Open numbers')}</a>`)}</div></div>`.s;
    return;
  }
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Send')}</h1><p>${t('Send now, or schedule it. Many recipients go out one by one with safe gaps.')}</p></div>
      <div class="seg" role="tablist" aria-label="${t('What to send')}">
        <button role="tab" data-mode="message" aria-selected="${st.mode === 'message'}">${ic('send', 16)}${t('Message')}</button>
        <button role="tab" data-mode="status" aria-selected="${st.mode === 'status'}">${ic('story', 16)}${t('Status (story)')}</button></div></div>
    <div class="stack">
      <section class="panel" aria-labelledby="toH">
        <div class="panel-head"><h2 id="toH" data-to-h>${t('From and to')}</h2></div>
        <div class="panel-body stack">
          <label class="field" style="max-width:420px"><span>${t('Send from')}</span><select data-session>${sessionOptions(sessions, st.session)}</select></label>
          <div data-to-wrap></div>
        </div>
      </section>
      <section class="panel" aria-labelledby="msgH">
        <div class="panel-head"><h2 id="msgH" data-msg-h>${t('Message')}</h2><button class="btn sm ghost" data-a="load-tpl">${ic('template', 15)}${t('Use a template')}</button></div>
        <div class="panel-body" data-composer></div>
        <div class="panel-foot" data-foot></div>
      </section>
    </div></div>`.s;

  const comp = createComposer(root.querySelector('[data-composer]'), { groupMode: st.target === 'chats', story: st.mode === 'status' });
  const prefill = sessionStorage.getItem('vr_prefill');
  if (prefill) { try { comp.set(JSON.parse(prefill)); } catch { /* */ } sessionStorage.removeItem('vr_prefill'); }

  const connected = () => sessions.find((x) => x.name === st.session)?.status === 'connected';
  const notConnected = () => html`<div class="note warn">${ic('alert', 16)}<span>${t('Connect this number to load its contacts, groups and channels.')}</span></div>`.s;

  // ---------------------------------------------------------------- recipients
  function drawTo() {
    const w = root.querySelector('[data-to-wrap]');
    root.querySelector('[data-to-h]').textContent = st.mode === 'status' ? t('Who sees this status') : t('From and to');
    if (st.mode === 'status') { drawStatusAudience(w); return; }
    w.innerHTML = html`<div class="field"><span class="field-label">${t('Send to')}</span>
      <div class="seg" role="radiogroup" aria-label="${t('Send to')}">${TARGETS.map(([k, i, l]) => html`<button type="button" role="radio" data-target="${k}" aria-checked="${st.target === k}" aria-selected="${st.target === k}">${ic(i, 15)}${t(l)}</button>`)}</div></div>
      <div data-to class="mt-s"></div><div data-rsum class="mt-s"></div>`.s;
    drawTarget();
  }
  function drawTarget() {
    const box = root.querySelector('[data-to]'); if (!box) return;
    comp.setGroupMode(st.target === 'chats');
    if (st.target === 'phone') {
      box.innerHTML = html`<label class="field"><span>${t('Phone numbers with country code')}</span><textarea data-phones rows="3" placeholder="919876543210, 919812345678">${st.phones}</textarea>
        <small>${t('Separate with commas or new lines.')}</small></label>`.s;
    } else if (st.target === 'contact') {
      box.innerHTML = html`<div class="search" style="max-width:520px">${ic('search', 16)}<input type="search" data-csearch placeholder="${t('Search contacts by name or phone')}" aria-label="${t('Search contacts')}"></div><div data-cres class="mt-s"></div><div class="row mt-s" data-cpicked></div>`.s;
      drawPicked();
    } else if (!connected()) {
      box.innerHTML = notConnected();
    } else if (st.target === 'wa') {
      waContactsPicker(box, { session: st.session, selected: st.waSel, onChange: summary });
    } else {
      chatsPicker(box, { session: st.session, selected: st.chatSel, onChange: summary });
    }
    summary();
  }
  function drawPicked() {
    const box = root.querySelector('[data-cpicked]'); if (!box) return;
    box.innerHTML = st.contacts.map((c) => html`<span class="chip">${c.name || fmtPhone(c.phone)}<button type="button" data-rmc="${c.id}" aria-label="${t('Remove')}">${ic('x')}</button></span>`.s).join('');
  }
  function recipients() {
    if (st.target === 'phone') return parsePhones(root.querySelector('[data-phones]')?.value ?? st.phones);
    if (st.target === 'contact') return st.contacts.map((c) => 'contact:' + c.id);
    if (st.target === 'wa') return [...st.waSel];
    return [...st.chatSel];
  }
  function summary() {
    const el = root.querySelector('[data-rsum]'); if (!el) return;
    const to = recipients();
    const many = needsCampaign(to);
    el.innerHTML = to.length ? html`<div class="note ${many ? 'info' : 'ok'}">${ic(many ? 'clock' : 'check', 16)}<span>${many
      ? t('{n} recipients — they will get it one by one with safe gaps (as a campaign you can follow and pause).', { n: fmtNum(to.length) })
      : t('{n} recipient(s) — sent right away.', { n: to.length })}</span></div>`.s : '';
  }
  const isChat = (x) => x.endsWith('@g.us') || x.endsWith('@newsletter');
  const needsCampaign = (to) => to.length > DIRECT_MAX || to.filter(isChat).length > 1;

  // ---------------------------------------------------------------- Status audience
  async function drawStatusAudience(w) {
    const a = st.aud;
    w.innerHTML = html`<div class="aud-modes" role="radiogroup" aria-label="${t('Who sees this status')}">
        ${[['auto', 'sparkle', 'Automatic', 'People who chat with you first, then saved contacts, then your phone’s address book.'], ['tags', 'users', 'Contacts with tags', 'Only contacts that have the tags you pick.'], ['phones', 'contact', 'Choose people', 'Pick exactly who sees it from your WhatsApp contacts.']]
          .map(([k, i, l, d]) => html`<label class="choice"><input type="radio" name="audmode" value="${k}" ${a.mode === k ? 'checked' : ''}><b>${ic(i, 16)}${t(l)}</b><small>${t(d)}</small></label>`)}</div>
      <div data-aud-body class="mt-s"></div>
      <div class="row mt-s" style="align-items:flex-end;gap:14px">
        <label class="field" style="max-width:200px"><span>${t('At most')}</span><input type="number" min="10" max="5000" step="10" data-aud-limit value="${a.limit}" placeholder="1000"></label>
        <p class="hint grow">${t('WhatsApp refuses a Status sent to thousands of people at once from a linked device, so each post goes to at most this many (server limit: STATUS_MAX_RECIPIENTS, default 1000). People only see it if they also saved your number.')}</p></div>`.s;
    const body = w.querySelector('[data-aud-body]');
    if (a.mode === 'tags') {
      if (!st.tags) { body.innerHTML = skel(1).s; const j = await api('GET', '/api/contacts/tags', undefined, { silent: true }); st.tags = j.ok ? j.tags || [] : []; }
      body.innerHTML = st.tags.length ? html`<div class="tag-pick">${st.tags.map((x) => html`<button type="button" data-atag="${x.tag}" class="${a.tags.includes(x.tag) ? 'inc' : ''}" aria-pressed="${a.tags.includes(x.tag)}">${x.tag}<span class="badge">${fmtNum(x.count)}</span></button>`)}</div>`.s
        : html`<p class="muted">${t('No tags yet. Import contacts with a tag in Contacts.')}</p>`.s;
    } else if (a.mode === 'phones') {
      if (!connected()) body.innerHTML = notConnected();
      else waContactsPicker(body, { session: st.session, selected: st.audSel, onChange: () => {} });
    } else body.innerHTML = '';
  }
  function statusAudience() {
    const a = st.aud;
    const lim = Number(root.querySelector('[data-aud-limit]')?.value || 0);
    const o = { mode: a.mode };
    if (lim > 0) o.limit = lim;
    if (a.mode === 'tags') o.tags = a.tags;
    if (a.mode === 'phones') o.phones = [...st.audSel];
    return o;
  }
  function statusMessage() { return { ...comp.get(), status_audience: statusAudience() }; }
  function checkStatusAudience() {
    if (st.aud.mode === 'tags' && !st.aud.tags.length) return t('Pick at least one tag.');
    if (st.aud.mode === 'phones' && !st.audSel.size) return t('Choose at least one person.');
    return '';
  }

  // ---------------------------------------------------------------- footer
  function drawFoot() {
    const f = root.querySelector('[data-foot]');
    f.innerHTML = st.mode === 'status'
      ? html`<button class="btn" data-a="schedule-status">${ic('clock', 16)}${t('Schedule')}</button><button class="btn primary lg" data-a="post-status">${ic('story', 16)}${t('Post status now')}</button>`.s
      : html`<button class="btn" data-a="save-tpl">${ic('template', 16)}${t('Save as template')}</button><button class="btn" data-a="schedule">${ic('clock', 16)}${t('Schedule')}</button><button class="btn primary lg" data-a="send">${ic('send', 16)}${t('Send now')}</button>`.s;
    root.querySelector('[data-msg-h]').textContent = st.mode === 'status' ? t('Status') : t('Message');
  }

  // ---------------------------------------------------------------- events
  root.addEventListener('input', (e) => {
    if (e.target.matches('[data-csearch]')) search(e.target.value);
    if (e.target.matches('[data-phones]')) { st.phones = e.target.value; summary(); }
    if (e.target.matches('[data-aud-limit]')) st.aud.limit = e.target.value;
  });
  const search = debounce(async (q) => {
    const r = root.querySelector('[data-cres]'); if (!r) return;
    if (!q.trim()) { r.innerHTML = ''; return; }
    const j = await api('GET', `/api/contacts?limit=8&q=${encodeURIComponent(q)}`, undefined, { silent: true });
    r._list = j.ok ? j.contacts || [] : [];
    r.innerHTML = r._list.length ? html`<div class="target-list">${r._list.map((c) => html`<label><span class="grow"><b>${c.name || fmtPhone(c.phone)}</b> <small class="muted">${fmtPhone(c.phone)}</small></span>${c.opted_out ? raw(`<span class="pill bad">${t('opted out')}</span>`) : ''}<button type="button" class="btn sm" data-addc="${c.id}">${ic('plus', 14)}${t('Add')}</button></label>`)}</div>`.s : html`<p class="muted">${t('No contacts match.')}</p>`.s;
  }, 250);
  root.addEventListener('change', (e) => {
    if (e.target.matches('[data-session]')) { st.session = e.target.value; st.waSel.clear(); st.chatSel.clear(); st.audSel.clear(); drawTo(); }
    if (e.target.name === 'audmode') { st.aud.mode = e.target.value; drawTo(); }
  });
  root.addEventListener('click', async (e) => {
    const m = e.target.closest('[data-mode]');
    if (m) { st.mode = m.dataset.mode; root.querySelectorAll('[data-mode]').forEach((b) => b.setAttribute('aria-selected', String(b === m))); comp.setStory(st.mode === 'status'); drawTo(); drawFoot(); return; }
    const tg = e.target.closest('[data-target]');
    if (tg) { st.target = tg.dataset.target; root.querySelectorAll('[data-target]').forEach((b) => { b.setAttribute('aria-selected', String(b === tg)); b.setAttribute('aria-checked', String(b === tg)); }); drawTarget(); return; }
    const at = e.target.closest('[data-atag]');
    if (at) { const k = at.dataset.atag; const a = st.aud; a.tags = a.tags.includes(k) ? a.tags.filter((x) => x !== k) : [...a.tags, k]; at.className = a.tags.includes(k) ? 'inc' : ''; at.setAttribute('aria-pressed', String(a.tags.includes(k))); return; }
    const addc = e.target.closest('[data-addc]');
    if (addc) { const c = (root.querySelector('[data-cres]')._list || []).find((x) => String(x.id) === addc.dataset.addc); if (c && !st.contacts.some((x) => x.id === c.id)) { st.contacts.push(c); drawPicked(); summary(); } return; }
    const rmc = e.target.closest('[data-rmc]');
    if (rmc) { st.contacts = st.contacts.filter((x) => String(x.id) !== rmc.dataset.rmc); drawPicked(); summary(); return; }
    const b = e.target.closest('[data-a]'); if (!b) return;
    const a = b.dataset.a;
    if (a === 'load-tpl') loadTemplate(comp);
    if (a === 'save-tpl') {
      const err = comp.validate(); if (err) return toast(err, 'warn');
      const name = await promptBox(t('Save as template'), { label: t('Template name'), placeholder: t('Diwali offer') });
      if (name && (await api('POST', '/api/templates', { name, message: comp.get() })).ok) toast(t('Template saved'));
    }
    if (a === 'send') {
      const err = comp.validate(); if (err) return toast(err, 'warn');
      const to = recipients(); if (!to.length) return toast(t('Choose who to send to.'), 'warn');
      if (needsCampaign(to)) return scheduleModal(to, { now: true });
      b.classList.add('busy');
      let ok = 0, late = 0;
      for (const x of to) {
        const j = await api('POST', `/api/sessions/${encodeURIComponent(st.session)}/send`, { ...comp.get(), to: x });
        if (!j.ok) break;
        ok++; if (j.message?.unconfirmed) late++;
      }
      b.classList.remove('busy');
      if (ok) toast(late ? t('Sent. WhatsApp’s receipt is taking longer than usual; it will show as delivered shortly.') : ok === 1 ? t('Message sent') : t('{n} messages sent', { n: ok }));
    }
    if (a === 'schedule') {
      const err = comp.validate(); if (err) return toast(err, 'warn');
      const to = recipients(); if (!to.length) return toast(t('Choose who to send to.'), 'warn');
      scheduleModal(to, { now: false });
    }
    if (a === 'post-status') {
      const err = comp.validate() || checkStatusAudience(); if (err) return toast(err, 'warn');
      b.classList.add('busy');
      const j = await api('POST', `/api/sessions/${encodeURIComponent(st.session)}/status`, statusMessage());
      b.classList.remove('busy');
      if (j.ok) toast(j.message?.recipients ? t('Status posted — shared with {n} people.', { n: fmtNum(j.message.recipients) }) : t('Status posted'));
    }
    if (a === 'schedule-status') {
      const err = comp.validate() || checkStatusAudience(); if (err) return toast(err, 'warn');
      scheduleModal(['status'], { now: false, status: true });
    }
  });

  // ---------------------------------------------------------------- schedule / send-many
  function scheduleModal(to, { now, status = false }) {
    const people = to.filter((x) => !isChat(x) && x !== 'status').length;
    const title = status ? t('Schedule status') : now ? t('Send to {n} recipients', { n: fmtNum(to.length) }) : t('Schedule message');
    const m = modal({ title, body: html`<form class="stack">
      <label class="field"><span>${t('Name (to find it later)')}</span><input type="text" name="name" required value="${status ? t('Status on {d}', { d: new Date().toLocaleDateString() }) : t('Message on {d}', { d: new Date().toLocaleDateString() })}"></label>
      ${now ? '' : html`<div class="form-grid">
        <label class="field"><span>${t('First time')}</span><input name="at" type="datetime-local" required value="${toLocalInput(Math.floor(Date.now() / 1000) + 3600)}"></label>
        <label class="field"><span>${t('Repeat')}</span><select name="repeat">${['none', 'daily', 'weekly', 'monthly'].map((r) => html`<option value="${r}">${t({ none: 'Does not repeat', daily: 'Every day', weekly: 'Every week on…', monthly: 'Every month (same date)' }[r])}</option>`)}</select></label></div>
        <div data-days hidden><span class="field-label">${t('On these days')}</span><div class="days-pick mt-s">${DAYS.map((d, i) => html`<label><input type="checkbox" name="days" value="${i}" ${i >= 1 && i <= 5 ? 'checked' : ''}><span>${t(d)}</span></label>`)}</div></div>`}
      ${status || to.length < 2 ? '' : html`<fieldset><legend>${t('Gap between messages')}</legend><div class="choice-grid">${SPEEDS.map(([k, i, l, d]) => html`<label class="choice"><input type="radio" name="speed" value="${k}" ${k === 'safe' ? 'checked' : ''}><b>${ic(i, 16)}${t(l)}</b><small>${t(d)}</small></label>`)}</div></fieldset>`}
      ${people ? html`<p class="hint">${t('Daily limits, quiet hours (SEND_HOUR_FROM–TO) and STOP opt-outs still apply. Follow progress, pause or cancel it in Campaigns.')}</p>` : ''}
    </form>`, footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${now ? t('Start sending') : t('Schedule')}</button>` });
    const f = m.body.querySelector('form');
    f.addEventListener('change', (e) => { if (e.target.name === 'repeat') m.body.querySelector('[data-days]').hidden = e.target.value !== 'weekly'; });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      if (!f.reportValidity()) return;
      const audience = { contacts: [], tags: [], exclude_tags: [], phones: [], groups: [], channels: [], statuses: false };
      to.forEach((x) => { if (x === 'status') audience.statuses = true; else if (x.startsWith('contact:')) audience.contacts.push(Number(x.slice(8))); else if (x.endsWith('@g.us')) audience.groups.push(x); else if (x.endsWith('@newsletter')) audience.channels.push(x); else audience.phones.push(x); });
      const body = { name: f.name.value, sessions: [st.session], message: status ? statusMessage() : comp.get(), audience, speed: f.speed?.value || 'safe' };
      if (!now) {
        const at = fromLocalInput(f.at.value);
        if (at < Date.now() / 1000 - 60) return toast(t('That time is in the past.'), 'warn');
        const repeat = f.repeat.value; const days = $$('[name=days]:checked', f).map((x) => +x.value);
        if (repeat === 'weekly' && !days.length) return toast(t('Pick at least one day.'), 'warn');
        body.schedule = { at, repeat, days: repeat === 'weekly' ? days : [] };
      }
      const btn = m.foot.querySelector('[data-ok]'); btn.classList.add('busy');
      const j = await api('POST', '/api/campaigns', body);
      btn.classList.remove('busy');
      if (j.ok) { m.close(); toast(now ? t('Sending started') : t('Scheduled')); ctx.navigate('/app/campaigns/' + (j.campaign?.id ?? '')); }
    });
  }

  drawTo(); drawFoot();
}

export async function loadTemplate(comp) {
  const m = modal({ title: t('Use a template'), body: skel(4) });
  const j = await api('GET', '/api/templates');
  const list = j.ok ? j.templates || [] : [];
  const { summarize } = await import('/static/app/composer.js');
  if (!list.length) { m.body.innerHTML = empty('template', t('No templates yet'), t('Write a message and press "Save as template" to reuse it later.')).s; return; }
  m.body.innerHTML = html`<ul class="list" style="margin:0 -22px">${list.map((x) => html`<li><span class="list-ico">${ic('template')}</span><span class="li-main"><b>${x.name}</b><small>${summarize(x.message)}</small></span><button class="btn sm" data-id="${x.id}">${t('Use')}</button></li>`)}</ul>`.s;
  m.body.addEventListener('click', (e) => { const b = e.target.closest('[data-id]'); if (!b) return; const x = list.find((y) => String(y.id) === b.dataset.id); comp.set(x.message); m.close(); toast(t('Template loaded')); });
}
