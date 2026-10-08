// A copy button on every block meant to be copied: tokens, invite links, join and
// MCP settings, shell commands, the terminal view. The blocks are plain text and
// can be selected by hand without this; the button is a convenience.
(function () {
  var SEL = '.inset.sel, [data-copy]';
  var NS = 'http://www.w3.org/2000/svg';

  function icon(id) {
    var s = document.createElementNS(NS, 'svg'), u = document.createElementNS(NS, 'use');
    s.setAttribute('class', 'icon'); s.setAttribute('aria-hidden', 'true');
    u.setAttribute('href', '#' + id); s.appendChild(u);
    return s;
  }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
    // plain http on a private network: the old way
    return new Promise(function (resolve, reject) {
      var t = document.createElement('textarea');
      t.value = text; t.setAttribute('readonly', ''); t.className = 'sprite';
      document.body.appendChild(t); t.select();
      var ok = false;
      try { ok = document.execCommand('copy'); } catch (e) {}
      document.body.removeChild(t);
      if (ok) resolve(); else reject(new Error('copy refused'));
    });
  }

  function enhance(el) {
    if (el.getAttribute('data-copy-ready')) return;
    el.setAttribute('data-copy-ready', '1');
    var wrap = document.createElement('div');
    wrap.className = 'copyable';
    el.parentNode.insertBefore(wrap, el);
    wrap.appendChild(el);
    var b = document.createElement('button');
    b.type = 'button'; b.className = 'copy-btn';
    b.setAttribute('aria-label', 'Copy to clipboard'); b.title = 'Copy';
    b.appendChild(icon('i-copy'));
    var status = document.createElement('span');
    status.className = 'sprite'; status.setAttribute('role', 'status');
    wrap.appendChild(b); wrap.appendChild(status);
    var timer = null;
    b.addEventListener('click', function () {
      var text = el.textContent.replace(/\s+$/, '');
      copyText(text).then(function () { done('i-check', 'Copied', status); }, function () {
        // could not write to the clipboard: select the text so Ctrl+C works
        var r = document.createRange(); r.selectNodeContents(el);
        var s = window.getSelection(); s.removeAllRanges(); s.addRange(r);
        done('i-circle-alert', 'Press Ctrl+C to copy', status);
      });
      function done(id, msg, st) {
        b.replaceChild(icon(id), b.firstChild); b.title = msg; st.textContent = msg;
        b.classList.toggle('done', id === 'i-check');
        clearTimeout(timer);
        timer = setTimeout(function () { b.replaceChild(icon('i-copy'), b.firstChild); b.title = 'Copy'; st.textContent = ''; b.classList.remove('done'); }, 1800);
      }
    });
  }

  function scan(root) {
    var found = (root.querySelectorAll ? root.querySelectorAll(SEL) : []);
    for (var i = 0; i < found.length; i++) enhance(found[i]);
    if (root.matches && root.matches(SEL)) enhance(root);
  }
  function start() {
    scan(document);
    // pages that refresh part of themselves bring new blocks
    if (window.MutationObserver) new MutationObserver(function (ms) {
      for (var i = 0; i < ms.length; i++) for (var j = 0; j < ms[i].addedNodes.length; j++) if (ms[i].addedNodes[j].nodeType === 1) scan(ms[i].addedNodes[j]);
    }).observe(document.body, { childList: true, subtree: true });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start); else start();
})();
