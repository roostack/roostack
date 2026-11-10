// The mock-ups' switch: light or dark (the tabs switch between the pages).
(() => {
  const mode = new URLSearchParams(location.search).get("m");
  if (mode) {
    document.documentElement.dataset.mode = mode;
    for (const a of document.querySelectorAll(".acct-tabs a, .acct-brand")) a.href += "?m=" + mode;
  }
  const bar = document.createElement("nav");
  bar.className = "mock-bar";
  bar.setAttribute("aria-label", "Mock-ups");
  const b = document.createElement("button");
  b.textContent = mode === "dark" ? "Light" : "Dark";
  b.onclick = () => { location.search = "?m=" + (mode === "dark" ? "light" : "dark"); };
  bar.append(b);
  document.body.append(bar);
})();
