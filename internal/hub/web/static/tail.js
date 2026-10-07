// The terminal view. While the page is open and visible, tell the hub the
// screen is wanted (it expires in under a minute on its own) and fetch the last
// screen it has. Nothing here runs when the page is hidden.
(function () {
  var box = document.getElementById('screen');
  if (!box) return;
  var tail = box.getAttribute('data-tail'), watch = box.getAttribute('data-watch');
  var meta = document.querySelector('meta[name=csrf-token]');
  var token = meta ? meta.getAttribute('content') : '';
  var asked = 0;

  function ask() {
    var now = Date.now();
    if (now - asked < 20000) return Promise.resolve();
    asked = now;
    return fetch(watch, { method: 'POST', credentials: 'same-origin', headers: { 'X-CSRF-Token': token } }).catch(function () {});
  }
  function read() {
    return fetch(tail, { credentials: 'same-origin' })
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
    if (document.visibilityState === 'visible') ask().then(read);
  }
  loop();
  setInterval(loop, 4000);
  document.addEventListener('visibilitychange', function () { if (document.visibilityState === 'visible') loop(); });
})();
