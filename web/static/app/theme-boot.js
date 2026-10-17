// Runs before first paint (classic script) so the saved theme and language apply without a flash.
(function () {
  var th = null, lang = null;
  try { th = localStorage.getItem('vr_theme'); lang = localStorage.getItem('vr_lang'); } catch (e) { /* storage blocked */ }
  if (th === 'light' || th === 'dark') document.documentElement.setAttribute('data-theme', th);
  if (lang === 'hi') document.documentElement.setAttribute('lang', 'hi');
})();
