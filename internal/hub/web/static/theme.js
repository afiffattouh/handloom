// Theme: follows the system unless the person chose light or dark here. The
// choice is kept in this browser only. Runs before the page paints so there is
// no flash; the page works without it.
(function () {
  var key = 'handloom-theme', root = document.documentElement;
  try { var saved = localStorage.getItem(key); if (saved === 'light' || saved === 'dark') root.setAttribute('data-theme', saved); } catch (e) {}
  document.addEventListener('DOMContentLoaded', function () {
    var cur = root.getAttribute('data-theme') || 'auto';
    var btns = document.querySelectorAll('[data-theme-set]');
    function mark() { for (var i = 0; i < btns.length; i++) btns[i].setAttribute('aria-pressed', btns[i].getAttribute('data-theme-set') === cur ? 'true' : 'false'); }
    mark();
    document.addEventListener('click', function (e) {
      var b = e.target.closest ? e.target.closest('[data-theme-set]') : null;
      if (!b) return;
      cur = b.getAttribute('data-theme-set');
      if (cur === 'auto') root.removeAttribute('data-theme'); else root.setAttribute('data-theme', cur);
      try { if (cur === 'auto') localStorage.removeItem(key); else localStorage.setItem(key, cur); } catch (e2) {}
      mark();
    });
  });
})();
