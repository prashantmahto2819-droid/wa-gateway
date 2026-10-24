// Vivid Relay dashboard — shared helpers: escaping, API, toasts, modals, formatting, icons.
import { t, getLang } from '/static/i18n.js';
export { t, getLang };

export const $ = (s, r = document) => r.querySelector(s);
export const $$ = (s, r = document) => [...r.querySelectorAll(s)];

const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;', '`': '&#96;' };
export const esc = (s) => String(s ?? '').replace(/[&<>"'`]/g, (c) => ESC[c]);

class Raw { constructor(s) { this.s = s; } toString() { return this.s; } }
export const raw = (s) => new Raw(String(s ?? ''));
function v2s(v) {
  if (v == null || v === false) return '';
  if (v instanceof Raw) return v.s;
  if (Array.isArray(v)) return v.map(v2s).join('');
  return esc(v);
}
/** Tagged template: interpolations are escaped unless wrapped with raw() or produced by html``. */
export function html(strings, ...vals) {
  let o = strings[0];
  for (let i = 0; i < vals.length; i++) o += v2s(vals[i]) + strings[i + 1];
  return new Raw(o);
}

// ---------------------------------------------------------------- API
const LIC_CODES = new Set(['licence_required', 'licence_expired', 'feature_locked', 'limit_reached']);

export async function api(method, path, body, { silent = false } = {}) {
  const opt = { method, credentials: 'same-origin', headers: { 'X-Requested-With': 'vivid', Accept: 'application/json' } };
  if (body !== undefined) {
    if (body instanceof FormData) opt.body = body;
    else { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  }
  let r, j;
  try { r = await fetch(path, opt); } catch {
    j = { ok: false, code: 'network', error: t('Network error. Check your connection and try again.') };
    if (!silent) toast(j.error, 'bad');
    return j;
  }
  try { j = await r.json(); } catch { j = { ok: r.ok, code: r.ok ? '' : 'bad_response', error: r.ok ? '' : `HTTP ${r.status}` }; }
  if (j == null || typeof j !== 'object') j = { ok: r.ok };
  if (j.ok === undefined) j.ok = r.ok;
  j._status = r.status;
  return handleResult(j, r.status, silent);
}

function handleResult(j, status, silent) {
  if (status === 401) {
    location.href = '/login?next=' + encodeURIComponent(location.pathname + location.search);
    return j;
  }
  if (!j.ok) {
    if (LIC_CODES.has(j.code) || status === 402) licenceBanner(j);
    if (!silent) toast(j.error || j.code || t('Something went wrong'), 'bad');
  }
  return j;
}

/** Multipart upload with progress (XMLHttpRequest). onProgress(0..1). */
export function upload(path, formData, onProgress) {
  return new Promise((resolve) => {
    const x = new XMLHttpRequest();
    x.open('POST', path);
    x.withCredentials = true;
    x.setRequestHeader('X-Requested-With', 'vivid');
    x.setRequestHeader('Accept', 'application/json');
    x.upload.addEventListener('progress', (e) => { if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total); });
    x.addEventListener('load', () => {
      let j; try { j = JSON.parse(x.responseText); } catch { j = { ok: x.status < 300, error: `HTTP ${x.status}` }; }
      if (j.ok === undefined) j.ok = x.status < 300;
      resolve(handleResult(j, x.status, false));
    });
    x.addEventListener('error', () => resolve(handleResult({ ok: false, code: 'network', error: t('Upload failed. Check your connection.') }, 0, false)));
    x.send(formData);
  });
}

export function licenceBanner(j) {
  const el = document.getElementById('licBanner');
  if (!el) return;
  const msg = {
    licence_required: t('You need an active plan to use this.'),
    licence_expired: t('Your plan has expired.'),
    feature_locked: t('This feature is not included in your plan.'),
    limit_reached: t('You have reached a plan limit') + (j.limit ? ` (${j.limit})` : '') + '.',
  }[j.code] || j.error || t('Plan problem');
  el.innerHTML = html`<span class="ico">${raw(icon('shield'))}</span><span>${msg}</span>
    <a class="btn sm" href="/app/licence" data-link>${t('Open licence')}</a>
    <button class="btn-icon" data-dismiss aria-label="${t('Dismiss')}">${raw(icon('x'))}</button>`.s;
  el.hidden = false;
  el.querySelector('[data-dismiss]').addEventListener('click', () => { el.hidden = true; });
}

// ---------------------------------------------------------------- toasts
export function toast(msg, tone = 'ok', ms = 4200) {
  const box = document.getElementById('toasts');
  if (!box) return;
  const d = document.createElement('div');
  d.className = 'toast ' + tone;
  d.setAttribute('role', tone === 'bad' ? 'alert' : 'status');
  d.innerHTML = html`<span class="ico">${raw(icon(tone === 'bad' ? 'alert' : tone === 'warn' ? 'alert' : 'check'))}</span><span>${msg}</span>`.s;
  box.appendChild(d);
  setTimeout(() => { d.classList.add('out'); setTimeout(() => d.remove(), 300); }, ms);
}

export async function copy(text, label) {
  try { await navigator.clipboard.writeText(String(text)); toast((label || t('Copied')) + ' ✓'); }
  catch {
    const ta = document.createElement('textarea'); ta.value = text; document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); toast(t('Copied') + ' ✓'); } catch { toast(t('Copy failed'), 'bad'); }
    ta.remove();
  }
}

// ---------------------------------------------------------------- modals & drawers
const stack = [];
const FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]):not([type=hidden]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

document.addEventListener('keydown', (e) => {
  const top = stack[stack.length - 1];
  if (!top) return;
  if (e.key === 'Escape') { e.preventDefault(); top.close(); return; }
  if (e.key === 'Tab') {
    const f = $$(FOCUSABLE, top.dialog).filter((n) => n.offsetParent !== null || n === document.activeElement);
    if (!f.length) { e.preventDefault(); return; }
    const first = f[0], last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    else if (!top.dialog.contains(document.activeElement)) { e.preventDefault(); first.focus(); }
  }
});

/**
 * Open a modal (or side drawer with side:true).
 * body: html string/Raw. footer: html string/Raw. Returns {el, body, foot, close, onClose(fn)}.
 */
export function modal({ title, body = '', footer = '', size = '', side = false, label } = {}) {
  const prev = document.activeElement;
  const ov = document.createElement('div');
  ov.className = 'overlay' + (side ? ' side' : '');
  const id = 'dlg' + Math.random().toString(36).slice(2, 8);
  ov.innerHTML = html`<div class="dialog ${size}" role="dialog" aria-modal="true" aria-labelledby="${id}">
      <header class="dlg-head"><h2 id="${id}">${title || label || ''}</h2>
        <button class="btn-icon" data-close aria-label="${t('Close')}">${raw(icon('x'))}</button></header>
      <div class="dlg-body">${raw(String(body))}</div>
      <footer class="dlg-foot" ${footer ? '' : 'hidden'}>${raw(String(footer))}</footer>
    </div>`.s;
  document.body.appendChild(ov);
  document.body.classList.add('noscroll');
  const dialog = ov.querySelector('.dialog');
  const closers = [];
  const timers = new Set();
  let closed = false;
  const m = {
    el: dialog, dialog, body: dialog.querySelector('.dlg-body'), foot: dialog.querySelector('.dlg-foot'),
    close() {
      if (closed) return; closed = true;
      timers.forEach((x) => clearInterval(x));
      closers.forEach((fn) => { try { fn(); } catch (e) { console.warn(e); } });
      const i = stack.indexOf(m); if (i >= 0) stack.splice(i, 1);
      ov.classList.add('out');
      setTimeout(() => ov.remove(), reduced() ? 0 : 160);
      if (!stack.length) document.body.classList.remove('noscroll');
      if (prev && prev.focus) prev.focus();
    },
    onClose(fn) { closers.push(fn); },
    every(ms, fn) { const x = setInterval(fn, ms); timers.add(x); return x; },
    setFooter(h) { m.foot.innerHTML = String(h); m.foot.hidden = !h; },
  };
  ov.addEventListener('mousedown', (e) => { if (e.target === ov) m.close(); });
  dialog.addEventListener('click', (e) => { if (e.target.closest('[data-close]')) m.close(); });
  stack.push(m);
  requestAnimationFrame(() => {
    const f = dialog.querySelector('[autofocus]') || $$(FOCUSABLE, m.body)[0] || dialog.querySelector('[data-close]');
    f && f.focus();
  });
  return m;
}

export function confirmBox(message, { okText, danger = false, title } = {}) {
  return new Promise((resolve) => {
    let done = false;
    const m = modal({
      title: title || t('Are you sure?'), size: 'sm',
      body: html`<p class="lead">${message}</p>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button>
        <button class="btn ${danger ? 'danger' : 'primary'}" data-ok autofocus>${okText || t('Confirm')}</button>`,
    });
    m.foot.querySelector('[data-ok]').addEventListener('click', () => { done = true; resolve(true); m.close(); });
    m.onClose(() => { if (!done) resolve(false); });
  });
}

export function promptBox(title, { label = '', value = '', placeholder = '', okText, type = 'text', hint = '' } = {}) {
  return new Promise((resolve) => {
    let done = false;
    const m = modal({
      title, size: 'sm',
      body: html`<form data-f><label class="field"><span>${label}</span>
        ${type === 'textarea' ? html`<textarea name="v" rows="6" placeholder="${placeholder}" autofocus required>${value}</textarea>` : html`<input name="v" type="${type}" value="${value}" placeholder="${placeholder}" autofocus required>`}</label>
        ${hint ? html`<p class="hint">${hint}</p>` : ''}</form>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${okText || t('Save')}</button>`,
    });
    const f = m.body.querySelector('form');
    const ok = () => { if (!f.reportValidity()) return; done = true; resolve(f.v.value.trim()); m.close(); };
    f.addEventListener('submit', (e) => { e.preventDefault(); ok(); });
    m.foot.querySelector('[data-ok]').addEventListener('click', ok);
    m.onClose(() => { if (!done) resolve(null); });
  });
}

// ---------------------------------------------------------------- formatting
export const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
const loc = () => (getLang() === 'hi' ? 'hi-IN' : 'en-IN');
export const fmtNum = (n) => (n == null || isNaN(n) ? '0' : Number(n).toLocaleString(loc()));
export function fmtDate(ts, withTime = true) {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return d.toLocaleString(loc(), withTime ? { day: 'numeric', month: 'short', year: 'numeric', hour: 'numeric', minute: '2-digit' } : { day: 'numeric', month: 'short', year: 'numeric' });
}
export function fmtTime(ts) { return ts ? new Date(ts * 1000).toLocaleTimeString(loc(), { hour: 'numeric', minute: '2-digit' }) : ''; }
export function relTime(ts) {
  if (!ts) return t('never');
  const s = Math.round(Date.now() / 1000 - ts);
  const rtf = new Intl.RelativeTimeFormat(loc(), { numeric: 'auto' });
  const a = Math.abs(s), sign = s > 0 ? -1 : 1;
  if (a < 60) return rtf.format(sign * a, 'second');
  if (a < 3600) return rtf.format(sign * Math.round(a / 60), 'minute');
  if (a < 86400) return rtf.format(sign * Math.round(a / 3600), 'hour');
  if (a < 86400 * 30) return rtf.format(sign * Math.round(a / 86400), 'day');
  return fmtDate(ts, false);
}
export function fmtBytes(b) {
  if (!b) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB']; let i = 0; let n = b;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (n < 10 && i ? n.toFixed(1) : Math.round(n)) + ' ' + u[i];
}
export function fmtDuration(sec) {
  sec = Math.max(0, Math.round(sec));
  const h = Math.floor(sec / 3600), m = Math.round((sec % 3600) / 60);
  if (h >= 24) { const d = Math.floor(h / 24); return t('{d} d {h} h', { d, h: h % 24 }); }
  if (h) return t('{h} h {m} min', { h, m });
  return t('{m} min', { m: Math.max(1, m) });
}
export const fmtPhone = (p) => (p ? '+' + String(p).replace(/^\+/, '') : '');
export function pct(a, b) { if (!b) return 0; return Math.max(0, Math.min(100, Math.round((a / b) * 100))); }
export function toLocalInput(ts) {
  const d = ts ? new Date(ts * 1000) : new Date(Date.now() + 3600e3);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}
export const fromLocalInput = (v) => (v ? Math.floor(new Date(v).getTime() / 1000) : 0);
export const parsePhones = (s) => String(s || '').split(/[\s,;]+/).map((x) => x.replace(/[^\d+]/g, '').replace(/^\+/, '')).filter((x) => x.length >= 7);

// ---------------------------------------------------------------- status semantics
const SS = {
  connected: ['Connected', 'ok'], connecting: ['Connecting', 'warn'], disconnected: ['Reconnecting', 'warn'],
  qr: ['Waiting for scan', 'info'], qr_expired: ['QR expired', 'bad'], logged_out: ['Logged out', 'bad'],
  replaced: ['Opened elsewhere', 'bad'], temp_banned: ['Temporarily banned', 'bad'], outdated: ['Update needed', 'bad'], new: ['Not linked', 'muted'],
};
export function sessionStatus(st) { const [l, tone] = SS[st] || [st || 'unknown', 'muted']; return { label: t(l), tone }; }
const CS = { draft: 'muted', scheduled: 'info', running: 'ok', paused: 'warn', done: 'ok', cancelled: 'muted' };
export const campaignTone = (s) => CS[s] || 'muted';
export const pill = (text, tone = 'muted', dot = true) => html`<span class="pill ${tone}">${dot ? raw('<i></i>') : ''}${text}</span>`;

// ---------------------------------------------------------------- UI bits
export function skel(rows = 3, cls = '') {
  return html`<div class="skel-wrap ${cls}" aria-busy="true" aria-label="${t('Loading')}">${Array.from({ length: rows }, () => raw('<div class="skel"></div>'))}</div>`;
}
export function empty(ic, title, text = '', action = '') {
  return html`<div class="empty"><div class="empty-ico">${raw(icon(ic))}</div><h3>${title}</h3>${text ? html`<p>${text}</p>` : ''}${action ? raw(String(action)) : ''}</div>`;
}
export function meter(label, used, limit, unit = '') {
  const p = limit ? pct(used, limit) : 0;
  const tone = p >= 90 ? 'bad' : p >= 70 ? 'warn' : 'ok';
  return html`<div class="meter ${tone}">
    <div class="meter-top"><span>${label}</span><b>${fmtNum(Math.round(used * 10) / 10)}${unit}<small> / ${limit ? fmtNum(limit) + unit : t('Unlimited')}</small></b></div>
    <div class="bar ${limit ? '' : 'unlimited'}" role="progressbar" aria-label="${label}" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${limit ? p : 0}"><span style="width:${limit ? p : 100}%"></span></div></div>`;
}
export function toggle(name, checked, label, hint = '') {
  return html`<label class="switch"><input type="checkbox" name="${name}" ${checked ? 'checked' : ''}><span class="track" aria-hidden="true"></span>
    <span class="switch-text"><span>${label}</span>${hint ? html`<small>${hint}</small>` : ''}</span></label>`;
}
export function formData(form) {
  const o = {};
  for (const el of form.elements) {
    if (!el.name || el.disabled) continue;
    if (el.type === 'checkbox') { if (el.dataset.multi != null) { (o[el.name] ||= []); if (el.checked) o[el.name].push(el.value); } else o[el.name] = el.checked; }
    else if (el.type === 'radio') { if (el.checked) o[el.name] = el.value; }
    else if (el.type === 'number') o[el.name] = el.value === '' ? null : Number(el.value);
    else o[el.name] = el.value;
  }
  return o;
}
export function debounce(fn, ms = 300) { let x; return (...a) => { clearTimeout(x); x = setTimeout(() => fn(...a), ms); }; }
export function downloadBlob(name, text, type = 'text/csv') {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement('a'); a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** Chip input: returns {el html, bind(root) → {get,set}}. */
export function chipInput(root, { values = [], placeholder = '', name = 'chips' } = {}) {
  root.classList.add('chips-input');
  let list = [...values];
  const draw = () => {
    root.innerHTML = html`${list.map((v, i) => html`<span class="chip">${v}<button type="button" data-rm="${i}" aria-label="${t('Remove')} ${v}">${raw(icon('x'))}</button></span>`)}
      <input type="text" aria-label="${placeholder}" placeholder="${list.length ? '' : placeholder}" data-chip-in="${name}">`.s;
  };
  const add = (v) => { v.split(',').map((x) => x.trim()).filter(Boolean).forEach((x) => { if (!list.includes(x)) list.push(x); }); draw(); root.querySelector('input').focus(); };
  root.addEventListener('click', (e) => {
    const b = e.target.closest('[data-rm]');
    if (b) { list.splice(+b.dataset.rm, 1); draw(); root.querySelector('input').focus(); return; }
    if (e.target === root) root.querySelector('input').focus();
  });
  root.addEventListener('keydown', (e) => {
    const inp = e.target.closest('input'); if (!inp) return;
    if ((e.key === 'Enter' || e.key === ',') && inp.value.trim()) { e.preventDefault(); add(inp.value); }
    else if (e.key === 'Backspace' && !inp.value && list.length) { list.pop(); draw(); root.querySelector('input').focus(); }
  });
  root.addEventListener('focusout', (e) => { const inp = e.target.closest('input'); if (inp && inp.value.trim()) { const v = inp.value; inp.value = ''; v.split(',').map((x) => x.trim()).filter(Boolean).forEach((x) => { if (!list.includes(x)) list.push(x); }); draw(); } });
  draw();
  return { get: () => { const inp = root.querySelector('input'); if (inp && inp.value.trim()) add(inp.value); return [...list]; }, set: (v) => { list = [...v]; draw(); } };
}

// ---------------------------------------------------------------- icons (24px stroke)
const P = {
  home: 'M3 11.5 12 4l9 7.5M5 10v10h5v-6h4v6h5V10',
  phone: 'M8 3h8a1 1 0 0 1 1 1v16a1 1 0 0 1-1 1H8a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1zM11 18h2',
  send: 'M4 12 20 4l-5 16-3-7-8-1zM12 13l8-9',
  megaphone: 'M3 10v4h3l7 4V6L6 10H3zM16 9a4 4 0 0 1 0 6M18.5 6.5a7.5 7.5 0 0 1 0 11',
  calendar: 'M4 6h16v14H4zM4 10h16M8 3v4M16 3v4',
  users: 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM2 21a7 7 0 0 1 14 0M16 3.5a4 4 0 0 1 0 7.5M18 14a6 6 0 0 1 4 7',
  group: 'M7 10a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM17 10a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM2 20a5 5 0 0 1 10 0M12 20a5 5 0 0 1 10 0',
  image: 'M4 5h16v14H4zM4 16l5-5 4 4 3-3 4 4M15 9.5a1.5 1.5 0 1 0 0-.01',
  file: 'M6 3h8l4 4v14H6zM14 3v4h4M9 13h6M9 17h6',
  template: 'M4 4h16v6H4zM4 14h7v6H4zM15 14h5v6h-5z',
  bot: 'M7 8h10a3 3 0 0 1 3 3v6a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3v-6a3 3 0 0 1 3-3zM12 4v4M9 14h.01M15 14h.01M2 13v2M22 13v2',
  code: 'M8 7l-5 5 5 5M16 7l5 5-5 5M14 4l-4 16',
  key: 'M14 10a4 4 0 1 0-1.2 2.8L21 21M17 17l2-2M19 19l2-2',
  shield: 'M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6zM9 12l2 2 4-4',
  settings: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-2.7 1.2V21a2 2 0 1 1-4 0v-.1a1.6 1.6 0 0 0-2.7-1.2l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1A1.6 1.6 0 0 0 3.6 15H3a2 2 0 1 1 0-4h.1a1.6 1.6 0 0 0 1.2-2.7l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1A1.6 1.6 0 0 0 9.8 4.6V4a2 2 0 1 1 4 0v.1a1.6 1.6 0 0 0 2.7 1.2l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0 1.2 2.7H21a2 2 0 1 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1z',
  crown: 'M3 8l4 4 5-7 5 7 4-4-2 11H5zM5 19h14',
  inbox: 'M3 13l3-8h12l3 8v6H3zM3 13h5l1 3h6l1-3h5',
  server: 'M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01',
  log: 'M5 4h14v16H5zM9 8h6M9 12h6M9 16h4',
  plus: 'M12 5v14M5 12h14',
  x: 'M6 6l12 12M18 6 6 18',
  check: 'M5 12.5l4.5 4.5L19 7',
  alert: 'M12 3l10 18H2zM12 10v5M12 18h.01',
  info: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 11v6M12 7h.01',
  qr: 'M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h2v2h-2zM18 18h2v2h-2zM18 14h2M14 18h2',
  refresh: 'M20 11a8 8 0 0 0-14.6-4.5L4 8M4 4v4h4M4 13a8 8 0 0 0 14.6 4.5L20 16M20 20v-4h-4',
  pause: 'M8 5v14M16 5v14',
  play: 'M7 4l13 8-13 8z',
  globe: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c3 3.5 3 14.5 0 18M12 3c-3 3.5-3 14.5 0 18',
  link: 'M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1',
  edit: 'M4 20h4L19 9l-4-4L4 16zM14 6l4 4',
  trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3',
  logout: 'M15 4h4v16h-4M10 8l-4 4 4 4M6 12h10',
  copy: 'M9 9h11v11H9zM5 15H4V4h11v1',
  search: 'M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4',
  upload: 'M12 16V4M7 9l5-5 5 5M4 16v4h16v-4',
  download: 'M12 4v12M7 11l5 5 5-5M4 20h16',
  poll: 'M5 20V10M12 20V4M19 20v-7',
  pin: 'M12 21s7-6.2 7-11.5a7 7 0 1 0-14 0C5 14.8 12 21 12 21zM12 12a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5z',
  contact: 'M4 4h16v16H4zM12 12a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM7 18a5 5 0 0 1 10 0',
  menu: 'M4 6h16M4 12h16M4 18h16',
  list: 'M9 6h11M9 12h11M9 18h11M4 6h.01M4 12h.01M4 18h.01',
  text: 'M5 6V4h14v2M12 4v16M9 20h6',
  moon: 'M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z',
  sun: 'M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4',
  lang: 'M4 5h8M8 3v2M6 5c0 4 2 7 6 9M10 5c0 4-3 7-6 9M13 21l4-9 4 9M14.5 18h5',
  more: 'M5 12h.01M12 12h.01M19 12h.01',
  bolt: 'M13 2 4 14h7l-1 8 9-12h-7z',
  heart: 'M12 20s-8-4.5-8-11a4.5 4.5 0 0 1 8-2.8A4.5 4.5 0 0 1 20 9c0 6.5-8 11-8 11z',
  chevron: 'M9 6l6 6-6 6',
  up: 'M6 15l6-6 6 6',
  down: 'M6 9l6 6 6-6',
  clock: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2',
  story: 'M12 3a9 9 0 1 1-6.4 2.6M12 8v8M8 12h8',
  channel: 'M4 11a8 8 0 0 1 8 8M4 5a14 14 0 0 1 14 14M5 19h.01',
  sparkle: 'M12 3l2 5 5 2-5 2-2 5-2-5-5-2 5-2zM19 15l1 2 2 1-2 1-1 2-1-2-2-1 2-1z',
  mic: 'M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3zM5 11a7 7 0 0 0 14 0M12 18v3',
  video: 'M3 6h12v12H3zM15 10l6-3v10l-6-3',
  eye: 'M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  call: 'M5 4h4l2 5-2.5 1.5a11 11 0 0 0 5 5L15 13l5 2v4a2 2 0 0 1-2 2A16 16 0 0 1 3 6a2 2 0 0 1 2-2z',
  filter: 'M3 5h18l-7 8v6l-4 2v-8z',
  grip: 'M9 6h.01M15 6h.01M9 12h.01M15 12h.01M9 18h.01M15 18h.01',
  repeat: 'M17 2l4 4-4 4M3 11V8a2 2 0 0 1 2-2h16M7 22l-4-4 4-4M21 13v3a2 2 0 0 1-2 2H3',
  bell: 'M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9M10 21a2 2 0 0 0 4 0',
  form: 'M5 3h14v18H5zM9 8h6M9 12h6M9 16h4',
  briefcase: 'M3 8h18v12H3zM8 8V5a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v3M3 13h18',
};
export function icon(name, size = 18) {
  const d = P[name] || P.info;
  return `<svg class="i" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="${d}"/></svg>`;
}
export const ic = (name, size) => raw(icon(name, size));

// ---------------------------------------------------------------- shared caches
let sessCache = null, sessAt = 0;
export async function getSessions(force = false) {
  if (!force && sessCache && Date.now() - sessAt < 8000) return sessCache;
  const j = await api('GET', '/api/sessions', undefined, { silent: true });
  if (j.ok) { sessCache = j.sessions || []; sessAt = Date.now(); }
  return sessCache || [];
}
export function invalidateSessions() { sessAt = 0; }
export const sessionName = (s) => s.label || s.name;
export function sessionOptions(list, selected, { all = false, allLabel } = {}) {
  return html`${all ? html`<option value="">${allLabel || t('All my numbers')}</option>` : ''}${list.map((s) => html`<option value="${s.name}" ${s.name === selected ? 'selected' : ''}>${sessionName(s)}${s.phone ? ' · ' + fmtPhone(s.phone) : ''}${s.status !== 'connected' ? ' (' + sessionStatus(s.status).label + ')' : ''}</option>`)}`;
}
export const hasFeature = (me, f) => { const p = me?.licence?.plan; if (me?.user?.role === 'superadmin' || p?.unlimited) return true; return !!(p && p.features && p.features[f]); };
/** Plan limit text: 0 (or an unlimited plan) means no limit. */
export const limitText = (n, plan) => (plan?.unlimited || !n ? t('Unlimited') : fmtNum(n));
/** True when the licence never expires (owner / unlimited plans). */
export const neverExpires = (lic) => !!(lic?.plan?.unlimited || (lic?.status === 'active' && !lic?.expires_at));
