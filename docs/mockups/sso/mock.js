// The mock-ups' switch: the pages, each page's states, light or dark, the
// sign-in page's theme and logo. Choices go into the address (?s=on&m=dark&t=nord&logo=0).
(() => {
  const q = new URLSearchParams(location.search);
  const root = document.documentElement;
  const here = location.pathname.split("/").pop() || "signin.html";
  const link = (changes) => {
    const p = new URLSearchParams(location.search);
    for (const [k, v] of Object.entries(changes)) v === null ? p.delete(k) : p.set(k, v);
    const s = p.toString();
    return s ? "?" + s : location.pathname;
  };

  if (q.get("m")) root.dataset.mode = q.get("m");
  if (q.get("t")) root.dataset.palette = q.get("t");
  if (q.get("logo") === "0") root.dataset.logo = "0";
  if (q.get("embed") === "1") root.dataset.embed = "1";

  // Each page's states: elements with data-state show only in theirs.
  const states = (root.dataset.states || "").split(",").filter(Boolean).map((s) => s.split(":"));
  const state = q.get("s") || (states[0] && states[0][0]);
  document.querySelectorAll("[data-state]").forEach((el) => { el.hidden = el.dataset.state !== state; });

  if (root.dataset.embed === "1") return;

  // People: the sign-in page's preview follows the chosen theme and logo.
  const frame = document.getElementById("look-frame");
  if (frame) {
    const update = () => {
      const t = document.querySelector('input[name="look-theme"]:checked').value;
      const logo = frame.dataset.logo === "0" ? "&logo=0" : "";
      frame.src = "signin.html?embed=1&t=" + t + logo + (q.get("m") ? "&m=" + q.get("m") : "");
    };
    document.querySelectorAll('input[name="look-theme"]').forEach((r) => r.addEventListener("change", update));
    const remove = document.getElementById("look-logo-remove");
    remove.addEventListener("click", () => {
      frame.dataset.logo = frame.dataset.logo === "0" ? "1" : "0";
      remove.textContent = frame.dataset.logo === "0" ? "Use the lab's logo again" : "Use Roostack's hen instead";
      document.querySelector(".logo-now").style.opacity = frame.dataset.logo === "0" ? ".3" : "1";
      update();
    });
    update();
  }

  const bar = document.createElement("nav");
  bar.className = "mock-bar";
  bar.setAttribute("aria-label", "Mock-ups");
  const add = (el) => bar.append(el);
  const sep = () => { const s = document.createElement("span"); s.className = "sep"; s.textContent = "|"; add(s); };

  for (const [href, name] of [["signin.html", "Sign-in page"], ["service.html", "Mealie (OIDC)"], ["wall.html", "Stirling-PDF (wall)"], ["people.html", "People and groups"]]) {
    const a = document.createElement("a");
    const keep = new URLSearchParams(location.search);
    keep.delete("s");
    a.href = href + (keep.toString() ? "?" + keep : "");
    a.textContent = name;
    if (href === here) a.setAttribute("aria-current", "page");
    add(a);
  }
  if (states.length) {
    sep();
    for (const [id, name] of states) {
      const a = document.createElement("a");
      a.href = link({ s: id });
      a.textContent = name;
      if (id === state) a.setAttribute("aria-current", "true");
      add(a);
    }
  }
  sep();
  const mode = q.get("m");
  const m = document.createElement("a");
  m.href = link({ m: mode === "dark" ? "light" : "dark" });
  m.textContent = mode === "dark" ? "Light" : "Dark";
  add(m);
  if (here === "signin.html") {
    const sel = document.createElement("select");
    sel.setAttribute("aria-label", "The lab's theme");
    for (const t of ["teal", "ocean", "catppuccin", "gruvbox", "nord", "solarized", "everforest", "rose-pine", "tokyo-night"]) {
      const o = document.createElement("option");
      o.value = t; o.textContent = t; o.selected = (q.get("t") || "teal") === t;
      sel.append(o);
    }
    sel.onchange = () => { location.search = link({ t: sel.value }); };
    add(sel);
    const l = document.createElement("a");
    l.href = link({ logo: q.get("logo") === "0" ? null : "0" });
    l.textContent = q.get("logo") === "0" ? "Lab's logo" : "No logo";
    add(l);
  }
  document.body.append(bar);
})();
