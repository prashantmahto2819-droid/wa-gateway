import { api, html, raw, ic, t, fmtNum, fmtPhone, pill, skel, empty, modal, confirmBox, promptBox, toast, copy, getSessions, sessionOptions, parsePhones, formData, toggle, debounce, $$ } from '/static/app/core.js';

export async function render(ctx) {
  const { root } = ctx;
  const sessions = await getSessions(true);
  if (!ctx.isCurrent()) return;
  const st = { session: ctx.query.get('session') || sessions.find((s) => s.status === 'connected')?.name || sessions[0]?.name || '', tab: 'groups', groups: [], channels: [], q: '' };
  if (!sessions.length) {
    root.innerHTML = html`<div class="page"><div class="page-head"><div><h1>${t('Groups & channels')}</h1></div></div><div class="panel">${empty('group', t('Link a number first'), t('Groups and channels belong to a WhatsApp number.'), html`<a class="btn primary" href="/app/numbers" data-link>${t('Open numbers')}</a>`)}</div></div>`.s;
    return;
  }
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Groups & channels')}</h1><p>${t('Manage groups, communities and channels of a number, and post to them.')}</p></div>
      <div class="page-actions"><button class="btn" data-a="join">${ic('link', 16)}${t('Join by link')}</button><button class="btn primary" data-a="create">${ic('plus', 16)}<span data-create-l>${t('Create group')}</span></button></div></div>
    <div class="row mb">
      <label class="field" style="flex:0 1 320px"><span class="sr">${t('Number')}</span><select data-session aria-label="${t('Number')}">${sessionOptions(sessions, st.session)}</select></label>
      <div class="seg" role="tablist"><button role="tab" data-tab="groups" aria-selected="true">${ic('group', 15)}${t('Groups')}</button><button role="tab" data-tab="channels" aria-selected="false">${ic('channel', 15)}${t('Channels')}</button></div>
      <div class="search grow" style="max-width:280px;margin-left:auto">${ic('search', 16)}<input type="search" data-q placeholder="${t('Search')}" aria-label="${t('Search')}"></div></div>
    <section class="panel" data-list>${raw('<div class="panel-body">' + skel(6).s + '</div>')}</section></div>`.s;
  const box = root.querySelector('[data-list]');
  const S = () => encodeURIComponent(st.session);

  async function load() {
    box.innerHTML = '<div class="panel-body">' + skel(6).s + '</div>';
    const key = st.tab;
    const j = await api('GET', `/api/sessions/${S()}/${key}`, undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    if (!j.ok) { box.innerHTML = empty('alert', t('Could not load'), j.error || t('Is this number connected?')).s; return; }
    st[key] = j[key] || []; draw();
  }
  function draw() {
    const q = st.q.toLowerCase();
    if (st.tab === 'groups') {
      const l = st.groups.filter((g) => !q || (g.name || '').toLowerCase().includes(q));
      if (!st.groups.length) { box.innerHTML = empty('group', t('No groups yet'), t('Create a group or join one with an invite link.')).s; return; }
      const typeL = { group: t('Group'), community: t('Community'), community_announcements: t('Community announcements') };
      box.innerHTML = html`<ul class="list">${l.map((g) => html`<li>
        <span class="list-ico">${ic(g.type === 'community' ? 'users' : g.type === 'community_announcements' ? 'megaphone' : 'group')}</span>
        <span class="li-main"><b>${g.name || g.jid}</b><small>${typeL[g.type] || g.type} · ${t('{n} members', { n: fmtNum(g.participants || 0) })}${g.announce_only ? ' · ' + t('only admins post') : ''}</small></span>
        ${g.i_am_admin ? pill(t('Admin'), 'info', false) : ''}
        <span class="btn-row"><button class="btn sm" data-post="${g.jid}">${ic('send', 14)}<span class="hide-xs">${t('Post')}</span></button><button class="btn sm ghost" data-open="${g.jid}">${t('Manage')}</button></span></li>`)}</ul>`.s;
    } else {
      const l = st.channels.filter((c) => !q || (c.name || '').toLowerCase().includes(q));
      if (!st.channels.length) { box.innerHTML = empty('channel', t('No channels yet'), t('Create a channel to broadcast updates to followers.')).s; return; }
      box.innerHTML = html`<ul class="list">${l.map((c) => html`<li><span class="list-ico">${ic('channel')}</span>
        <span class="li-main"><b>${c.name}</b><small>${t('{n} followers', { n: fmtNum(c.subscribers || 0) })} · ${t(c.role || 'follower')}</small></span>
        ${c.can_post ? pill(t('Can post'), 'ok') : pill(t('Read only'), 'muted')}
        <span class="btn-row">${c.invite ? html`<button class="btn sm ghost" data-copy="https://whatsapp.com/channel/${c.invite}" aria-label="${t('Copy link')}">${ic('copy', 14)}</button>` : ''}
        ${c.can_post ? html`<button class="btn sm" data-post="${c.jid}">${ic('send', 14)}${t('Post')}</button>` : ''}
        <button class="btn sm ghost" data-copy="${c.jid}">${t('Copy ID')}</button></span></li>`)}</ul>`.s;
    }
  }

  root.addEventListener('change', (e) => { if (e.target.matches('[data-session]')) { st.session = e.target.value; load(); } });
  root.querySelector('[data-q]').addEventListener('input', debounce((e) => { st.q = e.target.value; draw(); }, 150));
  root.addEventListener('click', async (e) => {
    const tb = e.target.closest('[data-tab]');
    if (tb) { st.tab = tb.dataset.tab; $$('[data-tab]', root).forEach((x) => x.setAttribute('aria-selected', String(x === tb))); root.querySelector('[data-create-l]').textContent = st.tab === 'groups' ? t('Create group') : t('Create channel'); root.querySelector('[data-a="join"]').innerHTML = html`${ic('link', 16)}${st.tab === 'groups' ? t('Join by link') : t('Find by link')}`.s; load(); return; }
    const cp = e.target.closest('[data-copy]'); if (cp) return copy(cp.dataset.copy);
    const po = e.target.closest('[data-post]');
    if (po) return ctx.navigate(`/app/send?session=${S()}&to=${encodeURIComponent(po.dataset.post)}`);
    const op = e.target.closest('[data-open]'); if (op) return groupDrawer(op.dataset.open);
    const a = e.target.closest('[data-a]'); if (!a) return;
    if (a.dataset.a === 'create') return st.tab === 'groups' ? createGroup() : createChannel();
    if (a.dataset.a === 'join') {
      if (st.tab === 'groups') {
        const link = await promptBox(t('Join a group'), { label: t('Invite link'), placeholder: 'https://chat.whatsapp.com/…', okText: t('Join') });
        if (link && (await api('POST', `/api/sessions/${S()}/groups/join`, { link })).ok) { toast(t('Joined')); load(); }
      } else {
        const inv = await promptBox(t('Find a channel'), { label: t('Channel link or invite code'), placeholder: 'https://whatsapp.com/channel/…', okText: t('Find') });
        if (!inv) return;
        const code = inv.replace(/^.*\/channel\//, '').trim();
        const j = await api('GET', `/api/sessions/${S()}/channels/resolve?invite=${encodeURIComponent(code)}`);
        if (j.ok) {
          const c = j.channel || j;
          modal({ title: c.name || t('Channel'), size: 'sm', body: html`<dl class="kv"><dt>ID</dt><dd class="mono">${c.jid || '—'}</dd><dt>${t('Followers')}</dt><dd>${fmtNum(c.subscribers || 0)}</dd>${c.description ? html`<dt>${t('About')}</dt><dd>${c.description}</dd>` : ''}</dl>`,
            footer: c.jid ? html`<button class="btn" data-copy-id>${ic('copy', 15)}${t('Copy ID')}</button>` : '' }).foot.querySelector('[data-copy-id]')?.addEventListener('click', () => copy(c.jid));
        }
      }
    }
  });

  function createGroup() {
    const m = modal({ title: t('Create group'), body: html`<form class="stack">
      <label class="field"><span>${t('Name')}</span><input name="name" required maxlength="100" autofocus></label>
      <label class="field"><span>${t('Members (phone numbers)')}</span><textarea name="participants" rows="3" placeholder="919876543210, 919812345678"></textarea><small>${t('Only add people who expect it. Being added by strangers is a common reason for reports.')}</small></label>
      ${toggle('community', false, t('Create as a community'), t('A community holds several groups and an announcements channel.'))}</form>`,
    footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Create group')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form'); if (!f.reportValidity()) return;
      const j = await api('POST', `/api/sessions/${S()}/groups`, { name: f.name.value.trim(), participants: parsePhones(f.participants.value), community: f.community.checked });
      if (j.ok) { toast(t('Group created')); m.close(); load(); }
    });
  }
  function createChannel() {
    const m = modal({ title: t('Create channel'), body: html`<form class="stack"><label class="field"><span>${t('Name')}</span><input name="name" required autofocus></label>
      <label class="field"><span>${t('Description')}</span><textarea name="description" rows="3"></textarea></label></form>`,
    footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Create channel')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form'); if (!f.reportValidity()) return;
      if ((await api('POST', `/api/sessions/${S()}/channels`, { name: f.name.value.trim(), description: f.description.value })).ok) { toast(t('Channel created')); m.close(); load(); }
    });
  }

  async function groupDrawer(jid) {
    const J = encodeURIComponent(jid);
    const m = modal({ title: t('Group'), side: true, size: 'lg', body: skel(8) });
    async function fill() {
      const j = await api('GET', `/api/sessions/${S()}/groups/${J}`);
      if (!j.ok) { m.body.innerHTML = empty('alert', t('Could not load group')).s; return; }
      const g = j.group; const brief = st.groups.find((x) => x.jid === jid) || {};
      const admin = brief.i_am_admin ?? g.i_am_admin;
      m.el.querySelector('.dlg-head h2').textContent = g.name || jid;
      const parts = g.participants || [];
      m.body.innerHTML = html`<div class="stack">
        <div class="row"><button class="btn primary sm" data-post>${ic('send', 14)}${t('Post to this group')}</button><button class="btn sm" data-invite>${ic('link', 14)}${t('Invite link')}</button><button class="btn sm" data-save-c>${ic('users', 14)}${t('Save members to contacts')}</button><button class="btn sm danger" data-leave>${ic('logout', 14)}${t('Leave')}</button></div>
        <div data-inv></div>
        <form class="panel panel-pad stack" data-settings ${admin ? '' : 'inert'}><h3>${t('Settings')} ${admin ? '' : html`<small class="muted">· ${t('only admins can change these')}</small>`}</h3>
          <label class="field"><span>${t('Name')}</span><input name="name" value="${g.name || ''}"></label>
          <label class="field"><span>${t('Description')}</span><textarea name="topic" rows="2">${g.topic || ''}</textarea></label>
          ${toggle('announce_only', !!(g.announce_only ?? brief.announce_only), t('Only admins can send messages'))}
          ${toggle('locked', !!g.locked, t('Only admins can edit group info'))}
          <div><button class="btn sm" type="submit">${t('Save settings')}</button></div></form>
        <section class="panel"><div class="panel-head"><h3>${t('Members')} <span class="sub">${fmtNum(parts.length)}</span></h3>
          ${admin ? html`<button class="btn sm" data-addp>${ic('plus', 14)}${t('Add')}</button>` : ''}</div>
          <div class="table-wrap" style="max-height:420px;overflow:auto"><table class="t"><tbody>${parts.map((p) => html`<tr>
            <td><span class="cell-main">${fmtPhone(p.phone || String(p.jid).split('@')[0])}</span></td>
            <td>${p.is_super_admin ? pill(t('Owner'), 'info', false) : p.is_admin ? pill(t('Admin'), 'info', false) : ''}</td>
            <td class="act">${admin ? html`<span class="btn-row" style="justify-content:flex-end">${p.is_admin ? html`<button class="btn sm ghost" data-p="demote" data-j="${p.jid}">${t('Remove admin')}</button>` : html`<button class="btn sm ghost" data-p="promote" data-j="${p.jid}">${t('Make admin')}</button>`}
              <button class="btn-icon danger" data-p="remove" data-j="${p.jid}" aria-label="${t('Remove')} ${p.phone || ''}">${ic('x', 16)}</button></span>` : ''}</td></tr>`)}</tbody></table></div></section></div>`.s;
      m.body.querySelector('[data-settings]').addEventListener('submit', async (e) => {
        e.preventDefault(); const d = formData(e.target);
        if ((await api('POST', `/api/sessions/${S()}/groups/${J}/settings`, d)).ok) { toast(t('Group updated')); load(); }
      });
    }
    m.body.addEventListener('click', async (e) => {
      if (e.target.closest('[data-post]')) { m.close(); return ctx.navigate(`/app/send?session=${S()}&to=${J}`); }
      const inv = e.target.closest('[data-invite],[data-reset]');
      if (inv) {
        const reset = !!inv.dataset.reset || inv.hasAttribute('data-reset');
        if (reset && !(await confirmBox(t('Reset the invite link? The old link stops working.'), { okText: t('Reset link'), danger: true }))) return;
        const j = await api('GET', `/api/sessions/${S()}/groups/${J}/invite?reset=${reset ? 1 : 0}`);
        if (j.ok) m.body.querySelector('[data-inv]').innerHTML = html`<div class="secret"><span>${j.link}</span><button class="btn sm" data-copy="${j.link}">${ic('copy', 14)}${t('Copy')}</button><button class="btn sm ghost" data-reset>${t('Reset')}</button></div>`.s;
        return;
      }
      const cp = e.target.closest('[data-copy]'); if (cp) return copy(cp.dataset.copy);
      if (e.target.closest('[data-save-c]')) {
        const tag = await promptBox(t('Save members to contacts'), { label: t('Tag for these contacts'), value: 'group-' + (st.groups.find((x) => x.jid === jid)?.name || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').slice(0, 24) });
        if (!tag) return;
        const j = await api('POST', `/api/sessions/${S()}/groups/${J}/save-contacts`, { tag });
        if (j.ok) toast(t('{n} members saved to contacts', { n: j.saved ?? 0 }));
        return;
      }
      if (e.target.closest('[data-leave]')) {
        if (!(await confirmBox(t('Leave this group from this number?'), { okText: t('Leave group'), danger: true }))) return;
        if ((await api('POST', `/api/sessions/${S()}/groups/${J}/leave`, {})).ok) { toast(t('Left the group')); m.close(); load(); }
        return;
      }
      if (e.target.closest('[data-addp]')) {
        const v = await promptBox(t('Add members'), { label: t('Phone numbers with country code'), placeholder: '919876543210, 919812345678' });
        if (v && (await api('POST', `/api/sessions/${S()}/groups/${J}/participants`, { action: 'add', participants: parsePhones(v) })).ok) { toast(t('Members added')); fill(); }
        return;
      }
      const p = e.target.closest('[data-p]');
      if (p) {
        if (p.dataset.p === 'remove' && !(await confirmBox(t('Remove this member from the group?'), { okText: t('Remove'), danger: true }))) return;
        if ((await api('POST', `/api/sessions/${S()}/groups/${J}/participants`, { action: p.dataset.p, participants: [p.dataset.j] })).ok) { toast(t('Done')); fill(); }
      }
    });
    fill();
  }

  await load();
}
