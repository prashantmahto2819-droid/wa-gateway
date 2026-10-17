// Vivid Relay — UI strings. Keys are the English text; HI maps them to Hindi.
// t('Hello {name}', {name}) interpolates {placeholders}. Missing Hindi falls back to English.

let lang = 'en';
try { lang = localStorage.getItem('vr_lang') === 'hi' ? 'hi' : 'en'; } catch { /* storage blocked */ }
const listeners = new Set();

export function getLang() { return lang; }
export function setLang(l) {
  lang = l === 'hi' ? 'hi' : 'en';
  try { localStorage.setItem('vr_lang', lang); } catch { /* ignore */ }
  document.documentElement.setAttribute('lang', lang);
  listeners.forEach((fn) => fn(lang));
}
export function onLangChange(fn) { listeners.add(fn); return () => listeners.delete(fn); }
export function t(key, vars) {
  let s = (lang === 'hi' && HI[key]) || key;
  if (vars) s = s.replace(/\{(\w+)\}/g, (m, k) => (vars[k] ?? m));
  return s;
}
document.documentElement.setAttribute('lang', lang);

/* HINDI_START */
const HI = {};
/* HINDI_END */
