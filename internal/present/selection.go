package present

// SelectionScript is a JavaScript expression that evaluates to a JSON
// string {"text": "<selected text>", "section": "<heading>"}: the page's
// selected text, cut to 4000 characters, and the text of the nearest
// heading before the selection's start, or "". It needs nothing from the
// page runtime, so it also works in an isolated script world and in any
// HTML page.
const SelectionScript = `(function () {
  var sel = window.getSelection();
  var text = sel ? sel.toString() : '';
  var chars = Array.from(text);
  if (chars.length > 4000) text = chars.slice(0, 4000).join('');
  var section = '';
  if (sel && sel.rangeCount > 0) {
    var start = sel.getRangeAt(0).cloneRange();
    start.collapse(true);
    var heads = document.querySelectorAll('h1, h2, h3, h4, h5, h6');
    for (var i = 0; i < heads.length; i++) {
      try {
        if (start.comparePoint(heads[i], 0) > 0) break;
      } catch (e) {
        break;
      }
      section = heads[i].textContent.replace(/\s+/g, ' ').trim();
    }
  }
  return JSON.stringify({text: text, section: section});
})()`
