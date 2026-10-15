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
  // A part that replaces itself (hx-swap="outerHTML") is a new element by
  // now: the fingerprint goes on the one with its id.
  document.addEventListener("htmx:afterRequest", (e) => {
    const fp = e.detail.xhr && e.detail.xhr.getResponseHeader("X-Roostack-Fingerprint");
    if (!fp || !e.detail.successful) return;
    e.detail.elt.dataset.fingerprint = fp;
    const now = e.detail.elt.id && document.getElementById(e.detail.elt.id);
    if (now) now.dataset.fingerprint = fp;
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

  // Bars (the space-bar) give their widths as data-width (percent): the
  // Content-Security-Policy blocks style attributes, but not styles set
  // from this script. Applied at load and to every part htmx swaps in.
  const widths = (root) => {
    for (const el of root.querySelectorAll("[data-width]")) el.style.width = `${Number(el.dataset.width) || 0}%`;
    // Columns (the bar-chart) give their heights the same way.
    for (const el of root.querySelectorAll("[data-height]")) el.style.height = `${Number(el.dataset.height) || 0}%`;
  };
  document.addEventListener("DOMContentLoaded", () => widths(document));
  document.addEventListener("htmx:load", (e) => widths(e.detail.elt));

  // A refreshed status line is a new element, whose spinner would start
  // its turn again (a visible hitch). Every spinner turns in step with the
  // page's clock instead, so a new one carries on where the old one was.
  const spinTurn = 800; // ms, as .spinner's animation in app.css
  const spinners = (root) => {
    const delay = `-${Math.round(performance.now() % spinTurn)}ms`;
    for (const el of root.querySelectorAll(".spinner")) el.style.animationDelay = delay;
    if (root.matches && root.matches(".spinner")) root.style.animationDelay = delay;
  };
  document.addEventListener("DOMContentLoaded", () => spinners(document));
  document.addEventListener("htmx:load", (e) => spinners(e.detail.elt));

  // The sidebar's links scroll when the window is short (Settings lists
  // all its pages while you're in it): show where you are.
  document.addEventListener("DOMContentLoaded", () => {
    const here = document.querySelector(".sidebar-nav [aria-current=page]");
    if (here) here.scrollIntoView({ block: "nearest" });
  });

  // roostackSize formats bytes as units.Bytes does (decimal: 1 GB = 1000³
  // bytes), for figures that change as someone types.
  window.roostackSize = (n) => {
    if (!(n >= 0)) return "unknown";
    if (n < 1000) return `${Math.floor(n)} B`;
    let div = 1000, exp = 0;
    for (let m = Math.floor(n / 1000); m >= 1000 && exp < 4; m = Math.floor(m / 1000)) { div *= 1000; exp++; }
    return `${(n / div).toFixed(1)} ${"kMGTP"[exp]}B`;
  };

  // roostackCopy copies an element's text (the copy-box component). The
  // browser's clipboard API needs HTTPS (or localhost); elsewhere the text
  // is selected and copied the older way.
  window.roostackCopy = (el) => {
    const text = el.textContent.trim();
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
    const range = document.createRange();
    range.selectNodeContents(el);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
    return document.execCommand("copy") ? Promise.resolve() : Promise.reject(new Error("copy failed"));
  };
})();
