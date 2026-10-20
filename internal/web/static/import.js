// Bringing a service's data in (SPEC.md §8.4): a Compose file pasted on
// the import page is read here, in the browser, by the package editor's
// own Compose reader (/editor/compose.js), and only what it found is sent:
// the containers' images, folders and settings, secrets' values masked.
// The Compose file and .env themselves never leave the browser.
(() => {
  "use strict";
  document.addEventListener("submit", (e) => {
    const form = e.target.closest("#setup-form");
    if (!form) return;
    const from = form.querySelector('input[name="from"]:checked');
    if (!from || from.value !== "compose") return;
    const R = globalThis.RoostackEditor;
    if (!R || !R.compose || !R.compose.readSetup) return; // the server says what to do instead
    const got = R.compose.readSetup(form.elements.compose.value, { env: form.elements.env.value });
    let shown = form.querySelector("p.error[data-compose]");
    if (got.error) {
      e.preventDefault();
      if (!shown) {
        shown = Object.assign(document.createElement("p"), { className: "error" });
        shown.setAttribute("role", "alert");
        shown.dataset.compose = "";
        form.prepend(shown);
      }
      shown.textContent = got.error;
      return;
    }
    form.elements.compose_setup.value = JSON.stringify(got.setup);
    form.elements.compose.disabled = true;
    form.elements.env.disabled = true;
    form.elements.inspect.disabled = true;
  });
})();
