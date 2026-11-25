// The check on Your account's Services tab (SPEC.md §6.5.3;
// internal/web/dnscheck.go). The page works without this script: its
// link asks on a page of its own.
//
// Once the picture marked data-dns-image has loaded or failed (either
// way, the browser has looked its name up), this asks data-dns-check
// whether the lab's DNS server was asked for that name, and shows the
// element marked data-dns="yes" or "no".
(function () {
  "use strict";
  const box = document.querySelector("[data-dns-check]");
  if (!box || !window.fetch) return;
  const img = box.querySelector("[data-dns-image]");
  function show(state) {
    box.querySelectorAll("[data-dns]").forEach(function (el) { el.hidden = el.dataset.dns !== state; });
  }
  function ask() {
    fetch(box.dataset.dnsCheck + "&json=1", { credentials: "same-origin", cache: "no-store" })
      .then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
      .then(function (a) { show(!a.known ? "link" : a.seen ? "yes" : "no"); })
      .catch(function () { show("link"); });
  }
  show("checking");
  if (!img || img.complete) {
    ask();
  } else {
    img.addEventListener("load", ask, { once: true });
    img.addEventListener("error", ask, { once: true });
  }
})();
