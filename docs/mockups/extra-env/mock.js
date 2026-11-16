// The mock-ups' switch: the three pages, and light or dark. Opens on the
// Environment variables card.
(() => {
  const pages = [["forgejo.html", "One container"], ["refused.html", "Refused"], ["immich.html", "Two containers"]];
  const here = location.pathname.split("/").pop() || "forgejo.html";
  const bar = document.createElement("nav");
  bar.className = "mock-bar";
  bar.setAttribute("aria-label", "Mock-ups");
  for (const [href, name] of pages) {
    const a = document.createElement("a");
    a.href = href + location.search + "#advanced"; a.textContent = name;
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
