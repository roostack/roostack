// The mock-ups' switch: the pages, each page's states, light or dark.
// Choices go into the address (?s=list&m=dark). An element shows in every
// state its data-state lists (space-separated).
(() => {
  const q = new URLSearchParams(location.search);
  const root = document.documentElement;
  const here = location.pathname.split("/").pop() || "security.html";
  const link = (changes) => {
    const p = new URLSearchParams(location.search);
    for (const [k, v] of Object.entries(changes)) v === null ? p.delete(k) : p.set(k, v);
    const s = p.toString();
    return s ? "?" + s : location.pathname;
  };
  if (q.get("m")) root.dataset.mode = q.get("m");

  const states = (root.dataset.states || "").split(",").filter(Boolean).map((s) => s.split(":"));
  const state = q.get("s") || (states[0] && states[0][0]);
  document.querySelectorAll("[data-state]").forEach((el) => { el.hidden = !el.dataset.state.split(" ").includes(state); });

  const bar = document.createElement("nav");
  bar.className = "mock-bar";
  bar.setAttribute("aria-label", "Mock-ups");
  const sep = () => { const s = document.createElement("span"); s.className = "sep"; s.textContent = "|"; bar.append(s); };
  for (const [href, name] of [["security.html", "Security"], ["signin.html", "Sign-in page"]]) {
    const a = document.createElement("a");
    a.href = href + (q.get("m") ? "?m=" + q.get("m") : "");
    a.textContent = name;
    if (href === here) a.setAttribute("aria-current", "page");
    bar.append(a);
  }
  sep();
  for (const [id, name] of states) {
    const a = document.createElement("a");
    a.href = link({ s: id });
    a.textContent = name;
    if (id === state) a.setAttribute("aria-current", "true");
    bar.append(a);
  }
  sep();
  const mode = q.get("m");
  const m = document.createElement("a");
  m.href = link({ m: mode === "dark" ? "light" : "dark" });
  m.textContent = mode === "dark" ? "Light" : "Dark";
  bar.append(m);
  document.body.append(bar);
})();
