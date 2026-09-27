// Design gallery (/design and docs/design/gallery.html): shows the value
// each token resolves to, and switches themes. Not used by other pages.
(function () {
  function hex(rgb) {
    const m = rgb.match(/[\d.]+/g);
    if (!m || m.length < 3) return rgb;
    return "#" + m.slice(0, 3).map(v => Math.round(+v).toString(16).padStart(2, "0")).join("");
  }
  function resolve() {
    document.querySelectorAll("[data-resolve]").forEach(el => {
      const row = el.closest("[data-token]");
      const sample = row.hasAttribute("data-sample") ? row : row.querySelector("[data-sample]");
      let v = getComputedStyle(sample).getPropertyValue(el.dataset.resolve);
      if (el.dataset.resolve.includes("color")) v = hex(v);
      else v = v.replace(/(\d+\.\d)\d+px/, "$1px");
      if (el.dataset.var) v = getComputedStyle(document.documentElement).getPropertyValue(el.dataset.var).trim() + " = " + v;
      el.textContent = v;
    });
  }
  document.addEventListener("DOMContentLoaded", () => {
    const select = document.getElementById("gallery-theme");
    select.addEventListener("change", () => {
      document.documentElement.dataset.palette = select.value;
      resolve(); // reading computed styles applies the new theme first
    });
    resolve();
  });
})();
