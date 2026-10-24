// Business Kit: pick a business type, apply its ready-made auto-replies,
// follow-up sequences, renewal reminders, checklists and enquiry forms;
// then run the day from here (tasks, renewals, follow-ups, form entries).
import { api, html, raw, ic, t, toast, modal, confirmBox, promptBox, copy, skel, empty, pill, fmtDate, fmtNum, relTime, getSessions, sessionOptions, debounce, parsePhones } from '/static/app/core.js';
import { listsPicker } from '/static/app/lists.js';

const VIEWS = [['today', 'Today', 'check'], ['sequences', 'Follow-ups', 'repeat'], ['renewals', 'Renewals', 'bell'], ['forms', 'Forms', 'form'], ['setup', 'Business type', 'briefcase']];
const ACTION = { auto_whatsapp: ['bot', 'Automatic WhatsApp'], whatsapp: ['send', 'WhatsApp (by you)'], call: ['call', 'Call'], task: ['check', 'Task'] };
const FREQ = { yearly: 'Yearly', half_yearly: 'Every 6 months', quarterly: 'Quarterly', monthly: 'Monthly', weekly: 'Weekly', once: 'Once' };
const TRIGGER = { lead_new: 'New lead (form or manual)', form: 'A form is filled', tag: 'Tag added', stage: 'Stage reached', manual: 'Only when I add someone' };

export async function render(ctx) {
  const { root, me } = ctx;
  ctx.setTitle(t('Business Kit'));
  const canAuto = me.can?.automation !== false;
  const canSend = me.can?.send !== false;
  let view = new URL(location.href).searchParams.get('view') || 'today';
  let biz = null;
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Business Kit')}</h1><p data-sub>${t('Ready-made WhatsApp setup for your kind of business: answers, follow-ups, reminders and forms.')}</p></div>
      <div class="page-actions" data-actions></div></div>
    <div class="row mb"><div class="seg" role="tablist" data-tabs>${VIEWS.map(([k, l, i]) => html`<button role="tab" data-v="${k}" aria-selected="${k === view}">${ic(i, 15)}${t(l)}</button>`)}</div></div>
    <section data-view>${skel(4)}</section></div>`.s;
  const sub = root.querySelector('[data-sub]'), actions = root.querySelector('[data-actions]'), box = root.querySelector('[data-view]');
  root.querySelector('[data-tabs]').addEventListener('click', (e) => {
    const b = e.target.closest('[data-v]'); if (!b) return;
    view = b.dataset.v;
    root.querySelectorAll('[data-v]').forEach((x) => x.setAttribute('aria-selected', x === b));
    history.replaceState(null, '', '/app/kit?view=' + view);
    show();
  });

  async function loadBiz() {
    const j = await api('GET', '/api/business');
    if (!ctx.isCurrent() || !j.ok) return;
    biz = j;
    const k = j.kit;
    sub.textContent = k ? `${k.icon} ${k.name} — ${k.tagline}` : t('Ready-made WhatsApp setup for your kind of business: answers, follow-ups, reminders and forms.');
    actions.innerHTML = k ? html`<button class="btn" data-a="tips">${ic('info', 16)}${t('Tips & KPIs')}</button>` : '';
    if (!k && view !== 'setup') { view = 'setup'; root.querySelectorAll('[data-v]').forEach((x) => x.setAttribute('aria-selected', x.dataset.v === view)); }
  }
  actions.addEventListener('click', (e) => { if (e.target.closest('[data-a="tips"]')) tipsModal(); });

  async function show() {
    if (!biz) await loadBiz();
    if (!ctx.isCurrent()) return;
    box.innerHTML = skel(4).s;
    ({ today, sequences, renewals, forms, setup })[view]();
  }

  // ---------------------------------------------------------------- today
  async function today() {
    const [tk, rn, ru, fe] = await Promise.all([api('GET', '/api/tasks?view=today'), api('GET', '/api/renewals?status=open&due=week'), api('GET', '/api/sequences/runs?status=active'), api('GET', '/api/business/entries')]);
    if (!ctx.isCurrent()) return;
    const tasks = tk.tasks || [], rens = rn.renewals || [], runs = ru.runs || [], entries = (fe.entries || []).slice(0, 8);
    const c = biz.counts || {};
    box.innerHTML = html`<div class="grid g4 mb">
        ${kpi(t('Tasks today'), tasks.length, t('open, incl. overdue'))}${kpi(t('Renewals this week'), rens.length, t('due in 7 days'))}
        ${kpi(t('People in follow-up'), c.runs_active || 0, t('active sequences'))}${kpi(t('Auto-replies on'), c.autoreplies || 0, t('answering 24×7'))}</div>
      <div class="grid g2">
        <section class="panel"><div class="panel-head"><h2>${t('Tasks')}</h2>${canSend ? html`<button class="btn sm" data-a="task">${ic('plus', 14)}${t('Add task')}</button>` : ''}</div>
          ${tasks.length ? html`<ul class="list" data-tasks>${tasks.map(taskRow)}</ul>` : html`<div class="panel-body">${empty('check', t('Nothing due'), t('Checklist items from your kit, follow-up calls and renewal calls show up here on their day.'))}</div>`}</section>
        <section class="panel"><div class="panel-head"><h2>${t('Renewals due this week')}</h2><button class="btn sm ghost" data-v2="renewals">${t('All renewals')}</button></div>
          ${rens.length ? html`<ul class="list">${rens.map((r) => renewalRow(r, rn.today))}</ul>` : html`<div class="panel-body"><p class="muted">${t('No renewals due in the next 7 days.')}</p></div>`}</section>
        <section class="panel"><div class="panel-head"><h2>${t('New form entries')}</h2><button class="btn sm ghost" data-v2="forms">${t('Forms')}</button></div>
          ${entries.length ? html`<ul class="list">${entries.map((e) => html`<li><span class="list-ico">${ic('inbox')}</span><span class="li-main"><b>${e.name || e.phone}</b><small>${e.form} · ${Object.values(e.answers || {}).slice(0, 3).join(' · ')} · ${relTime(e.created_at)}</small></span>
            <a class="btn sm" href="https://wa.me/${e.phone}" target="_blank" rel="noopener">${ic('send', 14)}${t('Chat')}</a></li>`)}</ul>` : html`<div class="panel-body"><p class="muted">${t('Share your form links (Forms tab) on your website, QR code or WhatsApp. Entries land here and start the follow-up automatically.')}</p></div>`}</section>
        <section class="panel"><div class="panel-head"><h2>${t('In follow-up')}</h2><button class="btn sm ghost" data-v2="sequences">${t('Sequences')}</button></div>
          ${runs.length ? html`<ul class="list">${runs.slice(0, 8).map(runRow)}</ul>` : html`<div class="panel-body"><p class="muted">${t('Add people to a follow-up sequence from the Follow-ups tab, a form, or Contacts.')}</p></div>`}</section>
      </div>`.s;
  }
  const kpi = (label, val, s) => html`<div class="panel kpi"><span class="k-label">${label}</span><span class="k-val">${fmtNum(val)}</span><span class="k-sub">${s}</span></div>`;
  function taskRow(x) {
    const over = x.due < Math.floor(Date.now() / 1000) - 86400 && !x.done_at;
    return html`<li data-task="${x.id}"><label class="check" style="margin:0"><input type="checkbox" data-done="${x.id}" ${x.done_at ? 'checked' : ''} aria-label="${t('Done')}"><span></span></label>
      <span class="li-main"><b>${x.title}</b><small>${x.repeat ? t(x.repeat) + ' · ' : ''}${x.at_time || fmtDate(x.due, false)}${over ? ' · ' + t('overdue') : ''}${x.note ? ' · ' + x.note.split('\n')[0] : ''}</small></span>
      ${x.repeat ? pill(t('repeats'), 'muted') : ''}${x.phone ? html`<a class="btn-icon" href="https://wa.me/${x.phone}" target="_blank" rel="noopener" aria-label="${t('Chat')}">${ic('send', 16)}</a>` : ''}
      ${canSend ? html`<button class="btn-icon danger" data-tdel="${x.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button>` : ''}</li>`;
  }
  function renewalRow(r, todayTs) {
    const days = Math.round((r.due - todayTs) / 86400);
    const when = days < 0 ? t('{n} days overdue', { n: -days }) : days === 0 ? t('today') : t('in {n} days', { n: days });
    return html`<li><span class="list-ico">${r.icon || '🔔'}</span><span class="li-main"><b>${r.name || r.phone} — ${r.title}</b><small>${r.type} · ${fmtDate(r.due, false)} (${when})${r.amount ? ' · ₹' + r.amount : ''}${r.reminded?.length ? ' · ' + t('{n} reminders sent', { n: r.reminded.length }) : ''}</small></span>
      ${pill(days < 0 ? t('overdue') : t(r.status), days < 0 ? 'bad' : r.status === 'open' ? 'ok' : 'muted')}
      ${canSend ? html`<button class="btn sm" data-rdone="${r.id}">${t('Done')}</button><button class="btn-icon" data-redit="${r.id}" aria-label="${t('Edit')}">${ic('edit', 16)}</button>` : ''}</li>`;
  }
  function runRow(r) {
    return html`<li><span class="avatar">${(r.name || r.phone).slice(0, 1).toUpperCase()}</span><span class="li-main"><b>${r.name || r.phone}</b><small>${r.sequence} · ${t('step {a} of {b}', { a: r.step + 1, b: r.steps })} · ${r.status === 'active' ? t('next {t}', { t: relTime(r.next_at) }) : t(r.status)}</small></span>
      ${pill(t(r.status), r.status === 'active' ? 'ok' : 'muted')}${r.status === 'active' && canSend ? html`<button class="btn-icon danger" data-stop="${r.id}" aria-label="${t('Stop')}">${ic('x', 16)}</button>` : ''}</li>`;
  }

  // ---------------------------------------------------------------- sequences
  async function sequences() {
    const [sq, ru, sess] = await Promise.all([api('GET', '/api/sequences'), api('GET', '/api/sequences/runs?status=all'), getSessions()]);
    if (!ctx.isCurrent()) return;
    const seqs = sq.sequences || [], runs = ru.runs || [];
    box.innerHTML = html`<div class="page-actions mb">${canAuto ? html`<button class="btn primary" data-a="newseq">${ic('plus', 16)}${t('New sequence')}</button>` : ''}</div>
      ${seqs.length ? html`<div class="grid g2">${seqs.map((s) => html`<section class="panel" data-seq="${s.id}"><div class="panel-head"><h2>${s.name}</h2>${pill(s.active ? t('on') : t('off'), s.active ? 'ok' : 'muted')}</div>
        <div class="panel-body"><p class="muted">${t(TRIGGER[s.trigger] || s.trigger)}${s.trigger_value ? ': ' + s.trigger_value : ''} · ${t('{n} steps', { n: s.steps.length })} · ${s.stop_on_reply ? t('stops when they reply') : t('continues after replies')}${s.business_days ? ' · ' + t('skips Sundays') : ''}${s.session ? ' · ' + s.session : ''}</p>
          <ol class="steps-ol">${s.steps.map((st) => html`<li>${ic(ACTION[st.action]?.[0] || 'check', 14)} <b>${t('Day {n}', { n: st.day })}</b> — ${st.title || t(ACTION[st.action]?.[1] || st.action)}${st.action === 'auto_whatsapp' ? html`<small class="muted" style="display:block">${st.message}</small>` : ''}</li>`)}</ol></div>
        <div class="panel-foot btn-row">${canSend ? html`<button class="btn sm primary" data-enrol="${s.id}">${ic('users', 14)}${t('Add people')}</button>` : ''}<span class="muted">${t('{n} in progress', { n: s.runs })}</span>
          ${canAuto ? html`<button class="btn sm" data-eseq="${s.id}">${ic('edit', 14)}${t('Edit')}</button><button class="btn-icon danger" data-dseq="${s.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button>` : ''}</div></section>`)}</div>`
      : empty('repeat', t('No follow-up sequences yet'), t('Apply a Business Kit (Business type tab) or create one: day 0 welcome, day 1 call, day 7 reminder…'))}
      <section class="panel mt"><div class="panel-head"><h2>${t('People in sequences')}</h2><span class="muted">${t('{n} total', { n: runs.length })}</span></div>
        ${runs.length ? html`<ul class="list">${runs.slice(0, 200).map(runRow)}</ul>` : html`<div class="panel-body"><p class="muted">${t('Nobody yet.')}</p></div>`}</section>`.s;
    box._seqs = seqs; box._sess = sess;
  }
  function seqModal(s) {
    const isNew = !s;
    s = s || { name: '', trigger: 'manual', trigger_value: '', business_days: true, stop_on_reply: true, session: '', active: true, steps: [{ day: 0, action: 'auto_whatsapp', title: t('Welcome message'), message: 'नमस्ते {{name}} जी 🙏 {{business}} में आपकी enquiry मिल गई। हम जल्दी ही बात करेंगे।' }] };
    const steps = s.steps.map((x) => ({ ...x }));
    const m = modal({ title: isNew ? t('New sequence') : s.name, size: 'lg', body: '', footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Save')}</button>` });
    function draw() {
      m.body.innerHTML = html`<form class="stack" data-f>
        <div class="form-grid g2"><label class="field"><span>${t('Name')}</span><input name="name" value="${s.name}" required placeholder="${t('New enquiry → quote')}"></label>
          <label class="field"><span>${t('Starts when')}</span><select name="trigger">${Object.entries(TRIGGER).map(([k, l]) => html`<option value="${k}" ${k === s.trigger ? 'selected' : ''}>${t(l)}</option>`)}</select></label>
          <label class="field"><span>${t('Trigger value')} <small>(${t('form key, tag or stage; blank = any')})</small></span><input name="trigger_value" value="${s.trigger_value || ''}"></label>
          <label class="field"><span>${t('Send from')}</span><select name="session">${raw(sessionOptions(box._sess || [], s.session, { all: true, allLabel: t('Any linked number (smart pick)') }))}</select></label></div>
        <div class="row" style="gap:18px;flex-wrap:wrap">${raw(`<label class="switch"><input type="checkbox" name="stop_on_reply" ${s.stop_on_reply ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${t('Stop when they reply')}</span></span></label>`)}
          ${raw(`<label class="switch"><input type="checkbox" name="business_days" ${s.business_days ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${t('Skip Sundays')}</span></span></label>`)}
          ${raw(`<label class="switch"><input type="checkbox" name="active" ${s.active ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${t('Active')}</span></span></label>`)}</div>
        <h3 class="mt">${t('Steps')}</h3>
        <div class="stack" data-steps>${steps.map((st, i) => html`<div class="panel panel-pad" data-step="${i}"><div class="form-grid g3">
            <label class="field"><span>${t('Day')}</span><input type="number" min="0" max="365" data-k="day" value="${st.day}"></label>
            <label class="field"><span>${t('What')}</span><select data-k="action">${Object.entries(ACTION).map(([k, [, l]]) => html`<option value="${k}" ${k === st.action ? 'selected' : ''}>${t(l)}</option>`)}</select></label>
            <label class="field"><span>${t('Title')}</span><input data-k="title" value="${st.title || ''}"></label></div>
          <label class="field"><span>${st.action === 'auto_whatsapp' ? t('Message (sent automatically)') : t('Message / note for staff')} <small>{{name}} {{business}} {{phone}}</small></span><textarea data-k="message" rows="2">${st.message || ''}</textarea></label>
          <div class="btn-row"><button type="button" class="btn sm ghost" data-up="${i}" ${i === 0 ? 'disabled' : ''}>${ic('up', 14)}</button><button type="button" class="btn sm ghost" data-down="${i}" ${i === steps.length - 1 ? 'disabled' : ''}>${ic('down', 14)}</button><button type="button" class="btn-icon danger" data-rm="${i}" aria-label="${t('Remove step')}">${ic('trash', 16)}</button></div></div>`)}</div>
        <button type="button" class="btn" data-add>${ic('plus', 14)}${t('Add step')}</button>
        <p class="hint">${t('Automatic messages go through your normal queue — pacing, turns and daily limits apply. Other kinds become tasks for you or your staff on that day.')}</p></form>`.s;
    }
    function collect() {
      const f = m.body.querySelector('form');
      m.body.querySelectorAll('[data-step]').forEach((el, i) => { el.querySelectorAll('[data-k]').forEach((inp) => { steps[i][inp.dataset.k] = inp.dataset.k === 'day' ? +inp.value : inp.value; }); });
      Object.assign(s, { name: f.name.value.trim(), trigger: f.trigger.value, trigger_value: f.trigger_value.value.trim(), session: f.session.value, stop_on_reply: f.stop_on_reply.checked, business_days: f.business_days.checked, active: f.active.checked, steps });
    }
    draw();
    m.body.addEventListener('click', (e) => {
      const add = e.target.closest('[data-add]'), rm = e.target.closest('[data-rm]'), up = e.target.closest('[data-up]'), dn = e.target.closest('[data-down]');
      if (!add && !rm && !up && !dn) return;
      collect();
      if (add) steps.push({ day: (steps.at(-1)?.day || 0) + 1, action: 'call', title: '', message: '' });
      if (rm) steps.splice(+rm.dataset.rm, 1);
      if (up) { const i = +up.dataset.up; [steps[i - 1], steps[i]] = [steps[i], steps[i - 1]]; }
      if (dn) { const i = +dn.dataset.down; [steps[i + 1], steps[i]] = [steps[i], steps[i + 1]]; }
      draw();
    });
    m.body.addEventListener('change', (e) => { if (e.target.dataset.k === 'action') { collect(); draw(); } });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      collect();
      if (!s.name) return toast(t('Give the sequence a name'), 'warn');
      const j = await api(isNew ? 'POST' : 'PATCH', isNew ? '/api/sequences' : '/api/sequences/' + s.id, s);
      if (j.ok) { m.close(); toast(t('Saved')); show(); }
    });
  }
  function enrolModal(seq) {
    const lists = new Set();
    const m = modal({ title: t('Add people to “{n}”', { n: seq.name }), body: html`<div class="stack">
        <label class="field"><span>${t('Phone numbers')} <small>(${t('one per line or comma separated')})</small></span><textarea name="phones" rows="4" placeholder="98xxxxxxxx"></textarea></label>
        <div><span class="field-label">${t('Or a saved list')}</span><div data-lists></div></div>
        <p class="hint">${t('Each person starts at step 1 now. Someone already in this sequence is not added twice.')}</p></div>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Start follow-up')}</button>` });
    listsPicker(m.body.querySelector('[data-lists]'), { selected: lists });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const phones = parsePhones(m.body.querySelector('[name=phones]').value);
      let started = 0, people = 0;
      for (const lid of lists.size ? [...lists] : [0]) {
        const j = await api('POST', `/api/sequences/${seq.id}/enroll`, { phones: lid === [...lists][0] || !lists.size ? phones : [], list_id: lid || undefined });
        if (!j.ok) return;
        started += j.started; people += j.people;
      }
      m.close(); toast(t('{a} of {b} people added to the follow-up', { a: started, b: people })); show();
    });
  }

  // ---------------------------------------------------------------- renewals
  let rFilter = { status: 'open', due: '', q: '' };
  async function renewals() {
    const [ty, rn] = await Promise.all([api('GET', '/api/renewal-types'), api('GET', '/api/renewals?' + new URLSearchParams(Object.fromEntries(Object.entries(rFilter).filter(([, v]) => v))))]);
    if (!ctx.isCurrent()) return;
    const types = ty.types || [], rens = rn.renewals || [];
    box._types = types;
    box.innerHTML = html`<div class="row mb" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
        <div class="seg" role="tablist">${[['open', 'Open'], ['done', 'Done'], ['lost', 'Lost'], ['all', 'All']].map(([k, l]) => html`<button role="tab" data-rs="${k}" aria-selected="${rFilter.status === k}">${t(l)}</button>`)}</div>
        <div class="seg" role="tablist">${[['', 'Any date'], ['overdue', 'Overdue'], ['week', 'This week'], ['month', 'This month']].map(([k, l]) => html`<button role="tab" data-rd="${k}" aria-selected="${rFilter.due === k}">${t(l)}</button>`)}</div>
        <input type="search" data-rq style="max-width:240px" value="${rFilter.q}" placeholder="${t('Search name, phone, title')}" aria-label="${t('Search')}">
        ${canSend ? html`<button class="btn primary" data-a="newren">${ic('plus', 16)}${t('Add renewal')}</button>` : ''}</div>
      <section class="panel">${rens.length ? html`<ul class="list">${rens.map((r) => renewalRow(r, rn.today))}</ul>` : html`<div class="panel-body">${empty('bell', t('No renewals here'), t('Add a policy, subscription, service or any dated thing. Reminders go out automatically on the days each type says.'))}</div>`}</section>
      <section class="panel mt"><div class="panel-head"><h2>${t('Renewal types')}</h2>${canAuto ? html`<button class="btn sm" data-a="newtype">${ic('plus', 14)}${t('New type')}</button>` : ''}</div>
        ${types.length ? html`<ul class="list">${types.map((x) => html`<li><span class="list-ico">${x.icon || '🔔'}</span><span class="li-main"><b>${x.label}</b><small>${t(FREQ[x.frequency] || x.frequency)} · ${t('remind {b} days before', { b: x.remind_before.join(', ') || '—' })}${x.remind_after.length ? ' · ' + t('{a} days after', { a: x.remind_after.join(', ') }) : ''}${x.staff_task ? ' · ' + t('task for staff') : ''} · ${t('{n} open', { n: x.open })}</small></span>
          ${canAuto ? html`<button class="btn sm" data-etype="${x.id}">${ic('edit', 14)}${t('Edit')}</button><button class="btn-icon danger" data-dtype="${x.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button>` : ''}</li>`)}</ul>` : html`<div class="panel-body"><p class="muted">${t('No renewal types yet — apply a kit or add one.')}</p></div>`}</section>`.s;
    box.querySelector('[data-rq]').addEventListener('input', debounce((e) => { rFilter.q = e.target.value.trim(); renewals(); }, 350));
  }
  function renewalModal(r) {
    const isNew = !r;
    const types = box._types || [];
    if (!types.length) return toast(t('Add a renewal type first'), 'warn');
    r = r || { type_id: types[0].id, phone: '', name: '', title: '', amount: '', due: Math.floor(Date.now() / 1000) + 30 * 86400, fields: {}, status: 'open', note: '' };
    const m = modal({ title: isNew ? t('Add renewal') : r.title, body: '', footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Save')}</button>` });
    const d = (ts) => new Date(ts * 1000).toISOString().slice(0, 10);
    function draw() {
      const ty = types.find((x) => x.id === +r.type_id) || types[0];
      m.body.innerHTML = html`<form class="stack">
        <div class="form-grid g2"><label class="field"><span>${t('Type')}</span><select name="type_id">${types.map((x) => html`<option value="${x.id}" ${x.id === +r.type_id ? 'selected' : ''}>${x.icon} ${x.label}</option>`)}</select></label>
          <label class="field"><span>${t('Due date')}</span><input type="date" name="due" value="${d(r.due)}" required></label>
          <label class="field"><span>${t('WhatsApp number')}</span><input name="phone" value="${r.phone}" required placeholder="98xxxxxxxx"></label>
          <label class="field"><span>${t('Name')}</span><input name="name" value="${r.name}"></label>
          <label class="field"><span>${t('Title')} <small>(${t('e.g. LIC Jeevan Anand, Car 1234')})</small></span><input name="title" value="${r.title}" placeholder="${ty.label}"></label>
          <label class="field"><span>${t('Amount ₹')}</span><input name="amount" value="${r.amount}" inputmode="decimal"></label>
          ${ty.fields.map((f) => html`<label class="field"><span>${f.label}</span>${f.type === 'select' ? html`<select data-fld="${f.id}"><option value="">—</option>${(f.options || []).map((o) => html`<option ${r.fields[f.id] === o ? 'selected' : ''}>${o}</option>`)}</select>` : html`<input data-fld="${f.id}" value="${r.fields[f.id] || ''}">`}</label>`)}
          ${isNew ? '' : html`<label class="field"><span>${t('Status')}</span><select name="status">${['open', 'done', 'lost'].map((s) => html`<option value="${s}" ${s === r.status ? 'selected' : ''}>${t(s)}</option>`)}</select></label>`}</div>
        <label class="field"><span>${t('Note')}</span><textarea name="note" rows="2">${r.note || ''}</textarea></label>
        <p class="hint">${t('Reminder goes {b} days before due', { b: ty.remind_before.join(', ') || '—' })}${ty.remind_after.length ? ', ' + t('and {a} days after if still open', { a: ty.remind_after.join(', ') }) : ''}. ${t('Marking a recurring one done creates the next period automatically.')}</p></form>`.s;
    }
    draw();
    m.body.addEventListener('change', (e) => { if (e.target.name === 'type_id') { r.type_id = +e.target.value; draw(); } });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form');
      const fields = {}; m.body.querySelectorAll('[data-fld]').forEach((x) => { if (x.value) fields[x.dataset.fld] = x.value; });
      const body = { type_id: +f.type_id.value, due: Math.floor(new Date(f.due.value + 'T00:00:00').getTime() / 1000), phone: f.phone.value, name: f.name.value.trim(), title: f.title.value.trim(), amount: f.amount.value.trim(), fields, note: f.note.value, status: f.status ? f.status.value : 'open' };
      const j = await api(isNew ? 'POST' : 'PATCH', isNew ? '/api/renewals' : '/api/renewals/' + r.id, body);
      if (j.ok) { m.close(); toast(j.next ? t('Saved — next one created for {d}', { d: fmtDate(j.next.due, false) }) : t('Saved')); show(); }
    });
  }
  function typeModal(x) {
    const isNew = !x;
    x = x || { label: '', icon: '🔔', frequency: 'yearly', remind_before: [7, 1], remind_after: [], staff_task: false, message: '{{name}} जी, आपका {{title}} {{due}} को renew होना है। ₹{{amount}} — कोई सवाल हो तो इसी नंबर पर पूछें 🙏', fields: [] };
    const m = modal({ title: isNew ? t('New renewal type') : x.label, body: html`<form class="stack">
        <div class="form-grid g2"><label class="field"><span>${t('Name')}</span><input name="label" value="${x.label}" required placeholder="${t('Car insurance')}"></label>
          <label class="field"><span>${t('Icon')}</span><input name="icon" value="${x.icon}" maxlength="4"></label>
          <label class="field"><span>${t('Repeats')}</span><select name="frequency">${Object.entries(FREQ).map(([k, l]) => html`<option value="${k}" ${k === x.frequency ? 'selected' : ''}>${t(l)}</option>`)}</select></label>
          <label class="field"><span>${t('Remind days before')} <small>(${t('comma separated')})</small></span><input name="before" value="${x.remind_before.join(', ')}" placeholder="30, 7, 1"></label>
          <label class="field"><span>${t('Remind days after (if still open)')}</span><input name="after" value="${x.remind_after.join(', ')}" placeholder="3, 10"></label></div>
        <label class="field"><span>${t('Reminder message')} <small>{{name}} {{title}} {{amount}} {{due}} {{business}}</small></span><textarea name="message" rows="3">${x.message}</textarea></label>
        ${raw(`<label class="switch"><input type="checkbox" name="staff_task" ${x.staff_task ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${t('Also make a task for staff on the first reminder day')}</span></span></label>`)}</form>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Save')}</button>` });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const f = m.body.querySelector('form');
      const nums = (s) => s.split(/[,\s]+/).map((v) => parseInt(v, 10)).filter((v) => v >= 0 && v <= 365);
      const body = { ...x, label: f.label.value.trim(), icon: f.icon.value.trim(), frequency: f.frequency.value, remind_before: nums(f.before.value), remind_after: nums(f.after.value), message: f.message.value, staff_task: f.staff_task.checked };
      const j = await api(isNew ? 'POST' : 'PATCH', isNew ? '/api/renewal-types' : '/api/renewal-types/' + x.id, body);
      if (j.ok) { m.close(); toast(t('Saved')); show(); }
    });
  }

  // ---------------------------------------------------------------- forms
  async function forms() {
    const b = biz.business;
    const fe = await api('GET', '/api/business/entries');
    if (!ctx.isCurrent()) return;
    const entries = fe.entries || [];
    box.innerHTML = html`${b.forms.length ? html`<div class="grid g2">${b.forms.map((f) => html`<section class="panel"><div class="panel-head"><h2>${f.title}</h2>${pill(t('{n} fields', { n: f.fields.length + 2 }), 'muted')}</div>
        <div class="panel-body"><p class="muted">${f.description}</p>
          <div class="input-group"><input type="text" readonly value="${biz.form_base}${f.key}"><button class="btn" data-copy="${biz.form_base}${f.key}">${ic('copy', 14)}${t('Copy')}</button><a class="btn" href="${biz.form_base}${f.key}" target="_blank" rel="noopener">${ic('eye', 14)}${t('Open')}</a></div>
          <p class="hint mt-s">${t('Fields: name, WhatsApp number, {f}', { f: f.fields.map((x) => x.label).join(', ') })}</p></div></section>`)}</div>`
      : empty('form', t('No forms yet'), t('Apply a Business Kit — every kit comes with ready enquiry forms you can link from your website, Instagram bio or a QR code.'))}
      <section class="panel mt"><div class="panel-head"><h2>${t('Entries')}</h2><span class="muted">${t('{n} total', { n: entries.length })}</span></div>
        ${entries.length ? html`<div class="table-wrap"><table><thead><tr><th>${t('When')}</th><th>${t('Form')}</th><th>${t('Name')}</th><th>${t('Phone')}</th><th>${t('Answers')}</th><th></th></tr></thead><tbody>
          ${entries.map((e) => html`<tr><td class="nowrap">${fmtDate(e.created_at)}</td><td>${e.form}</td><td>${e.name}</td><td class="tnum">${e.phone}</td><td><small>${Object.entries(e.answers || {}).map(([k, v]) => k + ': ' + v).join(' · ')}</small></td>
            <td><a class="btn sm" href="https://wa.me/${e.phone}" target="_blank" rel="noopener">${ic('send', 14)}${t('Chat')}</a></td></tr>`)}</tbody></table></div>` : html`<div class="panel-body"><p class="muted">${t('When someone fills a form: a contact is saved (tagged with the form), a task appears in Today, the “new lead” follow-up starts and your webhook gets form.entry.')}</p></div>`}</section>
      <p class="hint mt">${t('Website → WhatsApp: put these links behind your “Enquire on WhatsApp” buttons. For a deeper integration (your own forms posting to the gateway) see Developer → Webhooks.')}</p>`.s;
  }

  // ---------------------------------------------------------------- setup (choose / change kit)
  async function setup() {
    const [cat, sess] = await Promise.all([api('GET', '/api/kits'), getSessions()]);
    if (!ctx.isCurrent()) return;
    const b = biz.business;
    let kitId = b.kit || '', subs = new Set(b.subs || []), q = '';
    const kitsAll = cat.kits || [], groups = cat.groups || [];
    box.innerHTML = html`<div class="split"><div>
      <section class="panel"><div class="panel-head"><h2>${t('1. Your business')}</h2><input type="search" data-q style="max-width:260px" placeholder="${t('Search: clinic, school, insurance…')}" aria-label="${t('Search')}"></div><div class="panel-body" data-kits></div></section>
      <section class="panel mt" data-subs hidden></section></div>
      <div class="stack"><section class="panel"><div class="panel-head"><h2>${t('2. Details')}</h2></div><div class="panel-body"><form class="stack" data-f>
        <label class="field"><span>${t('Business name')} <small>{{business}}</small></span><input name="name" value="${b.name}" placeholder="${t('Sharma Insurance')}"></label>
        <label class="field"><span>${t('WhatsApp number customers write to')}</span><input name="phone" value="${b.phone}" placeholder="98xxxxxxxx"></label>
        <label class="field"><span>${t('City')}</span><input name="city" value="${b.city}"></label>
        <label class="field"><span>${t('Auto-replies and sequences send from')}</span><select name="session">${raw(sessionOptions(sess, '', { all: true, allLabel: t('All my numbers') }))}</select></label></form></div></section>
      <section class="panel"><div class="panel-head"><h2>${t('3. Apply')}</h2></div><div class="panel-body" data-summary></div>
        <div class="panel-foot">${canAuto ? html`<button class="btn primary" data-a="apply" ${kitId ? '' : 'disabled'}>${ic('sparkle', 16)}${b.kit ? t('Re-apply kit') : t('Apply kit')}</button>` : html`<p class="muted">${t('Only a manager or admin can apply a kit.')}</p>`}</div></section></div></div>`.s;
    const kitsBox = box.querySelector('[data-kits]'), subsBox = box.querySelector('[data-subs]'), summary = box.querySelector('[data-summary]'), applyBtn = box.querySelector('[data-a="apply"]');
    function drawKits() {
      const ql = q.toLowerCase();
      const vis = kitsAll.filter((k) => !ql || (k.name + ' ' + k.tagline + ' ' + k.subs.map((s) => s.label + ' ' + s.hint).join(' ')).toLowerCase().includes(ql));
      kitsBox.innerHTML = groups.map((g) => {
        const ks = vis.filter((k) => k.group === g.id);
        return ks.length ? html`<h3 class="mt-s">${g.label}</h3><div class="choice-grid mb">${ks.map((k) => html`<label class="choice"><input type="radio" name="kit" value="${k.id}" ${k.id === kitId ? 'checked' : ''}><b><span>${k.icon}</span>${k.name}</b><small>${k.tagline}</small>
          <small>${t('{a} answers · {b} follow-ups · {c} reminders · {d} forms', { a: k.counts.bot, b: k.counts.plans, c: k.counts.renewals, d: k.counts.forms })}</small></label>`)}</div>`.s : '';
      }).join('') || html`<p class="muted">${t('Nothing matches. Pick the closest type — everything it sets up can be edited.')}</p>`.s;
    }
    function drawSubs() {
      const k = kitsAll.find((x) => x.id === kitId);
      subsBox.hidden = !k || !k.subs.length;
      if (subsBox.hidden) return;
      subsBox.innerHTML = html`<div class="panel-head"><h2>${t('What exactly do you do?')}</h2><span class="muted">${t('pick all that apply')}</span></div><div class="panel-body"><div class="choice-grid">${k.subs.map((s) => html`<label class="choice"><input type="checkbox" name="sub" value="${s.id}" ${subs.has(s.id) ? 'checked' : ''}><b><span>${s.icon}</span>${s.label}</b><small>${s.hint}</small></label>`)}</div></div>`.s;
    }
    function drawSummary() {
      const k = kitsAll.find((x) => x.id === kitId);
      if (!k) { summary.innerHTML = html`<p class="muted">${t('Choose a business type on the left.')}</p>`.s; return; }
      summary.innerHTML = html`<p><b>${k.icon} ${k.name}</b>${subs.size ? html` · ${[...subs].map((id) => k.subs.find((s) => s.id === id)?.label || id).join(', ')}` : ''}</p>
        <ul class="tips"><li>${t('{n} auto-replies: a welcome menu with the questions customers ask, one answer each (works on “menu”, “hi”, “help” too)', { n: k.counts.bot + 2 })}</li>
          <li>${t('{n} follow-up sequences (new lead, documents, after-sale …)', { n: k.counts.plans })}</li><li>${t('{n} renewal / reminder types with messages', { n: k.counts.renewals })}</li>
          <li>${t('{n} daily / weekly checklist tasks', { n: k.counts.checklists })}</li><li>${t('{n} enquiry forms with public links', { n: k.counts.forms })}</li></ul>
        <p class="hint">${b.kit ? t('Re-applying refreshes what the kit made earlier (auto-replies, sequences, renewal types, undone checklist tasks). Things you made yourself stay.') : t('Everything can be edited afterwards in Auto-replies, Follow-ups, Renewals and Today.')}</p>`.s;
      if (applyBtn) applyBtn.disabled = false;
    }
    drawKits(); drawSubs(); drawSummary();
    box.querySelector('[data-q]').addEventListener('input', debounce((e) => { q = e.target.value; drawKits(); }, 200));
    box.addEventListener('change', (e) => {
      if (e.target.name === 'kit') { kitId = e.target.value; subs = new Set(kitId === b.kit ? b.subs : []); drawSubs(); drawSummary(); }
      if (e.target.name === 'sub') { e.target.checked ? subs.add(e.target.value) : subs.delete(e.target.value); drawSummary(); }
    });
    applyBtn?.addEventListener('click', async () => {
      const f = box.querySelector('[data-f]');
      if (!f.name.value.trim() && !await confirmBox(t('No business name — messages will say {{business}} as your account name. Continue?'))) return;
      applyBtn.disabled = true;
      const j = await api('POST', '/api/business/apply', { kit: kitId, subs: [...subs], name: f.name.value.trim(), phone: f.phone.value.trim(), city: f.city.value.trim(), session: f.session.value });
      applyBtn.disabled = false;
      if (!j.ok) return;
      const c = j.created;
      toast(t('Kit applied: {a} auto-replies, {b} sequences, {c} renewal types, {d} tasks', { a: c.autoreplies, b: c.sequences, c: c.renewal_types, d: c.tasks }), 'ok', 7000);
      biz = null; view = 'today';
      root.querySelectorAll('[data-v]').forEach((x) => x.setAttribute('aria-selected', x.dataset.v === view));
      show();
    });
  }

  function tipsModal() {
    const k = biz.kit;
    modal({ title: `${k.icon} ${k.name}`, body: html`<div class="stack"><h3>${t('Tips')}</h3><ul class="tips">${(k.tips || []).map((x) => html`<li>${x}</li>`)}</ul>
      <h3>${t('Numbers to watch')}</h3><div class="row" style="flex-wrap:wrap;gap:8px">${(k.kpis || []).map((x) => pill(x, 'muted'))}</div>
      <p class="hint">${t('Keep the business profile up to date in the Business type tab; the name is used in every automatic message.')}</p></div>`, footer: html`<button class="btn primary" data-close>${t('Close')}</button>` });
  }

  // ---------------------------------------------------------------- actions
  box.addEventListener('click', async (e) => {
    const v2 = e.target.closest('[data-v2]'); if (v2) { root.querySelector(`[data-v="${v2.dataset.v2}"]`).click(); return; }
    const cp = e.target.closest('[data-copy]'); if (cp) return copy(cp.dataset.copy, t('Link'));
    if (e.target.closest('[data-a="task"]')) {
      const title = await promptBox(t('New task'), { label: t('What needs doing?'), placeholder: t('Call Ravi about the quote') });
      if (title && (await api('POST', '/api/tasks', { title, due: Math.floor(Date.now() / 1000) })).ok) { toast(t('Added')); show(); }
      return;
    }
    const tdel = e.target.closest('[data-tdel]'); if (tdel && (await api('DELETE', '/api/tasks/' + tdel.dataset.tdel)).ok) return show();
    if (e.target.closest('[data-a="newseq"]')) return seqModal();
    const es = e.target.closest('[data-eseq]'); if (es) return seqModal(box._seqs.find((s) => s.id === +es.dataset.eseq));
    const en = e.target.closest('[data-enrol]'); if (en) return enrolModal(box._seqs.find((s) => s.id === +en.dataset.enrol));
    const ds = e.target.closest('[data-dseq]');
    if (ds && await confirmBox(t('Delete this sequence? People currently in it are stopped.'), { danger: true, okText: t('Delete') })) { if ((await api('DELETE', '/api/sequences/' + ds.dataset.dseq)).ok) { toast(t('Deleted')); show(); } return; }
    const stop = e.target.closest('[data-stop]'); if (stop && (await api('DELETE', '/api/sequences/runs/' + stop.dataset.stop)).ok) { toast(t('Stopped')); return show(); }
    if (e.target.closest('[data-a="newren"]')) return renewalModal();
    if (e.target.closest('[data-a="newtype"]')) return typeModal();
    const et = e.target.closest('[data-etype]'); if (et) return typeModal(box._types.find((x) => x.id === +et.dataset.etype));
    const dt = e.target.closest('[data-dtype]');
    if (dt && await confirmBox(t('Delete this renewal type?'), { danger: true, okText: t('Delete') })) { if ((await api('DELETE', '/api/renewal-types/' + dt.dataset.dtype)).ok) { toast(t('Deleted')); show(); } return; }
    const rd = e.target.closest('[data-rdone]');
    if (rd) { const j = await api('PATCH', '/api/renewals/' + rd.dataset.rdone, { status: 'done' }); if (j.ok) { toast(j.next ? t('Done — next one on {d}', { d: fmtDate(j.next.due, false) }) : t('Done')); show(); } return; }
    const re = e.target.closest('[data-redit]');
    if (re) { if (!box._types) box._types = (await api('GET', '/api/renewal-types')).types || []; const j = await api('GET', '/api/renewals?status=all'); return renewalModal((j.renewals || []).find((x) => x.id === +re.dataset.redit)); }
    const rs = e.target.closest('[data-rs]'); if (rs) { rFilter.status = rs.dataset.rs; return renewals(); }
    const rdd = e.target.closest('[data-rd]'); if (rdd) { rFilter.due = rdd.dataset.rd; return renewals(); }
  });
  box.addEventListener('change', async (e) => {
    const d = e.target.closest('[data-done]');
    if (d) { const j = await api('PATCH', '/api/tasks/' + d.dataset.done, { done: e.target.checked }); if (j.ok) { if (j.task.repeat && e.target.checked) toast(t('Done — comes back {d}', { d: fmtDate(j.task.due) })); show(); } }
  });
  show();
}
