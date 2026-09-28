// Roostack's own small script. Parts of pages refresh themselves with htmx
// (the home page, Services, Backups); replacing their HTML would close any
// <details> panel someone opened. Panels with an id stay as they were.
(function () {
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
