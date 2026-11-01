// Refresh only the read-only overview. Never replace a form or an open terminal.
(function () {
  var overview = document.querySelector('[data-build-progress]');
  var status = document.getElementById('build-updates');
  if (!overview || !status) return;
  var busy = false;
  var failed = false;
  function revealEvidence() {
    var target = document.getElementById(location.hash.slice(1));
    if (!target || !target.closest('#build-details')) return;
    var details = document.getElementById('build-details');
    details.open = true;
    target.scrollIntoView({ block: 'start' });
  }
  window.addEventListener('hashchange', revealEvidence);
  revealEvidence();
  async function refresh() {
    if (busy || document.hidden || overview.contains(document.activeElement)) return;
    busy = true;
    var controller = new AbortController();
    var timeout = setTimeout(function () { controller.abort(); }, 8000);
    try {
      var response = await fetch(location.pathname, { credentials: 'same-origin', signal: controller.signal });
      if (!response.ok || response.redirected) throw new Error('unavailable');
      var doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      var next = doc.querySelector('[data-build-progress]');
      if (!next) throw new Error('missing overview');
      if (overview.innerHTML !== next.innerHTML || failed) {
        overview.replaceChildren.apply(overview, Array.from(next.childNodes));
        status.textContent = 'Progress updated at ' + new Date().toLocaleTimeString() + '. Details refresh separately.';
      }
      failed = false;
    } catch (_) {
      failed = true;
      status.textContent = 'Progress could not update. Retrying automatically; use Refresh all details to reconnect.';
    } finally {
      clearTimeout(timeout);
      busy = false;
    }
  }
  setInterval(refresh, 10000);
})();
