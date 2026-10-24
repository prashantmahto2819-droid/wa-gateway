import { api, html, raw, ic, t, fmtNum, fmtDate, relTime, pill, skel, empty, modal, confirmBox, promptBox, toast, copy, formData, toggle, debounce, $$ } from '/static/app/core.js';
import { FEATURES, planLimits } from '/static/app/licence.js';

const TITLES = { overview: 'Admin overview', users: 'Users', plans: 'Plans', licences: 'Licences', activations: 'Activations', settings: 'Site settings', inbox: 'Inbox', server: 'Licence server', audit: 'Audit log', proxies: 'Proxy pool' };

export async function render(ctx) {
  const v = ctx.params.view || 'overview';
  ctx.setTitle(t(TITLES[v]), html`<a href="/app/admin" data-link>${t('Admin')}</a> ${ic('chevron', 14)} <span>${t(TITLES[v])}</span>`);
  const fn = { overview, users, plans, licences, activations, settings, inbox, server, audit, proxies }[v];
  ctx.root.innerHTML = html`<div class="page"><div class="page-head"><div><h1>${t(TITLES[v])}</h1><p data-sub></p></div><div class="page-actions" data-actions></div></div><div data-body>${skel(6)}</div></div>`.s;
  await fn(ctx, ctx.root.querySelector('[data-body]'), ctx.root.querySelector('[data-actions]'), ctx.root.querySelector('[data-sub]'));
}
const featList = () => (featureKeys || FEATURES.map(([k]) => k)).map((k) => [k, (FEATURES.find(([x]) => x === k) || [k, k.charAt(0).toUpperCase() + k.slice(1)])[1]]);
const ENTITLEMENTS = [['staff', 'Staff seats'], ['websites', 'Websites (Link Publisher)'], ['flows', 'Chatbot flows'], ['sequences', 'Follow-up sequences'], ['lists', 'Saved lists']];
const truthy = (v) => v === true || v === 'true' || v === '1' || v === 1;
const statusTone = (s) => ({ active: 'ok', unused: 'info', expired: 'warn', suspended: 'bad', disabled: 'bad' }[s] || 'muted');
let plansCache = null, featureKeys = null;
async function getPlans(force) {
  if (!plansCache || force) { const j = await api('GET', '/api/admin/plans', undefined, { silent: true }); plansCache = j.ok ? j.plans || [] : []; if (j.ok && Array.isArray(j.features)) featureKeys = j.features; }
  return plansCache;
}
const planOptions = (plans, sel) => plans.map((p) => html`<option value="${p.id}" ${p.id === sel ? 'selected' : ''}>${p.name} · ${p.period_days}d</option>`);

// ------------------------------------------------------------------ overview
async function overview(ctx, body, _a, sub) {
  sub.textContent = t('The whole installation at a glance.');
  const j = await api('GET', '/api/admin/overview');
  if (!ctx.isCurrent() || !j.ok) return;
  const tiles = [['megaphone', 'Running campaigns', j.running_campaigns ?? 0], ['clock', 'Queued messages', j.queued ?? 0], ['users', 'Users', j.users, '/app/admin/users'], ['key', 'Active licences', j.active_licences, '/app/admin/licences'], ['phone', 'Numbers', `${fmtNum(j.connected)} / ${fmtNum(j.sessions)}`, null, 'connected / total'],
    ['send', 'Sent today', j.sent_today], ['poll', 'Sent this month', j.sent_month], ['inbox', 'Unread messages', j.messages_unread, '/app/admin/inbox'],
    ['link', 'Posts waiting approval', j.posts_pending ?? 0], ['sparkle', 'Active flows', j.flows_active ?? 0], ['globe', 'Proxies in pool', j.proxies ?? 0, '/app/admin/proxies'], ['alert', 'Numbers paused / banned', j.sessions_paused ?? 0]];
  body.innerHTML = html`<div class="grid g3">${tiles.map(([i, l, v, href, s]) => html`<${raw(href ? 'a' : 'div')} class="panel ${href ? 'tile-link' : ''}" ${href ? raw(`href="${href}" data-link`) : ''} style="${href ? '' : 'display:block'}">
    <div class="kpi"><span class="k-label">${ic(i, 15)}${t(l)}</span><span class="k-val">${typeof v === 'number' ? fmtNum(v) : v}</span>${s ? html`<span class="k-sub">${t(s)}</span>` : ''}</div></${raw(href ? 'a' : 'div')}>`)}</div>
    ${(j.alerts || []).length ? html`<section class="panel mt"><div class="panel-head"><h2>${t('Needs attention')}</h2></div><ul class="list">${j.alerts.map((a) => html`<li><span class="list-ico">${ic('alert')}</span><span class="li-main"><b>${a.title}</b><small>${a.detail}</small></span>${a.href ? html`<a class="btn sm" href="${a.href}" data-link>${t('Open')}</a>` : ''}</li>`)}</ul></section>` : ''}
    <div class="grid g4 mt">${[['users', '/app/admin/users', 'Users'], ['template', '/app/admin/plans', 'Plans'], ['key', '/app/admin/licences', 'Licences'], ['globe', '/app/admin/proxies', 'Proxy pool'], ['shield', '/app/admin/settings#sending', 'Sending safety & quotas'], ['log', '/app/admin/audit', 'Audit log'], ['inbox', '/app/admin/inbox', 'Inbox'], ['settings', '/app/admin/settings', 'Site settings']].map(([i, h, l]) => html`<a class="tile-link" href="${h}" data-link><span class="list-ico">${ic(i)}</span><b>${t(l)}</b></a>`)}</div>`.s;
}

// ------------------------------------------------------------------ users
async function users(ctx, body, actions, sub) {
  sub.textContent = t('Everyone with an account. Change roles, reset passwords, and give plans.');
  actions.innerHTML = html`<div class="search">${ic('search', 16)}<input type="search" data-q placeholder="${t('Search name or email')}" aria-label="${t('Search users')}"></div>`.s;
  let list = [];
  const plans = await getPlans();
  async function load(q = '') {
    const j = await api('GET', '/api/admin/users?q=' + encodeURIComponent(q), undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    list = j.ok ? j.users || [] : [];
    body.innerHTML = list.length ? html`<section class="panel"><div class="table-wrap"><table class="t"><thead><tr><th>${t('User')}</th><th>${t('Role')}</th><th>${t('Status')}</th><th>${t('Plan')}</th><th>${t('Numbers')}</th><th>${t('Last login')}</th><th class="act"></th></tr></thead><tbody>
      ${list.map((u) => html`<tr><td><div class="cell-main">${u.name || '—'}</div><div class="cell-sub">${u.email}${u.phone ? ' · ' + u.phone : ''}</div></td>
        <td><select data-role="${u.id}" style="width:auto;min-height:30px;padding:4px 30px 4px 8px" aria-label="${t('Role')}"><option value="user" ${u.role === 'user' ? 'selected' : ''}>${t('User')}</option><option value="superadmin" ${u.role === 'superadmin' ? 'selected' : ''}>${t('Super admin')}</option></select></td>
        <td>${pill(t(u.status === 'active' ? 'Active' : 'Disabled'), u.status === 'active' ? 'ok' : 'bad')}</td>
        <td>${u.licence?.plan ? html`<span class="cell-main">${u.licence.plan.name}</span><div class="cell-sub">${u.licence.status === 'active' ? (u.licence.plan.unlimited || !u.licence.expires_at ? t('Never expires') : t('{n} days left', { n: u.licence.days_left })) : t(u.licence.status)}</div>` : html`<span class="cell-sub">—</span>`}</td>
        <td class="tnum">${Array.isArray(u.sessions) ? u.sessions.length : u.sessions ?? 0}</td><td class="cell-sub nowrap">${u.last_login ? relTime(u.last_login) : t('never')}</td>
        <td class="act"><span class="btn-row" style="flex-wrap:nowrap"><button class="btn sm" data-grant="${u.id}">${t('Give plan')}</button><button class="btn sm ghost" data-umore="${u.id}" aria-label="${t('More')}">${ic('more', 16)}</button></span></td></tr>`)}</tbody></table></div></section>`.s
      : empty('users', t('No users found')).s;
  }
  actions.querySelector('[data-q]').addEventListener('input', debounce((e) => load(e.target.value), 300));
  body.addEventListener('change', async (e) => {
    const r = e.target.closest('[data-role]'); if (!r) return;
    if ((await api('PATCH', `/api/admin/users/${r.dataset.role}`, { role: r.value })).ok) toast(t('Role updated'));
  });
  body.addEventListener('click', async (e) => {
    const g = e.target.closest('[data-grant]');
    if (g) {
      const u = list.find((x) => String(x.id) === g.dataset.grant);
      const m = modal({ title: t('Give a plan to {n}', { n: u.name || u.email }), size: 'sm', body: html`<form class="stack"><label class="field"><span>${t('Plan')}</span><select name="plan_id">${planOptions(plans)}</select></label><label class="field"><span>${t('Days')}</span><input name="days" type="number" min="1" value="30" required></label></form>`,
        footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Give plan')}</button>` });
      m.foot.querySelector('[data-ok]').addEventListener('click', async () => { const f = m.body.querySelector('form'); if ((await api('POST', `/api/admin/users/${u.id}/grant`, { plan_id: +f.plan_id.value, days: +f.days.value })).ok) { toast(t('Plan given')); m.close(); load(); } });
      return;
    }
    const mo = e.target.closest('[data-umore]');
    if (mo) {
      const u = list.find((x) => String(x.id) === mo.dataset.umore);
      const m = modal({ title: u.name || u.email, size: 'sm', body: html`<div class="stack" style="gap:8px">
        <button class="btn" data-x="rename">${ic('edit', 16)}${t('Rename')}</button>
        <button class="btn" data-x="status">${ic(u.status === 'active' ? 'pause' : 'play', 16)}${u.status === 'active' ? t('Disable account') : t('Enable account')}</button>
        <button class="btn" data-x="reset">${ic('key', 16)}${t('Reset password')}</button>
        <button class="btn danger" data-x="delete">${ic('trash', 16)}${t('Delete user')}</button><div data-out></div></div>` });
      m.body.addEventListener('click', async (ev) => {
        const b = ev.target.closest('[data-x]'); if (!b) return;
        const x = b.dataset.x;
        if (x === 'rename') { const n = await promptBox(t('Rename'), { label: t('Name'), value: u.name }); if (n && (await api('PATCH', `/api/admin/users/${u.id}`, { name: n })).ok) { toast(t('Saved')); m.close(); load(); } }
        if (x === 'status') { if ((await api('PATCH', `/api/admin/users/${u.id}`, { status: u.status === 'active' ? 'disabled' : 'active' })).ok) { toast(t('Saved')); m.close(); load(); } }
        if (x === 'reset' && await confirmBox(t('Create a new temporary password for this user? Their old password stops working.'), { okText: t('Reset password') })) {
          const j = await api('POST', `/api/admin/users/${u.id}/reset-password`, {});
          if (j.ok) { m.body.querySelector('[data-out]').innerHTML = html`<div class="secret"><span>${j.password}</span><button class="btn sm" data-cp>${ic('copy', 14)}${t('Copy')}</button></div><p class="hint">${t('Share it privately. Ask them to change it in Settings.')}</p>`.s; m.body.querySelector('[data-cp]').addEventListener('click', () => copy(j.password)); }
        }
        if (x === 'delete' && await confirmBox(t('Delete {n} and all their numbers, contacts and campaigns? This cannot be undone.', { n: u.email }), { okText: t('Delete user'), danger: true })) {
          if ((await api('DELETE', `/api/admin/users/${u.id}`)).ok) { toast(t('User deleted')); m.close(); load(); }
        }
      });
    }
  });
  await load();
}

// ------------------------------------------------------------------ plans
async function plans(ctx, body, actions, sub) {
  sub.textContent = t('What each plan allows. Public plans appear on the website and the Licence page.');
  actions.innerHTML = html`<button class="btn primary" data-new>${ic('plus', 16)}${t('New plan')}</button>`.s;
  async function load() {
    const list = await getPlans(true);
    if (!ctx.isCurrent()) return;
    body.innerHTML = list.length ? html`<div class="plan-cards">${list.map((p) => html`<article class="panel plan-card ${p.highlight ? 'hl' : ''}">
      <div class="row between"><h3>${p.name}</h3><span class="row" style="gap:4px">${p.public ? pill(t('Public'), 'ok', false) : pill(t('Hidden'), 'muted', false)}${p.trial ? pill(t('Trial'), 'info', false) : ''}</span></div>
      <div class="price">${p.currency === 'INR' ? '₹' : (p.currency || '') + ' '}${fmtNum(p.price)}<small> / ${t('{n} days', { n: p.period_days })}</small></div>
      ${planLimits(p)}
      ${Object.values(p.limits || {}).some((v) => v > 0) ? html`<p class="cell-sub">${ENTITLEMENTS.filter(([k]) => p.limits?.[k] > 0).map(([k, l]) => t(l) + ': ' + fmtNum(p.limits[k])).join(' · ')}</p>` : ''}
      <p class="cell-sub" style="margin:0">${featList().filter(([k]) => p.features?.[k]).map(([, l]) => t(l)).join(' · ')}</p>
      <div class="row"><button class="btn sm" data-edit="${p.id}">${ic('edit', 14)}${t('Edit')}</button><button class="btn-icon danger" data-del="${p.id}" aria-label="${t('Delete')} ${p.name}">${ic('trash', 16)}</button></div></article>`)}</div>`.s
      : empty('template', t('No plans yet'), t('Create at least one plan before selling licences.')).s;
    body._list = list;
  }
  function editor(p) {
    const isNew = !p;
    p = p || { name: '', price: 0, currency: 'INR', period_days: 30, max_numbers: 1, daily_limit: 250, monthly_messages: 5000, max_contacts: 5000, storage_mb: 200, features: Object.fromEntries(featList().map(([k]) => [k, true])), public: true, trial: false, highlight: false, sort: 1, description: '' };
    const num = (k, l, extra = '') => html`<label class="field"><span>${t(l)}</span><input name="${k}" type="number" min="0" value="${p[k]}" ${raw(extra)}></label>`;
    const m = modal({ title: isNew ? t('New plan') : t('Edit plan'), size: 'lg', body: html`<form class="stack">
      <div class="form-grid g3"><label class="field"><span>${t('Name')}</span><input name="name" required value="${p.name}"></label>${num('price', 'Price')}<label class="field"><span>${t('Currency')}</span><input name="currency" value="${p.currency}"></label>
        ${num('period_days', 'Days')}${num('max_numbers', 'Max numbers')}${num('daily_limit', 'Daily limit per number')}${num('monthly_messages', 'Messages per month')}${num('max_contacts', 'Max contacts')}${num('storage_mb', 'Storage (MB)')}${num('sort', 'Sort order')}
        <label class="field full"><span>${t('Description')}</span><input name="description" value="${p.description || ''}"></label></div>
      <p class="hint" style="margin-top:-6px">${t('0 means unlimited (daily limit 0 uses the server default).')}</p>
      <fieldset><legend>${t('Entitlements')} <small class="muted">· ${t('0 = unlimited')}</small></legend><div class="form-grid g3">
        ${ENTITLEMENTS.map(([k, l]) => html`<label class="field"><span>${t(l)}</span><input name="lim_${k}" type="number" min="0" value="${p.limits?.[k] || 0}"></label>`)}</div></fieldset>
      <fieldset><legend>${t('Features')}</legend><div class="events-grid" style="font-family:inherit">${featList().map(([k, l]) => html`<label class="check"><input type="checkbox" name="f_${k}" ${p.features?.[k] ? 'checked' : ''}>${t(l)}</label>`)}</div></fieldset>
      <div class="row" style="gap:24px">${toggle('public', p.public, t('Show publicly'))}${toggle('trial', p.trial, t('Trial plan'))}${toggle('highlight', p.highlight, t('Highlight'))}</div></form>`,
    footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Save plan')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form'); if (!f.reportValidity()) return;
      const d = formData(f); const features = {}; const limits = {};
      featList().forEach(([k]) => { features[k] = !!d['f_' + k]; delete d['f_' + k]; });
      ENTITLEMENTS.forEach(([k]) => { limits[k] = Math.max(0, Number(d['lim_' + k]) || 0); delete d['lim_' + k]; });
      d.features = features; d.limits = limits;
      const j = isNew ? await api('POST', '/api/admin/plans', d) : await api('PATCH', `/api/admin/plans/${p.id}`, d);
      if (j.ok) { toast(t('Plan saved')); m.close(); load(); }
    });
  }
  actions.querySelector('[data-new]').addEventListener('click', () => editor(null));
  body.addEventListener('click', async (e) => {
    const ed = e.target.closest('[data-edit]'); if (ed) return editor(body._list.find((p) => String(p.id) === ed.dataset.edit));
    const d = e.target.closest('[data-del]');
    if (d && await confirmBox(t('Delete this plan? Existing licences keep working until they expire.'), { okText: t('Delete'), danger: true })) { if ((await api('DELETE', `/api/admin/plans/${d.dataset.del}`)).ok) { toast(t('Plan deleted')); load(); } }
  });
  await load();
}

// ------------------------------------------------------------------ licences
async function licences(ctx, body, actions, sub) {
  sub.textContent = t('Keys you sell or give away. SaaS keys activate on this server; self-hosted keys activate a buyer’s own install.');
  actions.innerHTML = html`<button class="btn primary" data-gen>${ic('plus', 16)}${t('Generate keys')}</button>`.s;
  const plans = await getPlans();
  let fStatus = '', fKind = '', list = [];
  body.innerHTML = html`<div class="row mb"><div class="seg" role="tablist" aria-label="${t('Status')}">${['', 'unused', 'active', 'expired', 'suspended'].map((s) => html`<button role="tab" data-fs="${s}" aria-selected="${s === fStatus}">${s ? t(s[0].toUpperCase() + s.slice(1)) : t('All')}</button>`)}</div>
    <select data-fk style="width:auto" aria-label="${t('Kind')}"><option value="">${t('All kinds')}</option><option value="saas">SaaS</option><option value="selfhosted">${t('Self-hosted')}</option></select></div><section class="panel" data-tbl>${skel(6)}</section>`.s;
  async function load() {
    const j = await api('GET', `/api/admin/licences?status=${fStatus}&kind=${fKind}`, undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    list = j.ok ? j.licences || [] : [];
    body.querySelector('[data-tbl]').innerHTML = list.length ? html`<div class="table-wrap"><table class="t"><thead><tr><th>${t('Key')}</th><th>${t('Plan')}</th><th>${t('Kind')}</th><th>${t('Status')}</th><th>${t('User')}</th><th>${t('Expires')}</th><th class="act"></th></tr></thead><tbody>
      ${list.map((l) => html`<tr><td><span class="mono" style="white-space:nowrap">${l.key}</span> <button class="btn-icon" style="display:inline-grid;vertical-align:middle;width:28px;height:28px" data-cp="${l.key}" aria-label="${t('Copy key')}">${ic('copy', 14)}</button>${l.notes ? html`<div class="cell-sub">${l.notes}</div>` : ''}</td>
        <td>${l.plan_name}<div class="cell-sub">${t('{n} days', { n: l.days })}</div></td>
        <td>${l.kind === 'selfhosted' ? html`${t('Self-hosted')}<div class="cell-sub">${t('{a} of {b} installs', { a: l.activations || 0, b: l.max_instances || 1 })}</div>` : 'SaaS'}</td>
        <td>${pill(t(l.status), statusTone(l.status))}</td><td class="cell-sub">${l.user_email || '—'}</td>
        <td class="cell-sub nowrap">${l.expires_at ? fmtDate(l.expires_at, false) : '—'}</td>
        <td class="act"><span class="btn-row" style="flex-wrap:nowrap"><button class="btn sm" data-ext="${l.id}">${t('Extend')}</button>
          <button class="btn sm ghost" data-susp="${l.id}">${l.status === 'suspended' ? t('Activate') : t('Suspend')}</button><button class="btn-icon danger" data-ldel="${l.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button></span></td></tr>`)}</tbody></table></div>`.s
      : empty('key', t('No licence keys'), t('Generate keys for a plan, then send them to buyers.')).s;
  }
  body.addEventListener('click', async (e) => {
    const fs = e.target.closest('[data-fs]'); if (fs) { fStatus = fs.dataset.fs; $$('[data-fs]', body).forEach((x) => x.setAttribute('aria-selected', String(x === fs))); return load(); }
    const cp = e.target.closest('[data-cp]'); if (cp) return copy(cp.dataset.cp, t('Key copied'));
    const ex = e.target.closest('[data-ext]');
    if (ex) { const d = await promptBox(t('Extend licence'), { label: t('Add days'), type: 'number', value: '30' }); if (d && (await api('PATCH', `/api/admin/licences/${ex.dataset.ext}`, { extend_days: +d })).ok) { toast(t('Extended')); load(); } }
    const su = e.target.closest('[data-susp]');
    if (su) { const l = list.find((x) => String(x.id) === su.dataset.susp); if ((await api('PATCH', `/api/admin/licences/${l.id}`, { status: l.status === 'suspended' ? 'active' : 'suspended' })).ok) { toast(t('Saved')); load(); } }
    const dl = e.target.closest('[data-ldel]');
    if (dl && await confirmBox(t('Delete this licence key? Anyone using it loses access.'), { okText: t('Delete'), danger: true })) { if ((await api('DELETE', `/api/admin/licences/${dl.dataset.ldel}`)).ok) { toast(t('Deleted')); load(); } }
  });
  body.querySelector('[data-fk]').addEventListener('change', (e) => { fKind = e.target.value; load(); });
  actions.querySelector('[data-gen]').addEventListener('click', () => {
    const m = modal({ title: t('Generate licence keys'), size: 'lg', body: html`<form class="stack"><div class="form-grid">
      <label class="field"><span>${t('Plan')}</span><select name="plan_id" required>${planOptions(plans)}</select></label>
      <label class="field"><span>${t('Kind')}</span><select name="kind"><option value="saas">${t('SaaS (this server)')}</option><option value="selfhosted">${t('Self-hosted (buyer’s server)')}</option></select></label>
      <label class="field"><span>${t('Days')}</span><input name="days" type="number" min="1" value="30" required></label>
      <label class="field"><span>${t('How many keys')}</span><input name="count" type="number" min="1" max="500" value="1" required></label>
      <label class="field"><span>${t('Max installs (self-hosted)')}</span><input name="max_instances" type="number" min="1" value="1"></label>
      <label class="field"><span>${t('Assign to user email')} <small>(${t('optional')})</small></span><input name="user_email" type="email"></label>
      <label class="field full"><span>${t('Notes')}</span><input name="notes" placeholder="${t('e.g. Paid via UPI, order 1042')}"></label></div><div data-out></div></form>`,
    footer: html`<button class="btn ghost" data-close>${t('Close')}</button><button class="btn primary" data-ok>${t('Generate')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form'); if (!f.reportValidity()) return;
      const d = formData(f); d.plan_id = +d.plan_id; if (!d.user_email) delete d.user_email;
      const j = await api('POST', '/api/admin/licences', d); if (!j.ok) return;
      const keys = (j.licences || []).map((l) => l.key);
      m.body.querySelector('[data-out]').innerHTML = html`<div class="note ok">${ic('check', 16)}<span>${t('{n} keys created', { n: keys.length })}</span></div><div class="codeblock mt-s"><button type="button" class="btn-icon" data-cpall aria-label="${t('Copy all')}">${ic('copy', 16)}</button><code>${keys.join('\n')}</code></div>`.s;
      m.body.querySelector('[data-cpall]').addEventListener('click', () => copy(keys.join('\n'), t('Keys copied')));
      load();
    });
  });
  await load();
}

// ------------------------------------------------------------------ activations
async function activations(ctx, body, _a, sub) {
  sub.textContent = t('Self-hosted installs that checked in with this licence server.');
  const j = await api('GET', '/api/admin/activations');
  if (!ctx.isCurrent()) return;
  const l = j.ok ? j.activations || [] : [];
  body.innerHTML = l.length ? html`<section class="panel"><div class="table-wrap"><table class="t"><thead><tr><th>${t('Domain')}</th><th>${t('Key')}</th><th>${t('Version')}</th><th>IP</th><th>${t('First seen')}</th><th>${t('Last seen')}</th></tr></thead><tbody>
    ${l.map((a) => html`<tr><td class="cell-main">${a.domain || '—'}<div class="cell-sub mono">${a.instance_id}</div></td><td class="mono">${a.key}</td><td>${a.version}</td><td class="mono">${a.ip}</td><td class="cell-sub">${fmtDate(a.first_seen, false)}</td><td class="cell-sub">${relTime(a.last_seen)}</td></tr>`)}</tbody></table></div></section>`.s
    : html`<div class="panel">${empty('server', t('No activations yet'), t('When a buyer starts a self-hosted install with their key, it shows up here.'))}</div>`.s;
}

// ------------------------------------------------------------------ site settings
async function settings(ctx, body, _a, sub) {
  sub.textContent = t('Business details shown on the website, sign-up rules and the announcement bar.');
  const [j, plans] = await Promise.all([api('GET', '/api/admin/settings'), getPlans()]);
  if (!ctx.isCurrent() || !j.ok) return;
  const s = j.settings || j;
  const txt = (k, l, type = 'text') => html`<label class="field"><span>${t(l)}</span><input name="${k}" type="${type}" value="${s[k] ?? ''}"></label>`;
  body.innerHTML = html`<form class="stack" data-f>
    <section class="panel"><div class="panel-head"><h2>${t('Business')}</h2></div><div class="panel-body form-grid">
      ${txt('site_name', 'Site name')}${txt('tagline', 'Tagline')}${txt('company_name', 'Company name')}${txt('gstin', 'GSTIN')}
      ${txt('support_email', 'Support email', 'email')}${txt('support_phone', 'Support phone', 'tel')}${txt('whatsapp_number', 'WhatsApp number', 'tel')}${txt('currency', 'Currency')}
      <label class="field full"><span>${t('Address')}</span><textarea name="address" rows="2">${s.address || ''}</textarea></label></div></section>
    <section class="panel"><div class="panel-head"><h2>${t('Sign-up and trial')}</h2></div><div class="panel-body form-grid">
      <div class="full">${toggle('signup_open', truthy(s.signup_open), t('Allow new sign-ups'), t('When off, only you can create accounts.'))}</div>
      <label class="field"><span>${t('Trial days')}</span><input name="trial_days" type="number" min="0" value="${s.trial_days ?? 0}"></label>
      <label class="field"><span>${t('Trial plan')}</span><select name="trial_plan_id"><option value="0">${t('No trial')}</option>${planOptions(plans, Number(s.trial_plan_id))}</select></label></div></section>
    <section class="panel"><div class="panel-head"><h2>${t('Messages to users')}</h2></div><div class="panel-body stack">
      <label class="field"><span>${t('Announcement bar')}</span><input name="announcement" value="${s.announcement || ''}" placeholder="${t('e.g. Maintenance on Sunday 2–3 AM')}"><small>${t('Shown at the top of every dashboard page. Leave empty to hide.')}</small></label>
      <label class="field"><span>${t('Payment note')}</span><textarea name="payment_note" rows="3">${s.payment_note || ''}</textarea><small>${t('How to pay (UPI, bank transfer). Shown on the pricing page.')}</small></label></div></section>
    <div class="row" style="justify-content:flex-end"><button class="btn primary lg">${t('Save site settings')}</button></div></form>
    <section class="panel mt" id="sending"><div class="panel-head"><h2>${ic('shield', 18)} ${t('Sending safety — all accounts')}</h2></div><div class="panel-body" data-pacing></div></section>`.s;
  import('/static/app/pacing.js').then(({ pacingPanel }) => pacingPanel(body.querySelector('[data-pacing]'), { admin: true }));
  body.querySelector('[data-f]').addEventListener('submit', async (e) => {
    e.preventDefault(); const d = formData(e.target); d.trial_plan_id = +d.trial_plan_id || 0; d.trial_days = +d.trial_days || 0;
    if ((await api('PATCH', '/api/admin/settings', d)).ok) toast(t('Site settings saved'));
  });
}

// ------------------------------------------------------------------ inbox
async function inbox(ctx, body, _a, sub) {
  sub.textContent = t('Messages from the contact form on your website.');
  async function load() {
    const j = await api('GET', '/api/admin/inbox', undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    const l = j.ok ? j.messages || j.inbox || [] : [];
    body.innerHTML = l.length ? html`<section class="panel"><ul class="list">${l.map((m) => html`<li style="align-items:flex-start">
      <span class="list-ico" style="${m.read ? '' : 'background:var(--signal);color:var(--signal-ink)'}">${ic('inbox')}</span>
      <div class="li-main"><b>${m.name} ${m.read ? '' : pill(t('New'), 'ok')}</b><small>${[m.email, m.phone].filter(Boolean).join(' · ')} · ${relTime(m.created_at || m.at)}</small>
        <p style="margin:8px 0 0;white-space:pre-wrap;color:var(--ink-2)">${m.message}</p></div>
      <span class="btn-row">${m.read ? '' : html`<button class="btn sm" data-read="${m.id}">${t('Mark read')}</button>`}${m.email ? html`<a class="btn sm ghost" href="mailto:${m.email}">${t('Reply')}</a>` : ''}<button class="btn-icon danger" data-del="${m.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button></span></li>`)}</ul></section>`.s
      : html`<div class="panel">${empty('inbox', t('Inbox is empty'), t('Contact-form messages from your website land here.'))}</div>`.s;
  }
  body.addEventListener('click', async (e) => {
    const r = e.target.closest('[data-read]'); if (r && (await api('POST', `/api/admin/inbox/${r.dataset.read}/read`, {})).ok) load();
    const d = e.target.closest('[data-del]'); if (d && await confirmBox(t('Delete this message?'), { okText: t('Delete'), danger: true }) && (await api('DELETE', `/api/admin/inbox/${d.dataset.del}`)).ok) { toast(t('Deleted')); load(); }
  });
  await load();
}

// ------------------------------------------------------------------ licence server
async function server(ctx, body, _a, sub) {
  sub.textContent = t('Details self-hosted buyers need to activate their install against this server.');
  const j = await api('GET', '/api/admin/licence-server');
  if (!ctx.isCurrent() || !j.ok) return;
  const url = j.server_url || location.origin;
  body.innerHTML = html`<div class="grid g2" style="align-items:start">
    <section class="panel"><div class="panel-head"><h2>${t('Server details')}</h2></div><div class="panel-body stack">
      <div class="field"><span class="field-label">${t('Licence server URL')}</span><div class="secret"><span>${url}</span><button class="btn sm" data-cp="${url}">${ic('copy', 14)}${t('Copy')}</button></div></div>
      <div class="field"><span class="field-label">${t('Public key (Ed25519)')}</span><div class="secret"><span>${j.public_key}</span><button class="btn sm" data-cp="${j.public_key}">${ic('copy', 14)}${t('Copy')}</button></div></div>
      <p class="hint">${t('The public key is safe to share. Never share the private key from your server .env.')}</p></div></section>
    <section class="panel"><div class="panel-head"><h2>${t('Send this to buyers')}</h2></div><div class="panel-body stack">
      <ol class="steps-ol"><li>${t('Install Vivid Relay on their server.')}</li><li>${t('Add these three lines to the .env file:')}</li></ol>
      <div class="codeblock"><button class="btn-icon" data-cpenv aria-label="${t('Copy')}">${ic('copy', 16)}</button><code>LICENCE_SERVER=${url}
LICENCE_KEY=VR-XXXX-XXXX-XXXX-XXXX
LICENCE_PUBLIC_KEY=${j.public_key}</code></div>
      <ol class="steps-ol" start="3"><li>${t('Restart: docker compose up -d. The install activates and appears under Activations.')}</li></ol></div></section></div>`.s;
  body.addEventListener('click', (e) => {
    const c = e.target.closest('[data-cp]'); if (c) copy(c.dataset.cp);
    if (e.target.closest('[data-cpenv]')) copy(body.querySelector('.codeblock code').textContent);
  });
}

// ------------------------------------------------------------------ audit
async function audit(ctx, body, actions, sub) {
  sub.textContent = t('Sign-ins and important changes: keys, licences, plans and deletions.');
  actions.innerHTML = html`<div class="search">${ic('search', 16)}<input type="search" data-q placeholder="${t('Filter')}" aria-label="${t('Filter audit log')}"></div>`.s;
  const j = await api('GET', '/api/admin/audit?limit=200');
  if (!ctx.isCurrent()) return;
  const all = j.ok ? j.entries || [] : [];
  const draw = (q = '') => {
    const l = all.filter((e) => !q || [e.user_email, e.action, e.detail, e.ip].join(' ').toLowerCase().includes(q));
    body.innerHTML = l.length ? html`<section class="panel"><div class="table-wrap"><table class="t"><thead><tr><th>${t('When')}</th><th>${t('Who')}</th><th>${t('Action')}</th><th>${t('Detail')}</th><th>IP</th></tr></thead><tbody>
      ${l.map((e) => html`<tr><td class="cell-sub nowrap" title="${fmtDate(e.at)}">${relTime(e.at)}</td><td>${e.user_email || (e.user_id ? '#' + e.user_id : t('system'))}</td>
        <td><span class="tag mono">${e.action}</span></td><td class="cell-sub" style="max-width:420px;overflow-wrap:anywhere">${e.detail}</td><td class="mono cell-sub">${e.ip}</td></tr>`)}</tbody></table></div></section>`.s
      : html`<div class="panel">${empty('log', t('No entries'))}</div>`.s;
  };
  actions.querySelector('[data-q]').addEventListener('input', debounce((e) => draw(e.target.value.toLowerCase()), 200));
  draw();
}

// ------------------------------------------------------------------ proxy pool
async function proxies(ctx, body, actions, sub) {
  sub.textContent = t('Proxies you bought, and who may use them. Only the gateway’s WhatsApp connection for a number goes through a proxy — the phone is not affected.');
  actions.innerHTML = html`<button class="btn primary" data-a="new">${ic('plus', 16)}${t('Add proxy')}</button>`.s;
  let data = null, users = [];
  async function load() {
    const [j, u] = await Promise.all([api('GET', '/api/admin/proxies'), api('GET', '/api/admin/users?limit=500', undefined, { silent: true })]);
    if (!ctx.isCurrent() || !j.ok) return;
    data = j; users = u.users || [];
    const by = {}; (j.usage || []).forEach((x) => { (by[x.proxy_id] = by[x.proxy_id] || []).push(x); });
    body.innerHTML = html`<section class="panel"><div class="panel-body">${toggle('own_allowed', !!j.own_allowed, t('Users may also enter their own proxy URL on a number'), t('Off = numbers can only use proxies from this pool.'))}</div></section>
      <section class="panel mt">${j.proxies.length ? html`<ul class="list">${j.proxies.map((p) => html`<li><span class="list-ico">${ic('globe')}</span>
          <span class="li-main"><b>${p.label}</b><small><code>${p.masked}</code>${p.country ? ' · ' + p.country : ''} · ${p.kind === 'shared' ? t('shared with everyone') : t('dedicated to {e}', { e: p.assigned_email || '#' + p.assigned_to })} · ${t('{n} numbers', { n: p.in_use })}${p.last_test ? ' · ' + t('tested {t}', { t: relTime(p.last_test) }) + ': ' + p.last_result : ''}${(by[p.id] || []).length ? html`<br>${(by[p.id] || []).map((x) => (x.label || x.session) + ' (' + x.email + ')').join(', ')}` : ''}</small></span>
          ${pill(p.active ? t('on') : t('off'), p.active ? 'ok' : 'muted')}<button class="btn sm" data-test="${p.id}">${t('Test')}</button><button class="btn sm" data-edit="${p.id}">${ic('edit', 14)}${t('Edit')}</button><button class="btn-icon danger" data-del="${p.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button></li>`)}</ul>`
        : html`<div class="panel-body">${empty('globe', t('No proxies in the pool'), t('Add proxies from your provider (socks5://user:pass@host:port). Mark each as shared, or dedicate it to one user; users then pick a proxy for a number without seeing the password.'))}</div>`}</section>`.s;
  }
  function editModal(p) {
    const isNew = !p;
    p = p || { label: '', kind: 'shared', assigned_to: 0, country: '', note: '', active: true };
    const m = modal({ title: isNew ? t('Add proxy') : p.label, body: html`<form class="stack">
        <div class="form-grid g2"><label class="field"><span>${t('Label')}</span><input name="label" value="${p.label}" placeholder="${t('Mumbai mobile 1')}"></label><label class="field"><span>${t('Country')}</span><input name="country" value="${p.country}" placeholder="IN"></label></div>
        <label class="field"><span>${t('Proxy URL')} ${isNew ? '' : html`<small>(${t('leave blank to keep the current one')})</small>`}</span><input name="url" placeholder="socks5://user:pass@host:1080" autocomplete="off"></label>
        <div class="form-grid g2"><label class="field"><span>${t('Who may use it')}</span><select name="kind"><option value="shared" ${p.kind === 'shared' ? 'selected' : ''}>${t('Shared — every user')}</option><option value="dedicated" ${p.kind === 'dedicated' ? 'selected' : ''}>${t('Dedicated — one user')}</option></select></label>
          <label class="field"><span>${t('Dedicated to')}</span><select name="assigned_to"><option value="0">—</option>${users.map((u) => html`<option value="${u.id}" ${u.id === p.assigned_to ? 'selected' : ''}>${u.name || u.email} (${u.email})</option>`)}</select></label></div>
        <label class="field"><span>${t('Note')}</span><input name="note" value="${p.note || ''}" placeholder="${t('provider, expiry, price')}"></label>
        ${toggle('active', p.active, t('Active'), '')}</form>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Save')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form');
      const d = { label: f.label.value.trim(), country: f.country.value.trim(), kind: f.kind.value, assigned_to: +f.assigned_to.value || 0, note: f.note.value, active: f.active.checked };
      if (f.url.value.trim()) d.url = f.url.value.trim();
      const j = await api(isNew ? 'POST' : 'PATCH', isNew ? '/api/admin/proxies' : '/api/admin/proxies/' + p.id, d);
      if (j.ok) { m.close(); toast(t('Saved')); load(); }
    });
  }
  ctx.root.addEventListener('click', async (e) => {
    if (e.target.closest('[data-a="new"]')) return editModal();
    const ed = e.target.closest('[data-edit]'); if (ed) return editModal(data.proxies.find((p) => p.id === +ed.dataset.edit));
    const ts = e.target.closest('[data-test]');
    if (ts) { ts.classList.add('busy'); const j = await api('POST', `/api/admin/proxies/${ts.dataset.test}/test`); ts.classList.remove('busy'); if (j.ok) toast(t('Works: {ip} {c}', { ip: j.result.ip, c: j.result.country || '' })); load(); return; }
    const del = e.target.closest('[data-del]');
    if (del && await confirmBox(t('Delete this proxy? Numbers using it go back to the server IP.'), { danger: true, okText: t('Delete') })) { if ((await api('DELETE', '/api/admin/proxies/' + del.dataset.del)).ok) load(); }
  });
  body.addEventListener('change', async (e) => { if (e.target.name === 'own_allowed') { if ((await api('POST', '/api/admin/proxies/settings', { own_allowed: e.target.checked })).ok) toast(t('Saved')); } });
  load();
}
