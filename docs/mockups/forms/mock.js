// The mock-ups' switch: the pages, and light or dark.
(() => {
  const pages = [["install-today.html", "Install: today"], ["install-forgejo-a.html", "A Forgejo"], ["install-plex-a.html", "A Plex"],
    ["install-forgejo-b.html", "B Forgejo"], ["install-plex-b.html", "B Plex"],
    ["settings-today.html", "Settings: today"], ["settings-a.html", "A"], ["settings-b.html", "B"]];
  const here = location.pathname.split("/").pop();
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
  document.addEventListener("DOMContentLoaded", () => document.body.append(bar));
})();
