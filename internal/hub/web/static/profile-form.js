// Helps while writing a profile. The form works without it: the server says the
// same things after "Check before saving". This only makes the quick settings
// and the plain-words summary react as you click.
(function () {
  var form = document.querySelector('form [name=tool]') ? document.querySelector('form [name=tool]').form : null;
  if (!form) return;
  var boxes = form.querySelectorAll('input[name=tool]');
  var deny = form.querySelector('[name=deny]'), write = form.querySelector('[name=write]');
  var out = document.getElementById('summary-text'), presets = document.getElementById('presets');
  if (presets) presets.hidden = false;

  function lines(el) { return el ? el.value.split('\n').map(function (s) { return s.trim(); }).filter(Boolean) : []; }
  function join(w) { return w.length < 2 ? w.join('') : w.slice(0, -1).join(', ') + ' and ' + w[w.length - 1]; }
  function summary() {
    var on = {};
    boxes.forEach(function (b) { if (b.checked) on[b.value] = true; });
    var can = [];
    if (on.read) can.push('read files');
    if (on.edit) can.push('edit files' + (lines(write).length ? ' (in a repository job, only ' + lines(write).join(', ') + ')' : ''));
    if (on.shell) can.push('run commands' + (lines(deny).length ? ' except ' + lines(deny).join(', ') : ''));
    if (on.web) can.push('use the web');
    var text = can.length ? 'This agent can ' + join(can) + '.' : 'This agent cannot do anything but talk.';
    var cannot = [['read', 'read files'], ['edit', 'edit files'], ['shell', 'run commands'], ['web', 'use the web']].filter(function (p) { return !on[p[0]]; }).map(function (p) { return p[1]; });
    if (cannot.length && can.length) text += ' It cannot ' + join(cannot) + '.';
    if (out) out.textContent = text;
  }
  if (presets) presets.addEventListener('click', function (e) {
    var b = e.target.closest ? e.target.closest('[data-preset]') : null;
    if (!b) return;
    var want = b.getAttribute('data-preset').split(',');
    boxes.forEach(function (x) { x.checked = want.indexOf(x.value) >= 0; });
    summary();
  });
  form.addEventListener('input', summary);
  form.addEventListener('change', summary);
})();
