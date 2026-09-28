// Roostack's own small script, for the parts of pages that refresh
// themselves with htmx (the home page, Services, Backups, the drive list).
// They only poll while the tab is visible (see their hx-trigger).
(function () {
  // Each refresh sends a fingerprint of what the part shows; the server
  // answers 204 No Content when nothing changed, and htmx leaves the page
  // alone (renderLive in internal/web/pages.go).
  document.addEventListener("htmx:configRequest", (e) => {
    const fp = e.detail.elt.dataset.fingerprint;
    if (fp) e.detail.headers["X-Roostack-Have"] = fp;
  });
  document.addEventListener("htmx:afterRequest", (e) => {
    const fp = e.detail.xhr && e.detail.xhr.getResponseHeader("X-Roostack-Fingerprint");
    if (fp && e.detail.successful) e.detail.elt.dataset.fingerprint = fp;
  });

  // When a part does change, replacing its HTML would close any <details>
  // panel someone opened: panels with an id stay as they were.
  let open = [];
  document.addEventListener("htmx:beforeSwap", (e) => {
    open = [...e.detail.target.querySelectorAll("details[open][id]")].map((d) => d.id);
    if (e.detail.target.matches("details[open][id]")) open.push(e.detail.target.id);
  });
  document.addEventListener("htmx:afterSwap", () => {
    for (const id of open) {
      const d = document.getElementById(id);
      if (d) d.open = true;
    }
    open = [];
  });
})();
