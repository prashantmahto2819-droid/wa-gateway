// Reusable message composer (Send, Templates, Campaigns, Auto-replies) + media picker.
import { html, raw, ic, icon, t, esc, api, upload, modal, toast, fmtBytes, fmtTime, empty, skel, toggle, debounce, $, $$ } from '/static/app/core.js';

const KINDS = [
  ['text', 'text', 'Text'], ['media', 'image', 'Media'], ['poll', 'poll', 'Poll'],
  ['location', 'pin', 'Location'], ['contact', 'contact', 'Contact card'], ['menu', 'list', 'Menu'],
  ['buttons', 'check', 'Buttons'], ['list', 'list', 'List'], ['carousel', 'image', 'Carousel'],
];
const BTN_TYPES = [['reply', 'Reply'], ['url', 'Link'], ['call', 'Call'], ['copy', 'Copy code']];
const BASE_VARS = ['name', 'phone', 'first_name', 'ref', 'random'];
const NO_TEXT = ['poll', 'location', 'contact'];
const SAMPLE = { name: 'Asha Verma', first_name: 'Asha', phone: '+91 98765 43210', city: 'Korba', ref: '7KQ3XP', random: '482913' };

export function kindOf(m) {
  if (!m) return 'text';
  if (m.poll) return 'poll';
  if (m.location) return 'location';
  if (m.contact) return 'contact';
  if (m.menu) return 'menu';
  if (m.buttons) return 'buttons';
  if (m.list) return 'list';
  if (m.carousel) return 'carousel';
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
  if (k === 'buttons') return '🔘 ' + (m.text || m.buttons.title || '') + ' (' + (m.buttons.buttons || []).length + ')';
  if (k === 'list') return '📋 ' + (m.list.title || m.text || '');
  if (k === 'carousel') return '🎠 ' + (m.text || '') + ' (' + (m.carousel.cards || []).length + ' ' + t('cards') + ')';
  if (k === 'media') return (m.media.type === 'image' ? '🖼 ' : m.media.type === 'video' ? '🎬 ' : m.media.type === 'audio' ? '🎵 ' : '📄 ') + (m.text || m.media.filename || '');
  return m.text || '';
}

/** Render a WhatsApp-like preview bubble for a Message. */
export function bubbleHTML(m, { vars = {}, story = false, bg = '#0b6e5f', link = null } = {}) {
  const sub = (s) => renderText(applyVars(spin(String(s || '')), { ...SAMPLE, ...vars }));
  const now = fmtTime(Date.now() / 1000);
  const ts = raw(`<span class="ts">${esc(now)} ${icon('check', 12)}</span>`);
  const k = kindOf(m);
  if (!story && m.unique_code && !NO_TEXT.includes(k)) {
    const line = m.unique_code === 'number' ? ((m.unique_prefix || '#') + ((m.unique_prefix || '#').endsWith('#') ? '' : ' ') + SAMPLE.random) : ((m.unique_prefix || 'Ref:') + ' ' + SAMPLE.ref);
    m = { ...m, text: (m.text ? m.text.replace(/\s+$/, '') + '\n\n' : '') + line };
  }
  if (story) {
    if (m.media && m.media.type === 'image' && (m.media.url || m.media.media_id)) {
      return html`<div class="bubble story" style="background:#000;padding:0;overflow:hidden"><img alt="" src="${mediaSrc(m.media)}" style="width:100%;max-height:300px;object-fit:cover"></div>${m.text ? html`<div class="chat-note">${m.text}</div>` : ''}`;
    }
    return html`<div class="bubble story" style="background:${bg}">${m.text ? sub(m.text) : t('Your status text')}</div>`;
  }
  let inner = '';
  if (k === 'text') {
    const linkM = m.preview !== false && /https?:\/\/[^\s]+/.exec(m.text || '');
    const real = link && link.ok && !link.loading && link.url && (m.text || '').includes(link.url) ? link : null;
    inner = html`${real ? html`<div class="b-linkcard real">${real.thumb ? html`<img alt="" src="${real.thumb}">` : ''}<div><b>${real.title || t('Link preview')}</b>${real.description ? html`<small class="desc">${real.description}</small>` : ''}<small>${real.url.replace(/^https?:\/\//, '').split('/')[0]}</small></div></div>`
      : linkM ? html`<div class="b-linkcard"><div class="b-linkimg">${ic('image', 22)}</div><div><b>${t('Link preview')}</b><small>${linkM[0].replace(/^https?:\/\//, '').split('/')[0]}</small></div></div>` : ''}${m.text ? sub(m.text) : html`<span style="opacity:.5">${t('Your message will appear here')}</span>`}`;
  }
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
  } else if (k === 'buttons') {
    const bt = m.buttons || {}; const md = m.media;
    const head = md && md.type === 'image' && (md.url || md.media_id) ? html`<div class="b-media"><img alt="" src="${mediaSrc(md)}"></div>` : (md ? html`<div class="b-media">${ic(md.type === 'video' ? 'video' : md.type === 'image' ? 'image' : 'file', 30)}</div>` : '');
    const icons = { reply: '', url: '↗ ', call: '📞 ', copy: '⧉ ' };
    const btns = (bt.buttons || []).filter((b) => (b.text || '').trim());
    return html`<div class="bubble btns">${head}${bt.title ? html`<div><b>${sub(bt.title)}</b></div>` : ''}${m.text ? sub(m.text) : html`<span style="opacity:.5">${t('Message text')}</span>`}${bt.footer ? html`<div class="b-foot">${bt.footer}</div>` : ''}${ts}</div>
      <div class="b-buttons">${(btns.length ? btns : [{ text: t('Button') }]).map((b) => html`<div class="b-btn">${icons[b.type || 'reply'] || ''}${b.text}</div>`)}</div>`;
  } else if (k === 'carousel') {
    const cards = (m.carousel?.cards || []);
    return html`${m.text ? html`<div class="bubble">${sub(m.text)}${ts}</div>` : ''}
      <div class="b-carousel">${(cards.length ? cards : [{ title: t('Card 1') }, { title: t('Card 2') }]).map((c) => html`<div class="b-card2">
        <div class="b-media sm">${c.media && c.media.type === 'image' && (c.media.url || c.media.media_id) ? html`<img alt="" src="${mediaSrc(c.media)}">` : ic('image', 22)}</div>
        <b>${c.title || ''}</b><div class="b-text">${c.body || ''}</div>
        ${(c.buttons || []).filter((b) => (b.text || '').trim()).map((b) => html`<div class="b-btn">${b.text}</div>`)}</div>`)}</div>`;
  } else if (k === 'list') {
    const l = m.list || {}; const rows = (l.sections || []).flatMap((sec) => sec.rows || []).filter((r) => (r.title || '').trim());
    return html`<div class="bubble btns">${l.title ? html`<div><b>${sub(l.title)}</b></div>` : ''}${m.text ? sub(m.text) : html`<span style="opacity:.5">${t('Message text')}</span>`}${l.footer ? html`<div class="b-foot">${l.footer}</div>` : ''}${ts}</div>
      <div class="b-buttons"><div class="b-btn">☰ ${l.button_text || t('Choose')}</div></div>
      <div class="b-listpreview">${(rows.length ? rows : [{ title: t('Row') }]).slice(0, 10).map((r) => html`<div class="b-row"><span class="r"></span><div><div>${r.title}</div>${r.description ? html`<small>${r.description}</small>` : ''}</div></div>`)}</div>`;
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
function newCard() { return { title: '', body: '', footer: '', media: null, buttons: [{ type: 'reply', id: '', text: '' }] }; }

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
    buttons: opts.message?.buttons ? { ...opts.message.buttons, buttons: opts.message.buttons.buttons.map((b) => ({ ...b })) } : { title: '', footer: '', buttons: [{ type: 'reply', id: '', text: '' }, { type: 'reply', id: '', text: '' }] },
    list: opts.message?.list ? JSON.parse(JSON.stringify(opts.message.list)) : { title: '', button_text: t('Choose'), footer: '', sections: [{ title: '', rows: [{ id: '', title: '', description: '' }, { id: '', title: '', description: '' }] }] },
    carousel: opts.message?.carousel ? JSON.parse(JSON.stringify(opts.message.carousel)) : { cards: [newCard(), newCard()] },
    mention_all: !!opts.message?.mention_all,
    typing: opts.message?.typing !== false,
    preview: opts.message?.preview !== false,
    link: null, linkFor: '',
    unique: opts.message?.unique_code || '', uniquePrefix: opts.message?.unique_prefix || '',
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
        <div data-uq ${NO_TEXT.includes(st.kind) ? 'hidden' : ''}>${uqBlock()}</div>
        ${st.groupMode && !st.story ? toggle('mention_all', st.mention_all, t('Mention everyone in the group'), t('Each member gets a notification. Use rarely.')) : ''}
      </div>
      <div class="preview" aria-label="${t('Preview')}">
        <p class="preview-label">${ic('eye', 14)}${t('Live preview')} <span class="muted">· ${t('sample values')}</span></p>
        <div class="chat"><div class="chat-top"><span class="avatar">AV</span><div><b>${opts.previewTitle || 'Asha Verma'}</b><small>${t('online')}</small></div></div>
        <div class="chat-body" data-preview></div></div>
      </div></div>`.s;
    updatePreview();
    if (st.kind === 'text' && !st.story) { st.linkFor = ''; checkLinkSoon(); }
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
      case 'buttons': {
        const bt = st.buttons; const kindsUsed = new Set(bt.buttons.map((b) => (b.type === 'reply' ? 'reply' : 'cta')));
        return html`${textArea(t('Message text'), 4)}
        <div class="form-grid">
          <label class="field"><span>${t('Title (bold header, optional)')}</span><input type="text" data-bind="buttons.title" value="${bt.title}" placeholder="${t('Diwali offer')}"></label>
          <label class="field"><span>${t('Footer (small, optional)')}</span><input type="text" data-bind="buttons.footer" value="${bt.footer}" placeholder="${t('Offer valid till Friday')}"></label></div>
        <div class="field"><span class="field-label">${t('Buttons')} <small>(${bt.buttons.length}/3)</small></span>
          <div class="opt-rows">${bt.buttons.map((b, i) => html`<div class="opt-row btn-row">
            <select data-btn-type="${i}" aria-label="${t('Button type')}">${BTN_TYPES.map(([v, l]) => html`<option value="${v}" ${b.type === v ? 'selected' : ''}>${t(l)}</option>`)}</select>
            <input type="text" data-btn-text="${i}" value="${b.text}" maxlength="25" placeholder="${t('Button text')}" aria-label="${t('Button text')} ${i + 1}">
            ${b.type === 'url' ? html`<input type="url" data-btn-val="${i}" value="${b.url || ''}" placeholder="https://…" aria-label="URL">`
            : b.type === 'call' ? html`<input type="tel" data-btn-val="${i}" value="${b.phone || ''}" placeholder="919876543210" aria-label="${t('Phone')}">`
            : b.type === 'copy' ? html`<input type="text" data-btn-val="${i}" value="${b.code || ''}" placeholder="${t('Code to copy')}" aria-label="${t('Code')}">`
            : html`<input type="text" data-btn-val="${i}" value="${b.id || ''}" placeholder="id" aria-label="${t('Button id')}">`}
            <button type="button" class="btn-icon danger" data-btn-rm="${i}" aria-label="${t('Remove button')} ${i + 1}" ${bt.buttons.length <= 1 ? 'disabled' : ''}>${ic('x', 16)}</button></div>`)}</div>
          ${bt.buttons.length < 3 ? html`<div><button type="button" class="btn sm ghost" data-btn-add>${ic('plus', 16)}${t('Add button')}</button></div>` : ''}
          ${kindsUsed.size > 1 ? html`<p class="hint warn">${t('Reply buttons can\'t be mixed with link/call/copy buttons in one message.')}</p>` : ''}
          <p class="hint">${t('Reply buttons come back as a tap with their id (use trigger "Menu choice" in auto-replies). Optional photo/video/document header below. If WhatsApp refuses buttons for a number, the same message is sent as a numbered text menu automatically.')}</p></div>
        <details class="field"><summary>${t('Header media (optional)')}</summary>${mediaBlock()}</details>`;
      }
      case 'list': {
        const l = st.list;
        return html`${textArea(t('Message text'), 3)}
        <div class="form-grid">
          <label class="field"><span>${t('Title (optional)')}</span><input type="text" data-bind="list.title" value="${l.title}"></label>
          <label class="field"><span>${t('Button text')}</span><input type="text" data-bind="list.button_text" value="${l.button_text}" maxlength="20"></label>
          <label class="field"><span>${t('Footer (optional)')}</span><input type="text" data-bind="list.footer" value="${l.footer}"></label></div>
        ${l.sections.map((sec, si) => html`<div class="field list-sec">
          <div class="row"><input type="text" data-sec-title="${si}" value="${sec.title}" placeholder="${t('Section title (optional)')}" class="grow" aria-label="${t('Section title')}">
          ${l.sections.length > 1 ? html`<button type="button" class="btn-icon danger" data-sec-rm="${si}" aria-label="${t('Remove section')}">${ic('x', 16)}</button>` : ''}</div>
          <div class="opt-rows">${sec.rows.map((r, ri) => html`<div class="opt-row menu">
            <input type="text" data-row="${si}:${ri}:id" value="${r.id}" placeholder="id" aria-label="${t('Row id')}">
            <input type="text" data-row="${si}:${ri}:title" value="${r.title}" maxlength="24" placeholder="${t('Row title')}" aria-label="${t('Row title')}">
            <input type="text" data-row="${si}:${ri}:description" value="${r.description || ''}" placeholder="${t('Description (optional)')}" aria-label="${t('Row description')}">
            <button type="button" class="btn-icon danger" data-row-rm="${si}:${ri}" aria-label="${t('Remove row')}" ${sec.rows.length <= 1 ? 'disabled' : ''}>${ic('x', 16)}</button></div>`)}</div>
          <div class="row"><button type="button" class="btn sm ghost" data-row-add="${si}">${ic('plus', 16)}${t('Add row')}</button></div></div>`)}
        <div class="row"><button type="button" class="btn sm ghost" data-sec-add>${ic('plus', 16)}${t('Add section')}</button><span class="hint">${t('Up to 10 rows in total. The person taps the button and picks a row; the row id comes back like a menu choice.')}</span></div>`;
      }
      case 'carousel': {
        const cs = st.carousel.cards;
        return html`${textArea(t('Intro text (shown above the cards)'), 2)}
        <p class="hint">${t('2–10 cards, each with a photo, title, text and up to 3 buttons. Shows on Android and WhatsApp Web; on iPhone the cards show but buttons may not be tappable, so the text also says what to reply. If WhatsApp refuses the carousel, each card is sent as a normal photo message.')}</p>
        ${cs.map((c, i) => html`<div class="field list-sec card-edit">
          <div class="row"><b>${t('Card')} ${i + 1}</b><span class="grow"></span>${cs.length > 2 ? html`<button type="button" class="btn-icon danger" data-card-rm="${i}" aria-label="${t('Remove card')}">${ic('x', 16)}</button>` : ''}</div>
          <div class="form-grid">
            <label class="field"><span>${t('Title')}</span><input type="text" data-card="${i}:title" value="${c.title}" maxlength="60"></label>
            <label class="field"><span>${t('Footer (optional)')}</span><input type="text" data-card="${i}:footer" value="${c.footer || ''}"></label></div>
          <label class="field"><span>${t('Text')}</span><textarea data-card="${i}:body" rows="2">${c.body}</textarea></label>
          <div class="row"><span class="field-label">${t('Photo')}:</span>
            ${c.media && (c.media.media_id || c.media.url) ? html`<span class="ellipsis">${c.media.filename || (c.media.media_id ? t('Library file #{id}', { id: c.media.media_id }) : c.media.url)}</span><button type="button" class="btn sm ghost" data-card-media-clear="${i}">${t('Change')}</button>`
            : html`<button type="button" class="btn sm" data-card-media-lib="${i}">${ic('image', 14)}${t('Media library')}</button><input type="url" class="grow" data-card-media-url="${i}" placeholder="https://…/photo.jpg" aria-label="${t('Photo link')}">`}</div>
          <div class="opt-rows">${c.buttons.map((b, bi) => html`<div class="opt-row btn-row">
            <select data-cbtn-type="${i}:${bi}">${BTN_TYPES.map(([v, l]) => html`<option value="${v}" ${b.type === v ? 'selected' : ''}>${t(l)}</option>`)}</select>
            <input type="text" data-cbtn="${i}:${bi}:text" value="${b.text}" maxlength="25" placeholder="${t('Button text')}">
            <input type="text" data-cbtn="${i}:${bi}:val" value="${b.type === 'url' ? (b.url || '') : b.type === 'call' ? (b.phone || '') : b.type === 'copy' ? (b.code || '') : (b.id || '')}" placeholder="${b.type === 'url' ? 'https://…' : b.type === 'call' ? '9198…' : b.type === 'copy' ? t('Code') : 'id'}">
            <button type="button" class="btn-icon danger" data-cbtn-rm="${i}:${bi}" aria-label="${t('Remove button')}" ${c.buttons.length <= 1 ? 'disabled' : ''}>${ic('x', 16)}</button></div>`)}</div>
          ${c.buttons.length < 3 ? html`<div><button type="button" class="btn sm ghost" data-cbtn-add="${i}">${ic('plus', 14)}${t('Add button')}</button></div>` : ''}
        </div>`)}
        ${cs.length < 10 ? html`<div><button type="button" class="btn sm ghost" data-card-add>${ic('plus', 16)}${t('Add card')}</button></div>` : ''}`;
      }
      default: return html`${textArea(t('Message'), 6)}
        ${toggle('preview', st.preview, t('Show link preview card'), t('If the message has a link, its title, description and image are fetched and shown as a card (like sending from the phone).'))}
        <div data-linknote>${linkNote()}</div>`;
    }
  }

  function linkNote() {
    const l = st.link;
    if (!l || !st.preview || st.kind !== 'text' || st.story) return '';
    if (l.loading) return html`<p class="hint">${ic('refresh', 14)} ${t('Checking the link…')}</p>`;
    const again = html`<button type="button" class="btn sm ghost" data-link-again>${ic('refresh', 14)}${t('Check again')}</button>`;
    if (!l.ok) return html`<div class="note warn">${ic('alert', 16)}<span class="grow">${l.error || t('Could not check the link.')}</span>${again}</div>`;
    if (l.has_image) return html`<div class="note ok">${ic('check', 16)}<span class="grow">${t('Preview ready: title and picture found.')}${l.upload === 'failed' ? ' ' + t('(The big picture could not be uploaded right now; a small one will be used.)') : ''}</span>${again}</div>`;
    return html`<div class="note warn">${ic('alert', 16)}<span class="grow">${l.error ? t('The server could not open this page:') + ' ' + l.error : l.advice || t('No picture found on this page.')}</span>${again}</div>`;
  }
  async function checkLink(fresh = false) {
    const m = /https?:\/\/[^\s<>"']+/.exec(st.text || '');
    const url = m ? m[0].replace(/[.,;:!?)]+$/, '') : '';
    if (!url || !st.preview || st.kind !== 'text' || st.story) { if (st.link) { st.link = null; st.linkFor = ''; drawLinkNote(); updatePreview(); } return; }
    if (url === st.linkFor && !fresh) return;
    st.linkFor = url; st.link = { loading: true }; drawLinkNote();
    const sess = typeof opts.session === 'function' ? opts.session() : '';
    const j = await api('GET', `/api/link-preview?url=${encodeURIComponent(url)}${fresh ? '&fresh=1' : ''}${sess ? '&session=' + encodeURIComponent(sess) : ''}`, undefined, { silent: true });
    if (st.linkFor !== url) return;
    st.link = j.ok ? { ...j, ok: true } : { ok: false, error: j.error };
    drawLinkNote(); updatePreview();
  }
  const checkLinkSoon = debounce(() => checkLink(false), 900);
  function drawLinkNote() { const el = root.querySelector('[data-linknote]'); if (el) el.innerHTML = String(linkNote()); }

  function uqBlock() {
    if (st.story) return '';
    return html`${toggle('unique', !!st.unique, t('Add a unique code under every message'), t('Each person gets a different code at the bottom (e.g. Ref: 7KQ3XP), so no two messages are exactly alike. Or put {{ref}} / {{random}} anywhere in the text.'))}
      ${st.unique ? html`<div class="row" style="gap:10px;margin:-2px 0 6px 48px">
        <select data-uq-mode aria-label="${t('Code style')}" style="max-width:300px"><option value="ref" ${st.unique === 'ref' ? 'selected' : ''}>${t('Letters and digits — Ref: 7KQ3XP')}</option><option value="number" ${st.unique === 'number' ? 'selected' : ''}>${t('Number — #482913')}</option></select>
        <input type="text" data-uq-prefix maxlength="30" value="${st.uniquePrefix}" placeholder="${st.unique === 'number' ? '#' : 'Ref:'}" aria-label="${t('Text before the code')}" style="max-width:160px"></div>` : ''}`;
  }
  function drawUq() { const el = root.querySelector('[data-uq]'); if (el) { el.innerHTML = uqBlock().s; el.hidden = NO_TEXT.includes(st.kind); } }

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
    if (p) p.innerHTML = bubbleHTML(get(true), { story: st.story, bg: st.bg, link: st.link }).s;
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
    else if (el.dataset.btnText != null) st.buttons.buttons[+el.dataset.btnText].text = el.value;
    else if (el.dataset.btnVal != null) { const b = st.buttons.buttons[+el.dataset.btnVal]; const v = el.value; if (b.type === 'url') b.url = v; else if (b.type === 'call') b.phone = v; else if (b.type === 'copy') b.code = v; else b.id = v.replace(/\s+/g, '_').toLowerCase(); }
    else if (el.dataset.secTitle != null) st.list.sections[+el.dataset.secTitle].title = el.value;
    else if (el.dataset.card != null) { const [i, f] = el.dataset.card.split(':'); st.carousel.cards[+i][f] = el.value; }
    else if (el.dataset.cbtn != null) { const [i, bi, f] = el.dataset.cbtn.split(':'); const b = st.carousel.cards[+i].buttons[+bi]; if (f === 'text') b.text = el.value; else if (b.type === 'url') b.url = el.value; else if (b.type === 'call') b.phone = el.value; else if (b.type === 'copy') b.code = el.value; else b.id = el.value.replace(/\s+/g, '_').toLowerCase(); }
    else if (el.dataset.row != null) { const [si, ri, f] = el.dataset.row.split(':'); st.list.sections[+si].rows[+ri][f] = f === 'id' ? el.value.replace(/\s+/g, '_').toLowerCase() : el.value; }
    else if (el.matches('[data-uq-prefix]')) st.uniquePrefix = el.value;
    else if (el.name === 'mention_all') st.mention_all = el.checked;
    else if (el.name === 'preview') st.preview = el.checked;
    else if (el.name === 'poll.multiple') st.poll.multiple = el.checked;
    else return;
    updatePreview();
  });
  root.addEventListener('input', (e) => { if (e.target.dataset.bind === 'text') checkLinkSoon(); });
  root.addEventListener('change', async (e) => {
    const el = e.target;
    if (el.name === 'mention_all') { st.mention_all = el.checked; updatePreview(); }
    if (el.name === 'unique') { st.unique = el.checked ? 'ref' : ''; drawUq(); updatePreview(); }
    if (el.matches('[data-uq-mode]')) { st.unique = el.value; drawUq(); updatePreview(); }
    if (el.name === 'preview') { st.preview = el.checked; updatePreview(); checkLink(false); }
    if (el.name === 'poll.multiple') { st.poll.multiple = el.checked; updatePreview(); }
    if (el.dataset.cbtnType != null) { const [i, bi] = el.dataset.cbtnType.split(':'); st.carousel.cards[+i].buttons[+bi].type = el.value; redrawBody(); return; }
    if (el.matches('[data-card-media-url]') && el.value.trim()) { st.carousel.cards[+el.dataset.cardMediaUrl].media = { type: 'image', url: el.value.trim() }; redrawBody(); return; }
    if (el.dataset.btnType != null) { const b = st.buttons.buttons[+el.dataset.btnType]; b.type = el.value; redrawBody(); return; }
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
    if (b.hasAttribute('data-link-again')) { checkLink(true); return; }
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
    if (b.hasAttribute('data-btn-add')) { const first = st.buttons.buttons[0]; st.buttons.buttons.push({ type: first && first.type !== 'reply' ? 'url' : 'reply', id: '', text: '' }); redrawBody(); focusLast('[data-btn-text]'); return; }
    if (b.dataset.btnRm != null) { st.buttons.buttons.splice(+b.dataset.btnRm, 1); redrawBody(); return; }
    if (b.hasAttribute('data-card-add')) { st.carousel.cards.push(newCard()); redrawBody(); return; }
    if (b.dataset.cardRm != null) { st.carousel.cards.splice(+b.dataset.cardRm, 1); redrawBody(); return; }
    if (b.dataset.cardMediaClear != null) { st.carousel.cards[+b.dataset.cardMediaClear].media = null; redrawBody(); return; }
    if (b.dataset.cardMediaLib != null) { const x = await pickMedia({ kind: 'image' }); if (x) { st.carousel.cards[+b.dataset.cardMediaLib].media = { type: 'image', media_id: x.id, filename: x.name }; redrawBody(); } return; }
    if (b.dataset.cbtnAdd != null) { st.carousel.cards[+b.dataset.cbtnAdd].buttons.push({ type: 'reply', id: '', text: '' }); redrawBody(); return; }
    if (b.dataset.cbtnRm != null) { const [i, bi] = b.dataset.cbtnRm.split(':'); st.carousel.cards[+i].buttons.splice(+bi, 1); redrawBody(); return; }
    if (b.hasAttribute('data-sec-add')) { st.list.sections.push({ title: '', rows: [{ id: '', title: '', description: '' }] }); redrawBody(); return; }
    if (b.dataset.secRm != null) { st.list.sections.splice(+b.dataset.secRm, 1); redrawBody(); return; }
    if (b.dataset.rowAdd != null) { st.list.sections[+b.dataset.rowAdd].rows.push({ id: '', title: '', description: '' }); redrawBody(); return; }
    if (b.dataset.rowRm != null) { const [si, ri] = b.dataset.rowRm.split(':'); st.list.sections[+si].rows.splice(+ri, 1); redrawBody(); return; }
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
      case 'buttons': {
        m.text = st.text;
        m.buttons = { title: st.buttons.title, footer: st.buttons.footer, buttons: st.buttons.buttons.filter((b) => forPreview || (b.text || '').trim()).map((b, i) => {
          const o = { type: b.type || 'reply', text: (b.text || '').trim() };
          if (o.type === 'reply') o.id = b.id || `btn_${i + 1}`; if (o.type === 'url') o.url = b.url || ''; if (o.type === 'call') o.phone = b.phone || ''; if (o.type === 'copy') o.code = b.code || '';
          return o; }) };
        if (st.media.media_id || st.media.url) m.media = cleanMedia(st.media);
        break;
      }
      case 'carousel': {
        m.text = st.text;
        m.carousel = { cards: st.carousel.cards.map((c, i) => {
          const o = { title: c.title, body: c.body, footer: c.footer || undefined };
          if (c.media && (c.media.media_id || c.media.url)) o.media = c.media.media_id ? { type: 'image', media_id: c.media.media_id } : { type: 'image', url: c.media.url };
          o.buttons = c.buttons.filter((b) => forPreview || (b.text || '').trim()).map((b, bi) => {
            const x = { type: b.type || 'reply', text: (b.text || '').trim() };
            if (x.type === 'reply') x.id = b.id || `c${i + 1}_${bi + 1}`; if (x.type === 'url') x.url = b.url || ''; if (x.type === 'call') x.phone = b.phone || ''; if (x.type === 'copy') x.code = b.code || '';
            return x; });
          if (!o.buttons.length) delete o.buttons;
          return o; }) };
        break;
      }
      case 'list': {
        m.text = st.text;
        m.list = { title: st.list.title, button_text: st.list.button_text, footer: st.list.footer,
          sections: st.list.sections.map((sec, si) => ({ title: sec.title, rows: sec.rows.filter((r) => forPreview || (r.title || '').trim()).map((r, ri) => ({ id: r.id || `row_${si + 1}_${ri + 1}`, title: (r.title || '').trim(), description: (r.description || '').trim() || undefined })) })).filter((sec) => forPreview || sec.rows.length) };
        break;
      }
    }
    if (st.groupMode && st.mention_all) m.mention_all = true;
    if (st.unique && !NO_TEXT.includes(st.kind)) { m.unique_code = st.unique; if (st.uniquePrefix.trim()) m.unique_prefix = st.uniquePrefix.trim(); }
    m.typing = st.typing;
    if (st.kind === 'text' && !st.preview) m.preview = false;
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
      case 'buttons': {
        if (!m.text.trim()) return t('Write the message text.');
        if (m.buttons.buttons.length < 1) return t('Add at least one button.');
        const kinds = new Set(m.buttons.buttons.map((b) => (b.type === 'reply' ? 'reply' : 'cta')));
        if (kinds.size > 1) return t('Reply buttons can\'t be mixed with link/call/copy buttons in one message.');
        for (const b of m.buttons.buttons) {
          if (b.type === 'url' && !/^https?:\/\//.test(b.url)) return t('Link buttons need a full https:// address.');
          if (b.type === 'call' && b.phone.replace(/\D/g, '').length < 8) return t('Call buttons need a phone number.');
          if (b.type === 'copy' && !b.code.trim()) return t('Copy buttons need a code.');
        }
        if (m.media && m.media.type === 'audio') return t('Buttons can have an image, video or document header, not audio.');
        break;
      }
      case 'carousel': {
        if (m.carousel.cards.length < 2) return t('A carousel needs at least 2 cards.');
        for (const [i, c] of m.carousel.cards.entries()) {
          if (!(c.title || '').trim() && !(c.body || '').trim()) return t('Card {n} needs a title or text.', { n: i + 1 });
          const kinds = new Set((c.buttons || []).map((b) => (b.type === 'reply' ? 'reply' : 'cta')));
          if (kinds.size > 1) return t('Card {n}: reply buttons can\'t be mixed with link/call/copy buttons.', { n: i + 1 });
        }
        break;
      }
      case 'list': {
        if (!m.text.trim()) return t('Write the message text.');
        const rows = m.list.sections.flatMap((s2) => s2.rows);
        if (rows.length < 1) return t('Add at least one list row.'); if (rows.length > 10) return t('A list can have at most 10 rows.');
        break;
      }
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
    if (msg?.buttons) st.buttons = { ...msg.buttons, buttons: msg.buttons.buttons.map((b) => ({ ...b })) };
    if (msg?.list) st.list = JSON.parse(JSON.stringify(msg.list));
    if (msg?.carousel) st.carousel = JSON.parse(JSON.stringify(msg.carousel));
    st.mention_all = !!msg?.mention_all;
    st.preview = msg?.preview !== false;
    st.unique = msg?.unique_code || ''; st.uniquePrefix = msg?.unique_prefix || '';
    draw();
  }
  function reset() {
    st.text = ''; st.media = { type: 'image', url: '' }; st.mediaName = '';
    draw();
  }
  function setGroupMode(on) { if (st.groupMode !== !!on) { st.groupMode = !!on; draw(); } }
  function setStory(on) { if (st.story !== !!on) { st.story = !!on; draw(); } }
  draw();
  return { get, set, reset, validate, setGroupMode, setStory };
}
