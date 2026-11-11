package present

// MarkQuotesScript is a format for a JavaScript expression in bunk-gui's
// isolated script world: %s takes a JSON array of quotes, passages the
// reader commented on in a review. It highlights each quote's first
// occurrence in the page with the CSS Custom Highlight API, which leaves
// the page's DOM alone, and evaluates to the number it found. An empty
// array clears the highlights. Whitespace counts as one space, so a quote
// that spans paragraphs still matches.
const MarkQuotesScript = `(function (quotes) {
  if (!window.CSS || !CSS.highlights || typeof Highlight === 'undefined') return -1;
  if (!document.getElementById('bunk-review-style')) {
    var st = document.createElement('style');
    st.id = 'bunk-review-style';
    st.textContent = '::highlight(bunk-review) { background-color: rgba(212, 167, 44, 0.4); }';
    (document.head || document.documentElement).appendChild(st);
  }
  var text = '', map = [];
  var walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (var n = walker.nextNode(); n; n = walker.nextNode()) {
    var p = n.parentNode && n.parentNode.nodeName;
    if (p === 'SCRIPT' || p === 'STYLE') continue;
    for (var i = 0; i < n.data.length; i++) {
      var ch = n.data[i];
      if (/\s/.test(ch)) {
        if (text === '' || text[text.length - 1] === ' ') continue;
        ch = ' ';
      }
      text += ch;
      map.push([n, i]);
    }
    if (text !== '' && text[text.length - 1] !== ' ') {
      text += ' ';
      map.push([n, n.data.length]);
    }
  }
  var ranges = [];
  quotes.forEach(function (q) {
    var want = String(q).replace(/\s+/g, ' ').trim();
    if (!want) return;
    var at = text.indexOf(want);
    if (at < 0 && want.length > 120) {
      want = want.slice(0, 120);
      at = text.indexOf(want);
    }
    if (at < 0) return;
    var a = map[at], b = map[at + want.length - 1];
    var r = document.createRange();
    r.setStart(a[0], a[1]);
    r.setEnd(b[0], Math.min(b[1] + 1, b[0].data.length));
    ranges.push(r);
  });
  if (ranges.length) CSS.highlights.set('bunk-review', new Highlight(...ranges));
  else CSS.highlights.delete('bunk-review');
  return ranges.length;
})(%s)`
