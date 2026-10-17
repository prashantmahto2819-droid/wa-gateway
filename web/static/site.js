/* Vivid Relay public site — no libraries, CSP-safe (no inline handlers). */
(function () {
  'use strict';
  var doc = document.documentElement;
  doc.classList.add('js');
  var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  function $(s, r) { return (r || document).querySelector(s); }
  function $$(s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); }

  /* Nav: shadow on scroll + mobile toggle */
  var nav = $('[data-nav]');
  if (nav) {
    var onScroll = function () { nav.classList.toggle('is-scrolled', window.scrollY > 8); };
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
    var tog = $('[data-nav-toggle]');
    var menu = $('#nav-menu');
    if (tog && menu) {
      tog.addEventListener('click', function () {
        var open = tog.getAttribute('aria-expanded') !== 'true';
        tog.setAttribute('aria-expanded', String(open));
        menu.classList.toggle('is-open', open);
      });
      document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape' && menu.classList.contains('is-open')) {
          tog.setAttribute('aria-expanded', 'false');
          menu.classList.remove('is-open');
          tog.focus();
        }
      });
    }
  }

  /* Counters */
  function fmtIN(n) {
    try { return Math.round(n).toLocaleString('en-IN'); } catch (e) { return String(Math.round(n)); }
  }
  function runCounter(el) {
    var target = parseFloat(el.getAttribute('data-count')) || 0;
    var suffix = el.getAttribute('data-suffix') || '';
    if (reduce) { el.textContent = fmtIN(target) + suffix; return; }
    var start = null, dur = 1400;
    function step(t) {
      if (!start) start = t;
      var p = Math.min(1, (t - start) / dur);
      var e = 1 - Math.pow(1 - p, 3);
      el.textContent = fmtIN(target * e) + suffix;
      if (p < 1) requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
  }

  /* Phone chat sequence */
  function runPhone(stage) {
    var items = $$('.seq', stage);
    var fills = $$('.poll__fill', stage);
    var ticks = $$('.ticks', stage);
    function finish() {
      fills.forEach(function (f) { f.style.width = f.getAttribute('data-w') || '0%'; });
      ticks.forEach(function (t) { t.classList.add('is-read'); });
    }
    if (reduce) {
      items.forEach(function (it) {
        if (it.hasAttribute('data-transient')) it.classList.add('is-gone'); else it.classList.add('is-shown');
      });
      finish();
      return;
    }
    var i = 0;
    function next() {
      if (i >= items.length) { setTimeout(finish, 300); return; }
      var it = items[i++];
      var wait = parseInt(it.getAttribute('data-wait') || '900', 10);
      it.classList.add('is-shown');
      if (it.hasAttribute('data-transient')) {
        setTimeout(function () { it.classList.add('is-gone'); next(); }, wait);
      } else {
        if (it.querySelector('.poll__fill')) {
          setTimeout(function () {
            $$('.poll__fill', it).forEach(function (f) { f.style.width = f.getAttribute('data-w') || '0%'; });
          }, 200);
        }
        setTimeout(next, wait);
      }
    }
    setTimeout(next, 500);
  }

  /* Reveal + triggers */
  var revealables = $$('.reveal, .warmup, [data-count], [data-phone]');
  function trigger(el) {
    if (el.hasAttribute('data-count')) runCounter(el);
    if (el.hasAttribute('data-phone')) runPhone(el);
    el.classList.add('is-in');
  }
  if ('IntersectionObserver' in window && !reduce) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (en.isIntersecting) { trigger(en.target); io.unobserve(en.target); }
      });
    }, { rootMargin: '0px 0px -8% 0px', threshold: 0 });
    revealables.forEach(function (el) { io.observe(el); });
  } else {
    revealables.forEach(trigger);
  }

  /* Copy buttons on code blocks */
  $$('pre[data-copy]').forEach(function (pre) {
    if (!navigator.clipboard) return;
    var b = document.createElement('button');
    b.type = 'button';
    b.className = 'copy';
    b.textContent = 'Copy';
    b.addEventListener('click', function () {
      var code = pre.querySelector('code');
      navigator.clipboard.writeText((code || pre).innerText).then(function () {
        b.textContent = 'Copied';
        setTimeout(function () { b.textContent = 'Copy'; }, 1600);
      });
    });
    pre.appendChild(b);
  });

  /* Password show/hide */
  $$('[data-pw-toggle]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var input = btn.parentNode.querySelector('input');
      var show = input.type === 'password';
      input.type = show ? 'text' : 'password';
      btn.textContent = show ? 'Hide' : 'Show';
      btn.setAttribute('aria-pressed', String(show));
    });
  });

  /* JSON forms: <form data-api="/auth/login" data-redirect="/app" data-success="..."> */
  $$('form[data-api]').forEach(function (form) {
    var msg = $('.form__msg', form);
    var btn = $('button[type="submit"]', form);
    function say(text, ok) {
      if (!msg) return;
      msg.textContent = text;
      msg.classList.toggle('is-err', !ok);
      msg.classList.toggle('is-ok', !!ok);
    }
    form.addEventListener('submit', function (e) {
      e.preventDefault();
      if (!form.checkValidity()) { form.reportValidity(); return; }
      var body = {};
      $$('input[name], textarea[name]', form).forEach(function (f) { body[f.name] = f.value.trim ? (f.type === 'password' ? f.value : f.value.trim()) : f.value; });
      var label = btn ? btn.textContent : '';
      if (btn) { btn.disabled = true; btn.textContent = btn.getAttribute('data-busy') || 'Please wait…'; }
      say('', true);
      fetch(form.getAttribute('data-api'), {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'vivid' },
        body: JSON.stringify(body)
      }).then(function (r) {
        return r.json().catch(function () { return { ok: false, error: 'The server sent an unexpected reply (HTTP ' + r.status + '). Try again in a minute.' }; })
          .then(function (j) { if (!r.ok && j.ok !== false) j.ok = false; return j; });
      }).then(function (j) {
        if (j && j.ok) {
          var to = form.getAttribute('data-redirect');
          if (to) { window.location.assign(to); return; }
          say(form.getAttribute('data-success') || 'Done.', true);
          form.reset();
        } else {
          say((j && j.error) || 'That did not work. Check the details and try again.', false);
        }
      }).catch(function () {
        say('Could not reach the server. Check your connection and try again.', false);
      }).then(function () {
        if (btn) { btn.disabled = false; btn.textContent = label; }
      });
    });
  });
})();
