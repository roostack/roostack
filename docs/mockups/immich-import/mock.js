// The mock-ups' switch: the pages, each page's states, and light or dark.
// Choices go into the address (?s=check&m=dark). An element with
// data-state="a b" shows only in states a and b.
(() => {
  const q = new URLSearchParams(location.search);
  const root = document.documentElement;
  const here = location.pathname.split("/").pop() || "service.html";
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
  // A dialog shown in this state opens as it would on the page.
  document.querySelectorAll("dialog[data-modal]:not([hidden])").forEach((d) => d.showModal());

  const bar = document.createElement("nav");
  bar.className = "mock-bar";
  bar.setAttribute("aria-label", "Mock-ups");
  const sep = () => { const s = document.createElement("span"); s.className = "sep"; s.textContent = "|"; bar.append(s); };
  const pages = [["service.html", "Immich's page"], ["import.html", "Migrate photos (Admins)"], ["immich-photos.html", "/immich-photos (everyone)"],
    ["signin.html", "Sign-in page"]];
  for (const [href, name] of pages) {
    const a = document.createElement("a");
    a.href = href + (q.get("m") ? "?m=" + q.get("m") : "");
    a.textContent = name;
    if (href === here) a.setAttribute("aria-current", "page");
    bar.append(a);
  }
  if (states.length > 1) {
    sep();
    const sel = document.createElement("select");
    sel.setAttribute("aria-label", "State");
    for (const [id, name] of states) {
      const o = document.createElement("option");
      o.value = id; o.textContent = name; o.selected = id === state;
      sel.append(o);
    }
    sel.onchange = () => { location.search = link({ s: sel.value }); };
    bar.append(sel);
    const i = states.findIndex(([id]) => id === state);
    if (i < states.length - 1) {
      const next = document.createElement("a");
      next.href = link({ s: states[i + 1][0] });
      next.textContent = "Next →";
      bar.append(next);
    }
  }
  sep();
  const m = document.createElement("a");
  m.href = link({ m: q.get("m") === "dark" ? "light" : "dark" });
  m.textContent = q.get("m") === "dark" ? "Light" : "Dark";
  bar.append(m);
  document.body.append(bar);
})();
