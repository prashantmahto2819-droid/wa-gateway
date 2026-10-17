import { api, html, raw, ic, t, fmtBytes, relTime, skel, empty, confirmBox, toast, copy, upload, meter, $$ } from '/static/app/core.js';
import { mediaIcon } from '/static/app/composer.js';

const KINDS = ['all', 'image', 'video', 'document', 'audio'];

export async function render(ctx) {
  const { root } = ctx;
  const st = { kind: 'all', list: [], used: 0, limit: 0 };
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Media library')}</h1><p>${t('Upload photos, videos and documents once, then reuse them in any message.')}</p></div></div>
    <div class="split" style="grid-template-columns:minmax(0,1fr) 300px">
      <label class="dropzone" data-drop tabindex="0">${ic('upload', 28)}<b>${t('Drop files here or click to upload')}</b><span>${t('Images, videos, PDFs and audio. Several files at once is fine.')}</span>
        <input type="file" multiple data-file hidden></label>
      <section class="panel panel-pad" data-meter>${skel(2)}</section></div>
    <div class="uploads" data-uploads></div>
    <div class="row mt mb"><div class="seg" role="tablist" aria-label="${t('Filter by type')}">${KINDS.map((k) => html`<button role="tab" data-k="${k}" aria-selected="${k === st.kind}">${k === 'all' ? t('All') : t(k[0].toUpperCase() + k.slice(1))}</button>`)}</div></div>
    <div data-grid>${skel(4)}</div></div>`.s;

  async function load() {
    const j = await api('GET', '/api/media', undefined, { silent: true });
    if (!ctx.isCurrent()) return;
    st.list = j.ok ? j.media || [] : []; st.used = j.used_mb || 0; st.limit = j.limit_mb || 0;
    root.querySelector('[data-meter]').innerHTML = html`${meter(t('Storage used'), st.used, st.limit, ' MB')}<p class="hint mt-s">${t('{n} files', { n: st.list.length })}. ${t('Delete files you no longer need to free space.')}</p>`.s;
    draw();
  }
  function draw() {
    const g = root.querySelector('[data-grid]');
    const l = st.list.filter((x) => st.kind === 'all' || x.kind === st.kind);
    if (!st.list.length) { g.innerHTML = html`<div class="panel">${empty('image', t('Your library is empty'), t('Upload your product photos, price lists or brochures to use them in campaigns.'))}</div>`.s; return; }
    if (!l.length) { g.innerHTML = empty('filter', t('No files of this type')).s; return; }
    g.innerHTML = html`<div class="media-grid">${l.map((x) => html`<div class="m-tile">
      <div class="m-prev">${x.kind === 'image' ? html`<img loading="lazy" alt="${x.name}" src="/api/media/${x.id}/file">` : ic(mediaIcon(x.kind), 34)}</div>
      <div class="m-info"><b title="${x.name}">${x.name}</b><small>${fmtBytes(x.size)} · ${relTime(x.created_at)}</small></div>
      <div class="m-act"><button class="btn-icon" data-copy="${x.id}" aria-label="${t('Copy media ID')} ${x.id}" title="${t('Copy media ID')}">${ic('copy', 16)}</button>
        <a class="btn-icon" href="/api/media/${x.id}/file" target="_blank" rel="noopener" aria-label="${t('Open')} ${x.name}" title="${t('Open')}">${ic('eye', 16)}</a>
        <span class="grow" style="flex:1"></span><span class="cell-sub" style="align-self:center">#${x.id}</span>
        <button class="btn-icon danger" data-del="${x.id}" aria-label="${t('Delete')} ${x.name}" title="${t('Delete')}">${ic('trash', 16)}</button></div></div>`)}</div>`.s;
  }
  async function send(files) {
    const box = root.querySelector('[data-uploads]');
    for (const f of files) {
      const row = document.createElement('div'); row.className = 'upl';
      row.innerHTML = html`<span class="ellipsis">${f.name} <small class="muted">${fmtBytes(f.size)}</small></span><div class="bar"><span style="width:0%"></span></div><small class="tnum">0%</small>`.s;
      box.appendChild(row);
      const fd = new FormData(); fd.append('file', f);
      const j = await upload('/api/media', fd, (p) => { row.querySelector('.bar span').style.width = Math.round(p * 100) + '%'; row.querySelector('small.tnum').textContent = Math.round(p * 100) + '%'; });
      row.querySelector('small.tnum').innerHTML = j.ok ? raw(ic('check', 16)).s : '✕';
      setTimeout(() => row.remove(), j.ok ? 1500 : 5000);
    }
    toast(t('Upload finished')); load();
  }
  const dz = root.querySelector('[data-drop]'), fi = root.querySelector('[data-file]');
  dz.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fi.click(); } });
  fi.addEventListener('change', () => { if (fi.files.length) send([...fi.files]); fi.value = ''; });
  ['dragenter', 'dragover'].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add('over'); }));
  ['dragleave', 'drop'].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove('over'); }));
  dz.addEventListener('drop', (e) => { if (e.dataTransfer?.files?.length) send([...e.dataTransfer.files]); });
  root.addEventListener('click', async (e) => {
    const k = e.target.closest('[data-k]');
    if (k) { st.kind = k.dataset.k; $$('[data-k]', root).forEach((x) => x.setAttribute('aria-selected', String(x === k))); draw(); return; }
    const c = e.target.closest('[data-copy]'); if (c) return copy(c.dataset.copy, t('Media ID copied'));
    const d = e.target.closest('[data-del]');
    if (d && await confirmBox(t('Delete this file? Templates or campaigns that use it will fail to send it.'), { okText: t('Delete'), danger: true })) {
      if ((await api('DELETE', `/api/media/${d.dataset.del}`)).ok) { toast(t('File deleted')); load(); }
    }
  });
  await load();
}
