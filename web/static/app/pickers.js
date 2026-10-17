// Searchable multi-select pickers used by Send and Campaigns:
//  - waContactsPicker: everyone the linked phone knows (saved + unsaved), A to Z
//  - chatsPicker: groups, community announcement groups and channels, with "only where I can post"
import { api, html, raw, ic, t, esc, fmtPhone, fmtNum, skel, empty, debounce } from '/static/app/core.js';

const cache = new Map(); // "session|kind" -> { at, data }
const TTL = 5 * 60 * 1000;
const PAGE = 200;

async function load(session, kind, force) {
  const key = session + '|' + kind;
  const c = cache.get(key);
  if (!force && c && Date.now() - c.at < TTL) return c.data;
  const enc = encodeURIComponent(session);
  let data;
  if (kind === 'wa') {
    const j = await api('GET', `/api/sessions/${enc}/wa-contacts`, undefined, { silent: true });
    if (!j.ok) return { error: j.error || t('Could not load the list. Is the number connected?') };
    data = { items: j.contacts || [], saved: j.saved || 0 };
  } else {
    const [g, ch] = await Promise.all([api('GET', `/api/sessions/${enc}/groups`, undefined, { silent: true }), api('GET', `/api/sessions/${enc}/channels`, undefined, { silent: true })]);
    if (!g.ok && !ch.ok) return { error: g.error || t('Could not load the list. Is the number connected?') };
    const items = [];
    for (const x of g.ok ? g.groups || [] : []) {
      if (x.type === 'community') continue; // the parent itself can't receive messages
      const kindOf = x.type === 'community_announcements' ? 'community' : 'group';
      items.push({ jid: x.jid, name: x.name || x.jid, kind: kindOf, can_post: x.can_post ?? (!x.announce_only || x.i_am_admin), admin: !!x.i_am_admin, size: x.participants ?? 0 });
    }
    for (const x of ch.ok ? ch.channels || [] : []) items.push({ jid: x.jid, name: x.name || x.jid, kind: 'channel', can_post: !!x.can_post, admin: x.role === 'owner' || x.role === 'admin', size: x.subscribers ?? 0 });
    items.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
    data = { items };
  }
  cache.set(key, { at: Date.now(), data });
  return data;
}

const letterOf = (s) => { const c = (s || '').trim().charAt(0).toUpperCase(); return c >= 'A' && c <= 'Z' ? c : '#'; };
const waName = (x) => x.name || x.push_name || x.business || '';

function shell(box, { search, filters, extra = '' }) {
  box.innerHTML = html`<div class="picker">
    <div class="pick-tools">
      <div class="search grow">${ic('search', 16)}<input type="search" data-q placeholder="${search}" aria-label="${search}"></div>
      <div class="seg" role="radiogroup" data-filters>${filters.map(([k, l], i) => html`<button type="button" role="radio" data-f="${k}" aria-checked="${i === 0}" aria-selected="${i === 0}">${l}</button>`)}</div>
      <button type="button" class="btn-icon" data-refresh aria-label="${t('Reload list')}" title="${t('Reload list')}">${ic('refresh', 16)}</button>
    </div>
    ${raw(extra)}
    <div class="az" data-az></div>
    <div class="pick-bar"><span data-count class="muted"></span><span class="grow"></span>
      <button type="button" class="btn sm ghost" data-all>${ic('check', 14)}${t('Select all shown')}</button>
      <button type="button" class="btn sm ghost" data-none>${t('Clear')}</button></div>
    <div class="target-list tall" data-list>${skel(4)}</div></div>`.s;
}

/** WhatsApp address book: saved and unsaved contacts of the linked number, A to Z. */
export function waContactsPicker(box, { session, selected = new Set(), onChange = () => {} }) {
  const st = { q: '', f: 'all', letter: '', shown: PAGE, data: null };
  shell(box, { search: t('Search name or number'), filters: [['all', t('All')], ['saved', t('Saved')], ['unsaved', t('Not saved')]] });
  const list = box.querySelector('[data-list]');

  function filtered() {
    const q = st.q.toLowerCase(); const qd = q.replace(/\D/g, '');
    return (st.data?.items || []).filter((x) => {
      if (st.f === 'saved' && !x.saved) return false;
      if (st.f === 'unsaved' && x.saved) return false;
      if (st.letter && letterOf(waName(x)) !== st.letter) return false;
      if (!q) return true;
      return waName(x).toLowerCase().includes(q) || (qd.length >= 3 && x.phone.includes(qd));
    });
  }
  function drawAZ() {
    const have = new Set((st.data?.items || []).map((x) => letterOf(waName(x))));
    box.querySelector('[data-az]').innerHTML = ['', ...'ABCDEFGHIJKLMNOPQRSTUVWXYZ', '#'].map((l) => `<button type="button" data-l="${l}" ${l && !have.has(l) ? 'disabled' : ''} aria-pressed="${st.letter === l}">${l || esc(t('All'))}</button>`).join('');
  }
  function drawList() {
    if (!st.data) return;
    const items = filtered();
    box.querySelector('[data-count]').textContent = t('{s} selected · {n} shown of {t}', { s: fmtNum(selected.size), n: fmtNum(items.length), t: fmtNum(st.data.items.length) });
    if (!items.length) { list.innerHTML = html`<p class="muted" style="padding:14px">${t('No contacts match.')}</p>`.s; return; }
    const rows = items.slice(0, st.shown);
    let last = '';
    list.innerHTML = rows.map((x) => {
      const l = letterOf(waName(x));
      const head = l !== last ? `<div class="az-head">${esc(l)}</div>` : '';
      last = l;
      const nm = waName(x);
      return head + html`<label><input type="checkbox" data-sel value="${x.phone}" ${selected.has(x.phone) ? 'checked' : ''}>
        <span class="avatar sm">${(nm || '#').slice(0, 1).toUpperCase()}</span>
        <span class="grow"><b class="ellipsis" style="display:block">${nm || fmtPhone(x.phone)}</b><small class="muted">${fmtPhone(x.phone)}${x.saved ? '' : ' · ' + t('not saved')}${!x.name && x.push_name ? ' · ~' + x.push_name : ''}</small></span></label>`.s;
    }).join('') + (items.length > rows.length ? html`<div class="pick-more"><button type="button" class="btn sm" data-more>${t('Show {n} more', { n: fmtNum(Math.min(PAGE, items.length - rows.length)) })}</button></div>`.s : '');
  }
  async function fetchData(force) {
    list.innerHTML = skel(4).s;
    const d = await load(session, 'wa', force);
    if (d.error) { list.innerHTML = html`<div class="note bad" style="margin:10px">${ic('alert', 16)}<span>${d.error}</span></div>`.s; return; }
    st.data = d;
    if (!d.items.length) { list.innerHTML = empty('users', t('No contacts yet'), t('Contacts appear a few minutes after linking, once the phone has synced its address book. Press reload to check again.')).s; drawAZ(); return; }
    drawAZ(); drawList();
  }
  const onQ = debounce((v) => { st.q = v.trim(); st.shown = PAGE; drawList(); }, 150);
  box.addEventListener('input', (e) => { if (e.target.matches('[data-q]')) onQ(e.target.value); });
  box.addEventListener('change', (e) => {
    if (!e.target.matches('[data-sel]')) return;
    if (e.target.checked) selected.add(e.target.value); else selected.delete(e.target.value);
    box.querySelector('[data-count]').textContent = t('{s} selected · {n} shown of {t}', { s: fmtNum(selected.size), n: fmtNum(filtered().length), t: fmtNum(st.data.items.length) });
    onChange(selected);
  });
  box.addEventListener('click', (e) => {
    const f = e.target.closest('[data-f]');
    if (f) { st.f = f.dataset.f; box.querySelectorAll('[data-f]').forEach((b) => { b.setAttribute('aria-checked', String(b === f)); b.setAttribute('aria-selected', String(b === f)); }); st.shown = PAGE; drawList(); return; }
    const l = e.target.closest('[data-l]');
    if (l) { st.letter = l.dataset.l; st.shown = PAGE; drawAZ(); drawList(); return; }
    if (e.target.closest('[data-more]')) { st.shown += PAGE; drawList(); return; }
    if (e.target.closest('[data-all]')) { filtered().forEach((x) => selected.add(x.phone)); drawList(); onChange(selected); return; }
    if (e.target.closest('[data-none]')) { selected.clear(); drawList(); onChange(selected); return; }
    if (e.target.closest('[data-refresh]')) fetchData(true);
  });
  fetchData(false);
  return { selected, reload: () => fetchData(true) };
}

/** Groups, community announcement groups and channels of the linked number. */
export function chatsPicker(box, { session, selected = new Set(), onChange = () => {}, kinds = ['group', 'community', 'channel'] }) {
  const st = { q: '', f: 'all', postable: true, data: null };
  const filters = [['all', t('All')], ['group', t('Groups')], ['community', t('Communities')], ['channel', t('Channels')]].filter(([k]) => k === 'all' || kinds.includes(k));
  shell(box, { search: t('Search groups and channels'), filters,
    extra: html`<label class="switch sm"><input type="checkbox" data-postable checked><span class="track"></span><span class="switch-text"><span>${t('Only where I can post')}</span></span></label>`.s });
  box.querySelector('[data-az]').remove();
  const list = box.querySelector('[data-list]');
  const label = { group: t('Group'), community: t('Community announcements'), channel: t('Channel') };

  function filtered() {
    const q = st.q.toLowerCase();
    return (st.data?.items || []).filter((x) => kinds.includes(x.kind) && (st.f === 'all' || x.kind === st.f) && (!st.postable || x.can_post) && (!q || x.name.toLowerCase().includes(q)));
  }
  function count() {
    box.querySelector('[data-count]').textContent = t('{s} selected · {n} shown of {t}', { s: fmtNum(selected.size), n: fmtNum(filtered().length), t: fmtNum((st.data?.items || []).filter((x) => kinds.includes(x.kind)).length) });
  }
  function drawList() {
    if (!st.data) return;
    const items = filtered();
    count();
    if (!items.length) { list.innerHTML = html`<p class="muted" style="padding:14px">${st.postable ? t('Nothing you can post to matches. Turn off "Only where I can post" to see everything.') : t('Nothing found')}</p>`.s; return; }
    list.innerHTML = items.map((x) => html`<label class="${x.can_post ? '' : 'dim'}"><input type="checkbox" data-sel value="${x.jid}" ${selected.has(x.jid) ? 'checked' : ''} ${x.can_post ? '' : 'disabled'}>
      ${ic(x.kind === 'channel' ? 'channel' : x.kind === 'community' ? 'megaphone' : 'group', 16)}
      <span class="grow"><b class="ellipsis" style="display:block">${x.name}</b><small class="muted">${label[x.kind]} · ${x.kind === 'channel' ? t('{n} followers', { n: fmtNum(x.size) }) : t('{n} members', { n: fmtNum(x.size) })}${x.can_post ? '' : ' · ' + t('only admins post')}</small></span>
      ${x.admin ? raw(`<span class="tag">${esc(t('admin'))}</span>`) : ''}</label>`.s).join('');
  }
  async function fetchData(force) {
    list.innerHTML = skel(4).s;
    const d = await load(session, 'chats', force);
    if (d.error) { list.innerHTML = html`<div class="note bad" style="margin:10px">${ic('alert', 16)}<span>${d.error}</span></div>`.s; return; }
    st.data = d; drawList();
  }
  const onQ = debounce((v) => { st.q = v.trim(); drawList(); }, 150);
  box.addEventListener('input', (e) => { if (e.target.matches('[data-q]')) onQ(e.target.value); });
  box.addEventListener('change', (e) => {
    if (e.target.matches('[data-postable]')) { st.postable = e.target.checked; drawList(); return; }
    if (!e.target.matches('[data-sel]')) return;
    if (e.target.checked) selected.add(e.target.value); else selected.delete(e.target.value);
    count(); onChange(selected);
  });
  box.addEventListener('click', (e) => {
    const f = e.target.closest('[data-f]');
    if (f) { st.f = f.dataset.f; box.querySelectorAll('[data-f]').forEach((b) => { b.setAttribute('aria-checked', String(b === f)); b.setAttribute('aria-selected', String(b === f)); }); drawList(); return; }
    if (e.target.closest('[data-all]')) { filtered().filter((x) => x.can_post).forEach((x) => selected.add(x.jid)); drawList(); onChange(selected); return; }
    if (e.target.closest('[data-none]')) { selected.clear(); drawList(); onChange(selected); return; }
    if (e.target.closest('[data-refresh]')) fetchData(true);
  });
  fetchData(false);
  return { selected, reload: () => fetchData(true), kindOf: (jid) => (st.data?.items || []).find((x) => x.jid === jid)?.kind };
}

export const SPEEDS = [
  ['normal', 'send', 'Normal', 'About 30–60 seconds between messages.'],
  ['safe', 'shield', 'Safe', 'About 1–2 minutes between messages. Recommended.'],
  ['slow', 'clock', 'Very slow', 'About 2–4 minutes between messages. Best for new numbers.'],
];
