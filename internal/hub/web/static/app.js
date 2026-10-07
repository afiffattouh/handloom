// Live updates for pages that show a running picture (the inbox, the command
// center). The page works without this file: every action is a plain form
// post and a plain link. The stream only says "something changed"; the content
// is fetched from the server again, so a missed event heals itself.
(function () {
  var box = document.querySelector('[data-live]');
  if (!box || !window.EventSource) return;
  var url = box.getAttribute('data-live');
  var stale = document.getElementById('stale');
  var busy = false, timer = null;

  function editing() {
    var fields = box.querySelectorAll('textarea, input[type=text], input:not([type])');
    for (var i = 0; i < fields.length; i++) {
      if (fields[i].value !== '' || fields[i] === document.activeElement) return true;
    }
    return false;
  }

  function refresh() {
    if (busy) return;
    busy = true;
    fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/html' } })
      .then(function (r) {
        if (r.redirected || r.status === 401) { location.reload(); return null; }
        return r.text();
      })
      .then(function (html) {
        if (html !== null) { box.innerHTML = html; if (stale) stale.hidden = true; }
      })
      .catch(function () {})
      .then(function () { busy = false; });
  }

  function changed() {
    if (editing()) { if (stale) stale.hidden = false; return; }
    // Several events often arrive together: one refresh is enough.
    clearTimeout(timer);
    timer = setTimeout(refresh, 400);
  }

  var es = new EventSource('/ui/stream');
  es.addEventListener('inbox-changed', changed);
  es.addEventListener('resync', changed);
  var again = document.getElementById('refresh');
  if (again) again.addEventListener('click', refresh);
})();
