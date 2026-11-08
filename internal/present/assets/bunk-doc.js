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
  // A rendered Markdown page names its theme; an agent's HTML page in
  // bunk-gui's panel gets --bunk-dark, and elsewhere asks the system.
  var theme = document.documentElement.getAttribute('data-bunk-theme');
  var flag = getComputedStyle(document.documentElement).getPropertyValue('--bunk-dark').trim();
  var dark = theme ? theme === 'dark' :
    flag ? flag === '1' : window.matchMedia('(prefers-color-scheme: dark)').matches;
  var loading = {};
  var mermaidReady = false;

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

  function typeset(root) {
    var nodes = root.querySelectorAll('.math:not([data-bunk-tex])');
    if (!nodes.length) return Promise.resolve();
    style();
    return load('katex/katex.min.js').then(function () {
      nodes.forEach(function (el) {
        var tex = el.textContent;
        el.setAttribute('data-bunk-tex', '');
        try {
          katex.render(tex, el, {
            displayMode: el.classList.contains('math-display'),
            throwOnError: false
          });
        } catch (err) {
          el.textContent = tex;
          warn(err);
        }
      });
    });
  }

  function diagrams(root) {
    var nodes = root.querySelectorAll('pre.mermaid:not([data-processed])');
    if (!nodes.length) return Promise.resolve();
    return load('mermaid.min.js').then(function () {
      if (!mermaidReady) {
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: 'strict',
          theme: dark ? 'dark' : 'default'
        });
        mermaidReady = true;
      }
      return mermaid.run({nodes: nodes, suppressErrors: true});
    });
  }

  // render typesets every math element and draws every diagram not done
  // yet. The promise settles when both are done.
  function render() {
    var root = doc();
    return Promise.all([typeset(root).catch(warn), diagrams(root).catch(warn)]);
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

  window.bunkDoc = {render: render, anchor: anchor, restore: restore, replace: replace};
  // An HTML page may load the runtime in its <head>, before the body.
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { render(); });
  } else {
    render();
  }
})();
