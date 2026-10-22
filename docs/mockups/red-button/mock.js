// The mock-ups' switch: the pages, and light or dark. Styled here, since the
// visitor's page has none of the lab's stylesheets.
(() => {
  const pages = [["home.html", "Home"], ["pause.html", "Pause"], ["paused.html", "Paused"], ["service.html", "A service"],
    ["settings.html", "Settings"], ["visitor.html", "A visitor"]];
  const here = location.pathname.split("/").pop() || "home.html";
  const bar = document.createElement("nav");
  bar.setAttribute("aria-label", "Mock-ups");
  bar.style.cssText = "position:fixed;right:12px;bottom:12px;z-index:50;display:flex;gap:6px;flex-wrap:wrap;padding:6px;" +
    "border-radius:10px;background:#222;color:#fff;font:13px system-ui,sans-serif;max-width:calc(100vw - 24px)";
  const item = "color:inherit;background:none;border:0;padding:2px 6px;font:inherit;cursor:pointer;";
  for (const [href, name] of pages) {
    const a = document.createElement("a");
    a.href = href + location.search; a.textContent = name;
    a.style.cssText = item + (href === here ? "text-decoration:underline" : "text-decoration:none");
    bar.append(a);
  }
  const mode = new URLSearchParams(location.search).get("m");
  if (mode) document.documentElement.dataset.mode = mode;
  const b = document.createElement("button");
  b.textContent = mode === "dark" ? "Light" : "Dark";
  b.style.cssText = item;
  b.onclick = () => { location.search = "?m=" + (mode === "dark" ? "light" : "dark"); };
  bar.append(b);
  document.body.append(bar);
})();
