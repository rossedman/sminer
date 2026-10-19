// Landing page: copy buttons and the typing terminal in the hero. Material's
// instant navigation swaps pages without reloading, so setup runs on its
// document$ observable.
(function () {
  function setupCopy() {
    document.querySelectorAll(".sm-copy").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var text = btn.dataset.copy;
        if (!text) {
          // Code blocks: copy the commands, without prompts or comments.
          text = btn.parentElement.querySelector("code").innerText
            .split("\n")
            .map(function (l) { return l.replace(/^\$ /, "").replace(/\s+# .*$/, ""); })
            .join("\n");
        }
        navigator.clipboard.writeText(text).then(function () {
          btn.classList.add("is-copied");
          setTimeout(function () { btn.classList.remove("is-copied"); }, 1500);
        });
      });
    });
  }

  function setupTyping() {
    var body = document.querySelector("[data-typing]");
    if (!body || window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

    var lines = Array.prototype.slice.call(body.children);
    var typed = lines.map(function (line) {
      var el = line.querySelector("[data-type]");
      if (!el) return null;
      var text = el.textContent;
      el.textContent = "";
      return { el: el, text: text };
    });
    body.classList.add("is-typing");

    var i = 0;
    function next() {
      if (i >= lines.length) return;
      var line = lines[i], t = typed[i];
      line.classList.add("is-shown");
      i++;
      if (!t) { setTimeout(next, 240); return; }
      var n = 0;
      (function type() {
        t.el.textContent = t.text.slice(0, ++n);
        if (n < t.text.length) setTimeout(type, 40 + Math.random() * 45);
        else setTimeout(next, 420);
      })();
    }
    setTimeout(next, 500);
  }

  function init() { setupCopy(); setupTyping(); }
  if (window.document$) window.document$.subscribe(init);
  else document.addEventListener("DOMContentLoaded", init);
})();
