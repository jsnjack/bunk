package present

// The state of a page that a reload would lose: which <details> are open,
// what form controls hold (inputs, selects, text areas; not files or
// passwords) and which tabs (role="tab") are selected. bunk-gui reads it
// with SaveStateScript just before an HTML page loads again after its file
// changed, and gives it back with RestoreStateScript once the new page has
// loaded, so the reader keeps the sections they opened, the tab they chose
// and what they typed. Elements are matched by id, a control by its name
// and value, else by their place among their kind, which holds while the
// agent's edit leaves the elements before them alone. Both scripts need
// nothing from the page, so they run in an isolated script world.

// SaveStateScript is a JavaScript expression that evaluates to the page's
// state as a JSON string.
const SaveStateScript = `(function () {
  ` + stateKeys + `
  var s = {details: {}, inputs: {}, tabs: []};
  document.querySelectorAll('details').forEach(function (d, i) {
    s.details[detailsKey(d, i)] = d.open;
  });
  document.querySelectorAll('input, select, textarea').forEach(function (e, i) {
    if (e.type === 'file' || e.type === 'password' || e.type === 'hidden') return;
    if (e.type === 'checkbox' || e.type === 'radio') s.inputs[inputKey(e, i)] = {c: e.checked};
    else s.inputs[inputKey(e, i)] = {v: e.value};
  });
  document.querySelectorAll('[role="tab"][aria-selected="true"]').forEach(function (t) {
    s.tabs.push(tabKey(t));
  });
  return JSON.stringify(s);
})()`

// RestoreStateScript is a format for a JavaScript expression: %s takes
// the JSON that SaveStateScript returned. It opens and closes the
// <details> as they were, sets the controls and tells the page with input
// and change events, and clicks the tabs that were selected, so the page's
// own scripts show them. It evaluates to the number of elements it
// changed.
const RestoreStateScript = `(function (s) {
  ` + stateKeys + `
  if (!s) return 0;
  var n = 0;
  document.querySelectorAll('details').forEach(function (d, i) {
    var k = detailsKey(d, i);
    if (k in s.details && d.open !== s.details[k]) { d.open = s.details[k]; n++; }
  });
  document.querySelectorAll('input, select, textarea').forEach(function (e, i) {
    var v = s.inputs[inputKey(e, i)];
    if (!v) return;
    if ('c' in v) {
      if (e.checked === v.c) return;
      e.checked = v.c;
    } else {
      if (e.value === v.v) return;
      e.value = v.v;
    }
    e.dispatchEvent(new Event('input', {bubbles: true}));
    e.dispatchEvent(new Event('change', {bubbles: true}));
    n++;
  });
  document.querySelectorAll('[role="tab"]').forEach(function (t) {
    if (t.getAttribute('aria-selected') !== 'true' && s.tabs.indexOf(tabKey(t)) >= 0) { t.click(); n++; }
  });
  return n;
})(%s)`

// stateKeys names elements for both scripts.
const stateKeys = `function detailsKey(d, i) { return d.id ? '#' + d.id : 'd' + i; }
  function inputKey(e, i) {
    if (e.id) return '#' + e.id;
    if (e.name) return 'n:' + e.name + (e.type === 'radio' || e.type === 'checkbox' ? ':' + e.value : '');
    return 'i' + i;
  }
  function tabKey(t) { return t.id ? '#' + t.id : 't:' + (t.textContent || '').trim().slice(0, 80); }`
