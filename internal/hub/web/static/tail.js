// The terminal views. While the page is open and visible, tell the hub each screen is wanted (it expires in
// under a minute on its own) and fetch the last screen it has. Nothing here runs when the page is hidden.
(function () {
  var boxes = document.querySelectorAll('pre[data-tail]');
  if (!boxes.length) return;
  var meta = document.querySelector('meta[name=csrf-token]');
  var token = meta ? meta.getAttribute('content') : '';

  function watch(box) {
    var state = box._state || (box._state = { asked: 0 });
    var now = Date.now();
    if (now - state.asked < 20000) return Promise.resolve();
    state.asked = now;
    return fetch(box.getAttribute('data-watch'), { method: 'POST', credentials: 'same-origin', headers: { 'X-CSRF-Token': token } }).catch(function () {});
  }
  function read(box) {
    // A screen inside a closed <details> is not looked at, so it is not fetched.
    var d = box.closest ? box.closest('details') : null;
    if (d && !d.open) return Promise.resolve();
    return fetch(box.getAttribute('data-tail'), { credentials: 'same-origin' })
      .then(function (r) { return r.ok ? r.text() : r.text().then(function (t) { box.textContent = t; return null; }); })
      .then(function (t) {
        if (t === null || t === undefined) return;
        var pinned = box.scrollTop + box.clientHeight >= box.scrollHeight - 8;
        box.textContent = t;
        if (pinned) box.scrollTop = box.scrollHeight;
      })
      .catch(function () {});
  }
  function loop() {
    if (document.visibilityState !== 'visible') return;
    for (var i = 0; i < boxes.length; i++) (function (b) {
      var d = b.closest ? b.closest('details') : null;
      if (d && !d.open) return;
      watch(b).then(function () { return read(b); });
    })(boxes[i]);
  }
  loop();
  setInterval(loop, 4000);
  document.addEventListener('visibilitychange', loop);
  document.addEventListener('toggle', loop, true);
})();
