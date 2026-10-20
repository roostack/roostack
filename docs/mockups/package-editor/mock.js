// The package editor mock-up's switches (2026-10-20): view, where it runs,
// which field's help is open, dark, and a phone-width frame. Choices go in
// the address, so a view can be linked: ?view=edit&where=website&help=login.
// Plain script, no Alpine: the real editor can't use Alpine either (the
// website's CSP has no 'unsafe-eval').
"use strict";

// Roostack's drawn icons (internal/web/icons.go), plus two the editor
// would add: import and spark (a suggestion).
const icons = {
  "home-page": '<path d="M3 11 12 4l9 7"/><path d="M5 10v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-9"/><path d="M10 20v-5h4v5"/>',
  services: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  backups: '<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9M10 13h4"/>',
  server: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>',
  monitoring: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  manage: '<path d="M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1"/><circle cx="15" cy="6" r="2"/><circle cx="9" cy="12" r="2"/><circle cx="17" cy="18" r="2"/>',
  help: '<circle cx="12" cy="12" r="9"/><path d="M9.5 9.5a2.5 2.5 0 1 1 3.4 2.3c-.6.3-.9.8-.9 1.4v.3"/><path d="M12 16.8h.01"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  warn: '<path d="M12 4 2.5 20h19z"/><path d="M12 10v4M12 17h.01"/>',
  passwords: '<circle cx="8" cy="15" r="4"/><path d="m11 12 8-8M16 7l2 2M14 9l2 2"/>',
  photos: '<rect x="3" y="5" width="18" height="14" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="m4 17 5-5 4 4 2.5-2.5L20 17"/>',
  documents: '<path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z"/><path d="M14 3v5h5M9 13h6M9 17h6"/>',
  development: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="8" r="2"/><path d="M6 7v10M18 10c0 4-6 3-11 7"/>',
  import: '<path d="M12 3v12M7 10l5 5 5-5"/><path d="M4 15v4a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-4"/>',
  spark: '<path d="M12 3v4M12 17v4M3 12h4M17 12h4M6 6l2.5 2.5M15.5 15.5 18 18M6 18l2.5-2.5M15.5 8.5 18 6"/>',
};

// The brand mark, as layout.html draws it.
const brand = '<svg class="brand-mark" viewBox="0 0 64 64" aria-hidden="true" focusable="false"><rect class="bm-f-soft" width="64" height="64" rx="14"/><g transform="translate(-3.6 .6) scale(1.2)"><path class="bm-f-hi" d="M34.5 14C33 11 34 8.2 36.3 8.6C36.6 6.2 39.6 5.6 40.4 7.8C42.2 6.9 44.2 8.6 43.2 11.2C42.8 12.4 42 13.4 41 14Z"/><path class="bm-f-hi" d="M43.4 18C45.8 18.4 46.6 21.8 45.2 24.2C43.4 23.6 42.4 20.4 43.4 18Z"/><path class="bm-f-fg" d="M44.2 14.6 49.2 16.6 44.4 18.6Z"/><path class="bm-f-fg bm-s-soft" d="M24 35C30 37.5 38 36 41 30C43 26 43.5 23 42.6 20.6L44.6 18.6C44.9 12.6 42.2 10.4 39 10.4C35.8 10.4 33.4 12.8 33.4 16C33.4 18.5 32.4 20 30 21C26 22.6 22 22.6 19.6 21.4C17 18 14.5 14.5 11.6 12.6C11.2 15.6 12 18 13.4 19.8C11.6 19 9.6 18.8 8 19.4C9.6 21.4 11.6 22.6 13.6 23.4C12.2 23.8 10.8 24.6 10 25.6C12 26.6 14 26.8 15.4 26.6C17 31 20 34 24 35Z" stroke-width="1.1" stroke-linejoin="round"/><circle class="bm-f-soft" cx="40.2" cy="14.6" r="1.3"/><path class="bm-s-fg" d="M27 34V37.4M25.4 37.4H29" fill="none" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/><path class="bm-s-fg" d="M31.4 34V37.4M29.8 37.4H33.4" fill="none" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></g><rect class="bm-f-fg" x="8" y="46.5" width="48" height="11.5" rx="3.2"/><circle class="bm-f-led" cx="15" cy="52.25" r="2.8"/></svg>';

function drawIcons() {
  for (const el of document.querySelectorAll("i[data-icon]")) {
    const name = el.dataset.icon;
    el.outerHTML = name === "brand" ? brand
      : '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' + (icons[name] || "") + "</svg>";
  }
}

const q = new URLSearchParams(location.search);
const state = {
  view: q.get("view") || "edit",
  where: q.get("where") || "roostack",
  help: q.get("help") || "licence",
  tab: q.get("tab") || "form",
  dark: q.get("mode") === "dark",
  phone: false,
  embed: q.has("embed"),
};

function query() {
  const u = new URLSearchParams({ view: state.view, where: state.where, help: state.help });
  if (state.tab !== "form") u.set("tab", state.tab);
  if (state.dark) u.set("mode", "dark");
  return u;
}

function render() {
  const html = document.documentElement;
  html.dataset.where = state.where;
  html.dataset.view = state.view;
  html.dataset.tab = state.tab;
  if (state.dark) html.dataset.mode = "dark"; else delete html.dataset.mode;
  html.classList.toggle("embed", state.embed);
  for (const v of document.querySelectorAll(".view")) v.hidden = v.dataset.view !== state.view;
  for (const h of document.querySelectorAll(".pe-help")) h.hidden = h.dataset.helpFor !== state.help;
  for (const f of document.querySelectorAll(".pe-field")) f.classList.toggle("is-focus", f.dataset.field === state.help);
  for (const t of document.querySelectorAll("[data-tab]")) t.setAttribute("aria-selected", t.dataset.tab === state.tab);
  for (const b of document.querySelectorAll("[data-set]")) {
    const [k, v] = b.dataset.set.split("=");
    b.setAttribute("aria-pressed", state[k] === v);
  }
  for (const b of document.querySelectorAll("[data-toggle]")) b.setAttribute("aria-pressed", state[b.dataset.toggle]);
  const stage = document.querySelector(".phone-stage");
  stage.hidden = !state.phone;
  const u = query();
  if (!state.embed) history.replaceState(null, "", "?" + u);
  if (state.phone) {
    u.set("embed", "1");
    const src = "?" + u;
    const f = stage.querySelector("iframe");
    if (f.getAttribute("src") !== src) f.setAttribute("src", src);
  }
}

document.addEventListener("DOMContentLoaded", () => {
  drawIcons();
  document.addEventListener("click", (e) => {
    const set = e.target.closest("[data-set]");
    if (set) { const [k, v] = set.dataset.set.split("="); state[k] = v; return render(); }
    const tog = e.target.closest("[data-toggle]");
    if (tog) { state[tog.dataset.toggle] = !state[tog.dataset.toggle]; return render(); }
    const go = e.target.closest("[data-go]");
    if (go) { state.view = go.dataset.go; scrollTo(0, 0); return render(); }
    const tab = e.target.closest("[data-tab]");
    if (tab) { state.tab = tab.dataset.tab; return render(); }
    const help = e.target.closest("[data-help]");
    if (help) { state.help = help.dataset.help; state.tab = "form"; render(); }
    if (e.target.closest(".phone-stage") && !e.target.closest("iframe")) { state.phone = false; render(); }
  });
  // Focusing a field opens its help, as the real editor would.
  document.addEventListener("focusin", (e) => {
    const f = e.target.closest(".pe-field");
    if (f && f.dataset.field !== state.help) { state.help = f.dataset.field; render(); }
  });
  render();
});
