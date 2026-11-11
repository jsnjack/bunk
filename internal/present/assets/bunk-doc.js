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

  // ---- Libraries: charts (ECharts), graphs (Graphviz), code (highlight.js) ----

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  // source keeps what an element held before the runtime drew into it, so
  // it can draw it again in other colours.
  function source(el) {
    var src = el.getAttribute('data-bunk-src');
    if (src === null) {
      src = el.textContent;
      el.setAttribute('data-bunk-src', src);
    }
    return src;
  }

  var charts = [];
  var sizer = window.ResizeObserver ? new ResizeObserver(function (entries) {
    entries.forEach(function (e) {
      var c = window.echarts && echarts.getInstanceByDom(e.target);
      if (c) c.resize();
    });
  }) : null;

  // chartsDraw draws each pre.bunk-chart: an ECharts option as JSON, in
  // the theme's colours, data-height pixels high (360 by default).
  function chartsDraw(root) {
    charts = charts.filter(function (c) {
      if (c.isDisposed() || c.getDom().isConnected) return !c.isDisposed();
      c.dispose();
      return false;
    });
    var nodes = root.querySelectorAll('pre.bunk-chart:not([data-bunk-drawn])');
    if (!nodes.length) return Promise.resolve();
    nodes.forEach(source);
    return load('echarts.min.js').then(function () {
      nodes.forEach(function (pre) {
        var n = nth(pre, 'pre.bunk-chart');
        pre.setAttribute('data-bunk-drawn', '');
        var option;
        try {
          option = JSON.parse(source(pre));
        } catch (err) {
          problem('chart ' + n + ': the option is not JSON: ' + brief(err));
          return;
        }
        var box = make('div', 'bunk-chart-box');
        box.style.height = (parseInt(pre.getAttribute('data-height'), 10) || 360) + 'px';
        pre.textContent = '';
        pre.appendChild(box);
        try {
          var c = echarts.init(box, dark ? 'dark' : null, {renderer: 'svg'});
          if (option.backgroundColor === undefined) option.backgroundColor = 'transparent';
          if (option.animation === undefined) option.animation = false;
          c.setOption(option);
          charts.push(c);
          if (sizer) sizer.observe(box);
        } catch (err) {
          problem('chart ' + n + ': ' + brief(err));
        }
      });
    });
  }

  // graphs draws each pre.bunk-graphviz: a graph in Graphviz's DOT, its
  // lines and labels in the theme's colours unless the graph sets them.
  function graphs(root) {
    var nodes = root.querySelectorAll('pre.bunk-graphviz:not([data-bunk-drawn])');
    if (!nodes.length) return Promise.resolve();
    nodes.forEach(source);
    return load('viz-global.js').then(function () {
      return Viz.instance();
    }).then(function (viz) {
      var fg = cssVar('--bunk-fg') || (dark ? '#e6e8eb' : '#1f2328');
      var font = 'Helvetica,Arial,sans-serif';
      nodes.forEach(function (pre) {
        var n = nth(pre, 'pre.bunk-graphviz');
        pre.setAttribute('data-bunk-drawn', '');
        try {
          var svg = viz.renderSVGElement(source(pre), {
            graphAttributes: {bgcolor: 'transparent', fontname: font, fontcolor: fg},
            nodeAttributes: {fontname: font, color: fg, fontcolor: fg},
            edgeAttributes: {fontname: font, color: fg, fontcolor: fg}
          });
          pre.textContent = '';
          pre.appendChild(svg);
        } catch (err) {
          problem('Graphviz graph ' + n + ': ' + brief(err));
        }
      });
    });
  }

  // highlightCode colours the code of an HTML page that names its language
  // (<code class="language-go">); Markdown comes highlighted.
  function highlightCode(root) {
    if (markdown) return Promise.resolve();
    var nodes = Array.prototype.filter.call(
      root.querySelectorAll('pre > code[class*="language-"], pre > code[class*="lang-"]'),
      function (c) { return !c.classList.contains('hljs') && !c.closest('pre.chroma, pre.bunk-diff'); });
    if (!nodes.length) return Promise.resolve();
    codeStyle();
    return load('highlight/highlight.min.js').then(function () {
      nodes.forEach(function (c) {
        try {
          hljs.highlightElement(c);
        } catch (err) {
          warn(err);
        }
      });
    });
  }

  // codeStyle links highlight.js's GitHub style of the page's darkness.
  function codeStyle() {
    var l = document.getElementById('bunk-hljs-style');
    if (!l) {
      l = make('link');
      l.id = 'bunk-hljs-style';
      l.rel = 'stylesheet';
      document.head.appendChild(l);
    }
    l.href = base + (dark ? 'highlight/github-dark.min.css' : 'highlight/github.min.css');
  }

  // libraries the page's own scripts can load: bunkDoc.use('d3').then(...)
  var libraries = {
    d3: ['d3.min.js', function () { return window.d3; }],
    echarts: ['echarts.min.js', function () { return window.echarts; }],
    graphviz: ['viz-global.js', function () { return Viz.instance(); }],
    highlight: ['highlight/highlight.min.js', function () { return window.hljs; }],
    katex: ['katex/katex.min.js', function () { style(); return window.katex; }],
    mermaid: ['mermaid.min.js', function () { return window.mermaid; }]
  };

  function use(name) {
    var lib = libraries[name];
    if (!lib) return Promise.reject(new Error('bunk-doc: no library ' + name));
    return load(lib[0]).then(lib[1]);
  }

  // redraw draws the diagrams, charts and graphs again in the page's
  // colours, after the system's colour scheme changed the colours of a
  // page that follows it.
  function redraw() {
    if (isDark() === dark) return;
    dark = isDark();
    themeChanged();
    var root = doc();
    if (document.getElementById('bunk-hljs-style')) codeStyle();
    root.querySelectorAll('pre.bunk-chart[data-bunk-drawn], pre.bunk-graphviz[data-bunk-drawn]').forEach(function (pre) {
      var box = pre.querySelector('.bunk-chart-box');
      var c = box && window.echarts && echarts.getInstanceByDom(box);
      if (c) c.dispose();
      pre.textContent = pre.getAttribute('data-bunk-src');
      pre.removeAttribute('data-bunk-drawn');
    });
    chartsDraw(root).catch(warn);
    graphs(root).catch(warn);
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
    try {
      kit(root);
    } catch (err) {
      warn(err);
    }
    return Promise.all([
      typeset(root).catch(warn), diagrams(root).catch(warn), chartsDraw(root).catch(warn),
      graphs(root).catch(warn), highlightCode(root).catch(warn)
    ]).then(function () {
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

  // ---- The kit: components a page switches on with markup ----
  //
  //   table.bunk-sort          headers sort the rows (every Markdown table)
  //   table.bunk-filter        a box above filters the rows (Markdown
  //                            tables of more than 10 rows)
  //   div.bunk-tabs > section[data-title]     tabs
  //   div.bunk-steps > section[data-title]    one step at a time
  //   nav.bunk-toc             an outline of the h2 and h3 headings
  //                            ([TOC] alone in a Markdown paragraph)
  //   pre.bunk-diff            a unified diff, its lines coloured
  //                            (```diff in Markdown)
  //   pre > code               a copy button
  //
  // Tabs and steps are role="tab" buttons, so bunk-gui's panel keeps the
  // one chosen when the page reloads. Each element is set up once.

  var markdown = !!document.querySelector('#bunk-doc.bunk-md');

  function once(el, what) {
    var done = (el.getAttribute('data-bunk-kit') || '').split(' ');
    if (done.indexOf(what) >= 0) return false;
    done.push(what);
    el.setAttribute('data-bunk-kit', done.join(' ').trim());
    return true;
  }

  function make(tag, cls, text) {
    var el = document.createElement(tag);
    if (cls) el.className = cls;
    if (text !== undefined) el.textContent = text;
    return el;
  }

  function slugify(s) {
    return String(s).toLowerCase().replace(/[^a-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '') || 'item';
  }

  function kit(root) {
    root.querySelectorAll('table').forEach(function (t) {
      var rows = t.tBodies.length ? t.tBodies[0].rows.length : 0;
      if (t.classList.contains('bunk-sort') || (markdown && rows > 1)) sortable(t);
      if (t.classList.contains('bunk-filter') || (markdown && rows > 10)) filterable(t);
    });
    root.querySelectorAll('.bunk-tabs').forEach(function (el) { tabs(el, false); });
    root.querySelectorAll('.bunk-steps').forEach(function (el) { tabs(el, true); });
    root.querySelectorAll('nav.bunk-toc').forEach(outline);
    root.querySelectorAll('pre.bunk-diff').forEach(diffLines);
    root.querySelectorAll('pre').forEach(copyButton);
  }

  // cellValue is a cell's sort key: a number when the text is one (with
  // thousands separators, a sign, a unit or % after it), else the text.
  function cellValue(cell) {
    var text = (cell ? cell.textContent : '').trim();
    var m = text.replace(/[,\u00a0\u202f ]/g, '').match(/^([-+−]?\d*\.?\d+(?:e[-+]?\d+)?)\s*[%a-zA-Zµ]*$/);
    return m ? {n: parseFloat(m[1].replace('−', '-')), t: text} : {n: NaN, t: text};
  }

  function sortable(t) {
    if (!t.tHead || !t.tBodies.length || !once(t, 'sort')) return;
    t.classList.add('bunk-sort');
    var heads = t.tHead.rows[0] ? t.tHead.rows[0].cells : [];
    Array.prototype.forEach.call(heads, function (th, col) {
      th.tabIndex = 0;
      th.title = 'Sort by this column';
      var go = function () {
        var dir = th.getAttribute('aria-sort') === 'ascending' ? 'descending' : 'ascending';
        Array.prototype.forEach.call(heads, function (h) { h.removeAttribute('aria-sort'); });
        th.setAttribute('aria-sort', dir);
        var body = t.tBodies[0];
        var rows = Array.prototype.slice.call(body.rows);
        var keys = rows.map(function (r) { return cellValue(r.cells[col]); });
        var numeric = keys.every(function (k) { return !isNaN(k.n) || k.t === ''; });
        var order = rows.map(function (r, i) { return i; });
        order.sort(function (a, b) {
          var x = keys[a], y = keys[b];
          var c = numeric ? (isNaN(x.n) ? -Infinity : x.n) - (isNaN(y.n) ? -Infinity : y.n) :
            x.t.localeCompare(y.t, undefined, {numeric: true, sensitivity: 'base'});
          return (dir === 'ascending' ? c : -c) || a - b;
        });
        order.forEach(function (i) { body.appendChild(rows[i]); });
      };
      th.addEventListener('click', go);
      th.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          go();
        }
      });
    });
  }

  function filterable(t) {
    if (!t.tBodies.length || !once(t, 'filter')) return;
    var box = make('input', 'bunk-filter-box');
    box.type = 'search';
    box.placeholder = 'Filter rows';
    box.setAttribute('aria-label', 'Filter the rows of the table below');
    var count = make('span', 'bunk-filter-count');
    var bar = make('div', 'bunk-filter-bar');
    bar.appendChild(box);
    bar.appendChild(count);
    t.parentNode.insertBefore(bar, t);
    var apply = function () {
      var q = box.value.trim().toLowerCase();
      var rows = t.tBodies[0].rows;
      var shown = 0;
      Array.prototype.forEach.call(rows, function (r) {
        var hit = !q || r.textContent.toLowerCase().indexOf(q) >= 0;
        r.hidden = !hit;
        if (hit) shown++;
      });
      count.textContent = q ? shown + ' of ' + rows.length : '';
    };
    box.addEventListener('input', apply);
  }

  // tabs makes the sections of el tabs, or with steps a step-through:
  // numbered buttons, Back and Next, one section shown.
  function tabs(el, steps) {
    if (!once(el, steps ? 'steps' : 'tabs')) return;
    var panes = Array.prototype.filter.call(el.children, function (c) { return c.tagName === 'SECTION'; });
    if (!panes.length) return;
    var list = make('div', 'bunk-tablist');
    list.setAttribute('role', 'tablist');
    var base = el.id || slugify((panes[0].getAttribute('data-title') || 'tabs')) + '-' + nth(el, steps ? '.bunk-steps' : '.bunk-tabs');
    var buttons = panes.map(function (p, i) {
      var head = p.querySelector('h2, h3, h4');
      var title = p.getAttribute('data-title') || (head ? head.textContent.trim() : (steps ? 'Step ' : 'Tab ') + (i + 1));
      var b = make('button', 'bunk-tab', steps ? String(i + 1) : title);
      b.type = 'button';
      b.id = base + '-tab-' + (i + 1);
      b.title = title;
      b.setAttribute('role', 'tab');
      p.id = p.id || base + '-panel-' + (i + 1);
      p.setAttribute('role', 'tabpanel');
      p.setAttribute('aria-labelledby', b.id);
      b.setAttribute('aria-controls', p.id);
      list.appendChild(b);
      return b;
    });
    var caption = steps ? make('span', 'bunk-step-title') : null;
    var prev = steps ? make('button', 'bunk-step-move', '‹ Back') : null;
    var next = steps ? make('button', 'bunk-step-move', 'Next ›') : null;
    var cur = 0;
    var show = function (i) {
      cur = Math.max(0, Math.min(panes.length - 1, i));
      panes.forEach(function (p, j) { p.hidden = j !== cur; });
      buttons.forEach(function (b, j) {
        b.setAttribute('aria-selected', j === cur ? 'true' : 'false');
        b.tabIndex = j === cur ? 0 : -1;
      });
      if (steps) {
        caption.textContent = 'Step ' + (cur + 1) + ' of ' + panes.length + ': ' + buttons[cur].title;
        prev.disabled = cur === 0;
        next.disabled = cur === panes.length - 1;
      }
    };
    buttons.forEach(function (b, i) {
      b.addEventListener('click', function () { show(i); });
      b.addEventListener('keydown', function (e) {
        var to = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : -1;
        if (to >= 0 && to < buttons.length) {
          e.preventDefault();
          buttons[to].click();
          buttons[to].focus();
        }
      });
    });
    var bar = make('div', steps ? 'bunk-step-bar' : 'bunk-tab-bar');
    if (steps) {
      prev.type = next.type = 'button';
      prev.addEventListener('click', function () { buttons[Math.max(0, cur - 1)].click(); });
      next.addEventListener('click', function () { buttons[Math.min(buttons.length - 1, cur + 1)].click(); });
      bar.appendChild(prev);
      bar.appendChild(list);
      bar.appendChild(next);
      el.insertBefore(caption, el.firstChild);
    } else {
      bar.appendChild(list);
    }
    el.insertBefore(bar, el.firstChild);
    var first = panes.findIndex(function (p) { return p.hasAttribute('data-selected'); });
    show(first < 0 ? 0 : first);
  }

  function outline(nav) {
    if (!once(nav, 'toc')) return;
    var heads = doc().querySelectorAll('h2[id], h3[id]');
    if (!heads.length) return;
    nav.appendChild(make('p', 'bunk-toc-title', nav.getAttribute('data-title') || 'Contents'));
    var list = make('ul');
    heads.forEach(function (h) {
      if (nav.contains(h)) return;
      var li = make('li', h.tagName === 'H3' ? 'bunk-toc-sub' : '');
      var a = make('a', '', h.textContent.trim());
      a.href = '#' + h.id;
      li.appendChild(a);
      list.appendChild(li);
    });
    nav.appendChild(list);
  }

  // diffLines colours each line of a unified diff that the page wrote as
  // plain text; Markdown's diff fences come coloured.
  function diffLines(pre) {
    if (!once(pre, 'diff') || pre.querySelector('.d-add, .d-del, .d-ctx')) return;
    var text = pre.textContent;
    pre.textContent = '';
    var code = make('code');
    text.replace(/\n$/, '').split('\n').forEach(function (line) {
      code.appendChild(make('span', diffClass(line), line + '\n'));
    });
    pre.appendChild(code);
  }

  function diffClass(line) {
    if (/^(diff |index |--- |\+\+\+ )/.test(line)) return 'd-file';
    if (/^@@/.test(line)) return 'd-hunk';
    if (line[0] === '+') return 'd-add';
    if (line[0] === '-') return 'd-del';
    return 'd-ctx';
  }

  function copyButton(pre) {
    if (pre.classList.contains('mermaid') || !pre.querySelector('code') || !once(pre, 'copy')) return;
    var wrap = make('div', 'bunk-code');
    pre.parentNode.insertBefore(wrap, pre);
    wrap.appendChild(pre);
    var b = make('button', 'bunk-copy', 'Copy');
    b.type = 'button';
    b.title = 'Copy the code';
    var said = function (text) {
      b.textContent = text;
      setTimeout(function () { b.textContent = 'Copy'; }, 1500);
    };
    b.addEventListener('click', function () {
      var text = pre.querySelector('code').innerText.replace(/\n$/, '');
      var select = function () {
        var r = document.createRange();
        r.selectNodeContents(pre.querySelector('code'));
        var s = window.getSelection();
        s.removeAllRanges();
        s.addRange(r);
        said('Press Ctrl+C');
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(function () { said('Copied'); }, select);
      } else {
        select();
      }
    });
    wrap.appendChild(b);
  }

  // ---- Replies: the page asks bunk-gui to paste a reply into the agent's input ----
  //
  // A click on an element with data-bunk-reply="TEXT" (its text when the
  // attribute is empty), or bunkDoc.reply(TEXT) from the page's script,
  // opens bunk-gui's question popover with TEXT and the section it came
  // from; the user pastes it with Enter, and sends it in the pane. bunk-gui
  // takes a reply only just after the user clicked or typed in the page. In
  // a browser the reply goes to the clipboard.

  function sectionOf(el) {
    var heads = doc().querySelectorAll('h1, h2, h3, h4, h5, h6');
    var found = '';
    for (var i = 0; i < heads.length; i++) {
      if (el && heads[i].compareDocumentPosition(el) & Node.DOCUMENT_POSITION_PRECEDING) break;
      found = heads[i].textContent.replace(/\s+/g, ' ').trim();
    }
    return found;
  }

  function reply(text, from) {
    text = String(text === undefined ? '' : text).trim();
    if (!text) return false;
    var h = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.bunkReply;
    if (h) {
      h.postMessage(JSON.stringify({text: text, section: sectionOf(from || document.activeElement)}));
      return true;
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () {
        note('Copied the reply. Paste it into the agent.');
      }, function () {
        note('Reply: ' + text);
      });
    } else {
      note('Reply: ' + text);
    }
    return false;
  }

  // note shows a short message at the bottom of the page.
  function note(text) {
    var n = document.getElementById('bunk-note') || make('div', 'bunk-note');
    n.id = 'bunk-note';
    n.textContent = text;
    document.body.appendChild(n);
    clearTimeout(note.timer);
    note.timer = setTimeout(function () { n.remove(); }, 3000);
  }

  document.addEventListener('click', function (e) {
    var el = e.target.closest ? e.target.closest('[data-bunk-reply]') : null;
    if (!el) return;
    e.preventDefault();
    reply(el.getAttribute('data-bunk-reply') || el.textContent, el);
  });

  // A page that follows the system's colour scheme tells its own scripts
  // when it changes: bunk:theme on the document, detail.dark.
  function themeChanged() {
    document.dispatchEvent(new CustomEvent('bunk:theme', {detail: {dark: dark}}));
  }

  window.bunkDoc = {
    render: render, anchor: anchor, restore: restore, replace: replace, use: use, reply: reply,
    dark: function () { return dark; }
  };
  // An HTML page may load the runtime in its <head>, before the body.
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { render(); });
  } else {
    render();
  }
})();
