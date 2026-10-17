import { api, html, ic, t, relTime, skel, empty, modal, confirmBox, toast } from '/static/app/core.js';
import { createComposer, summarize, bubbleHTML } from '/static/app/composer.js';

export async function render(ctx) {
  const { root } = ctx;
  let list = [];
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Templates')}</h1><p>${t('Saved messages you can reuse in Send, Campaigns and Auto-replies.')}</p></div>
      <div class="page-actions"><button class="btn primary" data-a="new">${ic('plus', 16)}${t('New template')}</button></div></div>
    <div data-list>${skel(5)}</div></div>`.s;
  async function load() {
    const j = await api('GET', '/api/templates', undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    list = j.ok ? j.templates || [] : [];
    const box = root.querySelector('[data-list]');
    if (!list.length) { box.innerHTML = html`<div class="panel">${empty('template', t('No templates yet'), t('Save your best messages — greetings, price lists, offers — and reuse them.'), html`<button class="btn primary" data-a="new">${ic('plus', 16)}${t('New template')}</button>`)}</div>`.s; return; }
    box.innerHTML = html`<div class="media-grid" style="grid-template-columns:repeat(auto-fill,minmax(280px,1fr))">${list.map((x) => html`<article class="panel" style="display:flex;flex-direction:column;overflow:hidden">
      <div class="chat" style="border:0;border-radius:0;max-height:220px;overflow:hidden"><div class="chat-body" style="min-height:150px;padding:12px">${bubbleHTML(x.message)}</div></div>
      <div class="panel-body" style="flex:1"><b>${x.name}</b><div class="cell-sub">${t('Edited')} ${relTime(x.updated_at || x.created_at)}</div></div>
      <div class="num-actions"><button class="btn sm" data-edit="${x.id}">${ic('edit', 14)}${t('Edit')}</button><button class="btn sm" data-use="${x.id}">${ic('send', 14)}${t('Send')}</button><span style="flex:1"></span><button class="btn-icon danger" data-del="${x.id}" aria-label="${t('Delete')} ${x.name}">${ic('trash', 16)}</button></div></article>`)}</div>`.s;
  }
  function editor(x) {
    const isNew = !x; x = x || { name: '', message: { text: '' } };
    const m = modal({ title: isNew ? t('New template') : t('Edit template'), size: 'xl', body: html`<div class="stack"><label class="field" style="max-width:420px"><span>${t('Template name')}</span><input name="name" value="${x.name}" required placeholder="${t('Diwali offer')}"></label><div data-comp></div></div>`,
      footer: html`<button class="btn ghost" data-close>${t('Cancel')}</button><button class="btn primary" data-ok>${t('Save template')}</button>` });
    const comp = createComposer(m.body.querySelector('[data-comp]'), { message: x.message, compact: true });
    m.foot.querySelector('[data-ok]').addEventListener('click', async () => {
      const name = m.body.querySelector('[name=name]').value.trim();
      if (!name) return toast(t('Give the template a name.'), 'warn');
      const err = comp.validate(); if (err) return toast(err, 'warn');
      const j = isNew ? await api('POST', '/api/templates', { name, message: comp.get() }) : await api('PATCH', `/api/templates/${x.id}`, { name, message: comp.get() });
      if (j.ok) { toast(t('Template saved')); m.close(); load(); }
    });
  }
  root.addEventListener('click', async (e) => {
    if (e.target.closest('[data-a="new"]')) return editor(null);
    const ed = e.target.closest('[data-edit]'); if (ed) return editor(list.find((x) => String(x.id) === ed.dataset.edit));
    const us = e.target.closest('[data-use]');
    if (us) { sessionStorage.setItem('vr_prefill', JSON.stringify(list.find((x) => String(x.id) === us.dataset.use).message)); return ctx.navigate('/app/send'); }
    const d = e.target.closest('[data-del]');
    if (d && await confirmBox(t('Delete this template?'), { okText: t('Delete'), danger: true })) { if ((await api('DELETE', `/api/templates/${d.dataset.del}`)).ok) { toast(t('Template deleted')); load(); } }
  });
  void summarize;
  await load();
}
