import { api, html, raw, ic, t, fmtNum, meter, pill, sessionStatus, fmtPhone, skel, empty, getSessions, sessionName, pct, getLang } from '/static/app/core.js';

export async function render(ctx) {
  const { root, me } = ctx;
  const u = me.user, lic = me.licence || {}, plan = lic.plan || {}, usage = me.usage || {};
  const hour = new Date().getHours();
  const greet = hour < 12 ? t('Good morning') : hour < 17 ? t('Good afternoon') : t('Good evening');
  const first = (u.name || '').split(' ')[0] || '';

  root.innerHTML = html`<div class="page">
    <section class="hero" aria-labelledby="hello">
      <svg class="hero-signal" viewBox="0 0 200 200" aria-hidden="true"><g fill="none" stroke="#c4f26a" stroke-width="1.2">
        <circle cx="160" cy="40" r="20" opacity=".9"/><circle cx="160" cy="40" r="46" opacity=".55"/><circle cx="160" cy="40" r="74" opacity=".32"/><circle cx="160" cy="40" r="104" opacity=".16"/></g>
        <circle cx="160" cy="40" r="6" fill="#c4f26a"/></svg>
      <div>
        <h1 id="hello">${greet}${first ? ', ' + first : ''}</h1>
        <p data-hero-sub>${t('Here is how your numbers are doing today.')}</p>
        <div class="hero-meta" data-hero-meta></div>
      </div>
      <div class="hero-actions">
        <a class="btn primary" href="/app/send" data-link>${ic('send', 16)}${t('Send a message')}</a>
        <a class="btn ghost" href="/app/campaigns/new" data-link>${ic('megaphone', 16)}${t('New campaign')}</a>
      </div>
    </section>

    <div class="grid g4 mt" data-kpis>${[1, 2, 3, 4].map(() => raw('<div class="panel"><div class="kpi">' + skel(2).s + '</div></div>'))}</div>

    <div class="split mt">
      <section class="panel" aria-labelledby="chartH">
        <div class="panel-head"><h2 id="chartH">${t('Last 14 days')}</h2>
          <div class="legend" aria-hidden="true"><span><i style="background:var(--c-sent)"></i>${t('Sent')}</span><span><i style="background:var(--c-recv)"></i>${t('Received')}</span><span><i style="background:var(--c-fail)"></i>${t('Failed')}</span></div></div>
        <div class="panel-body"><div class="chart-wrap" data-chart>${skel(5)}</div></div>
      </section>
      <div class="stack">
        <section class="panel" aria-labelledby="licH">
          <div class="panel-head"><h2 id="licH">${t('Your plan')}</h2><a class="btn sm ghost" href="/app/licence" data-link>${t('Manage')}</a></div>
          <div class="panel-body">
            <div class="row between" style="margin-bottom:14px">
              <div><b style="font-family:var(--font-display);font-size:20px">${plan.name || t('No plan')}</b>
                <div class="cell-sub">${lic.status === 'active' ? t('Renews or ends on {d}', { d: lic.expires_at ? new Date(lic.expires_at * 1000).toLocaleDateString(getLang() === 'hi' ? 'hi-IN' : 'en-IN', { day: 'numeric', month: 'long' }) : '—' }) : t('Redeem a licence key to activate')}</div></div>
              ${lic.status === 'active' ? pill(t('{n} days left', { n: lic.days_left ?? 0 }), lic.days_left <= 7 ? 'warn' : 'ok') : pill(t(lic.status === 'expired' ? 'Expired' : 'Not active'), 'bad')}
            </div>
            ${lic.status === 'active' && lic.days_left <= 7 ? html`<div class="note warn mb">${ic('alert', 16)}<span>${t('Your plan ends soon. Renew to keep campaigns running.')}</span></div>` : ''}
            <div class="stack" style="gap:14px">
              ${meter(t('Numbers'), usage.numbers || 0, plan.max_numbers || 0)}
              ${meter(t('Messages this month'), usage.messages_month || 0, plan.monthly_messages || 0)}
              ${meter(t('Contacts'), usage.contacts || 0, plan.max_contacts || 0)}
              ${meter(t('Storage'), usage.storage_mb || 0, plan.storage_mb || 0, ' MB')}
            </div>
          </div>
        </section>
      </div>
    </div>

    <div class="split mt">
      <section class="panel" aria-labelledby="numH">
        <div class="panel-head"><h2 id="numH">${t('Numbers')}</h2><a class="btn sm ghost" href="/app/numbers" data-link>${t('Open numbers')}</a></div>
        <div data-numbers>${raw('<div class="panel-body">' + skel(3).s + '</div>')}</div>
      </section>
      <section class="panel" aria-labelledby="qaH">
        <div class="panel-head"><h2 id="qaH">${t('Quick actions')}</h2></div>
        <div class="panel-body stack" style="gap:10px">
          ${[['phone', '/app/numbers', 'Link a WhatsApp number', 'Scan a QR code from your phone'],
            ['upload', '/app/contacts', 'Import contacts', 'Upload a CSV with phone numbers'],
            ['bot', '/app/autoreplies', 'Set up auto-replies', 'Answer common questions instantly'],
            ['code', '/app/developer', 'Connect your website', 'API keys and webhooks']].map(([i, h, a, b]) => html`
            <a class="tile-link" href="${h}" data-link><span class="list-ico">${ic(i)}</span><span class="grow"><b>${t(a)}</b><small>${t(b)}</small></span>${ic('chevron', 16)}</a>`)}
        </div>
      </section>
    </div>
  </div>`.s;

  const [stats, sessions] = await Promise.all([api('GET', '/api/stats?days=14', undefined, { silent: true }), getSessions(true)]);
  if (!ctx.isCurrent()) return;

  const tot = stats.ok ? stats.totals || {} : {};
  const connected = sessions.filter((s) => s.status === 'connected').length;
  const sentToday = sessions.reduce((a, s) => a + (s.today?.sent || 0), 0);
  const limitToday = sessions.reduce((a, s) => a + (s.today?.limit || 0), 0);
  const failRate = tot.sent + tot.failed ? Math.round((tot.failed / (tot.sent + tot.failed)) * 1000) / 10 : 0;
  root.querySelector('[data-hero-meta]').innerHTML = html`
    ${pill(t('{a} of {b} numbers connected', { a: connected, b: sessions.length }), connected === sessions.length && connected ? 'ok' : 'warn')}
    ${pill(t('{n} sent today', { n: fmtNum(sentToday) }), 'muted', false)}`.s;
  root.querySelector('[data-kpis]').innerHTML = html`
    ${kpi(t('Sent today'), fmtNum(sentToday), limitToday ? t('{p}% of today’s safe limit', { p: pct(sentToday, limitToday) }) : '', 'send')}
    ${kpi(t('Sent · 14 days'), fmtNum(tot.sent || 0), '', null, 'var(--c-sent)')}
    ${kpi(t('Received · 14 days'), fmtNum(tot.received || 0), '', null, 'var(--c-recv)')}
    ${kpi(t('Failure rate'), failRate + '%', t('{n} failed', { n: fmtNum(tot.failed || 0) }), null, 'var(--c-fail)')}`.s;

  const chartBox = root.querySelector('[data-chart]');
  if (stats.ok && stats.days?.length) drawChart(chartBox, stats.days);
  else chartBox.innerHTML = empty('poll', t('No activity yet'), t('Once you send messages, daily totals show up here.')).s;

  const nb = root.querySelector('[data-numbers]');
  if (!sessions.length) {
    nb.innerHTML = empty('phone', t('No numbers yet'), t('Link your first WhatsApp number to start sending.'), html`<a class="btn primary" href="/app/numbers" data-link>${ic('plus', 16)}${t('Add number')}</a>`).s;
  } else {
    nb.innerHTML = html`<ul class="list">${sessions.map((s) => {
      const st = sessionStatus(s.status); const td = s.today || {};
      return html`<li><span class="list-ico">${ic('phone')}</span>
        <span class="li-main"><b>${sessionName(s)}</b><small>${s.phone ? fmtPhone(s.phone) : t('Not linked')}${s.push_name ? ' · ' + s.push_name : ''}</small></span>
        <span class="cell-sub tnum nowrap">${fmtNum(td.sent || 0)}/${fmtNum(td.limit || 0)}</span>
        ${pill(st.label, st.tone)}</li>`;
    })}</ul>`.s;
  }
}

function kpi(label, val, sub, icn, swatch) {
  return html`<div class="panel"><div class="kpi"><span class="k-label">${swatch ? raw(`<span class="swatch" style="background:${swatch}"></span>`) : icn ? ic(icn, 15) : ''}${label}</span>
    <span class="k-val">${val}</span>${sub ? html`<span class="k-sub">${sub}</span>` : ''}</div></div>`;
}

/** Grouped bar chart (sent / received / failed per day) in inline SVG with hover tooltips and a table fallback. */
export function drawChart(box, days) {
  const W = 640, H = 230, pl = 34, pr = 6, pt = 10, pb = 26;
  const max = Math.max(1, ...days.map((d) => Math.max(d.sent || 0, d.received || 0, d.failed || 0)));
  const step = niceStep(max); const top = Math.ceil(max / step) * step;
  const iw = W - pl - pr, ih = H - pt - pb, gw = iw / days.length, bw = Math.max(3, Math.min(10, (gw - 8) / 3));
  const y = (v) => pt + ih - (v / top) * ih;
  const loc = getLang() === 'hi' ? 'hi-IN' : 'en-IN';
  const lbl = (d) => new Date(d.day + 'T00:00:00').toLocaleDateString(loc, { day: 'numeric', month: 'short' });
  let g = '';
  for (let v = 0; v <= top; v += step) g += `<line class="grid-l" x1="${pl}" x2="${W - pr}" y1="${y(v)}" y2="${y(v)}"/><text class="axis-t" x="${pl - 6}" y="${y(v) + 4}" text-anchor="end">${fmtShort(v)}</text>`;
  let bars = '', hits = '', xl = '';
  const series = [['sent', 'b-sent'], ['received', 'b-recv'], ['failed', 'b-fail']];
  days.forEach((d, i) => {
    const gx = pl + i * gw + (gw - (bw * 3 + 4)) / 2;
    series.forEach(([k, c], j) => {
      const v = d[k] || 0; if (!v) return;
      const x = gx + j * (bw + 2), yy = y(v), h = Math.max(1.5, pt + ih - yy);
      bars += `<path class="${c}" d="${roundTop(x, pt + ih - h, bw, h, Math.min(3, bw / 2))}" style="animation-delay:${i * 20}ms"/>`;
    });
    hits += `<rect class="hit" x="${pl + i * gw}" y="${pt}" width="${gw}" height="${ih}" tabindex="0" data-i="${i}" aria-label="${lbl(d)}: ${d.sent || 0} sent, ${d.received || 0} received, ${d.failed || 0} failed"/>`;
    if (i % (days.length > 10 ? 2 : 1) === 0 || i === days.length - 1) xl += `<text class="axis-t" x="${pl + i * gw + gw / 2}" y="${H - 6}" text-anchor="middle">${lbl(d)}</text>`;
  });
  box.innerHTML = `<svg class="chart" viewBox="0 0 ${W} ${H}" role="img" aria-label="${t('Messages per day')}">${g}<g class="bars">${bars}</g>${xl}${hits}</svg><div class="tip" hidden></div>
    <details class="mt-s"><summary class="cell-sub" style="cursor:pointer">${t('Show as table')}</summary><div class="table-wrap"><table class="t"><thead><tr><th>${t('Day')}</th><th class="right">${t('Sent')}</th><th class="right">${t('Received')}</th><th class="right">${t('Failed')}</th></tr></thead><tbody>${days.map((d) => `<tr><td>${lbl(d)}</td><td class="right tnum">${d.sent || 0}</td><td class="right tnum">${d.received || 0}</td><td class="right tnum">${d.failed || 0}</td></tr>`).join('')}</tbody></table></div></details>`;
  const tip = box.querySelector('.tip'), svg = box.querySelector('svg');
  const show = (el) => {
    const d = days[+el.dataset.i]; const r = svg.getBoundingClientRect(), br = box.getBoundingClientRect();
    const sx = r.width / W;
    tip.innerHTML = html`<b>${lbl(d)}</b><span><i style="background:var(--c-sent)"></i>${t('Sent')} ${fmtNum(d.sent || 0)}</span><br><span><i style="background:var(--c-recv)"></i>${t('Received')} ${fmtNum(d.received || 0)}</span><br><span><i style="background:var(--c-fail)"></i>${t('Failed')} ${fmtNum(d.failed || 0)}</span>`.s;
    const cx = (+el.getAttribute('x') + gw / 2) * sx + (r.left - br.left);
    tip.style.left = Math.max(70, Math.min(br.width - 70, cx)) + 'px';
    tip.style.top = (pt * sx + (r.top - br.top) + 4) + 'px';
    tip.hidden = false;
  };
  svg.addEventListener('pointerover', (e) => { if (e.target.classList.contains('hit')) show(e.target); });
  svg.addEventListener('focusin', (e) => { if (e.target.classList.contains('hit')) show(e.target); });
  svg.addEventListener('pointerleave', () => { tip.hidden = true; });
  svg.addEventListener('focusout', () => { tip.hidden = true; });
}
function roundTop(x, y, w, h, r) { return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`; }
function niceStep(max) { const raw = max / 4; const p = Math.pow(10, Math.floor(Math.log10(raw))); const n = raw / p; return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p; }
function fmtShort(v) { return v >= 1000 ? (v / 1000).toFixed(v % 1000 ? 1 : 0) + 'k' : String(v); }
