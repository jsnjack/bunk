package present

// The problems of a page, which bunk-gui shows in the panel's status line
// and reports to "bunk present", so an agent learns that its page is
// broken: scripts that threw, content the page's policy blocked, files it
// could not load (bunk-gui sees those itself) and, from the page runtime,
// diagrams and formulas that do not parse.

// ProblemCatcherScript runs in bunk-gui's isolated script world before
// each page's own scripts. It records the page's script errors, unhandled
// promise rejections and policy violations; DOM events reach listeners of
// every world. ProblemsScript reads them.
const ProblemCatcherScript = `(function () {
  if (window.__bunkProblems) return;
  var list = [];
  function add(m) {
    m = String(m).replace(/\s+/g, ' ').trim();
    if (m && list.length < 20 && list.indexOf(m) < 0) list.push(m);
  }
  window.addEventListener('error', function (e) {
    if (e.target && e.target !== window) return; // a file that did not load: bunk-gui saw it
    var where = e.filename ? ' (' + e.filename.replace(/^.*\//, '') + (e.lineno ? ':' + e.lineno : '') + ')' : '';
    add('script error: ' + (e.message || 'unknown') + where);
  }, true);
  window.addEventListener('unhandledrejection', function (e) {
    var r = e.reason;
    add('unhandled promise rejection: ' + (r && r.message ? r.message : r));
  });
  document.addEventListener('securitypolicyviolation', function (e) {
    var what = e.blockedURI || 'code';
    add('blocked ' + what + ' (' + e.violatedDirective + ')' +
      (/^https?:/.test(what) ? ': pages load nothing from the network' : ''));
  });
  window.__bunkProblems = function (reset) {
    var l = list.slice();
    if (reset) list = [];
    return l;
  };
})();`

// ProblemsScript is a JavaScript expression for bunk-gui's isolated script
// world. It evaluates to "wait" while the page is still loading, or while
// the page runtime (bunk-doc.js) is still drawing its diagrams and math;
// else to a JSON array of the page's problems: ProblemCatcherScript's and
// the runtime's (the data-bunk-problems attribute of <html>).
const ProblemsScript = `(function () {
  var html = document.documentElement;
  var runtime = !!document.querySelector('script[src$="bunk-doc.js"]');
  if (document.readyState !== 'complete' || (runtime && !html.hasAttribute('data-bunk-rendered'))) return 'wait';
  var p = window.__bunkProblems ? window.__bunkProblems(false) : [];
  try {
    JSON.parse(html.getAttribute('data-bunk-problems') || '[]').forEach(function (m) {
      if (p.indexOf(m) < 0) p.push(m);
    });
  } catch (e) {}
  return JSON.stringify(p);
})()`

// ResetProblemsScript forgets the problems ProblemCatcherScript recorded,
// before bunk-gui swaps a Markdown page's content.
const ResetProblemsScript = `(window.__bunkProblems && window.__bunkProblems(true), 'ok')`
