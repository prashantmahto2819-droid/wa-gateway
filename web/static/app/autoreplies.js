import { api, html, raw, ic, t, fmtNum, pill, skel, empty, modal, confirmBox, toast, toggle, chipInput, getSessions, sessionOptions, sessionName, $$ } from '/static/app/core.js';
import { createComposer, summarize } from '/static/app/composer.js';

const TRIGGERS = [
  ['welcome', 'heart', 'Welcome', 'First message from a new person'],
  ['keyword', 'text', 'Exact keyword', 'Whole message equals a keyword'],
  ['contains', 'search', 'Contains', 'Message contains a keyword'],
  ['regex', 'code', 'Pattern (regex)', 'For advanced matching'],
  ['menu', 'list', 'Menu choice', 'Person picked an option from a menu'],
  ['any', 'send', 'Any message', 'Replies to everything'],
  ['away', 'clock', 'Away', 'Outside your business hours'],
];
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const TL = Object.fromEntries(TRIGGERS.map(([k, , l]) => [k, l]));

export async function render(ctx) {
  const { root } = ctx;
  const sessions = await getSessions();
  let list = [];
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Auto-replies')}</h1><p>${t('Answer common messages instantly. Rules are checked from top to bottom; the first match replies.')}</p></div>
      <div class="page-actions"><button class="btn primary" data-a="new">${ic('plus', 16)}${t('New rule')}</button></div></div>
    <section class="panel" data-list>${raw('<div class="panel-body">' + skel(5).s + '</div>')}</section></div>`.s;
  if (!ctx.isCurrent()) return;

  async function load() {
    const j = await api('GET', '/api/autoreplies', undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    list = (j.ok ? j.autoreplies || j.rules || [] : []).sort((a, b) => (b.priority || 0) - (a.priority || 0));
    draw();
  }
  function draw() {
    const box = root.querySelector('[data-list]');
    if (!list.length) { box.innerHTML = empty('bot', t('No auto-replies yet'), t('Start with a welcome message and an away message for nights and Sundays.'), html`<button class="btn primary" data-a="new">${ic('plus', 16)}${t('New rule')}</button>`).s; return; }
    box.innerHTML = html`<div data-rules>${list.map((r, i) => html`<div class="rule-card" draggable="true" data-id="${r.id}">
      <div class="prio"><button class="btn-icon" data-move="-1" data-i="${i}" aria-label="${t('Move up')}" ${i === 0 ? 'disabled' : ''}>${ic('up', 16)}</button><span class="cell-sub" title="${t('Drag to reorder')}">${ic('grip', 16)}</span><button class="btn-icon" data-move="1" data-i="${i}" aria-label="${t('Move down')}" ${i === list.length - 1 ? 'disabled' : ''}>${ic('down', 16)}</button></div>
      <div style="min-width:0"><b>${r.name || t(TL[r.trigger] || r.trigger)}</b>${r.source === 'kit' ? pill(t('kit'), 'muted') : ''}
        <div class="cell-sub ellipsis">${r.ai ? '✨ ' + t('AI reply') + ' — ' + (r.ai_prompt || '').slice(0, 80) : summarize(r.reply)}</div>
        <div class="rule-meta">${pill(t(TL[r.trigger] || r.trigger), 'info', false)}${(r.keywords || []).slice(0, 4).map((k) => html`<span class="tag">${k}</span>`)}
          <span class="pill">${r.session ? sessionName(sessions.find((s) => s.name === r.session) || { name: r.session }) : t('All numbers')}</span>
          ${r.only_dm ? html`<span class="pill">${t('Only private chats')}</span>` : ''}<span class="pill">${t('{n} replies sent', { n: fmtNum(r.hits || 0) })}</span></div></div>
      <div class="row" style="gap:6px;flex-wrap:nowrap">${raw(`<label class="switch" title="${t('Active')}"><input type="checkbox" data-active="${r.id}" ${r.active ? 'checked' : ''} aria-label="${t('Active')}"><span class="track"></span></label>`)}
        <button class="btn-icon" data-edit="${r.id}" aria-label="${t('Edit')}">${ic('edit', 16)}</button><button class="btn-icon danger" data-del="${r.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button></div></div>`)}</div>`.s;
    bindDrag();
  }
  async function saveOrder() {
    const n = list.length;
    await Promise.all(list.map((r, i) => { const p = (n - i) * 10; if (r.priority === p) return null; r.priority = p; return api('PATCH', `/api/autoreplies/${r.id}`, { priority: p }, { silent: true }); }));
    toast(t('Order saved'));
  }
  function bindDrag() {
    let dragId = null;
    const wrap = root.querySelector('[data-rules]');
    wrap.addEventListener('dragstart', (e) => { const c = e.target.closest('.rule-card'); if (!c) return; dragId = c.dataset.id; c.classList.add('dragging'); e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', dragId); });
    wrap.addEventListener('dragend', () => { $$('.rule-card', wrap).forEach((c) => c.classList.remove('dragging', 'drop-before')); });
    wrap.addEventListener('dragover', (e) => { e.preventDefault(); const c = e.target.closest('.rule-card'); $$('.rule-card', wrap).forEach((x) => x.classList.toggle('drop-before', x === c && x.dataset.id !== dragId)); });
    wrap.addEventListener('drop', (e) => {
      e.preventDefault(); const c = e.target.closest('.rule-card'); if (!c || !dragId) return;
      const from = list.findIndex((r) => String(r.id) === dragId); const [it] = list.splice(from, 1);
      const to = list.findIndex((r) => String(r.id) === c.dataset.id); list.splice(to < 0 ? list.length : to, 0, it);
      draw(); saveOrder();
    });
  }

  root.addEventListener('click', async (e) => {
    if (e.target.closest('[data-a="new"]')) return editor(null);
    const mv = e.target.closest('[data-move]');
    if (mv) { const i = +mv.dataset.i, j = i + +mv.dataset.move; [list[i], list[j]] = [list[j], list[i]]; draw(); saveOrder(); root.querySelector(`[data-move="${mv.dataset.move}"][data-i="${j}"]`)?.focus(); return; }
    const ed = e.target.closest('[data-edit]'); if (ed) return editor(list.find((r) => String(r.id) === ed.dataset.edit));
    const d = e.target.closest('[data-del]');
    if (d && await confirmBox(t('Delete this auto-reply rule?'), { okText: t('Delete'), danger: true })) { if ((await api('DELETE', `/api/autoreplies/${d.dataset.del}`)).ok) { toast(t('Rule deleted')); load(); } }
  });
  root.addEventListener('change', async (e) => {
    const a = e.target.closest('[data-active]'); if (!a) return;
    const j = await api('PATCH', `/api/autoreplies/${a.dataset.active}`, { active: a.checked });
    if (j.ok) toast(a.checked ? t('Rule turned on') : t('Rule turned off')); else a.checked = !a.checked;
  });

  function editor(r) {
    const isNew = !r;
    r = r || { name: '', session: '', trigger: 'keyword', keywords: [], reply: { text: '' }, ai: false, ai_prompt: '', only_dm: true, cooldown_min: 60, hours: { from: 9, to: 21, days: [1, 2, 3, 4, 5, 6] }, active: true, priority: (list[0]?.priority || 0) + 10 };
    const h = r.hours || { from: 9, to: 21, days: [1, 2, 3, 4, 5, 6] };
    const hourOpts = (sel) => Array.from({ length: 25 }, (_, i) => html`<option value="${i}" ${i === sel ? 'selected' : ''}>${String(i).padStart(2, '0')}:00</option>`);
    const m = modal({ title: isNew ? t('New auto-reply') : t('Edit auto-reply'), size: 'xl', body: html`<form class="stack" style="gap:22px" data-f>
      <div class="form-grid g3">
        <label class="field"><span>${t('Rule name')}</span><input name="name" value="${r.name}" placeholder="${t('Price question')}"></label>
        <label class="field"><span>${t('Number')}</span><select name="session">${sessionOptions(sessions, r.session, { all: true })}</select></label>
        <label class="field"><span>${t('Wait before replying again to the same person')}</span><div class="row" style="flex-wrap:nowrap"><input name="cooldown_min" type="number" min="0" value="${r.cooldown_min ?? 60}" style="width:110px"><span class="muted">${t('minutes')}</span></div></label></div>
      <fieldset><legend>${t('When to reply')}</legend><div class="choice-grid">${TRIGGERS.map(([k, i, l, d]) => html`<label class="choice"><input type="radio" name="trigger" value="${k}" ${r.trigger === k ? 'checked' : ''}><b>${ic(i, 16)}${t(l)}</b><small>${t(d)}</small></label>`)}</div></fieldset>
      <div data-kw class="field"><span class="field-label" data-kw-label></span><div data-chips></div><small data-kw-hint></small></div>
      <fieldset data-hours><legend>${t('Business hours')}</legend><p class="hint" style="margin:-6px 0 10px">${t('The away reply is sent outside these hours.')}</p>
        <div class="hour-pick"><span>${t('Open from')}</span><select name="h_from" aria-label="${t('Open from')}">${hourOpts(h.from)}</select><span>${t('to')}</span><select name="h_to" aria-label="${t('Closes at')}">${hourOpts(h.to)}</select></div>
        <div class="days-pick mt-s">${DAYS.map((d, i) => html`<label><input type="checkbox" name="h_days" value="${i}" ${(h.days || []).includes(i) ? 'checked' : ''}><span>${t(d)}</span></label>`)}</div></fieldset>
      <div class="row" style="gap:28px">${toggle('only_dm', r.only_dm, t('Only private chats'), t('Do not reply inside groups.'))}${toggle('active', r.active, t('Active'))}${toggle('ai', r.ai, t('Reply with AI'), t('Uses your AI assistant from Settings.'))}</div>
      <fieldset data-ai-box><legend>${t('AI instructions')}</legend>
        <textarea name="ai_prompt" rows="6" placeholder="${t('You are the helpful assistant of Sharma Electronics in Korba. Answer in the customer’s language, in 2–3 short sentences. Shop hours 10am–9pm, closed Tuesday. For prices, ask them to visit or call 98765 43210.')}">${r.ai_prompt || ''}</textarea>
        <p class="hint">${t('Describe your business, tone, and what the assistant must never promise. Set up the AI provider in Settings → AI assistant.')}</p></fieldset>
      <fieldset data-reply-box><legend>${t('Reply')}</legend><div data-comp></div></fieldset></form>`,
    footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Save rule')}</button>` });
    const f = m.body.querySelector('form');
    const chips = chipInput(m.body.querySelector('[data-chips]'), { values: r.keywords || [], placeholder: t('Type and press Enter') });
    const comp = createComposer(m.body.querySelector('[data-comp]'), { message: r.reply, compact: true });
    function sync() {
      const tr = f.querySelector('[name=trigger]:checked').value;
      const needsKw = ['keyword', 'contains', 'regex', 'menu'].includes(tr);
      m.body.querySelector('[data-kw]').hidden = !needsKw;
      m.body.querySelector('[data-kw-label]').textContent = tr === 'regex' ? t('Patterns') : tr === 'menu' ? t('Menu option ids') : t('Keywords');
      m.body.querySelector('[data-kw-hint]').textContent = tr === 'menu' ? t('Use the option id from your menu message, e.g. price.') : tr === 'regex' ? t('Go regular expressions, case-insensitive.') : t('Not case-sensitive. Add spelling variants, e.g. price, rate, kitna.');
      m.body.querySelector('[data-hours]').hidden = tr !== 'away';
      const ai = f.ai.checked;
      m.body.querySelector('[data-ai-box]').hidden = !ai;
      m.body.querySelector('[data-reply-box]').hidden = ai;
    }
    f.addEventListener('change', sync); sync();
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const tr = f.querySelector('[name=trigger]:checked').value;
      const kws = chips.get();
      if (['keyword', 'contains', 'regex', 'menu'].includes(tr) && !kws.length) return toast(t('Add at least one keyword.'), 'warn');
      const ai = f.ai.checked;
      if (ai && !f.ai_prompt.value.trim()) return toast(t('Write instructions for the AI.'), 'warn');
      if (!ai) { const err = comp.validate(); if (err) return toast(err, 'warn'); }
      const body = {
        name: f.name.value.trim() || t(TL[tr]), session: f.session.value, trigger: tr, keywords: kws,
        reply: ai ? (r.reply || { text: '' }) : comp.get(), ai, ai_prompt: f.ai_prompt.value,
        only_dm: f.only_dm.checked, cooldown_min: Number(f.cooldown_min.value) || 0, active: f.active.checked, priority: r.priority,
        hours: { from: +f.h_from.value, to: +f.h_to.value, days: $$('[name=h_days]:checked', f).map((x) => +x.value) },
      };
      const j = isNew ? await api('POST', '/api/autoreplies', body) : await api('PATCH', `/api/autoreplies/${r.id}`, body);
      if (j.ok) { toast(t('Rule saved')); m.close(); load(); }
    });
  }
  await load();
}
