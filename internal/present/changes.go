package present

// The sections of a page, to tell what an edit changed: bunk-gui reads
// them before an HTML page loads again or a Markdown page swaps its
// content, and again after, and compares them (both in its isolated
// script world).

// SectionsScript is a JavaScript expression that evaluates to a JSON
// array of the page's sections, [{"key", "title", "text"}]: one per h1-h4
// heading of the article (else the body), its text from the heading to
// the next one with whitespace collapsed, and first the text before the
// first heading (key "", title ""). key is the heading's id, else its
// title.
const SectionsScript = `(function () {
  var root = document.getElementById('bunk-doc') || document.body;
  if (!root) return '[]';
  var heads = Array.prototype.slice.call(root.querySelectorAll('h1, h2, h3, h4'));
  var clean = function (s) { return String(s).replace(/\s+/g, ' ').trim().slice(0, 20000); };
  // A section's text as its source has it: diagrams, charts, graphs and
  // math as their source, which they are before the runtime draws them,
  // and without the kit's own controls.
  var between = function (from, to) {
    var r = document.createRange();
    if (from) r.setStartAfter(from); else r.setStart(root, 0);
    if (to) r.setEndBefore(to); else r.setEnd(root, root.childNodes.length);
    var box = document.createElement('div');
    box.appendChild(r.cloneContents());
    box.querySelectorAll('pre.mermaid[data-bunk-src], pre.bunk-chart[data-bunk-src], pre.bunk-graphviz[data-bunk-src]').forEach(function (el) {
      el.textContent = el.getAttribute('data-bunk-src');
    });
    box.querySelectorAll('.math[data-bunk-tex]').forEach(function (el) {
      el.textContent = el.getAttribute('data-bunk-tex');
    });
    box.querySelectorAll('.bunk-copy, .bunk-filter-bar, .bunk-tab-bar, .bunk-step-bar, .bunk-step-title, nav.bunk-toc, .bunk-note, script, style').forEach(function (el) {
      el.remove();
    });
    return clean(box.textContent);
  };
  var out = [{key: '', title: '', text: between(null, heads[0] || null)}];
  heads.forEach(function (h, i) {
    var title = clean(h.textContent);
    out.push({key: h.id || title, title: title, text: between(h, heads[i + 1] || null)});
  });
  return JSON.stringify(out);
})()`

// MarkChangesScript is a format for a JavaScript expression: %s takes a
// JSON array of section keys. It marks the headings of those sections
// with data-bunk-changed, which a bar beside them shows (bunk-doc.css, or
// a style it adds for pages without it), clears the marks of others, and
// evaluates to the number it marked.
const MarkChangesScript = `(function (keys) {
  var root = document.getElementById('bunk-doc') || document.body;
  if (!root) return 0;
  if (!document.getElementById('bunk-change-style')) {
    var st = document.createElement('style');
    st.id = 'bunk-change-style';
    st.textContent = '[data-bunk-changed] { position: relative; }' +
      '[data-bunk-changed]::before { content: ""; position: absolute; top: 0; bottom: 0; left: -10px; width: 4px;' +
      ' border-radius: 2px; background: var(--bunk-accent, #5aa9ff); }';
    (document.head || document.documentElement).appendChild(st);
  }
  var n = 0;
  root.querySelectorAll('h1, h2, h3, h4').forEach(function (h) {
    var key = h.id || String(h.textContent).replace(/\s+/g, ' ').trim().slice(0, 20000);
    if (keys.indexOf(key) >= 0) {
      h.setAttribute('data-bunk-changed', '');
      n++;
    } else {
      h.removeAttribute('data-bunk-changed');
    }
  });
  return n;
})(%s)`
