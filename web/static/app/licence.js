import { api, html, raw, ic, t, fmtNum, fmtDate, meter, pill, skel, empty, toast } from '/static/app/core.js';

export const FEATURES = [['campaigns', 'Campaigns'], ['scheduler', 'Scheduling'], ['autoreply', 'Auto-replies'], ['pool', 'Number pool'], ['api', 'API access'], ['webhooks', 'Webhooks'], ['groups', 'Groups & channels'], ['status', 'Status posts'], ['polls', 'Polls']];
const SRC = { licence: 'Licence key', trial: 'Free trial', admin: 'Given by admin', selfhosted: 'Self-hosted licence', none: '—' };

export async function render(ctx) {
  const { root } = ctx;
  root.innerHTML = html`<div class="page">${skel(6)}</div>`.s;
  const [lj, pj] = await Promise.all([api('GET', '/api/licence', undefined, { silent: true }), api('GET', '/public/plans', undefined, { silent: true })]);
  if (!ctx.isCurrent()) return;
  const lic = lj.licence || ctx.me.licence || {}, usage = lj.usage || ctx.me.usage || {}, plan = lic.plan || {};
  const plans = pj.ok ? pj.plans || [] : [];
  const total = plan.period_days || 30, left = Math.max(0, lic.days_left || 0);
  const ringP = Math.min(1, left / total);
  const tone = lic.status !== 'active' ? 'bad' : left <= 7 ? 'warn' : 'ok';
  const R = 46, C = 2 * Math.PI * R;
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Licence')}</h1><p>${t('Your plan, its limits, and what you have used this month.')}</p></div></div>
    <div class="split">
      <div class="stack">
        <section class="panel panel-pad"><div class="licence-card">
          <div><div class="row" style="gap:10px"><h2 style="font-size:24px">${plan.name || t('No active plan')}</h2>${pill(t({ active: 'Active', expired: 'Expired', suspended: 'Suspended', none: 'Not active' }[lic.status] || lic.status || 'Not active'), tone)}</div>
            <dl class="kv mt"><dt>${t('Source')}</dt><dd>${t(SRC[lic.source] || lic.source || '—')}</dd>
              ${lic.key ? html`<dt>${t('Key')}</dt><dd class="mono">${lic.key}</dd>` : ''}
              <dt>${t('Started')}</dt><dd>${fmtDate(lic.starts_at, false)}</dd><dt>${t('Expires')}</dt><dd>${fmtDate(lic.expires_at, false)}</dd></dl></div>
          <div class="days-ring" aria-label="${t('{n} days left', { n: left })}"><svg viewBox="0 0 108 108" width="108" height="108" aria-hidden="true"><circle cx="54" cy="54" r="${R}" fill="none" stroke="var(--bg-sunk)" stroke-width="8"/>
            <circle cx="54" cy="54" r="${R}" fill="none" stroke="var(--${tone === 'ok' ? 'brand' : tone})" stroke-width="8" stroke-linecap="round" stroke-dasharray="${C}" stroke-dashoffset="${C * (1 - ringP)}" transform="rotate(-90 54 54)"/></svg>
            <b>${left}<small>${t('days left')}</small></b></div></div>
          ${lic.status === 'active' && left <= 7 ? html`<div class="note warn mt">${ic('alert', 16)}<span>${t('Your plan ends in {n} days. Redeem a new key or contact us to renew.', { n: left })}</span></div>` : ''}
          ${lic.status === 'expired' ? html`<div class="note bad mt">${ic('alert', 16)}<span>${t('Your plan has expired. Sending is paused until you renew.')}</span></div>` : ''}</section>
        <section class="panel"><div class="panel-head"><h2>${t('Usage')}</h2></div><div class="panel-body grid g2">
          ${meter(t('Numbers'), usage.numbers || 0, plan.max_numbers || 0)}${meter(t('Messages this month'), usage.messages_month || 0, plan.monthly_messages || 0)}
          ${meter(t('Contacts'), usage.contacts || 0, plan.max_contacts || 0)}${meter(t('Storage'), usage.storage_mb || 0, plan.storage_mb || 0, ' MB')}</div>
          <div class="panel-foot" style="justify-content:flex-start"><span class="cell-sub">${t('Daily limit per number')}: <b>${fmtNum(plan.daily_limit || 0)}</b></span></div></section>
        <section class="panel"><div class="panel-head"><h2>${t('Included features')}</h2></div><div class="panel-body"><ul class="feat-list">${FEATURES.map(([k, l]) => html`<li class="${plan.features?.[k] ? '' : 'off'}">${ic(plan.features?.[k] ? 'check' : 'x', 16)}${t(l)}</li>`)}</ul></div></section>
      </div>
      <section class="panel"><div class="panel-head"><h2>${t('Redeem a licence key')}</h2></div><form class="panel-body stack" data-redeem>
        <label class="field"><span>${t('Licence key')}</span><input name="key" required placeholder="VR-XXXX-XXXX-XXXX-XXXX" autocomplete="off" spellcheck="false" style="font-family:var(--font-mono);text-transform:uppercase"></label>
        <button class="btn primary">${ic('key', 16)}${t('Activate key')}</button>
        <p class="hint">${t('A new key replaces your current plan from today.')}</p></form></section>
    </div>
    <h2 class="mt" style="margin:32px 0 14px">${t('Plans')}</h2>
    ${plans.length ? html`<div class="plan-cards">${plans.map((p) => html`<article class="panel plan-card ${p.highlight ? 'hl' : ''}">
      <div class="row between"><h3>${p.name}</h3>${p.highlight ? pill(t('Popular'), 'ok', false) : ''}${p.id === plan.id ? pill(t('Current'), 'info', false) : ''}</div>
      <div class="price">${p.price ? (p.currency === 'INR' ? '₹' : (p.currency || '') + ' ') + fmtNum(p.price) : t('Free')}<small> / ${t('{n} days', { n: p.period_days })}</small></div>
      ${p.description ? html`<p class="cell-sub" style="margin:0">${p.description}</p>` : ''}
      <ul><li>${t('{n} numbers', { n: fmtNum(p.max_numbers) })}</li><li>${t('{n} messages / month', { n: fmtNum(p.monthly_messages) })}</li><li>${t('{n} contacts', { n: fmtNum(p.max_contacts) })}</li><li>${t('{n} per number per day', { n: fmtNum(p.daily_limit) })}</li></ul>
      <a class="btn ${p.highlight ? 'primary' : ''}" href="/contact?plan=${encodeURIComponent(p.name)}">${t('Contact to upgrade')}</a></article>`)}</div>`
    : empty('template', t('No public plans yet'))}
  </div>`.s;
  root.querySelector('[data-redeem]').addEventListener('submit', async (e) => {
    e.preventDefault(); const b = e.target.querySelector('button'); b.classList.add('busy');
    const j = await api('POST', '/api/licence/redeem', { key: e.target.key.value.trim().toUpperCase() });
    b.classList.remove('busy');
    if (j.ok) { toast(t('Licence activated')); await ctx.refreshMe(); location.reload(); }
  });
  void raw;
}
