// Doc pages: label each table cell with its column header, so narrow screens
// can show rows as stacked cards (see the phone media query in docs.css).
// Runs on Material's document$ observable, since instant navigation swaps
// pages without reloading.
(function () {
  function labelTables() {
    document.querySelectorAll(".md-typeset table:not([class])").forEach(function (table) {
      var heads = Array.prototype.map.call(table.querySelectorAll("thead th"), function (th) {
        return th.textContent.trim();
      });
      if (heads.length < 3) return; // two-column tables read fine as they are
      table.setAttribute("data-stack", "");
      table.querySelectorAll("tbody tr").forEach(function (tr) {
        Array.prototype.forEach.call(tr.children, function (td, i) {
          if (heads[i]) td.setAttribute("data-label", heads[i]);
        });
      });
    });
  }
  if (window.document$) window.document$.subscribe(labelTables);
  else document.addEventListener("DOMContentLoaded", labelTables);
})();
