import { api, html, raw, ic, t, fmtNum, fmtPhone, relTime, pill, skel, empty, modal, confirmBox, promptBox, toast, debounce, upload, downloadBlob, chipInput, $$ } from '/static/app/core.js';

const LIMIT = 50;

export async function render(ctx) {
  const { root } = ctx;
  const st = { q: '', tag: ctx.query.get('tag') || '', opted: '', page: 1, total: 0, list: [], sel: new Set(), tags: [] };
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Contacts')}</h1><p>${t('Everyone you can message. Tags decide who gets which campaign.')}</p></div>
      <div class="page-actions">
        <button class="btn" data-a="import">${ic('upload', 16)}${t('Import CSV')}</button>
        <a class="btn" data-export href="/api/contacts/export.csv" download>${ic('download', 16)}${t('Export CSV')}</a>
        <button class="btn primary" data-a="add">${ic('plus', 16)}${t('Add contact')}</button></div></div>
    <div class="split-r">
      <aside class="panel" aria-label="${t('Tags')}"><div class="panel-head"><h3>${t('Tags')}</h3></div><nav data-tags style="padding:6px">${skel(4)}</nav></aside>
      <div>
        <div class="row mb"><div class="search grow">${ic('search', 16)}<input type="search" data-q placeholder="${t('Search name, phone or email')}" aria-label="${t('Search contacts')}"></div>
          <select data-opted style="width:auto" aria-label="${t('Opt-out filter')}"><option value="">${t('Everyone')}</option><option value="0">${t('Subscribed')}</option><option value="1">${t('Opted out')}</option></select></div>
        <div data-bulk></div>
        <section class="panel" data-table>${raw('<div class="panel-body">' + skel(8).s + '</div>')}</section>
      </div></div></div>`.s;

  async function loadTags() {
    const j = await api('GET', '/api/contacts/tags', undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    st.tags = j.ok ? j.tags || [] : [];
    const nav = root.querySelector('[data-tags]');
    nav.innerHTML = html`<a href="#" class="nav-item tagnav ${!st.tag ? 'on' : ''}" data-tag="">${ic('users', 16)}<span class="grow">${t('All contacts')}</span></a>
      ${st.tags.map((x) => html`<a href="#" class="nav-item tagnav ${st.tag === x.tag ? 'on' : ''}" data-tag="${x.tag}"><span class="tag">${x.tag}</span><span class="grow"></span><span class="badge">${fmtNum(x.count)}</span></a>`)}
      ${st.tags.length ? '' : html`<p class="cell-sub" style="padding:8px 10px">${t('Tags you add show up here.')}</p>`}`.s;
  }
  async function load() {
    const p = new URLSearchParams({ q: st.q, tag: st.tag, opted_out: st.opted, page: st.page, limit: LIMIT });
    const j = await api('GET', '/api/contacts?' + p, undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    st.list = j.ok ? j.contacts || [] : []; st.total = j.total || 0;
    root.querySelector('[data-export]').href = '/api/contacts/export.csv' + (st.tag ? '?tag=' + encodeURIComponent(st.tag) : '');
    drawTable();
  }
  function drawTable() {
    const box = root.querySelector('[data-table]');
    if (!st.list.length) {
      box.innerHTML = (st.q || st.tag || st.opted ? empty('search', t('No contacts match'), t('Try another search or clear the filters.'))
        : empty('users', t('No contacts yet'), t('Import a CSV from your phone or CRM, or add people one by one.'), html`<button class="btn primary" data-a="import">${ic('upload', 16)}${t('Import CSV')}</button>`)).s;
      drawBulk(); return;
    }
    const pages = Math.max(1, Math.ceil(st.total / LIMIT));
    const allOn = st.list.every((c) => st.sel.has(c.id));
    box.innerHTML = html`<div class="table-wrap"><table class="t stack-sm"><thead><tr>
      <th class="cb"><input type="checkbox" data-all ${allOn ? 'checked' : ''} aria-label="${t('Select all on this page')}"></th>
      <th>${t('Contact')}</th><th>${t('Tags')}</th><th class="hide-sm">${t('Source')}</th><th class="hide-sm">${t('Last seen')}</th><th class="act"><span class="sr">${t('Actions')}</span></th></tr></thead><tbody>
      ${st.list.map((c) => html`<tr class="${st.sel.has(c.id) ? 'sel' : ''}">
        <td class="cb"><input type="checkbox" data-sel="${c.id}" ${st.sel.has(c.id) ? 'checked' : ''} aria-label="${t('Select')} ${c.name || c.phone}"></td>
        <td><div class="cell-main">${c.name || fmtPhone(c.phone)} ${c.opted_out ? pill(t('Opted out'), 'bad') : ''}</div><div class="cell-sub">${fmtPhone(c.phone)}${c.email ? ' · ' + c.email : ''}</div></td>
        <td><div class="row" style="gap:4px">${(c.tags || []).map((x) => html`<span class="tag">${x}</span>`)}</div></td>
        <td class="hide-sm cell-sub">${t(c.source || '—')}</td>
        <td class="hide-sm cell-sub nowrap">${c.last_seen ? relTime(c.last_seen) : '—'}</td>
        <td class="act"><button class="btn-icon" data-edit="${c.id}" aria-label="${t('Edit')} ${c.name || c.phone}">${ic('edit', 16)}</button></td></tr>`)}
      </tbody></table></div>
      <div class="pager"><span class="tnum">${t('{a}–{b} of {n}', { a: fmtNum((st.page - 1) * LIMIT + 1), b: fmtNum(Math.min(st.total, st.page * LIMIT)), n: fmtNum(st.total) })}</span>
        <span class="row"><button class="btn sm" data-page="-1" ${st.page <= 1 ? 'disabled' : ''}>${t('Previous')}</button><span class="tnum">${st.page} / ${pages}</span><button class="btn sm" data-page="1" ${st.page >= pages ? 'disabled' : ''}>${t('Next')}</button></span></div>`.s;
    drawBulk();
  }
  function drawBulk() {
    const b = root.querySelector('[data-bulk]');
    if (!st.sel.size) { b.innerHTML = ''; return; }
    b.innerHTML = html`<div class="bulkbar" role="region" aria-label="${t('Bulk actions')}"><b>${t('{n} selected', { n: st.sel.size })}</b>
      <button class="btn sm" data-bulk="add_tag">${ic('plus', 14)}${t('Add tag')}</button><button class="btn sm" data-bulk="remove_tag">${t('Remove tag')}</button>
      <button class="btn sm" data-bulk="opt_out">${t('Opt out')}</button><button class="btn sm" data-bulk="opt_in">${t('Opt in')}</button>
      <button class="btn sm" data-bulk="delete">${ic('trash', 14)}${t('Delete')}</button><button class="btn-icon" data-bulk="clear" aria-label="${t('Clear selection')}" style="color:inherit">${ic('x')}</button></div>`.s;
  }

  root.addEventListener('click', async (e) => {
    const tg = e.target.closest('[data-tag]');
    if (tg) { e.preventDefault(); st.tag = tg.dataset.tag; st.page = 1; st.sel.clear(); $$('.tagnav', root).forEach((x) => x.classList.toggle('on', x === tg)); load(); return; }
    const pg = e.target.closest('[data-page]');
    if (pg) { st.page += +pg.dataset.page; load(); return; }
    const ed = e.target.closest('[data-edit]');
    if (ed) return editDrawer(st.list.find((c) => String(c.id) === ed.dataset.edit));
    const bk = e.target.closest('[data-bulk]');
    if (bk) return bulk(bk.dataset.bulk);
    const a = e.target.closest('[data-a]');
    if (a?.dataset.a === 'add') editDrawer(null);
    if (a?.dataset.a === 'import') importModal();
  });
  root.addEventListener('change', (e) => {
    if (e.target.matches('[data-sel]')) { const id = +e.target.dataset.sel; e.target.checked ? st.sel.add(id) : st.sel.delete(id); e.target.closest('tr').classList.toggle('sel', e.target.checked); drawBulk(); }
    if (e.target.matches('[data-all]')) { st.list.forEach((c) => (e.target.checked ? st.sel.add(c.id) : st.sel.delete(c.id))); drawTable(); }
    if (e.target.matches('[data-opted]')) { st.opted = e.target.value; st.page = 1; load(); }
  });
  root.querySelector('[data-q]').addEventListener('input', debounce((e) => { st.q = e.target.value.trim(); st.page = 1; load(); }, 300));

  async function bulk(action) {
    if (action === 'clear') { st.sel.clear(); drawTable(); return; }
    const ids = [...st.sel];
    let tag;
    if (action === 'add_tag' || action === 'remove_tag') {
      tag = await promptBox(action === 'add_tag' ? t('Add tag') : t('Remove tag'), { label: t('Tag'), placeholder: 'vip' });
      if (!tag) return;
    }
    if (action === 'delete' && !(await confirmBox(t('Delete {n} contacts? This cannot be undone.', { n: ids.length }), { okText: t('Delete'), danger: true }))) return;
    const j = await api('POST', '/api/contacts/bulk', { ids, action, tag });
    if (j.ok) { toast(t('Updated {n} contacts', { n: ids.length })); st.sel.clear(); load(); loadTags(); }
  }

  function editDrawer(c) {
    const isNew = !c; c = c || { phone: '', name: '', email: '', tags: st.tag ? [st.tag] : [], notes: '', custom: {}, opted_out: false };
    const m = modal({ title: isNew ? t('Add contact') : (c.name || fmtPhone(c.phone)), side: true, body: html`<form class="stack" data-f>
      <div class="form-grid">
        <label class="field"><span>${t('Phone with country code')}</span><input name="phone" type="tel" required value="${c.phone}" ${isNew ? 'autofocus' : 'readonly'} placeholder="919876543210"></label>
        <label class="field"><span>${t('Name')}</span><input name="name" value="${c.name}"></label>
        <label class="field full"><span>${t('Email')}</span><input name="email" type="email" value="${c.email || ''}"></label>
        <div class="field full"><span class="field-label">${t('Tags')}</span><div data-chips></div><small>${t('Press Enter after each tag.')}</small></div>
        <label class="field full"><span>${t('Notes')}</span><textarea name="notes" rows="3">${c.notes || ''}</textarea></label></div>
      <fieldset><legend>${t('Custom fields')}</legend><p class="hint" style="margin:-6px 0 10px">${raw(t('Use them in messages, e.g. <code>{{city}}</code>.'))}</p>
        <div class="opt-rows" data-custom>${Object.entries(c.custom || {}).map(([k, v]) => kvRow(k, v))}</div>
        <button type="button" class="btn sm ghost mt-s" data-addkv>${ic('plus', 14)}${t('Add field')}</button></fieldset>
      ${raw(`<label class="switch"><input type="checkbox" name="opted_out" ${c.opted_out ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${t('Opted out')}</span><small>${t('Never send campaigns to this person.')}</small></span></label>`)}
      ${isNew ? '' : html`<dl class="kv cell-sub"><dt>${t('Source')}</dt><dd>${t(c.source || '—')}</dd><dt>${t('Added')}</dt><dd>${relTime(c.created_at)}</dd><dt>${t('Last seen')}</dt><dd>${c.last_seen ? relTime(c.last_seen) : '—'}</dd></dl>`}
      </form>`,
    footer: html`${isNew ? '' : html`<button class="btn danger" data-del style="margin-right:auto">${ic('trash', 16)}${t('Delete')}</button>`}<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-save>${t('Save contact')}</button>` });
    const chips = chipInput(m.body.querySelector('[data-chips]'), { values: c.tags || [], placeholder: t('Add a tag') });
    m.body.querySelector('[data-addkv]').addEventListener('click', () => { m.body.querySelector('[data-custom]').insertAdjacentHTML('beforeend', kvRow('', '').s); m.body.querySelector('[data-custom] .opt-row:last-child input').focus(); });
    m.body.addEventListener('click', (e) => { const r = e.target.closest('[data-rmkv]'); if (r) r.closest('.opt-row').remove(); });
    m.foot.querySelector('[data-save]').addEventListener('click', async () => {
      const f = m.body.querySelector('form'); if (!f.reportValidity()) return;
      const custom = {};
      $$('[data-custom] .opt-row', m.body).forEach((r) => { const [k, v] = r.querySelectorAll('input'); if (k.value.trim()) custom[k.value.trim().replace(/\s+/g, '_')] = v.value; });
      const body = { phone: f.phone.value.replace(/[^\d]/g, ''), name: f.name.value.trim(), email: f.email.value.trim(), tags: chips.get(), notes: f.notes.value, custom, opted_out: f.opted_out.checked };
      const j = isNew ? await api('POST', '/api/contacts', body) : await api('PATCH', `/api/contacts/${c.id}`, body);
      if (j.ok) { toast(t('Contact saved')); m.close(); load(); loadTags(); }
    });
    m.foot.querySelector('[data-del]')?.addEventListener('click', async () => {
      if (!(await confirmBox(t('Delete this contact?'), { okText: t('Delete'), danger: true }))) return;
      if ((await api('DELETE', `/api/contacts/${c.id}`)).ok) { toast(t('Contact deleted')); m.close(); load(); loadTags(); }
    });
  }
  const kvRow = (k, v) => html`<div class="opt-row" style="grid-template-columns:minmax(0,1fr) minmax(0,1.4fr) 34px"><input value="${k}" placeholder="city" aria-label="${t('Field name')}"><input value="${v}" placeholder="Korba" aria-label="${t('Value')}"><button type="button" class="btn-icon danger" data-rmkv aria-label="${t('Remove field')}">${ic('x', 16)}</button></div>`;

  function importModal() {
    const m = modal({ title: t('Import contacts'), size: 'lg', body: html`<form class="stack" data-f>
      <div class="note info">${ic('info', 16)}<span>${raw(t('The first row must be headers. Columns: <code>phone,name,email,tags,notes</code>. Any other column becomes a custom field. Separate several tags with <code>;</code>.'))}</span></div>
      <div class="codeblock">phone,name,email,tags,notes,city
919876543210,Asha Verma,asha@example.com,vip;diwali,Prefers Hindi,Korba
919812345678,Rahul Singh,,customer,,Raipur</div>
      <div class="row"><button type="button" class="btn sm" data-sample>${ic('download', 15)}${t('Download sample CSV')}</button></div>
      <label class="field"><span>${t('CSV file')}</span><input type="file" name="file" accept=".csv,text/csv" required></label>
      <label class="field"><span>${t('Add this tag to everyone imported')} <small>(${t('optional')})</small></span><input name="tag" placeholder="${t('e.g. expo-2026')}"></label>
      <div data-res></div></form>`,
    footer: html`<button class="btn ghost" data-close>${t('Close')}</button><button class="btn primary" data-go>${ic('upload', 16)}${t('Import')}</button>` });
    m.body.querySelector('[data-sample]').addEventListener('click', () => downloadBlob('vivid-relay-contacts-sample.csv', 'phone,name,email,tags,notes,city\n919876543210,Asha Verma,asha@example.com,vip;diwali,Prefers Hindi,Korba\n919812345678,Rahul Singh,,customer,,Raipur\n'));
    m.foot.querySelector('[data-go]').addEventListener('click', async (e) => {
      const f = m.body.querySelector('form'); if (!f.reportValidity()) return;
      const fd = new FormData(); fd.append('file', f.file.files[0]); if (f.tag.value.trim()) fd.append('tag', f.tag.value.trim());
      const b = e.currentTarget; b.classList.add('busy');
      const res = m.body.querySelector('[data-res]');
      res.innerHTML = html`<div class="bar"><span style="width:0%"></span></div>`.s;
      const j = await upload('/api/contacts/import', fd, (p) => { const s = res.querySelector('.bar span'); if (s) s.style.width = Math.round(p * 100) + '%'; });
      b.classList.remove('busy');
      res.innerHTML = j.ok ? html`<div class="grid g3">${[['Created', j.created, 'ok'], ['Updated', j.updated, 'info'], ['Invalid rows', j.invalid, j.invalid ? 'warn' : 'muted']].map(([l, v, tone]) => html`<div class="note ${tone}" style="display:grid"><small>${t(l)}</small><b style="font-size:22px;font-family:var(--font-display)">${fmtNum(v || 0)}</b></div>`)}</div>${j.stopped ? html`<div class="note warn mt-s">${ic('alert', 16)}<span>${j.stopped}</span></div>` : ''}`.s : '';
      if (j.ok) { load(); loadTags(); }
    });
  }

  await Promise.all([loadTags(), load()]);
}
