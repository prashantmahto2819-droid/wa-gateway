// Saved send lists: named mixes of numbers, contacts, groups, communities and channels.
import { api, html, raw, ic, t, toast, modal, confirmBox, promptBox, skel, empty, fmtNum, fmtPhone, relTime, parsePhones, getSessions, sessionOptions, debounce } from '/static/app/core.js';
import { waContactsPicker, chatsPicker } from '/static/app/pickers.js';

const KIND = { phones: ['phone', 'numbers'], contacts: ['users', 'contacts'], groups: ['group', 'groups'], channels: ['channel', 'channels'] };
export const kindOf = (to) => (to.startsWith('contact:') ? 'contacts' : to.endsWith('@g.us') ? 'groups' : to.endsWith('@newsletter') ? 'channels' : 'phones');
export function countsText(c = {}) {
  return ['phones', 'contacts', 'groups', 'channels'].filter((k) => c[k]).map((k) => t('{n} ' + KIND[k][1], { n: fmtNum(c[k]) })).join(' · ') || t('Empty');
}

export async function render(ctx) {
  const { root } = ctx;
  ctx.setTitle(t('Lists'));
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Lists')}</h1><p>${t('Save a mix of numbers, contacts, groups, communities and channels under one name, then pick the list in Send or a campaign to message all of them.')}</p></div>
      <div class="page-actions"><button class="btn primary" data-a="new">${ic('plus', 16)}${t('New list')}</button></div></div>
    <section class="panel" data-box>${skel(4)}</section></div>`.s;
  const box = root.querySelector('[data-box]');
  async function load() {
    const j = await api('GET', '/api/lists');
    if (!ctx.isCurrent()) return;
    const lists = j.ok ? j.lists || [] : [];
    box.innerHTML = lists.length ? html`<ul class="list">${lists.map((l) => html`<li><span class="list-ico">${ic('list')}</span>
        <span class="li-main"><b>${l.name}</b><small>${countsText(l.counts)} · ${t('updated {t}', { t: relTime(l.updated_at) })}</small></span>
        <button class="btn sm" data-edit="${l.id}">${ic('edit', 14)}${t('Open')}</button>
        <a class="btn sm ghost" href="/app/send?list=${l.id}" data-link>${ic('send', 14)}${t('Send')}</a>
        <button class="btn-icon danger" data-del="${l.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button></li>`)}</ul>`.s
      : empty('list', t('No lists yet'), t('Example: "Korba customers" with 300 numbers, 4 groups and your channel. Then one click sends to all of them, safely spaced.'), html`<button class="btn primary" data-a="new">${ic('plus', 16)}${t('New list')}</button>`).s;
  }
  root.addEventListener('click', async (e) => {
    if (e.target.closest('[data-a="new"]')) {
      const name = await promptBox(t('New list'), { label: t('List name'), placeholder: t('Korba customers') });
      if (!name) return;
      const j = await api('POST', '/api/lists', { name, items: [] });
      if (j.ok) { await load(); editList(j.list.id, load); }
    }
    const ed = e.target.closest('[data-edit]'); if (ed) editList(+ed.dataset.edit, load);
    const del = e.target.closest('[data-del]');
    if (del && await confirmBox(t('Delete this list? Campaigns that use it will skip it.'), { danger: true, okText: t('Delete') })) {
      if ((await api('DELETE', '/api/lists/' + del.dataset.del)).ok) { toast(t('List deleted')); load(); }
    }
  });
  load();
}

/** Edit a list in a side panel: items, add from many sources, rename. */
export async function editList(id, onDone = () => {}) {
  const m = modal({ title: t('List'), size: 'xl', side: true, body: skel(6) });
  m.onClose(onDone);
  const sessions = await getSessions();
  const conn = sessions.filter((s) => s.status === 'connected');
  let session = conn[0]?.name || sessions[0]?.name || '';
  let list = null, q = '';
  async function load() {
    const j = await api('GET', '/api/lists/' + id);
    if (!j.ok) { m.close(); return; }
    list = j.list; list.items = list.items || []; draw();
  }
  async function patch(body, msg) {
    const j = await api('PATCH', '/api/lists/' + id, body);
    if (j.ok) { list = { ...j.list, items: j.list.items || [] }; draw(); if (msg) toast(msg); }
    return j;
  }
  function draw() {
    const items = list.items.filter((it) => !q || (it.name || '').toLowerCase().includes(q) || it.to.includes(q));
    m.body.innerHTML = html`<div class="stack">
      <div class="row" style="gap:10px;align-items:flex-end"><label class="field grow"><span>${t('List name')}</span><input type="text" data-name value="${list.name}"></label>
        <button class="btn" data-rename>${t('Save name')}</button></div>
      <div class="note info">${ic('users', 16)}<span>${countsText(list.counts)}</span></div>
      <div class="field"><span class="field-label">${t('Add to this list')}</span>
        <div class="row wrap" style="gap:8px">
          <button class="btn sm" data-add="paste">${ic('phone', 14)}${t('Paste numbers')}</button>
          <button class="btn sm" data-add="wa">${ic('contact', 14)}${t('WhatsApp contacts')}</button>
          <button class="btn sm" data-add="chats">${ic('group', 14)}${t('Groups, communities & channels')}</button>
          <button class="btn sm" data-add="members">${ic('users', 14)}${t('Members of a group')}</button>
          <button class="btn sm" data-add="contacts">${ic('search', 14)}${t('Saved contacts')}</button></div>
        ${sessions.length > 1 ? html`<label class="field mt-s" style="max-width:340px"><span>${t('Number to read contacts and groups from')}</span><select data-sess>${sessionOptions(sessions, session)}</select></label>` : ''}</div>
      <div class="search">${ic('search', 16)}<input type="search" data-q value="${q}" placeholder="${t('Search in this list')}" aria-label="${t('Search in this list')}"></div>
      ${items.length ? html`<div class="target-list tall">${items.slice(0, 500).map((it) => { const k = kindOf(it.to); return html`<label>
          ${ic(KIND[k][0], 16)}<span class="grow"><b class="ellipsis" style="display:block">${it.name || (k === 'phones' ? fmtPhone(it.to) : it.to)}</b><small class="muted">${k === 'phones' ? fmtPhone(it.to) : t({ contacts: 'Saved contact', groups: 'Group', channels: 'Channel' }[k])}</small></span>
          <button type="button" class="btn-icon danger" data-rm="${it.to}" aria-label="${t('Remove')}">${ic('x', 15)}</button></label>`; })}
          ${items.length > 500 ? html`<p class="muted" style="padding:10px">${t('Showing 500 of {n}. Search to find others.', { n: fmtNum(items.length) })}</p>` : ''}</div>`
        : html`<p class="muted">${q ? t('Nothing matches.') : t('This list is empty. Add numbers, contacts, groups or channels above.')}</p>`}
    </div>`.s;
  }
  function needConn() { if (!conn.some((s) => s.name === session)) { toast(t('Connect this number to load its contacts, groups and channels.'), 'warn'); return true; } return false; }
  m.body.addEventListener('input', debounce((e) => { if (e.target.matches('[data-q]')) { q = e.target.value.trim().toLowerCase(); draw(); m.body.querySelector('[data-q]')?.focus(); } }, 250));
  m.body.addEventListener('change', (e) => { if (e.target.matches('[data-sess]')) session = e.target.value; });
  m.body.addEventListener('click', async (e) => {
    if (e.target.closest('[data-rename]')) { const n = m.body.querySelector('[data-name]').value.trim(); if (n) patch({ name: n }, t('Saved')); return; }
    const rm = e.target.closest('[data-rm]'); if (rm) { patch({ remove: [rm.dataset.rm] }); return; }
    const add = e.target.closest('[data-add]'); if (!add) return;
    const kind = add.dataset.add;
    if (kind === 'paste') {
      const v = await promptBox(t('Paste numbers'), { label: t('Phone numbers with country code'), type: 'textarea', placeholder: '919876543210, 919812345678' });
      const nums = parsePhones(v); if (!nums.length) return;
      patch({ add: nums.map((to) => ({ to })) }, t('{n} added', { n: nums.length }));
    } else if (kind === 'contacts') {
      pickSaved((picked) => patch({ add: picked.map((c) => ({ to: 'contact:' + c.id, name: c.name || '' })) }, t('{n} added', { n: picked.length })));
    } else {
      if (needConn()) return;
      const sel = new Set();
      const pm = modal({ title: kind === 'wa' ? t('WhatsApp contacts') : kind === 'members' ? t('Members of a group') : t('Groups, communities & channels'), size: 'xl',
        body: '<div></div>', footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${kind === 'members' ? t('Add their members') : t('Add selected')}</button>` });
      let data = [];
      if (kind === 'wa') waContactsPicker(pm.body.firstChild, { session, selected: sel });
      else chatsPicker(pm.body.firstChild, { session, selected: sel, kinds: kind === 'members' ? ['group', 'community'] : ['group', 'community', 'channel'] });
      if (kind === 'wa') { const j = await api('GET', `/api/sessions/${encodeURIComponent(session)}/wa-contacts`, undefined, { silent: true }); data = j.ok ? j.contacts || [] : []; }
      if (kind === 'chats') { const [g, c] = await Promise.all([api('GET', `/api/sessions/${encodeURIComponent(session)}/groups`, undefined, { silent: true }), api('GET', `/api/sessions/${encodeURIComponent(session)}/channels`, undefined, { silent: true })]); data = [...(g.groups || []), ...(c.channels || [])]; }
      pm.foot.querySelector('[data-ok]').addEventListener('click', async () => {
        if (!sel.size) return toast(t('Choose at least one.'), 'warn');
        const btn = pm.foot.querySelector('[data-ok]'); btn.classList.add('busy');
        if (kind === 'members') {
          const j = await api('POST', `/api/sessions/${encodeURIComponent(session)}/members-to-list`, { groups: [...sel], list_id: id });
          btn.classList.remove('busy');
          if (j.ok) { pm.close(); await load(); toast(t('{n} members added', { n: fmtNum(j.added) }) + (j.hidden ? ' · ' + t('{h} hidden by WhatsApp', { h: fmtNum(j.hidden) }) : '')); }
          return;
        }
        const name = (to) => { const x = data.find((d) => d.phone === to || d.jid === to); return x ? (x.name || x.push_name || '') : ''; };
        const j = await patch({ add: [...sel].map((to) => ({ to, name: name(to) })) }, t('{n} added', { n: sel.size }));
        btn.classList.remove('busy'); if (j.ok) pm.close();
      });
    }
  });
  load();
}

function pickSaved(onPick) {
  const picked = new Map();
  const m = modal({ title: t('Saved contacts'), body: html`<div class="search">${ic('search', 16)}<input type="search" data-s placeholder="${t('Search contacts by name or phone')}"></div><div data-r class="mt-s"></div>`,
    footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Add selected')}</button>` });
  const r = m.body.querySelector('[data-r]');
  const run = debounce(async (q) => {
    const j = await api('GET', `/api/contacts?limit=50&q=${encodeURIComponent(q)}`, undefined, { silent: true });
    const list = j.ok ? j.contacts || [] : [];
    r.innerHTML = list.length ? html`<div class="target-list">${list.map((c) => html`<label><input type="checkbox" data-c="${c.id}" ${picked.has(c.id) ? 'checked' : ''}><span class="grow"><b>${c.name || fmtPhone(c.phone)}</b> <small class="muted">${fmtPhone(c.phone)}</small></span></label>`)}</div>`.s : html`<p class="muted">${t('No contacts match.')}</p>`.s;
    r._list = list;
  }, 250);
  m.body.addEventListener('input', (e) => { if (e.target.matches('[data-s]')) run(e.target.value); });
  m.body.addEventListener('change', (e) => { const id = e.target.dataset.c; if (!id) return; const c = r._list.find((x) => String(x.id) === id); if (e.target.checked) picked.set(c.id, c); else picked.delete(c.id); });
  m.foot.querySelector('[data-ok]').addEventListener('click', () => { if (picked.size) onPick([...picked.values()]); m.close(); });
  run('');
}

/** Checkbox list of saved lists (Send and Campaigns). */
export async function listsPicker(box, { selected = new Set(), onChange = () => {} } = {}) {
  box.innerHTML = skel(2).s;
  const j = await api('GET', '/api/lists', undefined, { silent: true });
  const lists = j.ok ? j.lists || [] : [];
  if (!lists.length) { box.innerHTML = html`<p class="muted">${t('No lists yet.')} <a href="/app/lists" data-link>${t('Create a list')}</a></p>`.s; return lists; }
  box.innerHTML = html`<div class="target-list">${lists.map((l) => html`<label><input type="checkbox" data-list="${l.id}" ${selected.has(l.id) ? 'checked' : ''}>${ic('list', 16)}
    <span class="grow"><b>${l.name}</b><small class="muted" style="display:block">${countsText(l.counts)}</small></span></label>`)}</div>
    <p class="hint mt-s">${raw(t('Manage lists in <a href="/app/lists" data-link>Lists</a>.'))}</p>`.s;
  box.addEventListener('change', (e) => { const id = +e.target.dataset.list; if (!id) return; if (e.target.checked) selected.add(id); else selected.delete(id); onChange(selected); });
  return lists;
}
