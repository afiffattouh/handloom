// While a machine is joining (or has joined and its link has not called in yet), keep the
// list of machines fresh so the page says "ready" by itself. It stops when nobody is
// waiting (or after fifteen minutes).
// Without it the page still works: reload to see the state.
(function () {
  var box = document.getElementById('machines');
  var say = document.getElementById('machine-announce');
  if (!box || !window.fetch) return;
  var url = box.getAttribute('data-poll');
  var stop = Date.now() + 15 * 60 * 1000, timer = null, busy = false;

  function waiting() { return box.querySelector('[data-settling]') !== null; }
  function readyNames() {
    var out = {};
    box.querySelectorAll('[data-ready]').forEach(function (r) { out[r.getAttribute('data-name')] = true; });
    return out;
  }
  function tick() {
    if (busy || !waiting() || Date.now() > stop) return;
    busy = true;
    var before = readyNames();
    fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/html' } })
      .then(function (r) { if (r.redirected || r.status === 401) { location.reload(); return null; } return r.text(); })
      .then(function (html) {
        if (html === null) return;
        box.innerHTML = html;
        var after = readyNames();
        Object.keys(after).forEach(function (n) { if (!before[n] && say) say.textContent = n + ' is ready.'; });
      })
      .catch(function () {})
      .then(function () { busy = false; schedule(); });
  }
  function schedule() { clearTimeout(timer); if (waiting() && Date.now() < stop) timer = setTimeout(tick, 3000); }
  schedule();
  // a machine the person just added: the list the server drew has it pending, so polling starts; one added later in another tab is seen on reload
  document.addEventListener('visibilitychange', function () { if (document.visibilityState === 'visible') schedule(); });
})();
