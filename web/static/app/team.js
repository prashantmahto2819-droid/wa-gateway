// Team: staff of this workspace, roles and invite links.
import { api, html, raw, ic, t, toast, modal, confirmBox, copy, skel, empty, pill, relTime, fmtDate } from '/static/app/core.js';

const ROLE_HELP = {
  admin: 'Everything except billing and deleting the workspace.',
  manager: 'Numbers, campaigns, lists, automations, exports. No staff, API keys or webhooks.',
  supervisor: 'Campaigns, lists and automations. Cannot link or remove numbers.',
  agent: 'Send messages and manage contacts. No bulk sending, exports or automations.',
  analyst: 'Read only.',
};

export async function render(ctx) {
  const { root, me } = ctx;
  ctx.setTitle(t('Team'));
  const canStaff = me.can?.staff !== false;
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Team')}</h1><p>${t('Staff who work in this workspace with you. Each person gets their own login and a role that decides what they can do.')}</p></div>
      ${canStaff ? html`<div class="page-actions"><button class="btn primary" data-a="invite">${ic('plus', 16)}${t('Invite someone')}</button></div>` : ''}</div>
    <section class="panel" data-box>${skel(4)}</section>
    <section class="panel mt"><div class="panel-head"><h2>${t('What each role can do')}</h2></div><div class="panel-body">
      <dl class="summary">${Object.entries(ROLE_HELP).map(([r, d]) => html`<div><dt>${t(r)}</dt><dd>${t(d)}</dd></div>`)}</dl></div></section></div>`.s;
  const box = root.querySelector('[data-box]');
  let data = null;
  async function load() {
    const j = await api('GET', '/api/team');
    if (!ctx.isCurrent() || !j.ok) return;
    data = j;
    const seats = j.seat_limit ? t('{u} of {n} seats used', { u: j.members.length + j.invites.length, n: j.seat_limit }) : t('Unlimited seats');
    box.innerHTML = html`<div class="panel-head"><h2>${t('People')}</h2><span class="muted">${seats}</span></div>
      <ul class="list">
        <li><span class="avatar">${(j.owner?.name || '?').slice(0, 1).toUpperCase()}</span><span class="li-main"><b>${j.owner?.name || j.owner?.email}</b><small>${j.owner?.email} · ${t('owner')}</small></span>${pill(t('owner'), 'ok')}</li>
        ${j.members.map((m) => html`<li><span class="avatar">${(m.name || m.email).slice(0, 1).toUpperCase()}</span>
          <span class="li-main"><b>${m.name || m.email}</b><small>${m.email}${m.team ? ' · ' + m.team : ''} · ${m.last_login ? t('last login {t}', { t: relTime(m.last_login) }) : t('never logged in')}</small></span>
          ${pill(t(m.role), m.status === 'active' ? 'ok' : 'muted')}
          ${canStaff ? html`<button class="btn sm" data-edit="${m.user_id}">${t('Change')}</button><button class="btn-icon danger" data-rm="${m.user_id}" aria-label="${t('Remove')}">${ic('trash', 16)}</button>` : ''}</li>`)}
        ${j.invites.map((i) => html`<li><span class="list-ico">${ic('link')}</span><span class="li-main"><b>${i.name || i.email || t('Invite link')}</b><small>${t('invited as {r}', { r: t(i.role) })} · ${t('expires {d}', { d: fmtDate(i.expires_at) })}</small></span>
          ${canStaff ? html`<button class="btn-icon danger" data-rminv="${i.id}" aria-label="${t('Cancel invite')}">${ic('x', 16)}</button>` : ''}</li>`)}
      </ul>
      ${!j.members.length && !j.invites.length ? html`<div class="panel-body">${empty('users', t('Just you so far'), t('Invite your staff: each gets a login and only the access their role allows.'))}</div>` : ''}`.s;
  }
  root.addEventListener('click', async (e) => {
    if (e.target.closest('[data-a="invite"]')) return inviteModal();
    const ed = e.target.closest('[data-edit]'); if (ed) return editModal(data.members.find((m) => String(m.user_id) === ed.dataset.edit));
    const rm = e.target.closest('[data-rm]');
    if (rm && await confirmBox(t('Remove this person from the workspace? Their login will stop working.'), { danger: true, okText: t('Remove') })) {
      if ((await api('DELETE', '/api/team/' + rm.dataset.rm)).ok) { toast(t('Removed')); load(); }
    }
    const ri = e.target.closest('[data-rminv]');
    if (ri && (await api('DELETE', '/api/team/invites/' + ri.dataset.rminv)).ok) { toast(t('Invite cancelled')); load(); }
  });
  function roleSelect(cur) {
    return html`<label class="field"><span>${t('Role')}</span><select name="role">${(data?.roles || ['admin', 'manager', 'supervisor', 'agent', 'analyst']).map((r) => html`<option value="${r}" ${r === cur ? 'selected' : ''}>${t(r)} — ${t(ROLE_HELP[r])}</option>`)}</select></label>`;
  }
  function inviteModal() {
    const m = modal({ title: t('Invite someone'), body: html`<form class="stack" data-f>
      <label class="field"><span>${t('Name')}</span><input type="text" name="name" placeholder="${t('Ravi Sharma')}"></label>
      <label class="field"><span>${t('Email')} <small>(${t('optional — fixes the login email')})</small></span><input type="email" name="email"></label>
      ${roleSelect('agent')}
      <label class="field"><span>${t('Team')} <small>(${t('optional')})</small></span><input type="text" name="team" placeholder="${t('Sales')}"></label>
      <p class="hint">${t('You get a link to send on WhatsApp or email. It works once and expires in 7 days.')}</p></form>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Create invite link')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form');
      const j = await api('POST', '/api/team/invites', { name: f.name.value.trim(), email: f.email.value.trim(), role: f.role.value, team: f.team.value.trim() });
      if (!j.ok) return;
      m.body.innerHTML = html`<div class="stack"><p class="lead">${t('Send this link to {n}. It opens a page where they set their password.', { n: f.name.value.trim() || t('them') })}</p>
        <div class="input-group"><input type="text" readonly value="${j.link}" data-link-out><button type="button" class="btn" data-copy>${ic('copy', 14)}${t('Copy')}</button></div>
        <a class="btn" target="_blank" rel="noopener" href="https://wa.me/?text=${encodeURIComponent(t('Join our Vivid Relay workspace: ') + j.link)}">${ic('send', 14)}${t('Share on WhatsApp')}</a></div>`.s;
      m.foot.innerHTML = html`<button class="btn primary" data-close>${t('Done')}</button>`.s;
      m.body.querySelector('[data-copy]').addEventListener('click', () => copy(j.link, t('Invite link')));
      load();
    });
  }
  function editModal(mem) {
    const m = modal({ title: mem.name || mem.email, size: 'sm', body: html`<form class="stack" data-f>${roleSelect(mem.role)}
      <label class="field"><span>${t('Team')}</span><input type="text" name="team" value="${mem.team || ''}"></label>
      ${raw(`<label class="switch"><input type="checkbox" name="active" ${mem.status === 'active' ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${t('Login active')}</span><small>${t('Off = they are logged out and cannot log in until turned on again.')}</small></span></label>`)}</form>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Save')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form');
      if ((await api('PATCH', '/api/team/' + mem.user_id, { role: f.role.value, team: f.team.value.trim(), status: f.active.checked ? 'active' : 'disabled' })).ok) { m.close(); toast(t('Saved')); load(); }
    });
  }
  load();
}
