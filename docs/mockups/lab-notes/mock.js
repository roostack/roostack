// The mock-ups' switch: the pages, and light or dark.
(() => {
  const pages = [
    ["service-turn-off.html", "3 Turn off"], ["service-off.html", "3 Off"],
    ["services-removed.html", "5 Removed"], ["services-removed-delete.html", "5 Delete"],
    ["servers.html", "6 Servers"], ["clean-up.html", "6 Clean up"],
    ["people.html", "7 People"], ["sign-in.html", "7 Single sign-on"],
    ["look.html", "9 Name and look"]];
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
})();
