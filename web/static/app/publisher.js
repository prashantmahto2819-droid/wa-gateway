// Link Publisher: approval inbox for posts from websites and pasted links,
// where they go (numbers, lists, groups, channels, Status), and the
// registry of websites allowed to send signed webhooks.
import { api, html, raw, esc, ic, t, toast, modal, confirmBox, copy, skel, empty, pill, fmtDate, fmtNum, relTime, getSessions, sessionOptions, debounce, parsePhones } from '/static/app/core.js';
import { listsPicker } from '/static/app/lists.js';
import { chatsPicker } from '/static/app/pickers.js';

const VIEWS = [['inbox', 'Inbox', 'inbox'], ['history', 'History', 'clock'], ['websites', 'Websites', 'link']];

export async function render(ctx) {
  const { root, me } = ctx;
  ctx.setTitle(t('Link Publisher'));
  const canBulk = me.can?.bulk !== false, canInt = me.can?.integration !== false;
  let view = new URL(location.href).searchParams.get('view') || 'inbox';
  let sess = await getSessions();
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Link Publisher')}</h1><p>${t('Share links from your website or a pasted list on WhatsApp — numbers, lists, groups, channels or Status — after a quick approval.')}</p></div>
      <div class="page-actions">${canBulk ? html`<button class="btn primary" data-a="bulk">${ic('plus', 16)}${t('Add links')}</button>` : ''}</div></div>
    <div class="row mb"><div class="seg" role="tablist" data-tabs>${VIEWS.map(([k, l, i]) => html`<button role="tab" data-v="${k}" aria-selected="${k === view}">${ic(i, 15)}${t(l)}<span class="badge" data-badge="${k}" hidden></span></button>`)}</div></div>
    <section data-view>${skel(4)}</section></div>`.s;
  const box = root.querySelector('[data-view]');
  root.querySelector('[data-tabs]').addEventListener('click', (e) => {
    const b = e.target.closest('[data-v]'); if (!b) return;
    view = b.dataset.v;
    root.querySelectorAll('[data-v]').forEach((x) => x.setAttribute('aria-selected', x === b));
    history.replaceState(null, '', '/app/publisher?view=' + view);
    show();
  });
  root.querySelector('[data-a="bulk"]')?.addEventListener('click', () => bulkModal());
  function show() { box.innerHTML = skel(4).s; ({ inbox, history: historyView, websites })[view](); }

  const targetText = (tg) => (tg || []).map((x) => x.to === 'status' ? t('Status') : x.name || x.to).join(', ') || t('nowhere yet');
  function postCard(p) {
    const stats = p.stats ? Object.entries(p.stats).map(([k, v]) => `${v} ${t(k)}`).join(' · ') : '';
    return html`<article class="panel" data-post="${p.id}"><div class="panel-body" style="display:flex;gap:14px;align-items:flex-start">
      ${p.image ? html`<img src="${p.image}" alt="" style="width:88px;height:88px;object-fit:cover;border-radius:10px;flex:none" loading="lazy">` : html`<span class="list-ico" style="flex:none">${ic('link')}</span>`}
      <div style="flex:1;min-width:0"><b class="ellipsis" style="display:block">${p.title || p.url}</b>
        ${p.summary ? html`<p class="muted" style="margin:4px 0">${p.summary.slice(0, 220)}</p>` : ''}
        <small class="muted"><a href="${p.url}" target="_blank" rel="noopener">${p.url.slice(0, 80)}</a> · ${p.website || t('pasted')}${p.category ? ' · ' + p.category : ''} · ${relTime(p.created_at)}</small>
        <div class="mt-s"><small>${ic('send', 13)} ${t('To')}: ${targetText(p.targets)}${p.session ? ' · ' + t('from') + ' ' + p.session : ''}</small></div>
        ${p.note ? html`<div class="mt-s"><small class="muted">${p.note}</small></div>` : ''}${stats ? html`<div class="mt-s"><small>${stats}</small></div>` : ''}</div>
      <div class="stack" style="flex:none;gap:6px">${pill(t(p.status), p.status === 'pending' ? 'warn' : p.status === 'queued' ? 'ok' : 'muted')}
        ${p.status === 'pending' && canBulk ? html`<button class="btn sm primary" data-approve="${p.id}">${ic('check', 14)}${t('Approve')}</button><button class="btn sm" data-edit="${p.id}">${ic('edit', 14)}${t('Edit')}</button><button class="btn sm ghost danger" data-reject="${p.id}">${t('Reject')}</button>` : ''}
        ${p.status !== 'pending' && canBulk ? html`<button class="btn-icon danger" data-del="${p.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button>` : ''}</div></div></article>`;
  }
  async function inbox() {
    const j = await api('GET', '/api/posts?status=pending');
    if (!ctx.isCurrent() || !j.ok) return;
    const posts = j.posts || [];
    const badge = root.querySelector('[data-badge="inbox"]'); badge.hidden = !posts.length; badge.textContent = posts.length;
    box._posts = posts;
    box.innerHTML = posts.length ? html`<div class="row mb between"><span class="muted">${t('{n} waiting for approval', { n: posts.length })}</span>${canBulk ? html`<button class="btn" data-a="approve-all">${ic('check', 14)}${t('Approve all that have recipients')}</button>` : ''}</div><div class="stack">${posts.map(postCard)}</div>`.s
      : empty('inbox', t('Inbox is empty'), t('Posts from your websites and links you paste wait here until you approve them. Add a website under Websites, or paste links with “Add links”.')).s;
  }
  async function historyView() {
    const j = await api('GET', '/api/posts?status=all');
    if (!ctx.isCurrent() || !j.ok) return;
    const posts = (j.posts || []).filter((p) => p.status !== 'pending');
    box._posts = posts;
    box.innerHTML = posts.length ? html`<div class="stack">${posts.map(postCard)}</div>`.s : empty('clock', t('Nothing sent yet'), '').s;
  }

  // ---------- targets picker (shared by bulk, edit/approve, routes) ----------
  function targetsPicker(host, initial = []) {
    const sel = { lists: new Set(), chats: new Set(), phones: '', status: false, names: {} };
    for (const x of initial) {
      if (x.to === 'status') sel.status = true;
      else if (x.to.startsWith('list:')) sel.lists.add(+x.to.slice(5));
      else if (x.to.includes('@')) { sel.chats.add(x.to); sel.names[x.to] = x.name || ''; }
      else sel.phones += (sel.phones ? '\n' : '') + x.to;
    }
    const linked = sess.filter((s) => s.jid);
    host.innerHTML = html`<div class="stack">
      ${raw(`<label class="switch"><input type="checkbox" data-status ${sel.status ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${esc(t('Post as WhatsApp Status'))}</span><small>${esc(t('Picture with caption when the link has an image'))}</small></span></label>`)}
      <div><span class="field-label">${t('Saved lists')}</span><div data-lists></div></div>
      <div><div class="row between"><span class="field-label">${t('Groups, communities, channels')}</span>${linked.length > 1 ? html`<select data-chatsess style="max-width:220px">${linked.map((s) => html`<option value="${s.name}">${s.label || s.name}</option>`)}</select>` : ''}</div><div data-chats class="picker">${linked.length ? '' : html`<p class="muted">${t('Link a number to pick groups and channels.')}</p>`}</div></div>
      <label class="field"><span>${t('Phone numbers')} <small>(${t('one per line')})</small></span><textarea data-phones rows="2">${sel.phones}</textarea></label></div>`.s;
    listsPicker(host.querySelector('[data-lists]'), { selected: sel.lists });
    let picker = null;
    const mountChats = (name) => { if (!name) return; picker = chatsPicker(host.querySelector('[data-chats]'), { session: name, selected: sel.chats }); };
    mountChats(linked[0]?.name);
    host.querySelector('[data-chatsess]')?.addEventListener('change', (e) => mountChats(e.target.value));
    return () => {
      const out = [];
      if (host.querySelector('[data-status]').checked) out.push({ to: 'status', name: t('Status') });
      for (const id of sel.lists) out.push({ to: 'list:' + id, name: host.querySelector(`[data-list="${id}"]`)?.closest('label')?.querySelector('b')?.textContent || '' });
      for (const jid of sel.chats) out.push({ to: jid, name: host.querySelector(`[data-sel][value="${jid}"]`)?.closest('label')?.querySelector('b')?.textContent || sel.names[jid] || '' });
      for (const ph of parsePhones(host.querySelector('[data-phones]').value)) out.push({ to: ph });
      return out;
    };
  }

  function bulkModal() {
    const m = modal({ title: t('Add links'), size: 'lg', body: html`<form class="stack">
        <label class="field"><span>${t('Links')} <small>(${t('one per line, up to 100')})</small></span><textarea name="urls" rows="5" placeholder="https://charameti.com/post/1&#10;https://charameti.com/post/2" required></textarea></label>
        <label class="field"><span>${t('Message')} <small>{{title}} {{summary}} {{url}} {{site}}</small></span><textarea name="text" rows="3">*{{title}}*&#10;&#10;{{summary}}&#10;&#10;{{url}}</textarea></label>
        <div class="form-grid g2"><label class="field"><span>${t('Send from')}</span><select name="session">${raw(sessionOptions(sess, '', { all: true, allLabel: t('Any linked number') }))}</select></label>
          <label class="field"><span>${t('Category')} <small>(${t('optional')})</small></span><input name="category" placeholder="news"></label></div>
        ${raw(`<label class="switch"><input type="checkbox" name="fetch" checked><span class="track"></span><span class="switch-text"><span>${esc(t('Fetch title, summary and picture from each page'))}</span><small>${esc(t('Takes a few seconds per link'))}</small></span></label>`)}
        <div><span class="field-label">${t('Send to')}</span><div data-targets></div></div></form>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn" data-ok="pending">${t('Add to inbox')}</button><button class="btn primary" data-ok="approve">${ic('send', 14)}${t('Add and send now')}</button>` });
    const getTargets = targetsPicker(m.body.querySelector('[data-targets]'));
    m.foot.addEventListener('click', async (e) => {
      const b = e.target.closest('[data-ok]'); if (!b) return;
      const f = m.body.querySelector('form');
      const targets = getTargets();
      if (b.dataset.ok === 'approve' && !targets.length) return toast(t('Choose where to send'), 'warn');
      m.foot.querySelectorAll('button').forEach((x) => (x.disabled = true));
      const j = await api('POST', '/api/posts', { urls: [f.urls.value], text: f.text.value, session: f.session.value, category: f.category.value.trim(), fetch: f.fetch.checked, targets, approve: b.dataset.ok === 'approve' });
      m.foot.querySelectorAll('button').forEach((x) => (x.disabled = false));
      if (!j.ok) return;
      m.close(); toast(t('{n} links added', { n: j.created })); view = 'inbox'; root.querySelector('[data-v="inbox"]').click();
    });
  }
  function editModal(p, approveAfter) {
    const m = modal({ title: approveAfter ? t('Approve and send') : t('Edit post'), size: 'lg', body: html`<form class="stack">
        <div class="form-grid g2"><label class="field"><span>${t('Title')}</span><input name="title" value="${p.title}"></label><label class="field"><span>${t('Picture URL')}</span><input name="image" value="${p.image}"></label></div>
        <label class="field"><span>${t('Summary')}</span><textarea name="summary" rows="2">${p.summary}</textarea></label>
        <label class="field"><span>${t('Message')} <small>{{title}} {{summary}} {{url}} {{site}}</small></span><textarea name="text" rows="3">${p.text}</textarea></label>
        <div class="form-grid g2"><label class="field"><span>${t('Send from')}</span><select name="session">${raw(sessionOptions(sess, p.session, { all: true, allLabel: t('Any linked number') }))}</select></label>
          <div class="field"><span>${t('Link')}</span><div class="row"><a href="${p.url}" target="_blank" rel="noopener" class="ellipsis">${p.url}</a><button type="button" class="btn sm" data-refetch>${ic('refresh', 14)}${t('Re-fetch')}</button></div></div></div>
        <div><span class="field-label">${t('Send to')}</span><div data-targets></div></div></form>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn" data-save>${t('Save')}</button>${approveAfter || p.status === 'pending' ? html`<button class="btn primary" data-go>${ic('send', 14)}${t('Approve and send')}</button>` : ''}` });
    const getTargets = targetsPicker(m.body.querySelector('[data-targets]'), p.targets);
    const body = (extra) => { const f = m.body.querySelector('form'); return { title: f.title.value, image: f.image.value, summary: f.summary.value, text: f.text.value, session: f.session.value, targets: getTargets(), ...extra }; };
    m.body.querySelector('[data-refetch]').addEventListener('click', async () => {
      const j = await api('PATCH', '/api/posts/' + p.id, body({ refetch: true }));
      if (j.ok) { const f = m.body.querySelector('form'); f.title.value = j.post.title; f.summary.value = j.post.summary; f.image.value = j.post.image; toast(t('Fetched')); }
    });
    m.foot.querySelector('[data-save]').addEventListener('click', async () => { if ((await api('PATCH', '/api/posts/' + p.id, body())).ok) { m.close(); toast(t('Saved')); show(); } });
    m.foot.querySelector('[data-go]')?.addEventListener('click', async () => {
      const b = body();
      if (!b.targets.length) return toast(t('Choose where to send'), 'warn');
      if (!(await api('PATCH', '/api/posts/' + p.id, b)).ok) return;
      const j = await api('POST', `/api/posts/${p.id}/approve`, {});
      if (j.ok) { m.close(); toast(t('Queued: {n}', { n: j.post.note || '' })); show(); }
    });
  }
  box.addEventListener('click', async (e) => {
    const find = (id) => (box._posts || []).find((p) => p.id === +id);
    const ap = e.target.closest('[data-approve]');
    if (ap) { const p = find(ap.dataset.approve); if (!p.targets?.length) return editModal(p, true); const j = await api('POST', `/api/posts/${p.id}/approve`, {}); if (j.ok) { toast(t('Queued: {n}', { n: j.post.note || '' })); show(); } return; }
    const ed = e.target.closest('[data-edit]'); if (ed) return editModal(find(ed.dataset.edit), false);
    const rj = e.target.closest('[data-reject]'); if (rj && (await api('POST', `/api/posts/${rj.dataset.reject}/reject`, {})).ok) { toast(t('Rejected')); return show(); }
    const del = e.target.closest('[data-del]'); if (del && (await api('DELETE', '/api/posts/' + del.dataset.del)).ok) return show();
    if (e.target.closest('[data-a="approve-all"]')) {
      const ready = (box._posts || []).filter((p) => p.targets?.length);
      if (!ready.length) return toast(t('None of them has recipients yet — open each and choose where to send.'), 'warn');
      if (!await confirmBox(t('Approve {n} posts and queue them now?', { n: ready.length }))) return;
      let n = 0; for (const p of ready) { if ((await api('POST', `/api/posts/${p.id}/approve`, {}, { silent: true })).ok) n++; }
      toast(t('{n} queued', { n })); return show();
    }
    const ws = e.target.closest('[data-ws]'); if (ws) return websiteModal((box._sites || []).find((w) => w.id === +ws.dataset.ws));
    if (e.target.closest('[data-a="newws"]')) return websiteModal();
    const dw = e.target.closest('[data-dws]');
    if (dw && await confirmBox(t('Remove this website? Its key stops working immediately.'), { danger: true, okText: t('Remove') })) { if ((await api('DELETE', '/api/websites/' + dw.dataset.dws)).ok) { toast(t('Removed')); show(); } return; }
    const cp = e.target.closest('[data-copy]'); if (cp) return copy(cp.dataset.copy, cp.dataset.label || t('Copied'));
  });

  // ---------- websites ----------
  async function websites() {
    const j = await api('GET', '/api/websites');
    if (!ctx.isCurrent() || !j.ok) return;
    const sites = j.websites || [];
    box._sites = sites; box._inboundURL = j.inbound_url;
    box.innerHTML = html`<div class="page-actions mb">${canInt ? html`<button class="btn primary" data-a="newws">${ic('plus', 16)}${t('Add website')}</button>` : ''}</div>
      ${sites.length ? html`<ul class="list">${sites.map((w) => html`<li><span class="list-ico">${ic('link')}</span><span class="li-main"><b>${w.name}</b><small>${w.url || ''} · ${t('key')} <code>${w.key}</code> · ${t('{n} routes', { n: w.routes.length })} · ${t('{n} received', { n: w.received })}${w.last_seen ? ' · ' + t('last {t}', { t: relTime(w.last_seen) }) : ''}</small></span>
          ${pill(w.active ? t('on') : t('off'), w.active ? 'ok' : 'muted')}${canInt ? html`<button class="btn sm" data-ws="${w.id}">${ic('edit', 14)}${t('Edit')}</button><button class="btn-icon danger" data-dws="${w.id}" aria-label="${t('Remove')}">${ic('trash', 16)}</button>` : ''}</li>`)}</ul>`
      : empty('link', t('No websites yet'), t('Register a website (Charameti, WordPress, a Cloudflare Worker …) to get a key and secret. It sends a signed webhook for every new post; you approve from the Inbox.'))}
      <section class="panel mt"><div class="panel-head"><h2>${t('How a website sends posts')}</h2></div><div class="panel-body">
        <p>${t('Endpoint')}: <code>POST ${j.inbound_url}</code> · ${t('test')}: <code>GET ${j.inbound_url}/ping</code></p>
        <p class="muted">${t('Headers: X-Relay-Key, X-Relay-Timestamp (unix seconds), X-Relay-Signature = sha256 HMAC of “timestamp.body” with the secret. Body: {id, event, title, summary, text, url, image, category, audience, auto}. Same id twice = duplicate (ignored). Full details in Developer → API docs.')}</p></div></section>`.s;
  }
  function websiteModal(w) {
    const isNew = !w;
    w = w || { name: '', url: '', text: '*{{title}}*\n\n{{summary}}\n\n{{url}}', routes: [{ category: '', session: '', targets: [], text: '', auto: false }], active: true };
    const routes = w.routes.map((r) => ({ ...r, targets: [...(r.targets || [])] }));
    const getters = [];
    const m = modal({ title: isNew ? t('Add website') : w.name, size: 'lg', body: '', footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button>${isNew ? '' : html`<button class="btn" data-rotate>${t('New secret')}</button>`}<button class="btn primary" data-ok>${t('Save')}</button>` });
    function draw() {
      getters.length = 0;
      m.body.innerHTML = html`<form class="stack">
        <div class="form-grid g2"><label class="field"><span>${t('Name')}</span><input name="name" value="${w.name}" required placeholder="Charameti"></label><label class="field"><span>${t('Site URL')}</span><input name="url" value="${w.url}" placeholder="https://charameti.com"></label></div>
        <label class="field"><span>${t('Default message')} <small>{{title}} {{summary}} {{url}} {{site}} {{category}}</small></span><textarea name="text" rows="3">${w.text}</textarea></label>
        ${raw(`<label class="switch"><input type="checkbox" name="active" ${w.active ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${esc(t('Accept posts from this website'))}</span></span></label>`)}
        <h3 class="mt">${t('Routes')} <small class="muted">${t('by category: which number sends, and to whom')}</small></h3>
        <div class="stack" data-routes>${routes.map((r, i) => html`<div class="panel panel-pad" data-route="${i}"><div class="form-grid g2">
            <label class="field"><span>${t('Category')} <small>(${t('blank = default for everything else')})</small></span><input data-k="category" value="${r.category}" placeholder="news, sports, jobs…"></label>
            <label class="field"><span>${t('Send from')}</span><select data-k="session">${raw(sessionOptions(sess, r.session, { all: true, allLabel: t('Any linked number') }))}</select></label></div>
          <label class="field"><span>${t('Message for this category')} <small>(${t('blank = default')})</small></span><textarea data-k="text" rows="2">${r.text || ''}</textarea></label>
          ${raw(`<label class="switch"><input type="checkbox" data-k="auto" ${r.auto ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${esc(t('Send without approval'))}</span><small>${esc(t('only when the website also sends auto:true'))}</small></span></label>`)}
          <div class="mt-s"><span class="field-label">${t('Send to')}</span><div data-rt></div></div>
          <div class="btn-row mt-s"><button type="button" class="btn-icon danger" data-rmr="${i}" aria-label="${t('Remove route')}">${ic('trash', 16)}</button></div></div>`)}</div>
        <button type="button" class="btn" data-addr>${ic('plus', 14)}${t('Add route')}</button></form>`.s;
      m.body.querySelectorAll('[data-route]').forEach((el, i) => getters.push(targetsPicker(el.querySelector('[data-rt]'), routes[i].targets)));
    }
    function collect() {
      const f = m.body.querySelector('form');
      m.body.querySelectorAll('[data-route]').forEach((el, i) => {
        routes[i].category = el.querySelector('[data-k="category"]').value.trim();
        routes[i].session = el.querySelector('[data-k="session"]').value;
        routes[i].text = el.querySelector('[data-k="text"]').value;
        routes[i].auto = el.querySelector('[data-k="auto"]').checked;
        routes[i].targets = getters[i]();
      });
      return { name: f.name.value.trim(), url: f.url.value.trim(), text: f.text.value, active: f.active.checked, routes };
    }
    draw();
    m.body.addEventListener('click', (e) => {
      if (e.target.closest('[data-addr]')) { collect(); routes.push({ category: '', session: '', targets: [], text: '', auto: false }); draw(); }
      const rm = e.target.closest('[data-rmr]'); if (rm) { collect(); routes.splice(+rm.dataset.rmr, 1); draw(); }
    });
    async function save(extra) {
      const b = collect();
      if (!b.name) return toast(t('Give the website a name'), 'warn');
      const j = await api(isNew ? 'POST' : 'PATCH', isNew ? '/api/websites' : '/api/websites/' + w.id, { ...b, ...extra });
      if (!j.ok) return;
      if (j.website.secret) {
        m.body.innerHTML = html`<div class="stack"><p class="lead">${t('Put these in the website’s settings (Cloudflare Worker secrets / .env). The secret is shown only now.')}</p>
          ${[['RELAY_URL', box._inboundURL.replace(/\/api\/inbound$/, '')], ['RELAY_KEY', j.website.key], ['RELAY_SECRET', j.website.secret]].map(([k, v]) => html`<div class="field"><span>${k}</span><div class="input-group"><input readonly value="${v}"><button type="button" class="btn" data-copy="${v}" data-label="${k}">${ic('copy', 14)}${t('Copy')}</button></div></div>`)}</div>`.s;
        m.foot.innerHTML = html`<button class="btn primary" data-close>${t('Done')}</button>`.s;
        m.body.addEventListener('click', (e) => { const c = e.target.closest('[data-copy]'); if (c) copy(c.dataset.copy, c.dataset.label); });
        m.overlay?.addEventListener?.('close', show);
        show();
      } else { m.close(); toast(t('Saved')); show(); }
    }
    m.foot.querySelector('[data-ok]').addEventListener('click', () => save({}));
    m.foot.querySelector('[data-rotate]')?.addEventListener('click', async () => { if (await confirmBox(t('Make a new secret? The website must be updated or its posts will be refused.'))) save({ rotate_secret: true }); });
  }
  show();
}
