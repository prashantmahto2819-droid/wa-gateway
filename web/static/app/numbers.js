import { api, html, raw, ic, t, fmtNum, pill, sessionStatus, fmtPhone, fmtDate, skel, empty, modal, confirmBox, promptBox, toast, toggle, formData, invalidateSessions, sessionName, pct, $ } from '/static/app/core.js';

export async function render(ctx) {
  const { root, me } = ctx;
  ctx.setTitle(t('Numbers'));
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Numbers')}</h1><p>${t('WhatsApp numbers linked to Vivid Relay. Each number keeps its own safe daily limits.')}</p></div>
      <div class="page-actions"><button class="btn" data-a="refresh">${ic('refresh', 16)}${t('Refresh')}</button><button class="btn primary" data-a="add">${ic('plus', 16)}${t('Add number')}</button></div></div>
    <div class="num-grid" data-list>${[1, 2].map(() => raw('<div class="skel card" style="height:300px"></div>'))}</div>
  </div>`.s;
  let sessions = [];
  const maxNumbers = me.licence?.plan?.unlimited ? 0 : me.licence?.plan?.max_numbers || 0;

  async function load() {
    const j = await api('GET', '/api/sessions');
    if (!ctx.isCurrent()) return;
    sessions = j.ok ? j.sessions || [] : [];
    invalidateSessions();
    draw();
  }
  function draw() {
    const list = root.querySelector('[data-list]');
    const canAdd = me.user.role === 'superadmin' || !maxNumbers || sessions.length < maxNumbers;
    if (!sessions.length) {
      list.style.display = 'block';
      list.innerHTML = html`<div class="panel">${empty('phone', t('Link your first number'), t('Add a number, then scan the QR code with WhatsApp on your phone: Settings → Linked devices → Link a device.'), html`<button class="btn primary" data-a="add">${ic('plus', 16)}${t('Add number')}</button>`)}</div>`.s;
      return;
    }
    list.style.display = '';
    list.innerHTML = html`${sessions.map(card)}
      <button class="add-card" data-a="add" ${canAdd ? '' : 'disabled'}>${ic('plus', 26)}<b>${t('Add number')}</b>
        <span>${maxNumbers ? t('{a} of {b} numbers used on your plan', { a: sessions.length, b: maxNumbers }) : t('Link another WhatsApp number')}</span></button>`.s;
  }

  function card(s) {
    const st = sessionStatus(s.status); const td = s.today || {}; const h = s.health || {};
    const linked = !!s.jid;
    const now = Date.now() / 1000;
    const healthTone = h.state === 'ok' ? 'ok' : h.state === 'slow' ? 'warn' : 'bad';
    const healthLabel = { ok: t('Healthy'), slow: t('Slowed down'), paused: t('Paused for safety') }[h.state] || h.state;
    return html`<article class="panel num-card" data-name="${s.name}">
      <div class="num-top">
        <span class="num-avatar ${s.status === 'connected' ? 'ok' : ''}">${ic('phone', 20)}</span>
        <div class="num-title"><h3>${sessionName(s)}</h3>
          <div class="cell-sub">${linked ? fmtPhone(s.phone) : t('Not linked yet')}${s.push_name ? ' · ' + s.push_name : ''}</div></div>
        ${pill(st.label, st.tone)}
      </div>
      ${linked ? html`<div class="num-stats">
        ${mini(t('Sent today'), td.sent, td.limit)}
        ${mini(t('New contacts today'), td.new_contacts, td.new_contact_limit)}
      </div>` : ''}
      <div class="num-notes">
        ${s.status === 'outdated' ? note('bad', 'alert', t('WhatsApp changed something. The server admin needs to run update.sh --whatsmeow.')) : ''}
        ${s.banned_until && s.banned_until > now ? note('bad', 'alert', t('Temporarily banned until {d}. Do not send from this number until then.', { d: fmtDate(s.banned_until) })) : ''}
        ${s.timelock_until && s.timelock_until > now ? note('warn', 'clock', t('WhatsApp blocked messages to new contacts until {d}. Replies to existing chats still work.', { d: fmtDate(s.timelock_until) })) : ''}
        ${s.paused ? note('warn', 'pause', t('Queue paused') + (s.pause_reason ? ': ' + s.pause_reason : '')) : ''}
        ${s.error ? note('bad', 'alert', s.error) : ''}
        ${linked && h.state ? html`<div class="note ${healthTone}">${ic('heart', 16)}<span><b>${healthLabel}</b> · ${t('fail rate')} ${Math.round((h.fail_rate || 0) * 100)}% · ${t('reply rate')} ${Math.round((h.reply_rate || 0) * 1000) / 10}%${h.reason ? html`<br>${h.reason}` : ''}</span></div>` : ''}
      </div>
      <div class="num-meta">
        <span>${ic('globe', 14)}<span class="ellipsis">${s.proxy ? t('Proxy') + ': ' + s.proxy : t('No proxy — uses the server IP')}</span></span>
        ${s.linked_at ? html`<span>${ic('link', 14)}${t('Linked')} ${fmtDate(s.linked_at, false)}</span>` : ''}
      </div>
      <div class="num-actions">
        ${!linked || ['qr', 'qr_expired', 'logged_out', 'new'].includes(s.status) ? html`<button class="btn sm primary" data-a="link">${ic('qr', 15)}${t('Link')}</button>` : ''}
        ${linked ? html`<button class="btn sm" data-a="reconnect">${ic('refresh', 15)}${t('Reconnect')}</button>` : ''}
        <button class="btn sm" data-a="pause">${ic(s.paused ? 'play' : 'pause', 15)}${s.paused ? t('Resume queue') : t('Pause queue')}</button>
        <button class="btn sm" data-a="settings">${ic('settings', 15)}${t('Settings')}</button>
        <button class="btn sm" data-a="proxy">${ic('globe', 15)}${t('Proxy')}</button>
        <button class="btn sm ghost" data-a="more" aria-haspopup="dialog">${ic('more', 15)}${t('More')}</button>
      </div>
    </article>`;
  }
  const mini = (label, a = 0, b = 0) => html`<div class="meter ${pct(a, b) >= 90 ? 'bad' : pct(a, b) >= 70 ? 'warn' : 'ok'}"><div class="meter-top"><span>${label}</span><b>${fmtNum(a)}<small> / ${fmtNum(b)}</small></b></div><div class="bar"><span style="width:${pct(a, b)}%"></span></div></div>`;
  const note = (tone, i, text) => html`<div class="note ${tone}">${ic(i, 16)}<span>${text}</span></div>`;

  root.addEventListener('click', async (e) => {
    const b = e.target.closest('[data-a]'); if (!b) return;
    const a = b.dataset.a;
    if (a === 'refresh') return load();
    if (a === 'add') return addNumber();
    const name = b.closest('[data-name]')?.dataset.name;
    const s = sessions.find((x) => x.name === name); if (!s) return;
    if (a === 'link') return linkModal(s, load);
    if (a === 'reconnect') { const j = await api('POST', `/api/sessions/${enc(name)}/reconnect`, {}); if (j.ok) toast(t('Reconnecting…')); setTimeout(load, 1200); }
    if (a === 'pause') { const j = await api('PATCH', `/api/sessions/${enc(name)}`, { paused: !s.paused }); if (j.ok) toast(s.paused ? t('Queue resumed') : t('Queue paused')); load(); }
    if (a === 'proxy') return proxyModal(s, load);
    if (a === 'settings') return settingsModal(s, load);
    if (a === 'more') return moreModal(s);
  });

  async function addNumber() {
    const label = await promptBox(t('Add number'), { label: t('Label (for you, e.g. Sales or Support)'), placeholder: t('Main'), okText: t('Add and link') });
    if (!label) return;
    const j = await api('POST', '/api/sessions', { label });
    if (!j.ok) return;
    await load();
    linkModal(j.session || { name: label, label }, load);
  }

  async function moreModal(s) {
    const linked = !!s.jid;
    const m = modal({ title: sessionName(s), size: 'sm', body: html`<div class="stack" style="gap:8px">
      <button class="btn" data-m="rename">${ic('edit', 16)}${t('Rename label')}</button>
      <button class="btn" data-m="webhook">${ic('link', 16)}${t('Webhook URL for this number')}</button>
      ${linked ? html`<button class="btn" data-m="logout">${ic('logout', 16)}${t('Log out from WhatsApp')}</button>` : ''}
      <button class="btn danger" data-m="delete">${ic('trash', 16)}${t('Delete number')}</button>
      <p class="hint">${t('Internal name')}: <code>${s.name}</code></p></div>` });
    m.body.addEventListener('click', async (e) => {
      const b = e.target.closest('[data-m]'); if (!b) return;
      const k = b.dataset.m; m.close();
      const n = enc(s.name);
      if (k === 'rename') { const v = await promptBox(t('Rename label'), { label: t('Label'), value: s.label || '' }); if (v && (await api('PATCH', `/api/sessions/${n}`, { label: v })).ok) { toast(t('Renamed')); load(); } }
      if (k === 'webhook') {
        const v = await promptBox(t('Webhook URL'), { label: t('Incoming messages for this number are posted here'), value: s.webhook_url || '', type: 'url', placeholder: 'https://example.com/whatsapp-hook', hint: t('Leave empty to remove. For more control use Developer → Webhooks.') });
        if (v !== null && (await api('PATCH', `/api/sessions/${n}`, { webhook_url: v })).ok) { toast(t('Webhook saved')); load(); }
      }
      if (k === 'logout' && await confirmBox(t('This unlinks the number from Vivid Relay. You will need to scan a QR code again.'), { okText: t('Log out'), danger: true })) {
        if ((await api('POST', `/api/sessions/${n}/logout`, {})).ok) { toast(t('Logged out')); load(); }
      }
      if (k === 'delete' && await confirmBox(t('Delete {n}? Its queue and settings are removed. Contacts and campaigns stay.', { n: sessionName(s) }), { okText: t('Delete number'), danger: true })) {
        if ((await api('DELETE', `/api/sessions/${n}`)).ok) { toast(t('Number deleted')); load(); }
      }
    });
  }

  ctx.every(10000, () => { if (!document.querySelector('.overlay')) load(); });
  await load();
}

const enc = encodeURIComponent;

/** QR + pair-code modal, polls every 3 s. */
export function linkModal(s, onDone) {
  const name = s.name;
  let tab = 'qr';
  const m = modal({ title: t('Link {n}', { n: sessionName(s) }), size: 'lg', body: html`
    <div class="seg full mb" role="tablist"><button role="tab" data-tab="qr" aria-selected="true">${ic('qr', 16)}${t('Scan QR code')}</button><button role="tab" data-tab="code" aria-selected="false">${ic('phone', 16)}${t('Use phone number')}</button></div>
    <div data-pane="qr"><div class="grid g2" style="align-items:center">
      <div class="qr-box"><div class="qr-frame" data-qr>${skel(1)}</div><span class="cell-sub" data-qr-status>${t('Preparing QR code…')}</span></div>
      <div><h3 class="mb">${t('On your phone')}</h3><ol class="steps-ol">
        <li>${t('Open WhatsApp (or WhatsApp Business).')}</li><li>${t('Tap Settings → Linked devices.')}</li><li>${t('Tap Link a device and point the camera at this code.')}</li></ol>
        <p class="hint mt">${t('The code refreshes by itself every few seconds. Keep this window open until it says linked.')}</p></div></div></div>
    <div data-pane="code" hidden><form data-pair class="stack" style="max-width:420px;margin:0 auto">
      <label class="field"><span>${t('Phone number with country code')}</span><input name="phone" type="tel" required placeholder="919876543210" autocomplete="tel"></label>
      <button class="btn primary">${t('Get pairing code')}</button>
      <div data-code></div>
      <ol class="steps-ol"><li>${t('On your phone: Settings → Linked devices → Link a device.')}</li><li>${t('Tap "Link with phone number instead".')}</li><li>${t('Type the 8-character code shown here.')}</li></ol></form></div>` });
  const setTab = (k) => { tab = k; m.body.querySelectorAll('[data-tab]').forEach((b) => b.setAttribute('aria-selected', String(b.dataset.tab === k))); m.body.querySelector('[data-pane="qr"]').hidden = k !== 'qr'; m.body.querySelector('[data-pane="code"]').hidden = k !== 'code'; };
  m.body.addEventListener('click', (e) => { const b = e.target.closest('[data-tab]'); if (b) setTab(b.dataset.tab); const r = e.target.closest('[data-retry]'); if (r) start(); });
  m.body.querySelector('[data-pair]').addEventListener('submit', async (e) => {
    e.preventDefault();
    const out = m.body.querySelector('[data-code]');
    out.innerHTML = skel(1).s;
    const j = await api('POST', `/api/sessions/${enc(name)}/pair-code`, { phone: e.target.phone.value.replace(/[^\d]/g, '') });
    out.innerHTML = j.ok ? html`<div class="qr-box"><div class="pair-code" aria-live="polite">${j.code}</div><button type="button" class="btn sm" data-copy="${j.code}">${ic('copy', 15)}${t('Copy code')}</button></div>`.s : '';
  });
  m.body.addEventListener('click', async (e) => { const c = e.target.closest('[data-copy]'); if (c) { const { copy } = await import('/static/app/core.js'); copy(c.dataset.copy); } });

  let timer = null, done = false;
  function show(j) {
    const box = m.body.querySelector('[data-qr]'), st = m.body.querySelector('[data-qr-status]');
    if (!box) return;
    if (!j.ok) { st.textContent = j.error || t('Could not get a QR code'); return; }
    if (j.status === 'connected' || (j.status === 'connecting' && !j.qr_image)) {
      done = true; clearInterval(timer);
      box.innerHTML = html`<div class="qr-box"><span class="success-burst">${ic('check', 36)}</span><b>${t('Linked')}</b></div>`.s;
      st.textContent = t('Your number is connected. You can close this window.');
      setTab('qr'); onDone && onDone();
      return;
    }
    if (j.qr_image) { box.innerHTML = html`<img alt="${t('QR code to scan with WhatsApp')}" src="${j.qr_image}"><span class="scanline"></span>`.s; st.textContent = t('Waiting for you to scan…'); }
    else if (j.status === 'qr_expired' || j.status === 'logged_out') {
      clearInterval(timer);
      box.innerHTML = html`<div class="qr-box">${ic('clock', 30)}<button class="btn" data-retry>${t('Get a new QR code')}</button></div>`.s;
      st.textContent = t('The QR code expired.');
    }
  }
  async function start() {
    clearInterval(timer);
    const j = await api('POST', `/api/sessions/${enc(name)}/qr`, {}, { silent: true });
    show(j);
    if (done) return;
    timer = m.every(3000, async () => { if (tab !== 'qr' || done) return; show(await api('GET', `/api/sessions/${enc(name)}/qr`, undefined, { silent: true })); });
  }
  m.onClose(() => { clearInterval(timer); onDone && onDone(); });
  start();
}

function proxyModal(s, reload) {
  const m = modal({ title: t('Proxy for {n}', { n: sessionName(s) }), body: html`<form data-f class="stack">
    <p class="lead">${t('A proxy gives this number its own IP address. Use a residential or mobile proxy from the same country as the number.')}</p>
    <label class="field"><span>${t('Proxy URL')}</span><input name="proxy" type="text" placeholder="socks5://user:pass@host:1080" value="${s.proxy || ''}" autocomplete="off"></label>
    <p class="hint">${t('Formats: socks5://user:pass@host:port or http://host:port. Leave empty and save to remove.')}</p>
    <div data-result></div></form>`,
  footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn" data-test>${ic('globe', 16)}${t('Test proxy')}</button><button class="btn primary" data-save>${t('Save proxy')}</button>` });
  const f = m.body.querySelector('form'), res = m.body.querySelector('[data-result]');
  f.addEventListener('submit', (e) => e.preventDefault());
  m.foot.querySelector('[data-test]').addEventListener('click', async (e) => {
    const b = e.currentTarget; b.classList.add('busy');
    const j = await api('POST', `/api/sessions/${enc(s.name)}/proxy-test`, { proxy: f.proxy.value.trim() });
    b.classList.remove('busy');
    const r = j.result || {};
    res.innerHTML = j.ok ? html`<div class="note ok">${ic('check', 16)}<span><b>${t('Proxy works')}</b><br>IP ${r.ip}${r.country ? ' · ' + [r.city, r.region, r.country].filter(Boolean).join(', ') : ''}${r.org ? ' · ' + r.org : ''}${r.latency_ms ? ' · ' + r.latency_ms + ' ms' : ''}</span></div>${j.warning ? html`<div class="note warn mt-s">${ic('alert', 16)}<span>${j.warning}</span></div>` : ''}`.s
      : html`<div class="note bad">${ic('alert', 16)}<span>${j.error || t('Proxy test failed')}</span></div>`.s;
  });
  m.foot.querySelector('[data-save]').addEventListener('click', async () => {
    const j = await api('PATCH', `/api/sessions/${enc(s.name)}`, { proxy: f.proxy.value.trim() });
    if (j.ok) { toast(f.proxy.value.trim() ? t('Proxy saved. The number reconnects through it.') : t('Proxy removed')); m.close(); reload(); }
  });
}

function settingsModal(s, reload) {
  const c = { reject_calls: false, reject_message: '', ignore_groups: false, ignore_channels: true, auto_save_contacts: true, always_online: false, takeover_minutes: 30, ...(s.settings || {}) };
  const m = modal({ title: t('Number settings · {n}', { n: sessionName(s) }), side: true, body: html`<form data-f class="stack" style="gap:6px">
    <fieldset class="panel panel-pad"><legend>${t('Calls')}</legend>
      ${toggle('reject_calls', c.reject_calls, t('Reject incoming calls'), t('The caller gets the message below.'))}
      <label class="field mt-s"><span>${t('Message sent after a rejected call')}</span><textarea name="reject_message" rows="2">${c.reject_message}</textarea></label></fieldset>
    <fieldset class="panel panel-pad"><legend>${t('Incoming messages')}</legend>
      ${toggle('ignore_groups', c.ignore_groups, t('Ignore group messages'), t('No webhooks or auto-replies for groups.'))}
      ${toggle('ignore_channels', c.ignore_channels, t('Ignore channel updates'))}
      ${toggle('auto_save_contacts', c.auto_save_contacts, t('Save new chats as contacts'), t('People who message you are added to Contacts.'))}</fieldset>
    <fieldset class="panel panel-pad"><legend>${t('Presence')}</legend>
      ${toggle('always_online', c.always_online, t('Always show as online'), t('Your phone may stop getting notifications while this is on.'))}
      <label class="field mt-s"><span>${t('Human takeover (minutes)')}</span><input name="takeover_minutes" type="number" min="0" max="1440" value="${c.takeover_minutes}">
        <small>${t('When you reply from the phone, auto-replies pause in that chat for this long.')}</small></label></fieldset></form>`,
  footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-save>${t('Save settings')}</button>` });
  m.foot.querySelector('[data-save]').addEventListener('click', async () => {
    const d = formData(m.body.querySelector('form'));
    d.takeover_minutes = Number(d.takeover_minutes) || 0;
    const j = await api('PATCH', `/api/sessions/${enc(s.name)}`, { settings: d });
    if (j.ok) { toast(t('Settings saved')); m.close(); reload(); }
  });
}
void $;
