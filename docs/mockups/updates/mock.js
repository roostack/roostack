// The mock-ups' switch: the pages, and light or dark.
(() => {
  const pages = [
    ["services.html", "Services"], ["updates.html", "Updates"], ["updates-notes.html", "Notes"],
    ["updates-picked.html", "Chosen"], ["updates-confirm.html", "Update now"], ["updates-running.html", "Running"],
    ["updates-tonight.html", "Tonight"]];
  const here = location.pathname.split("/").pop() || pages[0][0];
  const bar = document.createElement("nav");
  bar.className = "mock-bar";
  bar.setAttribute("aria-label", "Mock-ups");
  for (const [href, name] of pages) {
    const a = document.createElement("a");
    a.href = href + location.search; a.textContent = name;
    if (href === here) a.setAttribute("aria-current", "page");
    bar.append(a);
  }
  const mode = new URLSearchParams(location.search).get("m");
  if (mode) document.documentElement.dataset.mode = mode;
  const b = document.createElement("button");
  b.textContent = mode === "dark" ? "Light" : "Dark";
  b.onclick = () => { location.search = "?m=" + (mode === "dark" ? "light" : "dark"); };
  bar.append(b);
  document.body.append(bar);
  // A dialog in the page opens over it, as the real one will (showModal).
  const d = document.querySelector("dialog[open]");
  if (d) { d.close(); d.showModal(); }
})();
