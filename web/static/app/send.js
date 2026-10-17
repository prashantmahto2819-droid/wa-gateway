import { api, html, raw, ic, t, getSessions, sessionOptions, toast, parsePhones, debounce, modal, promptBox, fmtPhone, skel, empty, toLocalInput, fromLocalInput, $ } from '/static/app/core.js';
import { createComposer } from '/static/app/composer.js';

const TARGETS = [['phone', 'phone', 'Phone numbers'], ['contact', 'users', 'Contact'], ['group', 'group', 'Group'], ['channel', 'channel', 'Channel'], ['community', 'megaphone', 'Community announcements']];

export async function render(ctx) {
  const { root, query } = ctx;
  ctx.setTitle(t('Send'));
  const sessions = await getSessions(true);
  if (!ctx.isCurrent()) return;
  const st = {
    mode: query.get('mode') === 'status' ? 'status' : 'message',
    session: query.get('session') || sessions.find((s) => s.status === 'connected')?.name || sessions[0]?.name || '',
    target: query.get('to')?.endsWith('@g.us') ? 'group' : query.get('to')?.endsWith('@newsletter') ? 'channel' : 'phone',
    to: query.get('to') || '', contact: null, groups: null, channels: null,
  };
  if (!sessions.length) {
    root.innerHTML = html`<div class="page"><div class="page-head"><div><h1>${t('Send')}</h1></div></div><div class="panel">${empty('phone', t('Link a number first'), t('You need at least one linked WhatsApp number to send messages.'), html`<a class="btn primary" href="/app/numbers" data-link>${t('Open numbers')}</a>`)}</div></div>`.s;
    return;
  }
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Send')}</h1><p>${t('Send one message now, save it as a template, or schedule it for later.')}</p></div>
      <div class="seg" role="tablist" aria-label="${t('What to send')}">
        <button role="tab" data-mode="message" aria-selected="${st.mode === 'message'}">${ic('send', 16)}${t('Message')}</button>
        <button role="tab" data-mode="status" aria-selected="${st.mode === 'status'}">${ic('story', 16)}${t('Status (story)')}</button></div></div>
    <div class="stack">
      <section class="panel" aria-labelledby="toH">
        <div class="panel-head"><h2 id="toH">${t('From and to')}</h2></div>
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

  const comp = createComposer(root.querySelector('[data-composer]'), { groupMode: st.target === 'group' || st.target === 'community', story: st.mode === 'status' });
  const prefill = sessionStorage.getItem('vr_prefill');
  if (prefill) { try { comp.set(JSON.parse(prefill)); } catch { /* */ } sessionStorage.removeItem('vr_prefill'); }

  function drawTo() {
    const w = root.querySelector('[data-to-wrap]');
    if (st.mode === 'status') { w.innerHTML = html`<div class="note info">${ic('info', 16)}<span>${t('Your status is shown to your WhatsApp contacts for 24 hours. People only see it if they saved your number.')}</span></div>`.s; return; }
    w.innerHTML = html`<div class="field"><span class="field-label">${t('Send to')}</span>
      <div class="seg" role="radiogroup" aria-label="${t('Send to')}">${TARGETS.map(([k, i, l]) => html`<button type="button" role="radio" data-target="${k}" aria-checked="${st.target === k}" aria-selected="${st.target === k}">${ic(i, 15)}${t(l)}</button>`)}</div></div>
      <div data-to class="mt-s"></div>`.s;
    drawTarget();
  }
  async function drawTarget() {
    const box = root.querySelector('[data-to]'); if (!box) return;
    comp.setGroupMode(st.target === 'group' || st.target === 'community');
    if (st.target === 'phone') {
      box.innerHTML = html`<label class="field"><span>${t('Phone numbers with country code')}</span><textarea data-phones rows="2" placeholder="919876543210, 919812345678">${st.to && !st.to.includes('@') ? st.to : ''}</textarea>
        <small>${t('Separate with commas or new lines. More than 5 numbers? Use a campaign instead, it sends safely with delays.')}</small></label>`.s;
    } else if (st.target === 'contact') {
      box.innerHTML = html`<div class="search" style="max-width:520px">${ic('search', 16)}<input type="search" data-csearch placeholder="${t('Search contacts by name or phone')}" aria-label="${t('Search contacts')}"></div><div data-cres class="mt-s"></div>`.s;
      if (st.contact) showPickedContact();
    } else {
      box.innerHTML = skel(3).s;
      const isCh = st.target === 'channel';
      const key = isCh ? 'channels' : 'groups';
      if (!st[key] || st[key]._s !== st.session) {
        const j = await api('GET', `/api/sessions/${encodeURIComponent(st.session)}/${key}`, undefined, { silent: true });
        st[key] = j.ok ? j[key] || [] : []; st[key]._s = st.session;
        if (!j.ok) { box.innerHTML = html`<div class="note bad">${ic('alert', 16)}<span>${j.error || t('Could not load the list. Is the number connected?')}</span></div>`.s; return; }
      }
      let list = st[key];
      if (st.target === 'group') list = list.filter((g) => g.type !== 'community_announcements' && g.type !== 'community');
      if (st.target === 'community') list = list.filter((g) => g.type === 'community_announcements');
      if (isCh) list = list.filter((c) => c.can_post);
      if (!list.length) { box.innerHTML = empty(isCh ? 'channel' : 'group', isCh ? t('No channels you can post to') : t('Nothing found'), isCh ? t('Create a channel in Groups & channels, or ask to be made an admin.') : t('This number is not in any matching group yet.')).s; return; }
      box.innerHTML = html`<div class="target-list" role="radiogroup" aria-label="${t('Choose one')}">${list.map((g) => html`<label><input type="radio" name="tgt" value="${g.jid}" ${st.to === g.jid ? 'checked' : ''}>
        <span class="grow"><b class="ellipsis" style="display:block">${g.name || g.jid}</b><small class="muted">${isCh ? t('{n} followers', { n: g.subscribers ?? 0 }) : t('{n} members', { n: g.participants ?? 0 })}${g.announce_only ? ' · ' + t('only admins post') : ''}</small></span>
        ${g.i_am_admin || g.role === 'owner' || g.role === 'admin' ? raw(`<span class="tag">${t('admin')}</span>`) : ''}</label>`)}</div>`.s;
    }
  }
  function showPickedContact() {
    const c = st.contact; const r = root.querySelector('[data-cres]');
    r.innerHTML = html`<div class="media-pick"><span class="avatar">${(c.name || '#').slice(0, 1).toUpperCase()}</span><div class="grow"><b>${c.name || fmtPhone(c.phone)}</b><div class="cell-sub">${fmtPhone(c.phone)} ${c.tags?.length ? '· ' + c.tags.join(', ') : ''}</div></div><button class="btn sm ghost" data-a="clear-contact">${t('Change')}</button></div>`.s;
  }
  const search = debounce(async (q) => {
    const r = root.querySelector('[data-cres]'); if (!r) return;
    if (!q.trim()) { r.innerHTML = ''; return; }
    const j = await api('GET', `/api/contacts?limit=8&q=${encodeURIComponent(q)}`, undefined, { silent: true });
    const list = j.ok ? j.contacts || [] : [];
    r.innerHTML = list.length ? html`<div class="target-list">${list.map((c) => html`<label><input type="radio" name="cpick" value="${c.id}"><span class="grow"><b>${c.name || fmtPhone(c.phone)}</b> <small class="muted">${fmtPhone(c.phone)}</small></span>${c.opted_out ? raw(`<span class="pill bad">${t('opted out')}</span>`) : ''}</label>`)}</div>`.s : html`<p class="muted">${t('No contacts match.')}</p>`.s;
    r._list = list;
  }, 250);

  function drawFoot() {
    const f = root.querySelector('[data-foot]');
    f.innerHTML = st.mode === 'status'
      ? html`<button class="btn primary lg" data-a="post-status">${ic('story', 16)}${t('Post status')}</button>`.s
      : html`<button class="btn" data-a="save-tpl">${ic('template', 16)}${t('Save as template')}</button><button class="btn" data-a="schedule">${ic('clock', 16)}${t('Schedule')}</button><button class="btn primary lg" data-a="send">${ic('send', 16)}${t('Send now')}</button>`.s;
    root.querySelector('[data-msg-h]').textContent = st.mode === 'status' ? t('Status') : t('Message');
  }

  function recipients() {
    if (st.target === 'phone') return parsePhones(root.querySelector('[data-phones]')?.value);
    if (st.target === 'contact') return st.contact ? ['contact:' + st.contact.id] : [];
    const r = root.querySelector('input[name=tgt]:checked'); return r ? [r.value] : [];
  }

  root.addEventListener('input', (e) => { if (e.target.matches('[data-csearch]')) search(e.target.value); });
  root.addEventListener('change', (e) => {
    if (e.target.matches('[data-session]')) { st.session = e.target.value; st.groups = st.channels = null; drawTarget(); }
    if (e.target.name === 'cpick') { const list = root.querySelector('[data-cres]')._list || []; st.contact = list.find((c) => String(c.id) === e.target.value); showPickedContact(); }
    if (e.target.name === 'tgt') st.to = e.target.value;
  });
  root.addEventListener('click', async (e) => {
    const m = e.target.closest('[data-mode]');
    if (m) { st.mode = m.dataset.mode; root.querySelectorAll('[data-mode]').forEach((b) => b.setAttribute('aria-selected', String(b === m))); comp.setStory(st.mode === 'status'); drawTo(); drawFoot(); return; }
    const tg = e.target.closest('[data-target]');
    if (tg) { st.target = tg.dataset.target; root.querySelectorAll('[data-target]').forEach((b) => { b.setAttribute('aria-selected', String(b === tg)); b.setAttribute('aria-checked', String(b === tg)); }); drawTarget(); return; }
    const b = e.target.closest('[data-a]'); if (!b) return;
    const a = b.dataset.a;
    if (a === 'clear-contact') { st.contact = null; drawTarget(); }
    if (a === 'load-tpl') loadTemplate(comp);
    if (a === 'save-tpl') {
      const err = comp.validate(); if (err) return toast(err, 'warn');
      const name = await promptBox(t('Save as template'), { label: t('Template name'), placeholder: t('Diwali offer') });
      if (name && (await api('POST', '/api/templates', { name, message: comp.get() })).ok) toast(t('Template saved'));
    }
    if (a === 'send') {
      const err = comp.validate(); if (err) return toast(err, 'warn');
      const to = recipients(); if (!to.length) return toast(t('Choose who to send to.'), 'warn');
      if (to.length > 5) return toast(t('For more than 5 recipients, use Schedule or a campaign so messages go out safely.'), 'warn');
      b.classList.add('busy');
      let ok = 0;
      for (const x of to) { const j = await api('POST', `/api/sessions/${encodeURIComponent(st.session)}/send`, { ...comp.get(), to: x }); if (j.ok) ok++; else break; }
      b.classList.remove('busy');
      if (ok) toast(ok === 1 ? t('Message sent') : t('{n} messages sent', { n: ok }));
    }
    if (a === 'schedule') scheduleModal();
    if (a === 'post-status') {
      const err = comp.validate(); if (err) return toast(err, 'warn');
      b.classList.add('busy');
      const j = await api('POST', `/api/sessions/${encodeURIComponent(st.session)}/status`, comp.get());
      b.classList.remove('busy');
      if (j.ok) toast(t('Status posted'));
    }
  });

  function scheduleModal() {
    const err = comp.validate(); if (err) return toast(err, 'warn');
    const to = recipients(); if (!to.length) return toast(t('Choose who to send to.'), 'warn');
    const m = modal({ title: t('Schedule message'), size: 'sm', body: html`<form class="stack">
      <label class="field"><span>${t('Campaign name')}</span><input name="name" required value="${t('Message on {d}', { d: new Date().toLocaleDateString() })}"></label>
      <label class="field"><span>${t('Send at')}</span><input name="at" type="datetime-local" required value="${toLocalInput()}"></label>
      <p class="hint">${t('This creates a campaign you can follow in Campaigns and Scheduled.')}</p></form>`,
    footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Schedule')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form'); if (!f.reportValidity()) return;
      const audience = { contacts: [], tags: [], exclude_tags: [], phones: [], groups: [], channels: [], statuses: false };
      to.forEach((x) => { if (x.startsWith('contact:')) audience.contacts.push(Number(x.slice(8))); else if (x.endsWith('@g.us')) audience.groups.push(x); else if (x.endsWith('@newsletter')) audience.channels.push(x); else audience.phones.push(x); });
      const j = await api('POST', '/api/campaigns', { name: f.name.value, sessions: [st.session], message: comp.get(), audience, schedule: { at: fromLocalInput(f.at.value), repeat: 'none' }, speed: 'safe' });
      if (j.ok) { m.close(); toast(t('Scheduled')); ctx.navigate('/app/campaigns/' + (j.campaign?.id ?? '')); }
    });
  }

  drawTo(); drawFoot();
  void $;
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
