// mrdns UI behaviors — theme toggle, toasts, and an htmx route-progress bar.
(function () {
  "use strict";

  function setTheme(t) {
    document.documentElement.setAttribute("data-theme", t);
    try { localStorage.setItem("mrdns-theme", t); } catch (e) {}
  }

  document.addEventListener("click", function (e) {
    var t = e.target.closest("[data-theme-toggle]");
    if (!t) return;
    var cur = document.documentElement.getAttribute("data-theme") === "light" ? "light" : "dark";
    setTheme(cur === "light" ? "dark" : "light");
  });

  function wireToasts(scope) {
    (scope || document).querySelectorAll(".toast:not([data-wired])").forEach(function (el) {
      el.setAttribute("data-wired", "1");
      setTimeout(function () {
        el.classList.add("toast-out");
        setTimeout(function () { el.remove(); }, 380);
      }, 4200);
    });
  }

  // htmx route-progress bar.
  document.addEventListener("DOMContentLoaded", function () {
    wireToasts();
    document.body.addEventListener("htmx:beforeRequest", function () {
      document.documentElement.classList.add("loading");
    });
    document.body.addEventListener("htmx:afterRequest", function () {
      document.documentElement.classList.remove("loading");
    });
    document.body.addEventListener("htmx:afterSwap", function (e) { wireToasts(e.target); });
  });
})();
