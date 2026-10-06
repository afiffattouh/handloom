// Live updates for the inbox. The page works without this file: every action
// is a plain form post. The stream only says "something changed"; the
// content is fetched from the server again, so a missed event heals itself.
(function () {
  var box = document.getElementById('inbox');
  if (!box || !window.EventSource) return;
  var stale = document.getElementById('stale');
  var busy = false;

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
    fetch('/inbox/fragment', { credentials: 'same-origin', headers: { Accept: 'text/html' } })
      .then(function (r) {
        if (r.redirected || r.status === 401) { location.reload(); return null; }
        return r.text();
      })
      .then(function (html) {
        if (html !== null) { box.innerHTML = html; stale.hidden = true; }
      })
      .catch(function () {})
      .then(function () { busy = false; });
  }

  function changed() {
    if (editing()) { stale.hidden = false; } else { refresh(); }
  }

  var es = new EventSource('/ui/stream');
  es.addEventListener('inbox-changed', changed);
  es.addEventListener('resync', changed);
  document.getElementById('refresh').addEventListener('click', refresh);
})();
