// The Documents page of bunk-gui's panel (present.RenderDocuments): the
// box above the list filters it by name, folder, agent or repository, and
// hides the repositories with nothing left.
(function () {
  'use strict';
  var box = document.getElementById('bunk-docs-filter');
  if (!box) return;
  var none = document.getElementById('bunk-docs-none');
  box.addEventListener('input', function () {
    var q = box.value.trim().toLowerCase();
    var shown = 0;
    document.querySelectorAll('.bunk-docs-group').forEach(function (g) {
      var group = g.querySelector('h2').textContent.toLowerCase().indexOf(q) >= 0;
      var n = 0;
      g.querySelectorAll('li').forEach(function (li) {
        var hit = !q || group || li.textContent.toLowerCase().indexOf(q) >= 0;
        li.hidden = !hit;
        if (hit) n++;
      });
      g.hidden = n === 0;
      shown += n;
    });
    if (none) none.hidden = shown > 0;
  });
})();
