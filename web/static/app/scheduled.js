import { api, html, ic, t, fmtTime, skel, empty, getLang } from '/static/app/core.js';
import { campaignPill } from '/static/app/campaigns.js';
import { summarize } from '/static/app/composer.js';

export async function render(ctx) {
  const { root } = ctx;
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Scheduled')}</h1><p>${t('Upcoming campaign runs, by day. Repeating campaigns show their next run.')}</p></div>
      <div class="page-actions"><a class="btn primary" href="/app/campaigns/new?step=1" data-link>${ic('plus', 16)}${t('Schedule a campaign')}</a></div></div>
    <section class="panel panel-pad" data-list>${skel(5)}</section></div>`.s;
  const j = await api('GET', '/api/campaigns', undefined, { silent: true });
  if (!ctx.isCurrent()) return;
  const now = Date.now() / 1000;
  const items = (j.ok ? j.campaigns || [] : [])
    .map((c) => ({ c, at: c.schedule?.next_run || (c.status === 'scheduled' ? c.schedule?.at : 0) }))
    .filter((x) => x.at && x.at > now - 3600 && x.c.status !== 'cancelled' && (x.c.status !== 'done' || (x.c.schedule?.repeat && x.c.schedule.repeat !== 'none')))
    .sort((a, b) => a.at - b.at);
  const box = root.querySelector('[data-list]');
  if (!items.length) { box.innerHTML = empty('calendar', t('Nothing scheduled'), t('Pick a date and time in step 4 of a new campaign, or use Schedule on the Send page.'), html`<a class="btn primary" href="/app/campaigns/new" data-link>${t('New campaign')}</a>`).s; return; }
  const loc = getLang() === 'hi' ? 'hi-IN' : 'en-IN';
  const days = new Map();
  items.forEach((x) => { const d = new Date(x.at * 1000); const k = d.toDateString(); if (!days.has(k)) days.set(k, { d, list: [] }); days.get(k).list.push(x); });
  const today = new Date().toDateString();
  const tomorrow = new Date(Date.now() + 86400e3).toDateString();
  box.innerHTML = html`${[...days.entries()].map(([k, { d, list }]) => html`<div class="day-block">
    <div class="day-date ${k === today ? 'today' : ''}"><small>${d.toLocaleDateString(loc, { weekday: 'short' })}</small><b>${d.getDate()}</b><small>${d.toLocaleDateString(loc, { month: 'short' })}</small></div>
    <div><p class="cell-sub" style="margin:4px 0 8px">${k === today ? t('Today') : k === tomorrow ? t('Tomorrow') : d.toLocaleDateString(loc, { weekday: 'long', day: 'numeric', month: 'long' })} · ${t('{n} runs', { n: list.length })}</p>
      ${list.map(({ c, at }) => html`<a class="run" href="/app/campaigns/${c.id}" data-link><span class="time">${fmtTime(at)}</span>
        <span class="grow" style="min-width:0"><b class="ellipsis" style="display:block">${c.name}</b><small class="muted ellipsis" style="display:block">${summarize(c.message)}${c.schedule?.repeat && c.schedule.repeat !== 'none' ? ' · ' + t('repeats {r}', { r: t(c.schedule.repeat) }) : ''}</small></span>
        ${campaignPill(c)}</a>`)}</div></div>`)}`.s;
}
