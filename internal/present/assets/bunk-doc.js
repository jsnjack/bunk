// The runtime of pages that bunk present renders from Markdown, which
// agents' HTML pages load too. It typesets math with KaTeX (.math and
// .math.math-display elements) and draws Mermaid diagrams (pre.mermaid),
// loading each library only for a page that needs it, and lets bunk-gui
// swap the document's content while the reader keeps their place.
// bunk-gui calls window.bunkDoc.
(function () {
  'use strict';

  var self = document.currentScript;
  var base = self ? self.src.replace(/[^/]*$/, '') : '';
  var scheme = window.matchMedia('(prefers-color-scheme: dark)');
  var dark = isDark();
  var loading = {};
  var mermaidReady = false;

  // isDark reports whether the page is dark. A rendered Markdown page in
  // bunk-gui's panel names its theme, and any page there gets --bunk-dark.
  // A page in a browser gets --bunk-dark by the system's colour scheme, so
  // it can change; a page without either asks the system.
  function isDark() {
    var theme = document.documentElement.getAttribute('data-bunk-theme');
    if (theme) return theme === 'dark';
    var flag = getComputedStyle(document.documentElement).getPropertyValue('--bunk-dark').trim();
    return flag ? flag === '1' : scheme.matches;
  }

  function doc() {
    return document.getElementById('bunk-doc') || document.body;
  }

  function load(name) {
    if (!loading[name]) {
      loading[name] = new Promise(function (resolve, reject) {
        var s = document.createElement('script');
        s.src = base + name;
        s.onload = function () { resolve(); };
        s.onerror = function () { reject(new Error('cannot load ' + name)); };
        document.head.appendChild(s);
      });
    }
    return loading[name];
  }

  // style adds KaTeX's stylesheet once, unless the page links it already,
  // as a rendered Markdown page does. Without it KaTeX's MathML copy of
  // each formula shows beside the formula.
  function style() {
    if (document.querySelector('link[href$="katex/katex.min.css"]')) return;
    var l = document.createElement('link');
    l.rel = 'stylesheet';
    l.href = base + 'katex/katex.min.css';
    document.head.appendChild(l);
  }

  function warn(err) {
    console.warn('bunk-doc:', err);
  }

  // problem records what went wrong drawing the page in the
  // data-bunk-problems attribute of <html>, which bunk-gui reads and
  // reports to the agent that presented it.
  function problem(msg) {
    var html = document.documentElement;
    var list = [];
    try {
      list = JSON.parse(html.getAttribute('data-bunk-problems') || '[]');
    } catch (err) {
      list = [];
    }
    msg = String(msg).replace(/\s+/g, ' ').trim();
    if (list.length < 20 && list.indexOf(msg) < 0) list.push(msg);
    html.setAttribute('data-bunk-problems', JSON.stringify(list));
  }

  // brief is an error's message cut to its first and last lines, which
  // for a parser name the place and what it expected.
  function brief(err) {
    var lines = String(err && err.message ? err.message : err).split('\n').filter(function (l) {
      return l.trim() !== '';
    });
    if (lines.length <= 1) return lines.join('');
    return lines[0] + ' ' + lines[lines.length - 1];
  }

  // nth is the place of el among the page's elements that match sel,
  // counted from 1, to name it in a problem.
  function nth(el, sel) {
    return Array.prototype.indexOf.call(doc().querySelectorAll(sel), el) + 1;
  }

  function typeset(root) {
    var nodes = root.querySelectorAll('.math:not([data-bunk-tex])');
    if (!nodes.length) return Promise.resolve();
    style();
    return load('katex/katex.min.js').then(function () {
      nodes.forEach(function (el) {
        var tex = el.textContent;
        el.setAttribute('data-bunk-tex', '');
        var display = el.classList.contains('math-display');
        try {
          katex.render(tex, el, {displayMode: display, throwOnError: true});
        } catch (err) {
          problem('math ' + nth(el, '.math') + ': ' + brief(err));
          try {
            // Shows the source in red where it fails.
            katex.render(tex, el, {displayMode: display, throwOnError: false});
          } catch (err2) {
            el.textContent = tex;
            warn(err2);
          }
        }
      });
    });
  }

  function diagrams(root) {
    var nodes = root.querySelectorAll('pre.mermaid:not([data-processed])');
    if (!nodes.length) return Promise.resolve();
    // Keep each diagram's source, so redraw can draw it again.
    nodes.forEach(function (el) {
      if (!el.hasAttribute('data-bunk-src')) el.setAttribute('data-bunk-src', el.textContent);
    });
    return load('mermaid.min.js').then(function () {
      if (!mermaidReady) {
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: 'strict',
          theme: dark ? 'dark' : 'default'
        });
        mermaidReady = true;
      }
      // Parse first: run draws a broken diagram as an error picture and
      // says nothing.
      return Promise.all(Array.prototype.map.call(nodes, function (el) {
        return Promise.resolve(mermaid.parse(el.getAttribute('data-bunk-src'))).catch(function (err) {
          problem('Mermaid diagram ' + nth(el, 'pre.mermaid') + ': ' + brief(err));
        });
      })).then(function () {
        return mermaid.run({nodes: nodes, suppressErrors: true});
      });
    });
  }

  // redraw draws the diagrams again in the page's colours, after the
  // system's colour scheme changed the colours of a page that follows it.
  function redraw() {
    if (isDark() === dark) return;
    dark = isDark();
    if (!mermaidReady) return;
    mermaidReady = false;
    var root = doc();
    root.querySelectorAll('pre.mermaid[data-processed][data-bunk-src]').forEach(function (el) {
      el.textContent = el.getAttribute('data-bunk-src');
      el.removeAttribute('data-processed');
    });
    diagrams(root).catch(warn);
  }

  // render typesets every math element and draws every diagram not done
  // yet. The promise settles when both are done.
  // <html> gets data-bunk-rendered once they are, for bunk-gui.
  function render() {
    var root = doc();
    return Promise.all([typeset(root).catch(warn), diagrams(root).catch(warn)]).then(function () {
      document.documentElement.setAttribute('data-bunk-rendered', '');
    });
  }

  // place returns the topmost heading at or above the top of the viewport
  // and how far the page is scrolled past it.
  function place() {
    var heads = doc().querySelectorAll('h1[id], h2[id], h3[id], h4[id], h5[id], h6[id]');
    var best = null;
    var top = 0;
    for (var i = 0; i < heads.length; i++) {
      var t = heads[i].getBoundingClientRect().top;
      if (t > 1) break;
      best = heads[i];
      top = t;
    }
    if (!best) return {id: '', offset: window.scrollY};
    return {id: best.id, offset: -top};
  }

  function anchor() {
    return JSON.stringify(place());
  }

  // restore scrolls back to an anchor from anchor(). When its heading is
  // gone, the page stays where it is.
  function restore(a) {
    if (typeof a === 'string') {
      try {
        a = JSON.parse(a);
      } catch (err) {
        warn(err);
        return;
      }
    }
    if (!a) return;
    var y = Number(a.offset) || 0;
    if (a.id) {
      var el = document.getElementById(a.id);
      if (!el) return;
      y += el.getBoundingClientRect().top + window.scrollY;
    }
    window.scrollTo(0, y);
  }

  // replace swaps the document's content for html, keeps the reader's
  // place, and marks the headings of sections that changed. A mark fades at
  // the next replace. It returns the number of changed sections.
  function replace(html) {
    var root = doc();
    var a = place();
    var before = {};
    var marked = {};
    root.querySelectorAll('[data-bunk-hash]').forEach(function (h) {
      var hash = h.getAttribute('data-bunk-hash');
      before[hash] = true;
      if (h.classList.contains('bunk-changed')) marked[hash] = true;
    });
    document.documentElement.removeAttribute('data-bunk-problems');
    document.documentElement.removeAttribute('data-bunk-rendered');
    root.innerHTML = html;
    var changed = 0;
    root.querySelectorAll('[data-bunk-hash]').forEach(function (h) {
      var hash = h.getAttribute('data-bunk-hash');
      if (!before[hash]) {
        h.classList.add('bunk-changed');
        changed++;
      } else if (marked[hash]) {
        h.classList.add('bunk-fading');
      }
    });
    restore(a);
    var y = window.scrollY;
    // Diagrams change the page's height as they draw; follow the anchor
    // unless the reader scrolled meanwhile.
    render().then(function () {
      if (Math.abs(window.scrollY - y) < 2) restore(a);
    });
    return changed;
  }

  // A <base> element sends fragment links to the document's directory, so
  // they scroll here instead.
  document.addEventListener('click', function (e) {
    var link = e.target.closest ? e.target.closest('a[href^="#"]') : null;
    if (!link) return;
    var id = link.getAttribute('href').slice(1);
    try {
      id = decodeURIComponent(id);
    } catch (err) {
      warn(err);
    }
    var target = document.getElementById(id);
    if (!target) return;
    e.preventDefault();
    target.scrollIntoView();
  });

  if (scheme.addEventListener) scheme.addEventListener('change', redraw);

  window.bunkDoc = {render: render, anchor: anchor, restore: restore, replace: replace};
  // An HTML page may load the runtime in its <head>, before the body.
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { render(); });
  } else {
    render();
  }
})();
