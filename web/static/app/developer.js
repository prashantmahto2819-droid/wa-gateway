import { api, html, raw, ic, t, relTime, pill, skel, empty, modal, confirmBox, toast, copy, $$, esc } from '/static/app/core.js';

export const EVENTS = ['*', 'message', 'message.receipt', 'message.deleted', 'poll.vote', 'call', 'queue.status', 'campaign.status', 'session.status', 'session.paired', 'session.timelock', 'session.health', 'contact.optout', 'contact.optin', 'group.update'];

export async function render(ctx) {
  const { root } = ctx;
  const base = location.origin;
  let hookEvents = EVENTS;
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Developer')}</h1><p>${t('Connect your website, CRM or app to Vivid Relay.')}</p></div>
      <div class="page-actions"><a class="btn" href="/docs" target="_blank" rel="noopener">${ic('file', 16)}${t('API docs')}</a><a class="btn" href="/openapi.json" target="_blank" rel="noopener">${ic('code', 16)}openapi.json</a></div></div>
    <div class="stack">
      <section class="panel"><div class="panel-head"><h2>${t('API keys')}</h2><button class="btn sm primary" data-a="newkey">${ic('plus', 14)}${t('Create key')}</button></div><div data-keys>${raw('<div class="panel-body">' + skel(3).s + '</div>')}</div></section>
      <section class="panel"><div class="panel-head"><h2>${t('Webhooks')}</h2><button class="btn sm primary" data-a="newhook">${ic('plus', 14)}${t('Add webhook')}</button></div><div data-hooks>${raw('<div class="panel-body">' + skel(3).s + '</div>')}</div></section>
      <section class="panel"><div class="panel-head"><h2>${t('Quick examples')}</h2></div><div class="panel-body stack">
        ${example(t('Send a text message'), `curl -X POST ${base}/api/sessions/YOUR_NUMBER/send \\
  -H "Authorization: Bearer vr_YOUR_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"to":"919876543210","text":"Hi {{name}}, your order is ready."}'`)}
        ${example(t('Send a photo with caption'), `curl -X POST ${base}/api/sessions/YOUR_NUMBER/send \\
  -H "Authorization: Bearer vr_YOUR_KEY" -H "Content-Type: application/json" \\
  -d '{"to":"919876543210","text":"New stock!","media":{"type":"image","media_id":12}}'`)}
        ${example(t('Add a contact'), `curl -X POST ${base}/api/contacts \\
  -H "Authorization: Bearer vr_YOUR_KEY" -H "Content-Type: application/json" \\
  -d '{"phone":"919876543210","name":"Asha","tags":["website-lead"]}'`)}
        ${example(t('Verify a webhook signature (Node.js)'), `const sig = 'sha256=' + crypto.createHmac('sha256', SECRET)
  .update(req.headers['x-timestamp'] + '.' + rawBody).digest('hex');
if (sig !== req.headers['x-signature']) return res.status(401).end();`)}
        <p class="hint">${t('YOUR_NUMBER is the internal name shown under Numbers → More.')}</p></div></section>
    </div></div>`.s;

  async function loadKeys() {
    const j = await api('GET', '/api/keys', undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    const l = j.ok ? j.keys || [] : [];
    root.querySelector('[data-keys]').innerHTML = l.length ? html`<div class="table-wrap"><table class="t"><thead><tr><th>${t('Name')}</th><th>${t('Key')}</th><th>${t('Created')}</th><th>${t('Last used')}</th><th class="act"></th></tr></thead><tbody>
      ${l.map((k) => html`<tr><td class="cell-main">${k.name}</td><td class="mono">${k.prefix}…</td><td class="cell-sub">${relTime(k.created_at)}</td><td class="cell-sub">${k.last_used ? relTime(k.last_used) : t('never')}</td>
        <td class="act"><button class="btn sm danger" data-revoke="${k.id}">${t('Revoke')}</button></td></tr>`)}</tbody></table></div>`.s
      : empty('key', t('No API keys'), t('Create a key for each app that sends through Vivid Relay, so you can revoke one without breaking the others.')).s;
  }
  async function loadHooks() {
    const j = await api('GET', '/api/webhooks', undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    const l = j.ok ? j.webhooks || [] : [];
    root.querySelector('[data-hooks]').innerHTML = l.length ? html`<ul class="list">${l.map((w) => html`<li>
      <span class="li-main"><b class="mono" style="font-weight:500">${w.url}</b><small>${(w.events || []).join(', ')}</small></span>
      ${w.last_status ? pill(String(w.last_status) + (w.last_at ? ' · ' + relTime(w.last_at) : ''), w.last_status < 300 ? 'ok' : 'bad') : pill(t('Not called yet'), 'muted')}
      ${raw(`<label class="switch" title="${esc(t('Active'))}"><input type="checkbox" data-hactive="${esc(w.id)}" ${w.active ? 'checked' : ''} aria-label="${esc(t('Active'))}"><span class="track"></span></label>`)}
      <span class="btn-row"><button class="btn sm" data-test="${w.id}">${t('Send test')}</button><button class="btn sm ghost" data-hedit="${w.id}">${t('Edit')}</button><button class="btn-icon danger" data-hdel="${w.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button></span></li>`)}</ul>`.s
      : empty('link', t('No webhooks'), t('Webhooks post incoming messages, delivery receipts and more to your server in real time.')).s;
    root._hooks = l;
    if (Array.isArray(j.events) && j.events.length) hookEvents = j.events.filter((e) => e !== 'test');
  }

  function hookModal(w) {
    const isNew = !w; w = w || { url: '', events: ['message'] };
    const m = modal({ title: isNew ? t('Add webhook') : t('Edit webhook'), size: 'lg', body: html`<form class="stack">
      <label class="field"><span>${t('URL')}</span><input name="url" type="url" required value="${w.url}" placeholder="https://example.com/vivid-hook" autofocus></label>
      <fieldset><legend>${t('Events')}</legend><div class="events-grid">${hookEvents.map((e) => html`<label class="check"><input type="checkbox" name="events" data-multi value="${e}" ${(w.events || []).includes(e) ? 'checked' : ''}>${e === '*' ? t('All events') : e}</label>`)}</div></fieldset>
      <div data-secret></div></form>`,
    footer: html`<button class="btn ghost" data-close>${t('Close')}</button><button class="btn primary" data-ok>${t('Save webhook')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async (ev) => {
      const f = m.body.querySelector('form'); if (!f.reportValidity()) return;
      const events = $$('[name=events]:checked', f).map((x) => x.value);
      if (!events.length) return toast(t('Pick at least one event.'), 'warn');
      const j = isNew ? await api('POST', '/api/webhooks', { url: f.url.value.trim(), events }) : await api('PATCH', `/api/webhooks/${w.id}`, { url: f.url.value.trim(), events });
      if (!j.ok) return;
      loadHooks();
      const secret = j.webhook?.secret || j.secret;
      if (isNew && secret) {
        m.body.querySelector('[data-secret]').innerHTML = html`<div class="note warn">${ic('alert', 16)}<span>${t('Copy the signing secret now. It is shown only once.')}</span></div><div class="secret mt-s"><span>${secret}</span><button type="button" class="btn sm" data-copy="${secret}">${ic('copy', 14)}${t('Copy')}</button></div>`.s;
        m.body.querySelector('[data-copy]').addEventListener('click', (e) => copy(e.currentTarget.dataset.copy));
        ev.currentTarget.remove();
      } else { toast(t('Webhook saved')); m.close(); }
    });
  }

  root.addEventListener('click', async (e) => {
    const a = e.target.closest('[data-a]')?.dataset.a;
    if (a === 'newkey') {
      const m = modal({ title: t('Create API key'), size: 'sm', body: html`<form class="stack"><label class="field"><span>${t('Name (where you will use it)')}</span><input name="name" required placeholder="${t('My website')}" autofocus></label><div data-out></div></form>`,
        footer: html`<button class="btn ghost" data-close>${t('Close')}</button><button class="btn primary" data-ok>${t('Create key')}</button>` });
      const go = async (ev) => {
        ev?.preventDefault(); const f = m.body.querySelector('form'); if (!f.reportValidity()) return;
        const j = await api('POST', '/api/keys', { name: f.name.value.trim() }); if (!j.ok) return;
        m.body.querySelector('[data-out]').innerHTML = html`<div class="note warn">${ic('alert', 16)}<span>${t('Copy this key now. For your safety it will not be shown again.')}</span></div><div class="secret mt-s"><span>${j.secret}</span><button type="button" class="btn sm" data-copy>${ic('copy', 14)}${t('Copy')}</button></div>`.s;
        m.body.querySelector('[data-copy]').addEventListener('click', () => copy(j.secret, t('API key copied')));
        m.foot.querySelector('[data-ok]')?.remove(); f.name.disabled = true; loadKeys();
      };
      m.body.querySelector('form').addEventListener('submit', go);
      m.foot.querySelector('[data-ok]').addEventListener('click', go);
      return;
    }
    if (a === 'newhook') return hookModal(null);
    const cb = e.target.closest('[data-copyblock]'); if (cb) return copy(cb.closest('.codeblock').querySelector('code').textContent);
    const rv = e.target.closest('[data-revoke]');
    if (rv && await confirmBox(t('Revoke this key? Apps using it stop working immediately.'), { okText: t('Revoke'), danger: true })) { if ((await api('DELETE', `/api/keys/${rv.dataset.revoke}`)).ok) { toast(t('Key revoked')); loadKeys(); } }
    const ts = e.target.closest('[data-test]');
    if (ts) { ts.classList.add('busy'); const j = await api('POST', `/api/webhooks/${ts.dataset.test}/test`, {}); ts.classList.remove('busy'); if (j.ok) toast(t('Test event sent') + (j.status ? ' · HTTP ' + j.status : '')); loadHooks(); }
    const he = e.target.closest('[data-hedit]'); if (he) return hookModal(root._hooks.find((w) => String(w.id) === he.dataset.hedit));
    const hd = e.target.closest('[data-hdel]');
    if (hd && await confirmBox(t('Delete this webhook?'), { okText: t('Delete'), danger: true })) { if ((await api('DELETE', `/api/webhooks/${hd.dataset.hdel}`)).ok) { toast(t('Webhook deleted')); loadHooks(); } }
  });
  root.addEventListener('change', async (e) => {
    const h = e.target.closest('[data-hactive]'); if (!h) return;
    if (!(await api('PATCH', `/api/webhooks/${h.dataset.hactive}`, { active: h.checked })).ok) h.checked = !h.checked;
  });
  await Promise.all([loadKeys(), loadHooks()]);
}

function example(title, code) {
  return html`<div><p class="field-label" style="margin-bottom:6px">${title}</p><div class="codeblock"><button class="btn-icon" data-copyblock aria-label="${t('Copy')}">${ic('copy', 16)}</button><code>${code}</code></div></div>`;
}
