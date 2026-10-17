import { api, html, ic, t, toast, getLang, skel } from '/static/app/core.js';
import { setLang } from '/static/i18n.js';
import { setTheme } from '/static/app.js';

const PRESETS = [
  ['OpenAI', 'https://api.openai.com/v1', 'gpt-4o-mini'],
  ['Gemini', 'https://generativelanguage.googleapis.com/v1beta/openai', 'gemini-2.0-flash'],
  ['Groq', 'https://api.groq.com/openai/v1', 'llama-3.1-8b-instant'],
  ['OpenRouter', 'https://openrouter.ai/api/v1', 'openai/gpt-4o-mini'],
];

export async function render(ctx) {
  const { root, me } = ctx;
  const u = me.user;
  let theme = document.documentElement.getAttribute('data-theme') || 'system';
  root.innerHTML = html`<div class="page">
    <div class="page-head"><div><h1>${t('Settings')}</h1><p>${t('Your profile, password, appearance and AI assistant.')}</p></div></div>
    <div class="grid g2" style="align-items:start">
      <div class="stack">
        <form class="panel" data-profile><div class="panel-head"><h2>${t('Profile')}</h2></div><div class="panel-body stack">
          <label class="field"><span>${t('Name')}</span><input name="name" value="${u.name || ''}" required autocomplete="name"></label>
          <label class="field"><span>${t('Email')}</span><input value="${u.email}" disabled></label>
          <label class="field"><span>${t('Phone')}</span><input name="phone" type="tel" value="${u.phone || ''}" autocomplete="tel"></label></div>
          <div class="panel-foot"><button class="btn primary">${t('Save profile')}</button></div></form>
        <form class="panel" data-pw><div class="panel-head"><h2>${t('Change password')}</h2></div><div class="panel-body stack">
          <label class="field"><span>${t('Current password')}</span><input name="current" type="password" required autocomplete="current-password"></label>
          <label class="field"><span>${t('New password')}</span><input name="new" type="password" minlength="8" required autocomplete="new-password"><small>${t('At least 8 characters.')}</small></label>
          <label class="field"><span>${t('Repeat new password')}</span><input name="again" type="password" minlength="8" required autocomplete="new-password"></label></div>
          <div class="panel-foot"><button class="btn primary">${t('Change password')}</button></div></form>
        <section class="panel"><div class="panel-head"><h2>${t('Appearance')}</h2></div><div class="panel-body stack">
          <div class="field"><span class="field-label">${t('Theme')}</span><div class="seg" role="radiogroup">${[['light', 'sun', 'Light'], ['dark', 'moon', 'Dark']].map(([k, i, l]) => html`<button role="radio" data-theme-pick="${k}" aria-checked="${theme === k}" aria-selected="${theme === k}">${ic(i, 15)}${t(l)}</button>`)}</div></div>
          <div class="field"><span class="field-label">${t('Language')}</span><div class="seg lang" role="radiogroup">${[['en', 'English'], ['hi', 'हिंदी']].map(([k, l]) => html`<button role="radio" data-lang-pick="${k}" aria-checked="${getLang() === k}" aria-selected="${getLang() === k}">${l}</button>`)}</div></div></div></section>
      </div>
      <form class="panel" data-ai><div class="panel-head"><h2>${ic('sparkle', 18)} ${t('AI assistant')}</h2></div><div class="panel-body stack" data-ai-body>${skel(5)}</div></form>
    </div></div>`.s;

  root.querySelector('[data-profile]').addEventListener('submit', async (e) => {
    e.preventDefault();
    const j = await api('PATCH', '/api/me', { name: e.target.name.value.trim(), phone: e.target.phone.value.trim() });
    if (j.ok) { toast(t('Profile saved')); await ctx.refreshMe(); }
  });
  root.querySelector('[data-pw]').addEventListener('submit', async (e) => {
    e.preventDefault(); const f = e.target;
    if (f.new.value !== f.again.value) return toast(t('The new passwords do not match.'), 'warn');
    if ((await api('POST', '/api/me/password', { current: f.current.value, new: f.new.value })).ok) { toast(t('Password changed')); f.reset(); }
  });
  root.addEventListener('click', (e) => {
    const th = e.target.closest('[data-theme-pick]');
    if (th) { theme = th.dataset.themePick; setTheme(theme); root.querySelectorAll('[data-theme-pick]').forEach((b) => { b.setAttribute('aria-checked', String(b === th)); b.setAttribute('aria-selected', String(b === th)); }); }
    const lg = e.target.closest('[data-lang-pick]'); if (lg && lg.dataset.langPick !== getLang()) setLang(lg.dataset.langPick);
  });

  const ai = await api('GET', '/api/me/ai', undefined, { silent: true });
  if (!ctx.isCurrent()) return;
  const a = ai.ok ? ai : { base_url: '', model: '', has_key: false };
  const form = root.querySelector('[data-ai]');
  form.querySelector('[data-ai-body]').innerHTML = html`<p class="lead" style="margin:0">${t('Auto-reply rules with "Reply with AI" use this provider. Any OpenAI-compatible service works.')}</p>
    <div class="field"><span class="field-label">${t('Provider')}</span><div class="seg">${PRESETS.map(([n, u2]) => html`<button type="button" data-preset="${n}" aria-selected="${a.base_url === u2}">${n}</button>`)}</div></div>
    <label class="field"><span>${t('Base URL')}</span><input name="base_url" type="url" value="${a.base_url || ''}" placeholder="https://api.openai.com/v1"></label>
    <label class="field"><span>${t('Model')}</span><input name="model" value="${a.model || ''}" placeholder="gpt-4o-mini"></label>
    <label class="field"><span>${t('API key')}</span><input name="api_key" type="password" autocomplete="off" placeholder="${a.has_key ? '•••••••• ' + t('(saved — type to replace)') : 'sk-…'}"><small>${t('Stored on the server and never shown again.')}</small></label>
    <div class="divider"></div>
    <label class="field"><span>${t('Try it')}</span><div class="input-group"><input name="test" placeholder="${t('What are your shop hours?')}"><button type="button" class="btn" data-test>${t('Test')}</button></div></label>
    <div data-test-out></div>`.s;
  form.insertAdjacentHTML('beforeend', html`<div class="panel-foot"><button class="btn primary">${t('Save AI settings')}</button></div>`.s);
  form.addEventListener('click', async (e) => {
    const p = e.target.closest('[data-preset]');
    if (p) { const x = PRESETS.find((y) => y[0] === p.dataset.preset); form.base_url.value = x[1]; if (!form.model.value || PRESETS.some((y) => y[2] === form.model.value)) form.model.value = x[2]; form.querySelectorAll('[data-preset]').forEach((b) => b.setAttribute('aria-selected', String(b === p))); }
    const tb = e.target.closest('[data-test]');
    if (tb) {
      tb.classList.add('busy');
      const j = await api('POST', '/api/me/ai/test', { prompt: form.test.value.trim() || 'Say hello in one short sentence.' });
      tb.classList.remove('busy');
      form.querySelector('[data-test-out]').innerHTML = j.ok ? html`<div class="chat"><div class="chat-body" style="min-height:0;align-items:flex-start"><div class="bubble" style="border-radius:12px 12px 12px 4px">${j.reply}</div></div></div>`.s : '';
    }
  });
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const body = { base_url: form.base_url.value.trim(), model: form.model.value.trim() };
    if (form.api_key.value) body.api_key = form.api_key.value;
    if ((await api('PATCH', '/api/me/ai', body)).ok) { toast(t('AI settings saved')); form.api_key.value = ''; form.api_key.placeholder = '•••••••• ' + t('(saved — type to replace)'); }
  });
}
