// The page frame: fold the menu to icons (or open it on a phone), and close dropdown
// menus. Everything works without it: the menu is always visible on a wide screen and
// the dropdowns are plain disclosure elements.
(function () {
  var root = document.documentElement, key = 'handloom-sidebar';
  var phone = window.matchMedia ? window.matchMedia('(max-width: 820px)') : { matches: false };

  function toggleMenu() {
    if (phone.matches) {
      if (root.hasAttribute('data-sidebar-open')) closeMenu(); else openMenu();
      return;
    }
    var folded = root.getAttribute('data-sidebar') === 'collapsed';
    if (folded) root.removeAttribute('data-sidebar'); else root.setAttribute('data-sidebar', 'collapsed');
    try { if (folded) localStorage.removeItem(key); else localStorage.setItem(key, 'collapsed'); } catch (e) {}
  }
  function openMenu() {
    root.setAttribute('data-sidebar-open', '');
    var s = document.querySelector('.scrim'); if (s) s.hidden = false;
  }
  function closeMenu() {
    root.removeAttribute('data-sidebar-open');
    var s = document.querySelector('.scrim'); if (s) s.hidden = true;
  }
  function closeDropdowns(except) {
    var open = document.querySelectorAll('details.dd[open]');
    for (var i = 0; i < open.length; i++) if (open[i] !== except) open[i].removeAttribute('open');
  }

  document.addEventListener('click', function (e) {
    var t = e.target.closest ? e.target : e.target.parentElement;
    if (!t) return;
    if (t.closest('[data-sidebar-toggle]')) { toggleMenu(); return; }
    if (t.closest('[data-sidebar-close]')) { closeMenu(); return; }
    if (phone.matches && t.closest('.sidebar a')) closeMenu();
    var dd = t.closest('details.dd');
    // choosing something in a menu closes it; a click elsewhere closes all
    if (dd && t.closest('.dd-menu .dd-item')) dd.removeAttribute('open');
    else closeDropdowns(dd);
  });
  document.addEventListener('toggle', function (e) {
    if (e.target.matches && e.target.matches('details.dd') && e.target.open) closeDropdowns(e.target);
  }, true);
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') { closeDropdowns(null); closeMenu(); }
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && (e.key === 'b' || e.key === 'B')) {
      var a = document.activeElement, typing = a && (a.tagName === 'INPUT' || a.tagName === 'TEXTAREA' || a.tagName === 'SELECT' || a.isContentEditable);
      if (!typing && document.querySelector('[data-sidebar-toggle]')) { e.preventDefault(); toggleMenu(); }
    }
  });
  if (phone.addEventListener) phone.addEventListener('change', closeMenu);
})();
