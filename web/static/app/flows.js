// Chatbot flows: a structured editor (steps with "then go to" links) over the
// same graph the engine runs, a test chat that runs the flow in a sandbox,
// and execution logs.
import { api, html, raw, esc, ic, t, toast, modal, confirmBox, skel, empty, pill, relTime, fmtDate, getSessions, sessionOptions, chipInput } from '/static/app/core.js';

const TYPES = {
  message: ['send', 'Message', 'Text, or buttons / a list the customer taps'],
  question: ['info', 'Question', 'Ask something and save the answer in a variable'],
  condition: ['filter', 'Condition', 'Go one way or the other depending on a variable or tag'],
  tag: ['tag', 'Tag', 'Add or remove tags on the contact'],
  api: ['code', 'API call', 'Send the collected answers to your system'],
  assign: ['users', 'Hand to a person', 'Pause the bot in this chat so a person can reply'],
  end: ['check', 'End', 'Finish the flow'],
};
const TRIGGER = { keyword: 'A keyword (exact)', contains: 'Message contains a word', welcome: 'First message from a new chat', any: 'Any message (fallback)', manual: 'Only from other flows / manual' };
const OPS = { exists: 'has a value', equals: 'equals', contains: 'contains', gt: 'is greater than', lt: 'is less than', tag: 'contact has tag' };
const uid = () => 'n' + Math.random().toString(36).slice(2, 8);

export async function render(ctx) {
  const { root, me } = ctx;
  ctx.setTitle(t('Chatbot flows'));
  const canAuto = me.can?.automation !== false;
  const sess = await getSessions();
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Chatbot flows')}</h1><p>${t('Multi-step conversations: buttons, questions that save answers, conditions, tags, an API call, hand-over to a person. Flows run before auto-replies.')}</p></div>
      <div class="page-actions">${canAuto ? html`<button class="btn primary" data-a="new">${ic('plus', 16)}${t('New flow')}</button>` : ''}</div></div>
    <section data-box>${skel(3)}</section></div>`.s;
  const box = root.querySelector('[data-box]');
  let flows = [];
  async function load() {
    const j = await api('GET', '/api/flows');
    if (!ctx.isCurrent() || !j.ok) return;
    flows = j.flows || [];
    box.innerHTML = flows.length ? html`<div class="grid g2">${flows.map((f) => html`<section class="panel"><div class="panel-head"><h2>${f.name}</h2>${f.test_mode ? pill(t('test mode'), 'warn') : ''}${pill(f.active ? t('on') : t('off'), f.active ? 'ok' : 'muted')}</div>
        <div class="panel-body"><p class="muted">${t(TRIGGER[f.trigger] || f.trigger)}${f.keywords.length ? ': ' + f.keywords.join(', ') : ''} · ${t('{n} steps', { n: f.graph.nodes.length })} · ${t('{n} runs', { n: f.runs })}${f.session ? ' · ' + f.session : ''}</p>
          ${f.problems.length ? html`<div class="note warn">${ic('alert', 16)}<span>${f.problems.join(' · ')}</span></div>` : ''}</div>
        <div class="panel-foot btn-row">${canAuto ? html`<button class="btn sm primary" data-edit="${f.id}">${ic('edit', 14)}${t('Open')}</button>` : ''}<button class="btn sm" data-test="${f.id}">${ic('bot', 14)}${t('Test')}</button><button class="btn sm ghost" data-logs="${f.id}">${ic('log', 14)}${t('Logs')}</button>
          ${canAuto ? html`<button class="btn-icon danger" data-del="${f.id}" aria-label="${t('Delete')}">${ic('trash', 16)}</button>` : ''}</div></section>`)}</div>`.s
      : empty('bot', t('No flows yet'), t('Example: “Quote” → ask policy type with buttons → ask how many members → tag the contact → hand over to you. Start with “New flow”.'), canAuto ? html`<button class="btn primary" data-a="new">${ic('plus', 16)}${t('New flow')}</button>` : '').s;
  }
  root.addEventListener('click', async (e) => {
    if (e.target.closest('[data-a="new"]')) return editor(null);
    const ed = e.target.closest('[data-edit]'); if (ed) return editor(flows.find((f) => f.id === +ed.dataset.edit));
    const ts = e.target.closest('[data-test]'); if (ts) return testChat(flows.find((f) => f.id === +ts.dataset.test));
    const lg = e.target.closest('[data-logs]'); if (lg) return logsModal(flows.find((f) => f.id === +lg.dataset.logs));
    const del = e.target.closest('[data-del]');
    if (del && await confirmBox(t('Delete this flow? Chats in the middle of it are dropped.'), { danger: true, okText: t('Delete') })) { if ((await api('DELETE', '/api/flows/' + del.dataset.del)).ok) { toast(t('Deleted')); load(); } }
  });

  // ---------------------------------------------------------------- editor
  function editor(f) {
    const isNew = !f;
    f = f ? JSON.parse(JSON.stringify(f)) : { name: '', trigger: 'keyword', keywords: [], session: '', active: false, test_mode: false, test_phones: [], priority: 10,
      graph: { nodes: [{ id: 'start', type: 'start', data: {} }, { id: 'm1', type: 'message', data: { label: t('Welcome'), message: { type: 'buttons', body: 'नमस्ते {{name}} जी 🙏 {{business}} में आपका स्वागत है। क्या चाहिए?', buttons: [{ id: 'b1', title: 'जानकारी' }, { id: 'b2', title: 'बात करें' }] } } }, { id: 'end', type: 'end', data: {} }], edges: [{ source: 'start', port: 'next', target: 'm1' }] } };
    const g = f.graph;
    let kw = null;
    const m = modal({ title: isNew ? t('New flow') : f.name, size: 'xl', body: '', footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn" data-try>${ic('bot', 14)}${t('Try it')}</button><button class="btn primary" data-ok>${t('Save')}</button>` });
    const nodeLabel = (n) => n.data?.label || (n.type === 'message' ? (n.data?.message?.text || n.data?.message?.body || '').slice(0, 30) : n.type === 'question' ? (n.data?.text || '').slice(0, 30) : '') || t(TYPES[n.type]?.[1] || n.type);
    const targetSel = (nodeId, port, label) => {
      const cur = g.edges.find((e) => e.source === nodeId && (e.port || 'next') === port)?.target || '';
      return html`<label class="field"><span>${label}</span><select data-edge="${nodeId}" data-port="${port}"><option value="">${t('— stop here —')}</option>${g.nodes.filter((x) => x.id !== nodeId && x.type !== 'start').map((x) => html`<option value="${x.id}" ${x.id === cur ? 'selected' : ''}>${x.type === 'end' ? '⏹ ' : ''}${nodeLabel(x)}</option>`)}</select></label>`;
    };
    function nodeCard(n, i) {
      const d = n.data = n.data || {};
      let body = '';
      switch (n.type) {
        case 'start': body = html`<p class="muted">${t('The flow starts here when its trigger matches.')}</p>${targetSel(n.id, 'next', t('First step'))}`; break;
        case 'message': {
          const msg = d.message = d.message || { type: 'text', text: '' };
          const kind = msg.type || 'text';
          body = html`<div class="seg mb-s" role="radiogroup">${['text', 'buttons', 'list'].map((k) => html`<button type="button" role="radio" data-mk="${k}" aria-checked="${kind === k}" aria-selected="${kind === k}">${t(k === 'text' ? 'Text' : k === 'buttons' ? 'Buttons (up to 3)' : 'List (up to 10)')}</button>`)}</div>
            <label class="field"><span>${t('Text')} <small>{{name}} {{business}} + ${t('your variables')}</small></span><textarea data-k="${kind === 'text' ? 'message.text' : 'message.body'}" rows="2">${kind === 'text' ? msg.text || '' : msg.body || ''}</textarea></label>
            ${kind === 'text' ? html`<label class="field"><span>${t('Picture URL')} <small>(${t('optional')})</small></span><input data-k="message.media_url" value="${msg.media_url || ''}"></label>${targetSel(n.id, 'next', t('Then'))}` : ''}
            ${kind !== 'text' ? html`<div class="stack" data-opts>${(kind === 'buttons' ? msg.buttons || [] : (msg.sections?.[0]?.rows || [])).map((o, k) => html`<div class="row"><input data-opt="${k}" value="${o.title}" placeholder="${t('Option text')}" style="max-width:220px">
                <select data-edge="${n.id}" data-port="${o.id}" class="grow"><option value="">${t('— stop here —')}</option>${g.nodes.filter((x) => x.id !== n.id && x.type !== 'start').map((x) => html`<option value="${x.id}" ${g.edges.find((e) => e.source === n.id && e.port === o.id)?.target === x.id ? 'selected' : ''}>${x.type === 'end' ? '⏹ ' : ''}${nodeLabel(x)}</option>`)}</select>
                <button type="button" class="btn-icon danger" data-rmopt="${k}" aria-label="${t('Remove')}">${ic('x', 14)}</button></div>`)}
              <div><button type="button" class="btn sm" data-addopt>${ic('plus', 14)}${t('Add option')}</button></div>
              ${targetSel(n.id, 'else', t('If they type something else'))}</div>` : ''}`;
          break;
        }
        case 'question': body = html`<label class="field"><span>${t('Question')}</span><textarea data-k="text" rows="2">${d.text || ''}</textarea></label>
          <div class="form-grid g3"><label class="field"><span>${t('Save answer as')} <small>{{…}}</small></span><input data-k="var" value="${d.var || ''}" placeholder="members"></label>
            <label class="field"><span>${t('Answer type')}</span><select data-k="kind">${['text', 'number', 'phone', 'email', 'date'].map((k) => html`<option value="${k}" ${(d.kind || 'text') === k ? 'selected' : ''}>${t(k)}</option>`)}</select></label>
            <label class="field"><span>${t('If invalid, say')}</span><input data-k="retry" value="${d.retry || ''}" placeholder="${t('Please type a number')}"></label></div>${targetSel(n.id, 'next', t('Then'))}`; break;
        case 'condition': body = html`<div class="form-grid g3"><label class="field"><span>${t('Variable')}</span><input data-k="var" value="${d.var || ''}" placeholder="members"></label>
            <label class="field"><span>${t('Check')}</span><select data-k="op">${Object.entries(OPS).map(([k, l]) => html`<option value="${k}" ${d.op === k ? 'selected' : ''}>${t(l)}</option>`)}</select></label>
            <label class="field"><span>${t('Value')}</span><input data-k="value" value="${d.value || ''}"></label></div>
          <div class="form-grid g2">${targetSel(n.id, 'yes', t('If yes'))}${targetSel(n.id, 'no', t('If no'))}</div>`; break;
        case 'tag': body = html`<div class="form-grid g2"><label class="field"><span>${t('Add tags')} <small>(${t('comma separated')})</small></span><input data-k="add" value="${(d.add || []).join(', ')}"></label><label class="field"><span>${t('Remove tags')}</span><input data-k="remove" value="${(d.remove || []).join(', ')}"></label></div>${targetSel(n.id, 'next', t('Then'))}`; break;
        case 'api': body = html`<div class="form-grid g2"><label class="field"><span>${t('URL')} <small>https://</small></span><input data-k="url" value="${d.url || ''}"></label><label class="field"><span>${t('Method')}</span><select data-k="method">${['POST', 'GET', 'PUT'].map((k) => html`<option ${(d.method || 'POST') === k ? 'selected' : ''}>${k}</option>`)}</select></label></div>
          <label class="field"><span>${t('JSON body')} <small>(${t('blank = phone, chat and all variables')})</small></span><textarea data-k="body" rows="2">${d.body || ''}</textarea></label>
          <label class="field"><span>${t('Save from the reply')} <small>(${t('variable = path, one per line, e.g. price = data.0.price')})</small></span><textarea data-k="save" rows="2">${Object.entries(d.save || {}).map(([k, v]) => k + ' = ' + v).join('\n')}</textarea></label>
          <div class="form-grid g2">${targetSel(n.id, 'ok', t('If it worked'))}${targetSel(n.id, 'fail', t('If it failed'))}</div>`; break;
        case 'assign': body = html`<div class="form-grid g2"><label class="field"><span>${t('Keep the bot quiet for (minutes)')}</span><input type="number" data-k="minutes" value="${d.minutes || 120}" min="5" max="1440"></label><label class="field"><span>${t('Note for the team')}</span><input data-k="note" value="${d.note || ''}"></label></div>
          <p class="hint">${t('You get a flow.handover webhook / notification with the collected answers.')}</p>${targetSel(n.id, 'next', t('Then'))}`; break;
        case 'end': body = html`<p class="muted">${t('The flow finishes; collected answers are saved on the contact.')}</p>`; break;
      }
      const fixed = n.type === 'start';
      return html`<section class="panel" data-node="${n.id}"><div class="panel-head"><h2>${ic(TYPES[n.type]?.[0] || 'flag', 16)} ${fixed ? t('Start') : html`<input data-k="label" value="${d.label || ''}" placeholder="${t(TYPES[n.type]?.[1] || n.type)}" style="max-width:260px">`}</h2>
        <span class="row">${fixed || n.type === 'end' ? '' : html`<button type="button" class="btn-icon" data-up="${i}" aria-label="${t('Move up')}">${ic('up', 14)}</button><button type="button" class="btn-icon" data-down="${i}" aria-label="${t('Move down')}">${ic('down', 14)}</button>`}${fixed ? '' : html`<button type="button" class="btn-icon danger" data-rmnode="${n.id}" aria-label="${t('Remove step')}">${ic('trash', 16)}</button>`}</span></div>
        <div class="panel-body">${body}</div></section>`;
    }
    function draw() {
      m.body.innerHTML = html`<div class="split"><div class="stack" data-steps>${g.nodes.map(nodeCard)}
          <div class="row" style="flex-wrap:wrap">${Object.entries(TYPES).map(([k, [i, l, h]]) => html`<button type="button" class="btn sm" data-addnode="${k}" title="${t(h)}">${ic(i, 14)}${t(l)}</button>`)}</div></div>
        <div class="stack"><section class="panel"><div class="panel-head"><h2>${t('Settings')}</h2></div><div class="panel-body stack">
          <label class="field"><span>${t('Name')}</span><input name="name" value="${f.name}" placeholder="${t('Health quote')}"></label>
          <label class="field"><span>${t('Starts when')}</span><select name="trigger">${Object.entries(TRIGGER).map(([k, l]) => html`<option value="${k}" ${f.trigger === k ? 'selected' : ''}>${t(l)}</option>`)}</select></label>
          <div class="field"><span>${t('Keywords')}</span><div data-kw></div></div>
          <label class="field"><span>${t('Only on number')}</span><select name="session">${raw(sessionOptions(sess, f.session, { all: true, allLabel: t('All numbers') }))}</select></label>
          <label class="field"><span>${t('Priority')} <small>(${t('higher wins when several match')})</small></span><input type="number" name="priority" value="${f.priority}" min="0" max="100"></label>
          ${raw(`<label class="switch"><input type="checkbox" name="active" ${f.active ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${esc(t('Flow is on'))}</span></span></label>`)}
          ${raw(`<label class="switch"><input type="checkbox" name="test_mode" ${f.test_mode ? 'checked' : ''}><span class="track"></span><span class="switch-text"><span>${esc(t('Test mode'))}</span><small>${esc(t('Only the numbers below can trigger it'))}</small></span></label>`)}
          <label class="field"><span>${t('Test numbers')}</span><textarea name="test_phones" rows="2">${f.test_phones.join('\n')}</textarea></label>
          <p class="hint">${t('Variables: {{name}}, {{phone}}, {{business}} and every question’s “save as” name. Buttons need WhatsApp on a phone; a numbered text fallback goes along automatically.')}</p></div></section>
          <div class="note" data-problems hidden></div></div></div>`.s;
      kw = chipInput(m.body.querySelector('[data-kw]'), { values: f.keywords, placeholder: t('health, बीमा'), name: 'keywords' });
    }
    function collectNode(el, n) {
      const d = n.data;
      el.querySelectorAll('[data-k]').forEach((inp) => {
        const k = inp.dataset.k, v = inp.value;
        if (k === 'add' || k === 'remove') d[k] = v.split(',').map((x) => x.trim()).filter(Boolean);
        else if (k === 'save') { d.save = {}; v.split('\n').forEach((line) => { const i = line.indexOf('='); if (i > 0) d.save[line.slice(0, i).trim().toLowerCase()] = line.slice(i + 1).trim(); }); }
        else if (k.startsWith('message.')) { d.message = d.message || {}; d.message[k.slice(8)] = v; }
        else d[k] = k === 'minutes' ? +v : v;
      });
      if (n.type === 'message' && d.message?.type !== 'text') {
        const opts = [...el.querySelectorAll('[data-opt]')].map((inp, i) => ({ id: (d.message.type === 'buttons' ? d.message.buttons : d.message.sections?.[0]?.rows)?.[i]?.id || uid(), title: inp.value.trim() || t('Option') + ' ' + (i + 1) }));
        if (d.message.type === 'buttons') d.message.buttons = opts.slice(0, 3); else d.message.sections = [{ title: '', rows: opts.slice(0, 10) }];
      }
    }
    function collect() {
      m.body.querySelectorAll('[data-node]').forEach((el) => collectNode(el, g.nodes.find((n) => n.id === el.dataset.node)));
      const edges = [];
      m.body.querySelectorAll('[data-edge]').forEach((sel) => { if (sel.value) edges.push({ source: sel.dataset.edge, port: sel.dataset.port, target: sel.value }); });
      g.edges = edges;
      const b = m.body;
      Object.assign(f, { name: b.querySelector('[name=name]').value.trim(), trigger: b.querySelector('[name=trigger]').value, session: b.querySelector('[name=session]').value, priority: +b.querySelector('[name=priority]').value || 10,
        active: b.querySelector('[name=active]').checked, test_mode: b.querySelector('[name=test_mode]').checked, test_phones: b.querySelector('[name=test_phones]').value.split(/[\s,]+/).filter(Boolean),
        keywords: kw ? kw.get() : f.keywords });
    }
    draw();
    m.body.addEventListener('click', (e) => {
      const add = e.target.closest('[data-addnode]'), rm = e.target.closest('[data-rmnode]'), mk = e.target.closest('[data-mk]'), ao = e.target.closest('[data-addopt]'), ro = e.target.closest('[data-rmopt]'), up = e.target.closest('[data-up]'), dn = e.target.closest('[data-down]');
      if (!add && !rm && !mk && !ao && !ro && !up && !dn) return;
      collect();
      if (add) {
        const n = { id: uid(), type: add.dataset.addnode, data: {} };
        if (n.type === 'message') n.data.message = { type: 'text', text: '' };
        const endIdx = g.nodes.findIndex((x) => x.type === 'end');
        g.nodes.splice(endIdx < 0 ? g.nodes.length : endIdx, 0, n);
        // link the previous dangling "next" to the new step when nothing is linked yet
        const prev = g.nodes[g.nodes.indexOf(n) - 1];
        if (prev && !['condition', 'api', 'end'].includes(prev.type) && !(prev.type === 'message' && prev.data.message?.type !== 'text') && !g.edges.some((x) => x.source === prev.id)) g.edges.push({ source: prev.id, port: 'next', target: n.id });
      }
      if (rm) { g.nodes = g.nodes.filter((x) => x.id !== rm.dataset.rmnode); g.edges = g.edges.filter((x) => x.source !== rm.dataset.rmnode && x.target !== rm.dataset.rmnode); }
      if (mk) { const n = g.nodes.find((x) => x.id === mk.closest('[data-node]').dataset.node); const old = n.data.message || {}; n.data.message = { type: mk.dataset.mk, text: old.text || old.body || '', body: old.body || old.text || '', buttons: old.buttons || [{ id: uid(), title: '' }], sections: old.sections || [{ title: '', rows: [{ id: uid(), title: '' }] }] }; }
      if (ao) { const n = g.nodes.find((x) => x.id === ao.closest('[data-node]').dataset.node); const msg = n.data.message; if (msg.type === 'buttons') { if (msg.buttons.length >= 3) return toast(t('WhatsApp allows 3 buttons — use a list for more'), 'warn'); msg.buttons.push({ id: uid(), title: '' }); } else { if (msg.sections[0].rows.length >= 10) return toast(t('At most 10 list rows'), 'warn'); msg.sections[0].rows.push({ id: uid(), title: '' }); } }
      if (ro) { const n = g.nodes.find((x) => x.id === ro.closest('[data-node]').dataset.node); const msg = n.data.message; const arr = msg.type === 'buttons' ? msg.buttons : msg.sections[0].rows; const [gone] = arr.splice(+ro.dataset.rmopt, 1); if (gone) g.edges = g.edges.filter((x) => !(x.source === n.id && x.port === gone.id)); }
      if (up) { const i = +up.dataset.up; if (i > 1) [g.nodes[i - 1], g.nodes[i]] = [g.nodes[i], g.nodes[i - 1]]; }
      if (dn) { const i = +dn.dataset.down; if (i < g.nodes.length - 1 && g.nodes[i + 1].type !== 'end') [g.nodes[i + 1], g.nodes[i]] = [g.nodes[i], g.nodes[i + 1]]; }
      draw();
    });
    async function save(andTest) {
      collect();
      if (!f.name) return toast(t('Give the flow a name'), 'warn');
      const j = await api(isNew ? 'POST' : 'PATCH', isNew ? '/api/flows' : '/api/flows/' + f.id, f);
      if (!j.ok) return;
      f.id = j.flow.id;
      const pr = m.body.querySelector('[data-problems]');
      if (j.flow.problems.length) { pr.hidden = false; pr.className = 'note warn'; pr.innerHTML = html`${ic('alert', 16)}<span>${j.flow.problems.join(' · ')}</span>`.s; }
      if (andTest) return testChat(j.flow);
      m.close(); toast(t('Saved')); load();
    }
    m.foot.querySelector('[data-ok]').addEventListener('click', () => save(false));
    m.foot.querySelector('[data-try]').addEventListener('click', () => save(true));
  }

  // ---------------------------------------------------------------- test chat
  function testChat(f) {
    const m = modal({ title: t('Test: {n}', { n: f.name }), size: 'sm', body: html`<div class="chat" data-chat style="min-height:260px;max-height:50vh;overflow:auto"></div>
      <div class="input-group mt-s"><input data-in placeholder="${t('Type like a customer…')}"><button class="btn primary" data-send>${ic('send', 14)}</button></div><p class="hint">${t('Nothing is sent to WhatsApp; API steps and tags are skipped. Tap an option or type its number.')}</p>`,
      footer: html`<button class="btn ghost" data-reset>${t('Restart')}</button><button class="btn primary" data-close>${t('Close')}</button>` });
    const chat = m.body.querySelector('[data-chat]'), inp = m.body.querySelector('[data-in]');
    const bubble = (who, text, opts = []) => { chat.insertAdjacentHTML('beforeend', html`<div class="bubble ${who}"><div>${raw(esc(text).replace(/\n/g, '<br>'))}</div>${opts.length ? html`<div class="row mt-s" style="flex-wrap:wrap">${opts.map((o) => html`<button class="btn sm" data-opt="${o.id}">${o.title}</button>`)}</div>` : ''}</div>`.s); chat.scrollTop = chat.scrollHeight; };
    async function send(body) {
      const j = await api('POST', `/api/flows/${f.id}/test`, body);
      if (!j.ok) return;
      if (j.problems?.length) return bubble('bot', t('Fix first: ') + j.problems.join('; '));
      for (const r of j.replies) {
        const opts = r.buttons ? r.buttons.buttons.map((b) => ({ id: b.id, title: b.text })) : r.list ? r.list.sections.flatMap((s) => s.rows).map((x) => ({ id: x.id, title: x.title })) : [];
        bubble('bot', r.text || '', opts);
      }
      if (j.done) bubble('sys', t('— flow finished —') + (Object.keys(j.vars || {}).length ? ' ' + Object.entries(j.vars).filter(([k]) => !['name', 'phone', 'business', 'business_phone', 'city'].includes(k)).map(([k, v]) => k + '=' + v).join(', ') : ''));
    }
    const go = () => { const v = inp.value.trim(); if (!v) return; bubble('me', v); inp.value = ''; send({ text: v }); };
    m.body.querySelector('[data-send]').addEventListener('click', go);
    inp.addEventListener('keydown', (e) => { if (e.key === 'Enter') go(); });
    m.body.addEventListener('click', (e) => { const o = e.target.closest('[data-opt]'); if (o) { bubble('me', o.textContent); send({ button_id: o.dataset.opt, text: o.textContent }); } });
    m.foot.querySelector('[data-reset]').addEventListener('click', () => { chat.innerHTML = ''; send({ reset: true, text: f.keywords?.[0] || 'hi' }); });
    send({ reset: true, text: f.keywords?.[0] || 'hi' });
  }

  async function logsModal(f) {
    const j = await api('GET', `/api/flows/${f.id}/logs`);
    if (!j.ok) return;
    modal({ title: t('Logs: {n}', { n: f.name }), size: 'lg', body: html`<p class="muted">${t('{n} chats in the middle of this flow', { n: j.in_progress })}</p>
      ${j.logs.length ? html`<div class="table-wrap"><table><thead><tr><th>${t('When')}</th><th>${t('Phone')}</th><th>${t('Step')}</th><th>${t('Event')}</th><th>${t('Detail')}</th></tr></thead><tbody>${j.logs.map((l) => html`<tr><td class="nowrap">${fmtDate(l.at)}</td><td class="tnum">${l.phone}</td><td>${f.graph.nodes.find((n) => n.id === l.node)?.data?.label || l.node}</td><td>${pill(l.event, l.event === 'error' ? 'bad' : 'muted')}</td><td><small>${l.detail}</small></td></tr>`)}</tbody></table></div>` : html`<p class="muted">${t('No runs yet.')}</p>`}`,
      footer: html`<button class="btn primary" data-close>${t('Close')}</button>` });
  }
  load();
}
