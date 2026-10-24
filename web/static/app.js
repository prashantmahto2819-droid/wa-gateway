// Vivid Relay dashboard — entry: shell, router, navigation, theme and language.
import { api, html, raw, esc, ic, icon, t, $, $$, toast, getLang, reduced } from '/static/app/core.js';
import { setLang, onLangChange } from '/static/i18n.js';

const ROUTES = [
  { re: /^\/app\/?$/, mod: 'overview', nav: 'overview' },
  { re: /^\/app\/numbers\/?$/, mod: 'numbers', nav: 'numbers' },
  { re: /^\/app\/send\/?$/, mod: 'send', nav: 'send' },
  { re: /^\/app\/campaigns\/?$/, mod: 'campaigns', nav: 'campaigns', view: 'list' },
  { re: /^\/app\/campaigns\/new\/?$/, mod: 'campaigns', nav: 'campaigns', view: 'new' },
  { re: /^\/app\/campaigns\/(\d+)\/?$/, mod: 'campaigns', nav: 'campaigns', view: 'detail', keys: ['id'] },
  { re: /^\/app\/scheduled\/?$/, mod: 'scheduled', nav: 'scheduled' },
  { re: /^\/app\/contacts\/?$/, mod: 'contacts', nav: 'contacts' },
  { re: /^\/app\/lists\/?$/, mod: 'lists', nav: 'lists' },
  { re: /^\/app\/groups\/?$/, mod: 'groups', nav: 'groups' },
  { re: /^\/app\/media\/?$/, mod: 'media', nav: 'media' },
  { re: /^\/app\/templates\/?$/, mod: 'templates', nav: 'templates' },
  { re: /^\/app\/autoreplies\/?$/, mod: 'autoreplies', nav: 'autoreplies' },
  { re: /^\/app\/developer\/?$/, mod: 'developer', nav: 'developer' },
  { re: /^\/app\/licence\/?$/, mod: 'licence', nav: 'licence' },
  { re: /^\/app\/settings\/?$/, mod: 'settings', nav: 'settings' },
  { re: /^\/app\/team\/?$/, mod: 'team', nav: 'team', perm: 'read' },
  { re: /^\/app\/kit\/?$/, mod: 'kit', nav: 'kit' },
  { re: /^\/app\/publisher\/?$/, mod: 'publisher', nav: 'publisher' },
  { re: /^\/app\/flows\/?$/, mod: 'flows', nav: 'flows' },
  { re: /^\/app\/admin\/?$/, mod: 'admin', nav: 'admin', view: 'overview', admin: true },
  { re: /^\/app\/admin\/(users|plans|licences|activations|settings|inbox|server|audit)\/?$/, mod: 'admin', nav: 'admin-$1', keys: ['view'], admin: true },
];

const NAV = [
  { group: 'Workspace', items: [
    ['overview', '/app', 'home', 'Overview'],
    ['numbers', '/app/numbers', 'phone', 'Numbers'],
    ['send', '/app/send', 'send', 'Send'],
    ['campaigns', '/app/campaigns', 'megaphone', 'Campaigns', 'read'],
    ['scheduled', '/app/scheduled', 'calendar', 'Scheduled'],
  ] },
  { group: 'Audience', items: [
    ['contacts', '/app/contacts', 'users', 'Contacts'],
    ['lists', '/app/lists', 'list', 'Lists'],
    ['groups', '/app/groups', 'group', 'Groups & channels'],
  ] },
  { group: 'Business', items: [
    ['kit', '/app/kit', 'briefcase', 'Business Kit'],
    ['publisher', '/app/publisher', 'link', 'Link Publisher', 'read'],
  ] },
  { group: 'Content', items: [
    ['media', '/app/media', 'image', 'Media library'],
    ['templates', '/app/templates', 'template', 'Templates'],
    ['autoreplies', '/app/autoreplies', 'bot', 'Auto-replies', 'automation'],
    ['flows', '/app/flows', 'sparkle', 'Chatbot flows', 'automation'],
  ] },
  { group: 'Account', items: [
    ['team', '/app/team', 'users', 'Team'],
    ['developer', '/app/developer', 'code', 'Developer', 'integration'],
    ['licence', '/app/licence', 'shield', 'Licence', 'billing'],
    ['settings', '/app/settings', 'settings', 'Settings'],
  ] },
];
const ADMIN_NAV = { group: 'Admin', items: [
  ['admin', '/app/admin', 'crown', 'Admin overview'],
  ['admin-users', '/app/admin/users', 'users', 'Users'],
  ['admin-plans', '/app/admin/plans', 'template', 'Plans'],
  ['admin-licences', '/app/admin/licences', 'key', 'Licences'],
  ['admin-activations', '/app/admin/activations', 'server', 'Activations'],
  ['admin-inbox', '/app/admin/inbox', 'inbox', 'Inbox'],
  ['admin-audit', '/app/admin/audit', 'log', 'Audit log'],
  ['admin-settings', '/app/admin/settings', 'settings', 'Site settings'],
  ['admin-server', '/app/admin/server', 'globe', 'Licence server'],
] };
const CLIENT_HIDDEN = ['admin-plans', 'admin-licences', 'admin-activations', 'admin-server'];
const TABS = [['overview', '/app', 'home', 'Home'], ['numbers', '/app/numbers', 'phone', 'Numbers'], ['send', '/app/send', 'send', 'Send'], ['campaigns', '/app/campaigns', 'megaphone', 'Campaigns']];

const state = { me: null, pub: null, cleanups: [], navKey: '', renderId: 0 };

// ---------------------------------------------------------------- theme
function currentTheme() {
  const a = document.documentElement.getAttribute('data-theme');
  if (a) return a;
  return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}
export function setTheme(th) {
  document.documentElement.setAttribute('data-theme', th);
  try { localStorage.setItem('vr_theme', th); } catch { /* ignore */ }
  const b = $('[data-act="theme"]');
  if (b) { b.innerHTML = icon(th === 'dark' ? 'sun' : 'moon'); b.setAttribute('aria-label', th === 'dark' ? t('Switch to light theme') : t('Switch to dark theme')); }
  window.dispatchEvent(new CustomEvent('vr:theme'));
}

// ---------------------------------------------------------------- shell
function navLink([key, href, ico, label]) {
  return html`<a href="${href}" data-link data-nav="${key}" class="nav-item">${ic(ico)}<span>${t(label)}</span></a>`;
}
function renderShell() {
  const me = state.me, u = me.user || {};
  const adminNav = { ...ADMIN_NAV, items: ADMIN_NAV.items.filter(([k]) => me.mode !== 'client' || !CLIENT_HIDDEN.includes(k)) };
  const can = me.can || {};
  const allowed = (it) => !it[4] || can[it[4]] !== false;
  const groups = [...NAV.map((g) => ({ ...g, items: g.items.filter(allowed) })).filter((g) => g.items.length), ...(u.role === 'superadmin' ? [adminNav] : [])];
  const ws = me.workspace || {};
  const lic = me.licence || {};
  const initials = (u.name || u.email || '?').split(/\s+/).map((x) => x[0]).slice(0, 2).join('').toUpperCase();
  $('#app').innerHTML = html`
  <a class="skip" href="#view">${t('Skip to content')}</a>
  <div class="shell">
    <aside class="rail" id="rail" aria-label="${t('Main navigation')}">
      <div class="rail-head">
        <a href="/app" data-link class="brand" aria-label="Vivid Relay — ${t('Overview')}"><img src="/static/favicon.svg" alt="" width="30" height="30"><span>Vivid Relay</span></a>
        <button class="btn-icon rail-close" data-act="drawer-close" aria-label="${t('Close menu')}">${ic('x')}</button>
      </div>
      <nav class="rail-nav">
        ${groups.map((g) => html`<div class="nav-group"><p class="nav-title">${t(g.group)}</p>${g.items.map(navLink)}</div>`)}
      </nav>
      <div class="rail-foot">
        <a class="plan-chip ${lic.status === 'active' ? (lic.expires_at && !lic.plan?.unlimited && lic.days_left <= 7 ? 'warn' : 'ok') : 'bad'}" href="/app/licence" data-link>
          <span class="dot"></span>
          <span><b>${lic.plan ? lic.plan.name : t('No plan')}</b><small>${lic.status === 'active' ? (lic.plan?.unlimited || !lic.expires_at ? t('Never expires') : t('{n} days left', { n: lic.days_left ?? 0 })) : t(lic.status === 'expired' ? 'Expired' : lic.status === 'suspended' ? 'Suspended' : 'Not active')}</small></span>
        </a>
        <div class="me-row">
          <span class="avatar" aria-hidden="true">${initials}</span>
          <span class="me-text"><b>${(me.actor || u).name || (me.actor || u).email}</b><small>${u.role === 'superadmin' ? t('Super admin') : ws.owner ? t('{r} at {b}', { r: t(ws.role), b: ws.owner }) : u.email}</small></span>
          <button class="btn-icon" data-act="logout" aria-label="${t('Log out')}" title="${t('Log out')}">${ic('logout')}</button>
        </div>
      </div>
    </aside>
    <div class="scrim" data-act="drawer-close"></div>
    <div class="main">
      <header class="topbar">
        <button class="btn-icon only-mobile" data-act="drawer" aria-label="${t('Open menu')}" aria-controls="rail" aria-expanded="false">${ic('menu', 20)}</button>
        <a href="/app" data-link class="brand only-mobile"><img src="/static/favicon.svg" alt="" width="26" height="26"><span>Vivid Relay</span></a>
        <div class="crumb" id="crumb"></div>
        <div class="top-actions">
          <button class="btn-text lang" data-act="lang" aria-label="${t('Change language')}">${getLang() === 'hi' ? 'English' : 'हिंदी'}</button>
          <button class="btn-icon" data-act="theme" aria-label="${currentTheme() === 'dark' ? t('Switch to light theme') : t('Switch to dark theme')}">${ic(currentTheme() === 'dark' ? 'sun' : 'moon')}</button>
        </div>
      </header>
      <div id="announce" class="banner info" hidden></div>
      <div id="licBanner" class="banner warn" hidden></div>
      <main id="view" tabindex="-1"></main>
    </div>
    <nav class="tabbar" aria-label="${t('Quick navigation')}">
      ${TABS.map(([k, h, i, l]) => html`<a href="${h}" data-link data-nav="${k}">${ic(i, 20)}<span>${t(l)}</span></a>`)}
      <button data-act="drawer" aria-controls="rail">${ic('more', 20)}<span>${t('More')}</span></button>
    </nav>
  </div>`.s;
  showAnnouncement();
  showExpiryBanner();
}

function showAnnouncement() {
  const a = state.pub?.announcement;
  const el = $('#announce');
  if (!el || !a) return;
  let seen = '';
  try { seen = localStorage.getItem('vr_ann') || ''; } catch { /* */ }
  if (seen === a) return;
  el.innerHTML = html`${ic('megaphone')}<span>${a}</span><button class="btn-icon" data-act="ann-close" aria-label="${t('Dismiss')}">${ic('x')}</button>`.s;
  el.hidden = false;
}
function showExpiryBanner() {
  const lic = state.me.licence || {};
  const el = $('#licBanner');
  if (state.me.licence_error) {
    el.innerHTML = html`${ic('shield')}<span>${t('Licence check failed')}: ${state.me.licence_error}</span><a class="btn sm" href="/app/licence" data-link>${t('Open licence')}</a>`.s;
    el.hidden = false; return;
  }
  if (lic.status !== 'active' && state.me.user.role !== 'superadmin') {
    el.innerHTML = html`${ic('shield')}<span>${lic.status === 'expired' ? t('Your plan has expired. Sending is paused until you renew.') : t('You do not have an active plan yet. Redeem a licence key to start sending.')}</span><a class="btn sm" href="/app/licence" data-link>${t('Open licence')}</a>`.s;
    el.hidden = false;
  }
}

function setActiveNav(key) {
  state.navKey = key;
  $$('[data-nav]').forEach((a) => {
    const on = a.dataset.nav === key;
    a.classList.toggle('active', on);
    if (on) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
  });
}

function openDrawer(open) {
  document.body.classList.toggle('drawer-open', open);
  $$('[data-act="drawer"]').forEach((b) => b.setAttribute('aria-expanded', String(open)));
  if (open) { const f = $('#rail .nav-item.active') || $('#rail .nav-item'); f && f.focus(); }
}

// ---------------------------------------------------------------- router
export function navigate(path, { replace = false } = {}) {
  if (path === location.pathname + location.search) { route(); return; }
  history[replace ? 'replaceState' : 'pushState']({}, '', path);
  route();
}

async function route() {
  const id = ++state.renderId;
  state.cleanups.splice(0).forEach((fn) => { try { fn(); } catch (e) { console.warn(e); } });
  openDrawer(false);
  const path = location.pathname.replace(/\/+$/, '') || '/app';
  let match = null, params = {};
  for (const r of ROUTES) {
    const m = path.match(r.re);
    if (m) { match = r; (r.keys || []).forEach((k, i) => { params[k] = m[i + 1]; }); break; }
  }
  const view = $('#view');
  if (!match || (match.admin && state.me.user.role !== 'superadmin') || (state.me.mode === 'client' && CLIENT_HIDDEN.includes(match.nav.replace('$1', params.view || '')))) {
    setActiveNav('');
    view.innerHTML = html`<div class="page"><div class="empty big">${ic('alert', 28)}<h3>${t('Page not found')}</h3><p>${t('This page does not exist or you do not have access.')}</p><a href="/app" data-link class="btn primary">${t('Go to overview')}</a></div></div>`.s;
    return;
  }
  const navKey = match.nav.replace('$1', params.view || '');
  setActiveNav(navKey);
  params.view ||= match.view;
  view.innerHTML = '<div class="page"><div class="skel-wrap"><div class="skel h1"></div><div class="skel"></div><div class="skel"></div></div></div>';
  let mod;
  try { mod = await import(`/static/app/${match.mod}.js`); } catch (e) {
    console.error(e);
    view.innerHTML = html`<div class="page">${t('Could not load this page. Reload and try again.')}</div>`.s;
    return;
  }
  if (id !== state.renderId) return;
  const ctx = {
    root: view, me: state.me, params, query: new URLSearchParams(location.search),
    navigate, refreshMe, isCurrent: () => id === state.renderId,
    onCleanup: (fn) => state.cleanups.push(fn),
    every: (ms, fn) => { const x = setInterval(() => { if (id === state.renderId && !document.hidden) fn(); }, ms); state.cleanups.push(() => clearInterval(x)); return x; },
    setTitle: (title, crumbs) => {
      document.title = title + ' · Vivid Relay';
      const c = $('#crumb');
      if (c) c.innerHTML = crumbs ? String(crumbs) : '';
    },
  };
  ctx.setTitle(t(labelFor(navKey)));
  try { await mod.render(ctx); } catch (e) {
    console.error(e);
    if (id === state.renderId) view.innerHTML = html`<div class="page"><div class="empty">${ic('alert', 28)}<h3>${t('Something went wrong')}</h3><p>${String(e.message || e)}</p></div></div>`.s;
  }
  if (id === state.renderId && !location.hash) {
    window.scrollTo({ top: 0, behavior: 'instant' in window ? 'instant' : 'auto' });
  }
}
function labelFor(key) {
  for (const g of [...NAV, ADMIN_NAV]) for (const it of g.items) if (it[0] === key) return it[3];
  return 'Vivid Relay';
}

export async function refreshMe() {
  const j = await api('GET', '/api/me', undefined, { silent: true });
  if (j.ok) {
    state.me.user = j.user; state.me.licence = j.licence; state.me.usage = j.usage; state.me.mode = j.mode; state.me.licence_error = j.licence_error || '';
  }
  return state.me;
}

// ---------------------------------------------------------------- events
document.addEventListener('click', (e) => {
  const a = e.target.closest('a[data-link]');
  if (a && !e.defaultPrevented && e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey && a.target !== '_blank') {
    e.preventDefault();
    const overlay = a.closest('.overlay');
    if (overlay) overlay.querySelector('[data-close]')?.click();
    navigate(a.getAttribute('href'));
    return;
  }
  const b = e.target.closest('[data-act]');
  if (!b) return;
  const act = b.dataset.act;
  if (act === 'drawer') openDrawer(!document.body.classList.contains('drawer-open'));
  else if (act === 'drawer-close') openDrawer(false);
  else if (act === 'theme') setTheme(currentTheme() === 'dark' ? 'light' : 'dark');
  else if (act === 'lang') setLang(getLang() === 'hi' ? 'en' : 'hi');
  else if (act === 'ann-close') {
    try { localStorage.setItem('vr_ann', state.pub?.announcement || ''); } catch { /* */ }
    $('#announce').hidden = true;
  } else if (act === 'logout') logout();
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && document.body.classList.contains('drawer-open')) openDrawer(false);
});
window.addEventListener('popstate', route);
onLangChange(() => { renderShell(); route(); });

async function logout() {
  await api('POST', '/auth/logout', {}, { silent: true });
  location.href = '/login';
}

// ---------------------------------------------------------------- boot
async function boot() {
  if (reduced()) document.documentElement.classList.add('reduce-motion');
  const [me, pub] = await Promise.all([
    api('GET', '/api/me', undefined, { silent: true }),
    api('GET', '/public/settings', undefined, { silent: true }),
  ]);
  if (!me.ok) {
    if (me._status !== 401) {
      $('#app').innerHTML = html`<div class="boot err">${ic('alert', 28)}<p>${me.error || t('Could not reach the server.')}</p><a class="btn" href="/app">${t('Retry')}</a></div>`.s;
    }
    return;
  }
  state.me = { user: me.user, licence: me.licence || {}, usage: me.usage || {}, mode: me.mode, licence_error: me.licence_error || '' };
  state.pub = pub.ok ? (pub.settings || pub) : null;
  renderShell();
  route();
}
window.addEventListener('vr:toast', (e) => toast(e.detail.msg, e.detail.tone));
boot();
export { esc, raw };
