// "Sending safety" form — used on Settings (each user) and Admin → Site settings (defaults + floors for everyone).
import { api, html, ic, t, toast, toggle, skel } from '/static/app/core.js';

export async function pacingPanel(box, { admin = false } = {}) {
  box.innerHTML = skel(5).s;
  const j = await api('GET', admin ? '/api/admin/pacing' : '/api/me/pacing', undefined, { silent: true });
  if (!j.ok) { box.innerHTML = html`<p class="muted">${t('Could not load sending settings.')}</p>`.s; return; }
  const p = admin ? j.pacing : j.effective;
  const floorGap = admin ? p.floor_gap : j.floor_gap;
  const floorAPI = admin ? p.floor_api_gap : j.floor_api_gap;
  const locked = !admin && !j.can_change;
  const num = (name, val, minv, label, unit) => html`<label class="field"><span>${t(label)}</span><div class="input-group"><input name="${name}" type="number" min="${minv}" step="1" value="${val ?? ''}" ${locked ? 'disabled' : ''} required><span class="addon">${t(unit)}</span></div></label>`;
  box.innerHTML = html`<form class="stack" data-pace>
    ${locked ? html`<div class="note info">${ic('shield', 16)}<span>${t('Your administrator has fixed these settings for all accounts.')}</span></div>` : ''}
    <section><h3 class="mb">${ic('megaphone', 16)} ${t('Campaigns and bulk messages')} <small class="muted">· ${t('per number')}</small></h3>
      <div class="form-grid">
        ${num('gap_min', p.gap_min, floorGap, 'Gap between messages — from', 'seconds')}
        ${num('gap_max', p.gap_max, floorGap, 'to (random in between)', 'seconds')}
        ${num('batch_size', p.batch_size, 0, 'Take a break after', 'messages')}
        ${num('batch_pause_min', p.batch_pause_min, 1, 'Break length', 'minutes')}</div>
      <p class="hint">${t('Each number waits a random time in this range between messages. With "take turns" in a campaign, the gap is between consecutive messages of any number. A campaign can also set its own gap. 0 messages = no breaks.')}</p></section>
    <section><h3 class="mb">${ic('code', 16)} ${t('API, webhooks and auto-replies')} <small class="muted">· ${t('per number')}</small></h3>
      <div class="form-grid">
        ${num('api_gap_min', p.api_gap_min, floorAPI, 'Gap between messages — from', 'seconds')}
        ${num('api_gap_max', p.api_gap_max, floorAPI, 'to (random in between)', 'seconds')}
        ${num('reply_delay_min', p.reply_delay_min, 0, 'Auto-reply waits before typing — from', 'seconds')}
        ${num('reply_delay_max', p.reply_delay_max, 0, 'to (random in between)', 'seconds')}</div>
      <div class="stack" style="gap:4px">
        ${toggle('typing', p.typing !== false, t('Show "typing…" before API and campaign messages'), t('The time depends on the length of the text (1.5–8 seconds).'))}
        ${toggle('reply_typing', p.reply_typing !== false, t('Show "typing…" before auto-replies'), t('Recommended: replies look like a person answering.'))}</div></section>
    ${admin ? html`<section><h3 class="mb">${ic('shield', 16)} ${t('Limits for every account')}</h3>
      <div class="form-grid">
        ${num('floor_gap', p.floor_gap, 1, 'Smallest campaign gap anyone may use', 'seconds')}
        ${num('floor_api_gap', p.floor_api_gap, 0, 'Smallest API / auto-reply gap anyone may use', 'seconds')}
        ${num('status_max', p.status_max || 30000, 10, 'Most people one Status goes to', 'people')}</div>
      ${toggle('users_can_change', !!p.users_can_change, t('Users may change their own pace'), t('They can only go slower than these limits, never faster.'))}</section>
    <section><h3 class="mb">${ic('chart', 16)} ${t('Quota engine — how much a number may send')}</h3>
      <div class="form-grid">
        ${num('warmup_start', p.warmup_start, 0, 'Day 1 allowance for a newly linked number', 'messages')}
        ${num('warmup_growth_pct', p.warmup_growth_pct, 0, 'Growth per day', '%')}
        ${num('daily_limit', p.daily_limit, 1, 'Ceiling per number per day', 'messages')}
        ${num('new_contact_limit', p.new_contact_limit, 1, 'First-time contacts per number per day', 'people')}</div>
      <p class="hint">${t('Example: 20 on day 1 and 60 % growth → 32, 51, 82, 131 … until the ceiling. A plan cap and the number’s own limit (Numbers → settings) can only lower this. 0 for day 1 = no warm-up.')}</p></section>`
    : html`<p class="hint">${t('Limits set by the administrator: campaign gap at least {a} s, API gap at least {b} s.', { a: floorGap, b: floorAPI })}</p>`}
    ${locked ? '' : html`<div class="row" style="justify-content:flex-end;gap:8px">${admin ? '' : html`<button type="button" class="btn ghost" data-reset>${t('Use the defaults')}</button>`}<button class="btn primary">${t('Save sending settings')}</button></div>`}
  </form>`.s;
  const f = box.querySelector('[data-pace]');
  const ints = ['gap_min', 'gap_max', 'batch_size', 'batch_pause_min', 'api_gap_min', 'api_gap_max', 'reply_delay_min', 'reply_delay_max', 'floor_gap', 'floor_api_gap', 'status_max', 'warmup_start', 'warmup_growth_pct', 'daily_limit', 'new_contact_limit'];
  f.addEventListener('submit', async (e) => {
    e.preventDefault();
    const d = {};
    ints.forEach((k) => { if (f[k]) d[k] = Math.max(0, Math.round(Number(f[k].value) || 0)); });
    if (d.gap_max < d.gap_min) return toast(t('"to" must be at least "from".'), 'warn');
    if (d.api_gap_max < d.api_gap_min) return toast(t('"to" must be at least "from".'), 'warn');
    if (d.batch_size === 0) d.batch_size = -1; // explicit "no breaks"
    d.typing = f.typing.checked; d.reply_typing = f.reply_typing.checked;
    if (admin) d.users_can_change = f.users_can_change.checked;
    const r = await api('PUT', admin ? '/api/admin/pacing' : '/api/me/pacing', d);
    if (r.ok) toast(t('Sending settings saved'));
  });
  box.querySelector('[data-reset]')?.addEventListener('click', async () => {
    if ((await api('PUT', '/api/me/pacing', {})).ok) { toast(t('Back to the default pace')); pacingPanel(box, { admin }); }
  });
}
