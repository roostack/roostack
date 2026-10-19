// The mock-ups' switch: the four pages, and light or dark.
(() => {
  const pages = [["service.html", "A service"], ["monitoring.html", "Monitoring"], ["check.html", "Details"], ["status.html", "Public page"]];
  const here = location.pathname.split("/").pop() || "service.html";
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
})();
