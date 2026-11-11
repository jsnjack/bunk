// Keeps a document's browser copy current. bunk-gui writes the copy again,
// with a new version.js beside it, each time the document changes while
// the panel lists it; this script, which only copies load, reads
// version.js every 1.5 s and reloads the page when the version changes.
// A script element, because browsers do not let a file: page fetch files.
(function () {
  'use strict';
  if (location.protocol !== 'file:') return;
  var url = new URL('version.js', location.href).href;
  var seen = null;
  window.bunkVersion = function (v) {
    if (seen === null) seen = v;
    else if (v !== seen) location.reload();
  };
  function poll() {
    var s = document.createElement('script');
    s.src = url + '?t=' + Date.now();
    s.onload = s.onerror = function () {
      s.remove();
      setTimeout(poll, 1500);
    };
    document.head.appendChild(s);
  }
  poll();
})();
