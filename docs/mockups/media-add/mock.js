// The mock-ups' switch: the directions, and light or dark.
(() => {
  const pages = [["a-dialog.html", "A: Dialog"], ["b-inline.html", "B: In the card"], ["today.html", "Today"]];
  const here = location.pathname.split("/").pop() || "a-dialog.html";
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
  b.onclick = () => { const q = new URLSearchParams(location.search); q.set("m", mode === "dark" ? "light" : "dark"); location.search = q; };
  bar.append(b);
  document.addEventListener("DOMContentLoaded", () => document.body.append(bar));
})();

// What the agent would list (agent.FolderList): each folder with how many
// folders and files it holds, and the readability check of the folder.
window.mockTree = {
  "drive:MEDIA": { check: { unreadable: 1, sampled: 2, owner: "dev", ownerFiles: 1 }, folders: {
    "Films": { files: 1, check: { unreadable: 1, sampled: 9, owner: "dev", ownerFiles: 1 }, folders: {
      "Blade Runner (1982)": { files: 3, folders: {} },
      "Paddington 2 (2017)": { files: 2, folders: {} } } },
    "Music": { files: 0, check: {}, folders: {
      "Nina Simone": { files: 0, folders: { "Pastel Blues (1965)": { files: 9, folders: {} }, "Wild Is the Wind (1966)": { files: 11, folders: {} }, "Little Girl Blue (1959)": { files: 14, folders: {} } } } } } } },
  "server": { folders: {
    "mnt": { files: 0, folders: { "nas-old": { files: 0, check: { unreadable: 212, sampled: 213, closed: false }, folders: { "photos": { files: 212, folders: {} } } } } },
    "media": { files: 0, folders: {} },
    "srv": { files: 0, folders: { "media": { files: 0, check: {}, folders: { "Films": { files: 0, check: {}, folders: { "Heat (1995)": { files: 2, folders: {} } } }, "TV": { files: 0, check: {}, folders: {} } } } } } } },
  "folder:/mnt": { folders: { "nas-old": { files: 0, folders: { "photos": { files: 212, folders: {} } } } } },
  "folder:/media": { folders: {} },
  "folder:/srv": { folders: { "media": { files: 0, check: {}, folders: { "Films": { files: 0, folders: { "Heat (1995)": { files: 2, folders: {} } } }, "TV": { files: 0, folders: {} } } } } },
};

// picker is the folder picker's state, shared by both directions.
window.picker = (start) => ({
  root: start, path: [], chosen: null, chosenRoot: null,
  where: new URLSearchParams(location.search).get("w") || "drive", share: "smb",
  node(root = this.root, path = this.path) {
    let n = window.mockTree[root];
    for (const p of path) n = n.folders[p];
    return n;
  },
  get rootName() { return this.root === "server" ? "roostack-dev-3" : this.root.split(":")[1]; },
  get canUse() { return this.root !== "server" || this.path.length > 0; },
  get here() { const n = this.node(); return this.counts({ folders: Object.keys(n.folders).length, files: n.files || 0 }); },
  start(root) { if (this.root !== root) { this.root = root; this.path = []; } else if (this.chosen && this.chosenRoot === root) this.path = [...this.chosen]; },
  get entries() { return Object.entries(this.node().folders).map(([name, n]) => ({ name, folders: Object.keys(n.folders).length, files: n.files })); },
  counts(e) {
    const parts = [];
    if (e.folders) parts.push(e.folders + (e.folders === 1 ? " folder" : " folders"));
    if (e.files) parts.push(e.files + (e.files === 1 ? " file" : " files"));
    return parts.join(", ") || "empty";
  },
  label(root = this.root, path = this.path) {
    const r = root.split(":");
    if (root === "server") return "/" + path.join("/");
    return r[0] === "drive" ? [r[1], ...path].join(" › ") : [r[1], ...path].join("/");
  },
  open(name) { this.path = [...this.path, name]; },
  up(i) { this.path = this.path.slice(0, i); },
  use() { this.chosen = [...this.path]; this.chosenRoot = this.root; },
  get chosenLabel() { return this.chosen && this.label(this.chosenRoot, this.chosen); },
  get chosenCheck() { return this.chosen ? (this.node(this.chosenRoot, this.chosen).check || {}) : {}; },
  get chosenCounts() {
    if (!this.chosen) return "";
    const n = this.node(this.chosenRoot, this.chosen);
    return this.counts({ folders: Object.keys(n.folders).length, files: n.files || 0 });
  },
  get suggested() { return this.chosen ? (this.chosen.length ? this.chosen.at(-1) : this.chosenRoot.split(":")[1]) : ""; },
  get hereCheck() { return this.node().check || {}; },
});
