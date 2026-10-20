// Marks the Help section being read in the sidebar list.
(function () {
  var links = document.querySelectorAll('.sb-sub a');
  var secs = [];
  links.forEach(function (a) {
    var s = document.getElementById(a.getAttribute('href').slice(1));
    if (s) secs.push({ a: a, s: s });
  });
  if (!secs.length) return;
  function mark() {
    var cur = secs[0];
    secs.forEach(function (x) { if (x.s.getBoundingClientRect().top < 120) cur = x; });
    secs.forEach(function (x) { if (x === cur) x.a.setAttribute('aria-current', 'location'); else x.a.removeAttribute('aria-current'); });
  }
  window.addEventListener('scroll', mark, { passive: true });
  mark();
})();
