// Helps fill in the New job form: example check commands and a brief template.
// The form works without it.
(function () {
  var examples = document.getElementById('verify-examples');
  if (examples) {
    examples.hidden = false;
    examples.addEventListener('click', function (e) {
      var b = e.target.closest ? e.target.closest('[data-fill]') : null;
      if (!b) return;
      var f = document.getElementById(b.getAttribute('data-fill'));
      if (f) { f.value = b.getAttribute('data-value'); f.focus(); }
    });
  }
  var tpl = document.getElementById('brief-template'), brief = document.getElementById('brief');
  if (tpl && brief) {
    tpl.hidden = false;
    tpl.addEventListener('click', function () {
      if (brief.value.trim() !== '' && !window.confirm) return;
      brief.value = 'Goal: \n\nDone when: \n\nLeave alone: \n\nContext: ';
      brief.focus();
      brief.setSelectionRange(6, 6);
    });
  }
})();
