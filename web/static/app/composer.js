// Reusable message composer (Send, Templates, Campaigns, Auto-replies) + media picker.
import { html, raw, ic, icon, t, esc, api, upload, modal, toast, fmtBytes, fmtTime, empty, skel, toggle, $, $$ } from '/static/app/core.js';

const KINDS = [
  ['text', 'text', 'Text'], ['media', 'image', 'Media'], ['poll', 'poll', 'Poll'],
  ['location', 'pin', 'Location'], ['contact', 'contact', 'Contact card'], ['menu', 'list', 'Menu'],
];
const BASE_VARS = ['name', 'phone', 'first_name'];
const SAMPLE = { name: 'Asha Verma', first_name: 'Asha', phone: '+91 98765 43210', city: 'Korba' };

export function kindOf(m) {
  if (!m) return 'text';
  if (m.poll) return 'poll';
  if (m.location) return 'location';
  if (m.contact) return 'contact';
  if (m.menu) return 'menu';
  if (m.media) return 'media';
  return 'text';
}
export function summarize(m) {
  if (!m) return '';
  const k = kindOf(m);
  if (k === 'poll') return '📊 ' + (m.poll.name || '');
  if (k === 'location') return '📍 ' + (m.location.name || `${m.location.lat}, ${m.location.lng}`);
  if (k === 'contact') return '👤 ' + (m.contact.name || m.contact.phone || '');
  if (k === 'menu') return '☰ ' + (m.menu.title || '') + ' (' + (m.menu.options || []).length + ')';
  if (k === 'media') return (m.media.type === 'image' ? '🖼 ' : m.media.type === 'video' ? '🎬 ' : m.media.type === 'audio' ? '🎵 ' : '📄 ') + (m.text || m.media.filename || '');
  return m.text || '';
}

/** Render a WhatsApp-like preview bubble for a Message. */
export function bubbleHTML(m, { vars = {}, story = false, bg = '#0b6e5f' } = {}) {
  const sub = (s) => renderText(applyVars(spin(String(s || '')), { ...SAMPLE, ...vars }));
  const now = fmtTime(Date.now() / 1000);
  const ts = raw(`<span class="ts">${esc(now)} ${icon('check', 12)}</span>`);
  const k = kindOf(m);
  if (story) {
    if (m.media && m.media.type === 'image' && (m.media.url || m.media.media_id)) {
      return html`<div class="bubble story" style="background:#000;padding:0;overflow:hidden"><img alt="" src="${mediaSrc(m.media)}" style="width:100%;max-height:300px;object-fit:cover"></div>${m.text ? html`<div class="chat-note">${m.text}</div>` : ''}`;
    }
    return html`<div class="bubble story" style="background:${bg}">${m.text ? sub(m.text) : t('Your status text')}</div>`;
  }
  let inner = '';
  if (k === 'text') inner = m.text ? sub(m.text) : html`<span style="opacity:.5">${t('Your message will appear here')}</span>`;
  else if (k === 'media') {
    const md = m.media || {};
    let block;
    if (md.type === 'image' && (md.url || md.media_id)) block = html`<div class="b-media"><img alt="" src="${mediaSrc(md)}"></div>`;
    else if (md.type === 'image' || md.type === 'video') block = html`<div class="b-media">${ic(md.type === 'video' ? 'video' : 'image', 30)}</div>`;
    else block = html`<div class="b-doc">${ic(md.type === 'audio' ? 'mic' : 'file', 22)}<span>${md.filename || (md.type === 'audio' ? t('Voice / audio') : t('Document'))}</span></div>`;
    inner = html`${block}${m.text ? sub(m.text) : ''}`;
  } else if (k === 'poll') {
    const p = m.poll || {}; const opts = (p.options || []).filter((x) => x.trim());
    inner = html`<div class="b-poll-q">${p.name || t('Poll question')}</div>
      <div class="b-poll-hint">${p.multiple ? t('Select one or more') : t('Select one')}</div>
      ${(opts.length ? opts : [t('Option 1'), t('Option 2')]).map((o) => html`<div class="b-opt ${p.multiple ? 'multi' : ''}"><span class="r"></span><span>${o}</span></div>`)}`;
  } else if (k === 'location') {
    const l = m.location || {};
    inner = html`<div class="b-map">${ic('pin', 30)}</div><b>${l.name || t('Location')}</b>${l.address ? html`<div class="b-foot">${l.address}</div>` : ''}<div class="b-foot">${l.lat ?? ''}, ${l.lng ?? ''}</div>`;
  } else if (k === 'contact') {
    const c = m.contact || {};
    inner = html`<div class="b-card"><span class="avatar">${(c.name || '?').slice(0, 1).toUpperCase()}</span><div><b>${c.name || t('Contact name')}</b><div class="b-foot">${c.phone ? '+' + c.phone : ''}</div></div></div><div class="b-foot" style="text-align:center">${t('Message')}</div>`;
  } else if (k === 'menu') {
    const mn = m.menu || {}; const opts = (mn.options || []).filter((o) => o.text);
    inner = html`${mn.title ? html`<div><b>${sub(mn.title)}</b></div>` : ''}${opts.map((o, i) => html`<div>${i + 1}. ${o.text}</div>`)}${mn.footer ? html`<div class="b-foot">${mn.footer}</div>` : ''}`;
  }
  const mention = m.mention_all ? html`<div class="chat-note">${t('@everyone will be mentioned')}</div>` : '';
  return html`${mention}<div class="bubble">${inner}${ts}</div>`;
}
function mediaSrc(md) { return md.media_id ? `/api/media/${encodeURIComponent(md.media_id)}/file` : (/^(https?:|data:|blob:|\/)/.test(md.url || '') ? md.url : ''); }
function spin(s) { return s.replace(/\{([^{}]*\|[^{}]*)\}/g, (_, g) => g.split('|')[0]); }
function applyVars(s, vars) { return s.replace(/\{\{\s*(\w+)\s*\}\}/g, (m, k) => (vars[k] != null ? vars[k] : m)); }
/** WhatsApp-ish formatting: *bold* _italic_ ~strike~ ```mono``` — escaped first. */
function renderText(s) {
  let h = esc(s);
  h = h.replace(/```([\s\S]+?)```/g, '<code>$1</code>')
    .replace(/(^|[\s(])\*([^*\n]+)\*/g, '$1<b>$2</b>')
    .replace(/(^|[\s(])_([^_\n]+)_/g, '$1<i>$2</i>')
    .replace(/(^|[\s(])~([^~\n]+)~/g, '$1<s>$2</s>')
    .replace(/@(\d{6,15})/g, '<span class="mention">@$1</span>');
  return raw('<span class="b-text">' + h + '</span>');
}

// ------------------------------------------------------------------ media picker
export function pickMedia({ kind = '' } = {}) {
  return new Promise((resolve) => {
    let done = false;
    const m = modal({ title: t('Choose from media library'), size: 'xl', body: skel(4) });
    m.onClose(() => { if (!done) resolve(null); });
    (async () => {
      const j = await api('GET', '/api/media');
      if (!j.ok) { m.body.innerHTML = empty('image', t('Could not load media')).s; return; }
      const list = (j.media || []).filter((x) => !kind || x.kind === kind);
      if (!list.length) { m.body.innerHTML = empty('image', t('No files yet'), t('Upload files in the Media library, or upload a new one from the composer.'), html`<a class="btn" href="/app/media" data-link>${t('Open media library')}</a>`).s; return; }
      m.body.innerHTML = html`<div class="media-grid">${list.map((x) => html`
        <div class="m-tile pickable" tabindex="0" role="button" data-id="${x.id}" aria-label="${t('Choose')} ${x.name}">
          <div class="m-prev">${x.kind === 'image' ? html`<img loading="lazy" alt="" src="/api/media/${x.id}/file">` : ic(mediaIcon(x.kind), 30)}</div>
          <div class="m-info"><b>${x.name}</b><small>${x.kind} · ${fmtBytes(x.size)}</small></div></div>`)}</div>`.s;
      const choose = (el) => { const x = list.find((y) => String(y.id) === el.dataset.id); done = true; resolve(x); m.close(); };
      m.body.addEventListener('click', (e) => { const el = e.target.closest('.m-tile'); if (el) choose(el); });
      m.body.addEventListener('keydown', (e) => { const el = e.target.closest('.m-tile'); if (el && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); choose(el); } });
    })();
  });
}
export const mediaIcon = (k) => ({ image: 'image', video: 'video', audio: 'mic', document: 'file' }[k] || 'file');
const kindFromMime = (mime) => (mime.startsWith('image/') ? 'image' : mime.startsWith('video/') ? 'video' : mime.startsWith('audio/') ? 'audio' : 'document');

// ------------------------------------------------------------------ composer
/**
 * createComposer(root, opts) → { get(), set(msg), validate() }
 * opts: { message, onChange, groupMode (show mention_all), customVars:[], compact, kinds:[...], previewTitle, story }
 */
export function createComposer(root, opts = {}) {
  const st = {
    kind: kindOf(opts.message),
    text: opts.message?.text || '',
    media: opts.message?.media ? { ...opts.message.media } : { type: 'image', url: '' },
    mediaName: '',
    poll: opts.message?.poll ? { ...opts.message.poll, options: [...opts.message.poll.options] } : { name: '', options: ['', ''], multiple: false },
    location: opts.message?.location ? { ...opts.message.location } : { lat: '', lng: '', name: '', address: '' },
    contact: opts.message?.contact ? { ...opts.message.contact } : { name: '', phone: '' },
    menu: opts.message?.menu ? { ...opts.message.menu, options: opts.message.menu.options.map((o) => ({ ...o })) } : { title: '', footer: t('Reply with a number'), options: [{ id: '', text: '' }, { id: '', text: '' }] },
    mention_all: !!opts.message?.mention_all,
    typing: opts.message?.typing !== false,
    vars: [...new Set([...BASE_VARS, ...(opts.customVars || [])])],
    groupMode: !!opts.groupMode,
    story: !!opts.story, bg: '#0b6e5f',
  };
  const kinds = KINDS.filter(([k]) => !opts.kinds || opts.kinds.includes(k));
  const uid = 'c' + Math.random().toString(36).slice(2, 7);

  function draw() {
    root.innerHTML = html`<div class="composer ${opts.compact ? 'compact' : ''}">
      <div class="comp-main">
        ${st.story ? '' : html`<div class="kind-tabs" role="tablist" aria-label="${t('Message type')}">
          ${kinds.map(([k, i, l]) => html`<button type="button" role="tab" data-kind="${k}" aria-selected="${st.kind === k}">${ic(i, 16)}${t(l)}</button>`)}
        </div>`}
        <div data-kind-body>${kindBody()}</div>
        ${st.groupMode && !st.story ? toggle('mention_all', st.mention_all, t('Mention everyone in the group'), t('Each member gets a notification. Use rarely.')) : ''}
      </div>
      <div class="preview" aria-label="${t('Preview')}">
        <p class="preview-label">${ic('eye', 14)}${t('Live preview')} <span class="muted">· ${t('sample values')}</span></p>
        <div class="chat"><div class="chat-top"><span class="avatar">AV</span><div><b>${opts.previewTitle || 'Asha Verma'}</b><small>${t('online')}</small></div></div>
        <div class="chat-body" data-preview></div></div>
      </div></div>`.s;
    updatePreview();
  }

  function textArea(label, rows = 5) {
    return html`<div class="field"><label for="${uid}-text" class="field-label">${label}</label>
      <textarea id="${uid}-text" data-bind="text" rows="${rows}" placeholder="${t('Hi {{name}}, ...')}">${st.text}</textarea>
      <div class="var-chips"><span class="muted">${t('Insert')}:</span>
        ${st.vars.map((v) => html`<button type="button" data-var="${v}">{{${v}}}</button>`)}
        <button type="button" data-var-custom>+ ${t('custom')}</button>
      </div>
      <p class="hint">${raw(t('Spintax: write <code>{Hi|Hello|Namaste}</code> and each person gets one at random. Formatting: *bold* _italic_ ~strike~.'))}</p></div>`;
  }

  function kindBody() {
    if (st.story) {
      return html`${textArea(t('Status text (or caption for a photo)'), 4)}
        <div class="row"><label class="field" style="flex:0 0 auto"><span>${t('Background')}</span><input type="color" data-bind="bg" value="${st.bg}"></label>
        <div class="field grow"><span class="field-label">${t('Photo or video (optional)')}</span>${mediaBlock()}</div></div>`;
    }
    switch (st.kind) {
      case 'media': return html`${mediaBlock()}${textArea(t('Caption (optional)'), 3)}`;
      case 'poll': return html`<div class="field"><label class="field-label" for="${uid}-pq">${t('Question')}</label><input id="${uid}-pq" type="text" data-bind="poll.name" maxlength="255" value="${st.poll.name}" placeholder="${t('Which day suits you?')}"></div>
        <div class="field"><span class="field-label">${t('Options')} <small>(${st.poll.options.length}/12)</small></span>
          <div class="opt-rows">${st.poll.options.map((o, i) => html`<div class="opt-row"><span class="opt-n">${i + 1}</span>
            <input type="text" data-poll-opt="${i}" value="${o}" maxlength="100" aria-label="${t('Option')} ${i + 1}">
            <button type="button" class="btn-icon danger" data-poll-rm="${i}" aria-label="${t('Remove option')} ${i + 1}" ${st.poll.options.length <= 2 ? 'disabled' : ''}>${ic('x', 16)}</button></div>`)}</div>
          ${st.poll.options.length < 12 ? html`<div><button type="button" class="btn sm ghost" data-poll-add>${ic('plus', 16)}${t('Add option')}</button></div>` : ''}</div>
        ${toggle('poll.multiple', st.poll.multiple, t('Allow more than one answer'))}`;
      case 'location': return html`<div class="form-grid">
          <label class="field"><span>${t('Latitude')}</span><input type="number" step="any" data-bind="location.lat" value="${st.location.lat}" placeholder="21.25"></label>
          <label class="field"><span>${t('Longitude')}</span><input type="number" step="any" data-bind="location.lng" value="${st.location.lng}" placeholder="81.63"></label>
          <label class="field"><span>${t('Place name')}</span><input type="text" data-bind="location.name" value="${st.location.name}"></label>
          <label class="field"><span>${t('Address')}</span><input type="text" data-bind="location.address" value="${st.location.address}"></label></div>
          <p class="hint">${t('Tip: in Google Maps, long-press a spot to copy its coordinates.')}</p>`;
      case 'contact': return html`<div class="form-grid">
          <label class="field"><span>${t('Name')}</span><input type="text" data-bind="contact.name" value="${st.contact.name}"></label>
          <label class="field"><span>${t('Phone with country code')}</span><input type="tel" data-bind="contact.phone" value="${st.contact.phone}" placeholder="919876543210"></label></div>`;
      case 'menu': return html`<div class="field"><label class="field-label" for="${uid}-mt">${t('Menu title')}</label><input id="${uid}-mt" type="text" data-bind="menu.title" value="${st.menu.title}" placeholder="${t('How can we help, {{name}}?')}"></div>
        <div class="field"><span class="field-label">${t('Options')} <small>${t('— the person replies with the number')}</small></span>
          <div class="opt-rows">${st.menu.options.map((o, i) => html`<div class="opt-row menu"><span class="opt-n">${i + 1}.</span>
            <input type="text" data-menu-id="${i}" value="${o.id}" placeholder="id" aria-label="${t('Option id')} ${i + 1}">
            <input type="text" data-menu-text="${i}" value="${o.text}" placeholder="${t('Price list')}" aria-label="${t('Option text')} ${i + 1}">
            <button type="button" class="btn-icon danger" data-menu-rm="${i}" aria-label="${t('Remove option')} ${i + 1}" ${st.menu.options.length <= 1 ? 'disabled' : ''}>${ic('x', 16)}</button></div>`)}</div>
          ${st.menu.options.length < 20 ? html`<div><button type="button" class="btn sm ghost" data-menu-add>${ic('plus', 16)}${t('Add option')}</button></div>` : ''}
          <p class="hint">${t('Use the option id in an auto-reply with trigger "Menu choice" to answer each option.')}</p></div>
        <label class="field"><span>${t('Footer')}</span><input type="text" data-bind="menu.footer" value="${st.menu.footer}"></label>`;
      default: return textArea(t('Message'), 6);
    }
  }

  function mediaBlock() {
    const md = st.media;
    const has = md.media_id || md.url;
    const name = st.mediaName || md.filename || (md.media_id ? t('Library file #{id}', { id: md.media_id }) : md.url);
    return html`<div class="stack" style="gap:10px">
      ${st.story ? '' : html`<div class="seg" role="radiogroup" aria-label="${t('Media type')}">${['image', 'video', 'document', 'audio'].map((k) => html`<button type="button" role="radio" data-mtype="${k}" aria-selected="${md.type === k}" aria-checked="${md.type === k}">${ic(mediaIcon(k), 15)}${t(k[0].toUpperCase() + k.slice(1))}</button>`)}</div>`}
      ${has ? html`<div class="media-pick"><div class="media-thumb">${md.type === 'image' ? html`<img alt="" src="${mediaSrc(md)}">` : ic(mediaIcon(md.type), 24)}</div>
        <div class="grow ellipsis"><b class="ellipsis" style="display:block">${name}</b><small class="muted">${md.media_id ? t('From media library') : t('From link')}</small></div>
        <button type="button" class="btn sm ghost" data-media-clear>${t('Change')}</button></div>`
      : html`<div class="row"><button type="button" class="btn" data-media-lib>${ic('image', 16)}${t('Media library')}</button>
          <label class="btn" tabindex="0" data-upload-label>${ic('upload', 16)}${t('Upload new')}<input type="file" data-media-file hidden></label>
          <span class="muted">${t('or')}</span>
          <input type="url" data-media-url class="grow" placeholder="https://…/offer.jpg" aria-label="${t('Media link')}" style="flex:1;min-width:180px"></div>
          <div data-upl-progress></div>`}
      ${md.type === 'document' && !st.story ? html`<label class="field"><span>${t('File name shown to the person')}</span><input type="text" data-bind="media.filename" value="${md.filename || ''}" placeholder="brochure.pdf"></label>` : ''}
    </div>`;
  }

  function updatePreview() {
    const p = root.querySelector('[data-preview]');
    if (p) p.innerHTML = bubbleHTML(get(true), { story: st.story, bg: st.bg }).s;
    opts.onChange && opts.onChange(get(true));
  }

  function setPath(path, v) {
    const [a, b] = path.split('.');
    if (b) st[a][b] = v; else st[a] = v;
  }

  root.addEventListener('input', (e) => {
    const el = e.target;
    if (el.dataset.bind) {
      let v = el.type === 'checkbox' ? el.checked : el.value;
      if (el.type === 'number') v = el.value === '' ? '' : Number(el.value);
      setPath(el.dataset.bind, v);
    } else if (el.dataset.pollOpt != null) st.poll.options[+el.dataset.pollOpt] = el.value;
    else if (el.dataset.menuId != null) st.menu.options[+el.dataset.menuId].id = el.value.replace(/\s+/g, '_').toLowerCase();
    else if (el.dataset.menuText != null) st.menu.options[+el.dataset.menuText].text = el.value;
    else if (el.name === 'mention_all') st.mention_all = el.checked;
    else if (el.name === 'poll.multiple') st.poll.multiple = el.checked;
    else return;
    updatePreview();
  });
  root.addEventListener('change', async (e) => {
    const el = e.target;
    if (el.name === 'mention_all') { st.mention_all = el.checked; updatePreview(); }
    if (el.name === 'poll.multiple') { st.poll.multiple = el.checked; updatePreview(); }
    if (el.matches('[data-media-url]') && el.value.trim()) { st.media.url = el.value.trim(); delete st.media.media_id; redrawBody(); }
    if (el.matches('[data-media-file]') && el.files[0]) {
      const f = el.files[0];
      const box = root.querySelector('[data-upl-progress]');
      box.innerHTML = html`<div class="upl"><span class="ellipsis">${f.name}</span><div class="bar"><span style="width:0%"></span></div><small data-p>0%</small></div>`.s;
      const fd = new FormData(); fd.append('file', f);
      const j = await upload('/api/media', fd, (p) => { const bar = box.querySelector('.bar span'); if (bar) { bar.style.width = Math.round(p * 100) + '%'; box.querySelector('[data-p]').textContent = Math.round(p * 100) + '%'; } });
      if (j.ok && j.media) {
        st.media = { type: st.story ? kindFromMime(j.media.mime || '') : (j.media.kind || kindFromMime(j.media.mime || '')), media_id: j.media.id, filename: j.media.kind === 'document' ? j.media.name : undefined };
        st.mediaName = j.media.name; toast(t('Uploaded to your media library'));
        redrawBody();
      } else box.innerHTML = '';
    }
  });
  root.addEventListener('keydown', (e) => {
    const l = e.target.closest('[data-upload-label]');
    if (l && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); l.querySelector('input').click(); }
  });
  root.addEventListener('click', async (e) => {
    const b = e.target.closest('button'); if (!b || !root.contains(b)) return;
    if (b.dataset.kind) { st.kind = b.dataset.kind; if (st.kind === 'media' && !st.media.type) st.media.type = 'image'; draw(); root.querySelector(`[data-kind="${st.kind}"]`)?.focus(); return; }
    if (b.dataset.var || b.hasAttribute('data-var-custom')) {
      let v = b.dataset.var;
      if (!v) {
        const { promptBox } = await import('/static/app/core.js');
        v = await promptBox(t('Custom field'), { label: t('Field name (a CSV column, e.g. city)'), placeholder: 'city' });
        if (!v) return; v = v.replace(/[^\w]/g, '_');
        if (!st.vars.includes(v)) st.vars.push(v);
      }
      insertAtCursor(root.querySelector('[data-bind="text"]') || root.querySelector('[data-bind="menu.title"]'), `{{${v}}}`);
      if (b.hasAttribute('data-var-custom')) redrawBody();
      return;
    }
    if (b.hasAttribute('data-poll-add')) { st.poll.options.push(''); redrawBody(); focusLast('[data-poll-opt]'); return; }
    if (b.dataset.pollRm != null) { st.poll.options.splice(+b.dataset.pollRm, 1); redrawBody(); return; }
    if (b.hasAttribute('data-menu-add')) { st.menu.options.push({ id: '', text: '' }); redrawBody(); focusLast('[data-menu-text]'); return; }
    if (b.dataset.menuRm != null) { st.menu.options.splice(+b.dataset.menuRm, 1); redrawBody(); return; }
    if (b.dataset.mtype) { st.media.type = b.dataset.mtype; redrawBody(); return; }
    if (b.hasAttribute('data-media-clear')) { st.media = { type: st.media.type || 'image', url: '' }; st.mediaName = ''; redrawBody(); return; }
    if (b.hasAttribute('data-media-lib')) {
      const x = await pickMedia({ kind: st.story ? '' : st.media.type });
      if (x) { st.media = { type: x.kind, media_id: x.id, filename: x.kind === 'document' ? x.name : undefined }; st.mediaName = x.name; redrawBody(); }
    }
  });
  function focusLast(sel) { const l = $$(sel, root); l[l.length - 1]?.focus(); }
  function redrawBody() {
    const kb = root.querySelector('[data-kind-body]');
    const focusSel = document.activeElement && root.contains(document.activeElement) ? null : null;
    kb.innerHTML = kindBody().s; updatePreview(); return focusSel;
  }
  function insertAtCursor(el, s) {
    if (!el) return;
    const a = el.selectionStart ?? el.value.length, b = el.selectionEnd ?? el.value.length;
    el.value = el.value.slice(0, a) + s + el.value.slice(b);
    el.focus(); el.selectionStart = el.selectionEnd = a + s.length;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }

  /** Build the API Message object. */
  function get(forPreview = false) {
    const m = {};
    if (st.story) {
      if (st.text) m.text = st.text;
      if (st.media.media_id || st.media.url) m.media = cleanMedia(st.media);
      m.background = st.bg;
      return m;
    }
    switch (st.kind) {
      case 'text': m.text = st.text; break;
      case 'media': m.media = cleanMedia(st.media); if (st.text) m.text = st.text; break;
      case 'poll': m.poll = { name: st.poll.name, options: forPreview ? st.poll.options : st.poll.options.map((x) => x.trim()).filter(Boolean), multiple: !!st.poll.multiple }; break;
      case 'location': m.location = { lat: Number(st.location.lat), lng: Number(st.location.lng), name: st.location.name, address: st.location.address }; break;
      case 'contact': m.contact = { name: st.contact.name, phone: String(st.contact.phone).replace(/[^\d]/g, '') }; break;
      case 'menu': m.menu = { title: st.menu.title, footer: st.menu.footer, options: st.menu.options.filter((o) => o.text.trim()).map((o, i) => ({ id: o.id || String(i + 1), text: o.text.trim() })) }; break;
    }
    if (st.groupMode && st.mention_all) m.mention_all = true;
    m.typing = st.typing;
    return m;
  }
  function cleanMedia(md) {
    const o = { type: md.type || 'image' };
    if (md.media_id) o.media_id = md.media_id; else o.url = md.url || '';
    if (md.filename) o.filename = md.filename;
    return o;
  }
  function validate() {
    const m = get();
    if (st.story) { if (!m.text && !m.media) return t('Write status text or add a photo.'); return ''; }
    switch (st.kind) {
      case 'text': if (!m.text.trim()) return t('Write a message first.'); break;
      case 'media': if (!m.media.media_id && !m.media.url) return t('Choose a file or paste a link.'); break;
      case 'poll': if (!m.poll.name.trim()) return t('Add a poll question.'); if (m.poll.options.length < 2) return t('A poll needs at least 2 options.'); break;
      case 'location': if (!isFinite(m.location.lat) || !isFinite(m.location.lng) || st.location.lat === '' || st.location.lng === '') return t('Enter latitude and longitude.'); break;
      case 'contact': if (!m.contact.name || m.contact.phone.length < 7) return t('Add the contact name and phone.'); break;
      case 'menu': if (m.menu.options.length < 1) return t('Add at least one menu option.'); break;
    }
    return '';
  }
  function set(msg) {
    const c = createComposer; void c;
    st.kind = kindOf(msg); st.text = msg?.text || '';
    if (msg?.media) st.media = { ...msg.media };
    if (msg?.poll) st.poll = { ...msg.poll, options: [...msg.poll.options] };
    if (msg?.location) st.location = { ...msg.location };
    if (msg?.contact) st.contact = { ...msg.contact };
    if (msg?.menu) st.menu = { ...msg.menu, options: msg.menu.options.map((o) => ({ ...o })) };
    st.mention_all = !!msg?.mention_all;
    draw();
  }
  function setGroupMode(on) { if (st.groupMode !== !!on) { st.groupMode = !!on; draw(); } }
  function setStory(on) { if (st.story !== !!on) { st.story = !!on; draw(); } }
  draw();
  return { get, set, validate, setGroupMode, setStory };
}
