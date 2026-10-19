// Settings in the sidebar: mock-ups (2026-10-19). One Alpine component
// draws the sidebar for each direction from the real settings pages
// (internal/web/settings.go: settingsAreas, sidebarSettings, Icon), and a
// stand-in page for wherever you are. Pins and recent pages are kept in
// this browser's localStorage here; the real thing would keep them per
// person (README.md).

// The real icons (internal/web/icons.go), and a pin, new.
const ICONS = {
  "home-page": '<path d="M3 11 12 4l9 7"/><path d="M5 10v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-9"/><path d="M10 20v-5h4v5"/>',
  services: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  backups: '<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9M10 13h4"/>',
  server: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>',
  monitoring: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  manage: '<path d="M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1"/><circle cx="15" cy="6" r="2"/><circle cx="9" cy="12" r="2"/><circle cx="17" cy="18" r="2"/>',
  tag: '<path d="M3 12V4a1 1 0 0 1 1-1h8l9 9-9 9z"/><circle cx="8" cy="8" r="1.5"/>',
  history: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l3 2"/>',
  users: '<circle cx="9" cy="8" r="3.5"/><path d="M3 20c0-3.3 2.7-6 6-6s6 2.7 6 6"/><circle cx="17" cy="9" r="2.5"/><path d="M16 14c2.8 0 5 2.2 5 5"/>',
  at: '<circle cx="12" cy="12" r="4"/><path d="M16 12v1.5a2.5 2.5 0 0 0 5 0V12a9 9 0 1 0-3.5 7.1"/>',
  certificate: '<path d="M12 3 5 6v5c0 4.5 3 8.3 7 10 4-1.7 7-5.5 7-10V6l-7-3z"/><path d="m9 12 2 2 4-4"/>',
  location: '<path d="M12 21s-7-6.2-7-11.5a7 7 0 0 1 14 0C19 14.8 12 21 12 21z"/><circle cx="12" cy="9.5" r="2.5"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3 3.5 3 14.5 0 18M12 3c-3 3.5-3 14.5 0 18"/>',
  cloud: '<path d="M7 18a4.5 4.5 0 0 1-.5-9A6 6 0 0 1 18 8.5 4.75 4.75 0 0 1 17.5 18z"/>',
  media: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 4v16M17 4v16M3 9h4M3 15h4M17 9h4M17 15h4"/>',
  disk: '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6"/><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>',
  changes: '<path d="M20 11a8 8 0 0 0-14.5-4.5L3 9"/><path d="M3 4v5h5"/><path d="M4 13a8 8 0 0 0 14.5 4.5L21 15"/><path d="M21 20v-5h-5"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  terminal: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3M13 15h4"/>',
  chevron: '<path d="m10 7 5 5-5 5"/>',
  help: '<circle cx="12" cy="12" r="9"/><path d="M9.5 9.5a2.5 2.5 0 1 1 3.4 2.3c-.6.3-.9.8-.9 1.4v.3"/><path d="M12 16.8h.01"/>',
  menu: '<path d="M4 7h16M4 12h16M4 17h16"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="m16 16 4.5 4.5"/>',
  // The catalog's categories, for the services' own pages under Services.
  photos: '<rect x="3" y="5" width="18" height="14" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="m4 17 5-5 4 4 2.5-2.5L20 17"/>',
  passwords: '<circle cx="8" cy="15" r="4"/><path d="m11 12 8-8M16 7l2 2M14 9l2 2"/>',
  development: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="8" r="2"/><path d="M6 7v10M18 10c0 4-6 3-11 7"/>',
  "smart-home": '<path d="M3 11 12 4l9 7"/><path d="M5 10v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-9"/><circle cx="12" cy="14.5" r="2.5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  // New for direction A: a drawing pin, filled when pinned.
  pin: '<path d="M9 3.5h6l-1 5.5 3.5 3.5h-11L10 9z"/><path d="M12 12.5V21"/>',
};

// The settings pages in the overview's order and groups (settingsAreas),
// without Servers and Backups, which are main areas. Words are what the
// filter (D) also matches.
const AREAS = [
  { id: "lab", group: "Lab", title: "Lab name", icon: "tag", lede: "What your lab is called, here and in emails.", words: "name rename" },
  { id: "history", group: "Lab", title: "History", icon: "history", lede: "What Roostack has done, and how long it keeps the record.", words: "log activity events audit" },
  { id: "people", group: "People and access", title: "People", icon: "users", lede: "Who can log in, and what each person can do.", words: "users accounts passwords roles owner admin member invite two-factor" },
  { id: "email", group: "People and access", title: "Email", icon: "at", lede: "How Roostack sends email: password resets, alerts.", words: "smtp mail notifications" },
  { id: "domain", group: "Network", title: "Your lab's address", icon: "certificate", lede: "The domain your services live under, and its certificates.", words: "domain https certificate dns address url" },
  { id: "home-network", group: "Network", title: "Where it is and DNS", icon: "location", lede: "Whether your lab is at home or on the internet, and its DNS records.", words: "dns network router home internet records" },
  { id: "public", group: "Network", title: "Public access", icon: "globe", lede: "Which services anyone on the internet can reach.", words: "public internet visitors port forward share" },
  { id: "cloud", group: "Network", title: "Roostack Cloud", icon: "cloud", lede: "A name on roost.ac and a way in from anywhere.", words: "roost.ac link remote" },
  { id: "media", group: "Servers", title: "Media libraries", icon: "media", lede: "Folders of films, music and photos your services share.", words: "films music photos smb nfs shares folders" },
  { id: "storage", group: "Servers", title: "Storage for others", icon: "disk", lede: "Room on your servers for other labs' backups.", words: "backup space friends offsite" },
  { id: "updates", group: "Each server", title: "Updates and restarts", icon: "changes", lede: "When each server installs updates and restarts.", words: "os updates reboot restart maintenance window" },
  { id: "time-zone", group: "Each server", title: "Time zone", icon: "clock", lede: "Each server's time zone, which every time Roostack shows uses.", words: "time clock zone" },
  { id: "remote-access", group: "Each server", title: "Remote access", icon: "terminal", lede: "Logging in to each server over SSH.", words: "ssh keys terminal shell" },
];
// Today's everyday set (sidebarSettings), the default pins.
const EVERYDAY = ["people", "domain", "updates", "history"];

// What the overview's cards say (settingsPage), for a lab of three
// servers; Servers and Backups have cards there too.
const SUMMARY = {
  lab: ["Smiths' lab", ""], history: ["Kept for 90 days", ""], people: ["3 people can log in", ""],
  email: ["Sends from roostack@smith.family", "ok"], domain: ["smith.family", "ok"],
  "home-network": ["At home · devices at home can reach your services", "ok"], public: ["1 service is public: Git", "ok"],
  cloud: ["Not linked", ""], servers: ["3 servers · 1 needs attention", "warn"], media: ["2 libraries: Films, Music", "ok"],
  storage: ["Not offered to other labs", ""], backups: ["Every night at 3:00", "ok"],
  updates: [[["nuc", "Automatic · up to date", "ok"], ["mum-and-dad", "A restart is due", "warn"], ["cloud-box", "Automatic · up to date", "ok"]]],
  "time-zone": [[["nuc", "Europe/London", ""], ["mum-and-dad", "Europe/London", ""], ["cloud-box", "UTC", ""]]],
  "remote-access": [[["nuc", "Keys only", "ok"], ["mum-and-dad", "Keys only", "ok"], ["cloud-box", "Keys only", "ok"]]],
};

const MAIN = [
  { id: "home", title: "Home", icon: "home-page", lede: "Everything's working." },
  { id: "services", title: "Services", icon: "services", lede: "Everything your lab runs." },
  { id: "backups", title: "Backups", icon: "backups", lede: "Each server's backups and the plan: three copies, two kinds of place, one away." },
  { id: "servers", title: "Servers", icon: "server", lede: "The servers in your lab.", count: 1 },
  { id: "monitoring", title: "Monitoring", icon: "monitoring", lede: "Whether each service answers, checked every minute." },
];

// Under Services while you're there, each service (layout.html), to
// compare F's settings pages with.
const SERVICES = [
  { id: "svc-photos", title: "Photos", icon: "photos", lede: "Immich, on nuc." },
  { id: "svc-passwords", title: "Passwords", icon: "passwords", lede: "Vaultwarden, on mum-and-dad." },
  { id: "svc-films", title: "Films", icon: "media", lede: "Jellyfin, on nuc." },
  { id: "svc-git", title: "Git", icon: "development", lede: "Forgejo, on cloud-box." },
  { id: "svc-home", title: "Smart home", icon: "smart-home", lede: "Home Assistant, on nuc." },
];

const store = {
  get(k, d) { try { const v = localStorage.getItem("settings-nav-" + k); return v ? JSON.parse(v) : d; } catch { return d; } },
  set(k, v) { try { localStorage.setItem("settings-nav-" + k, JSON.stringify(v)); } catch {} },
};

function mock() {
  const q = new URLSearchParams(location.search);
  return {
    dir: q.get("d") || "f",
    role: q.get("r") || "owner",
    mode: q.get("m") || "light",
    embed: q.has("embed"),
    phone: false,
    menu: q.has("menu"),
    page: location.hash.slice(1) || "updates",
    areas: AREAS, main: MAIN, services: SERVICES,
    // F: icons on the pages under Settings (and Services) or not.
    subIcons: q.get("i") !== "0",
    // A: pins, and whether the full list is open.
    pins: store.get("pins", EVERYDAY),
    open: store.get("open", false),
    // B: which groups are open (the current page's opens by itself).
    groupsOpen: store.get("groups", []),
    // C: recently opened settings pages, latest first.
    recent: store.get("recent", ["domain", "people", "updates", "email"]),
    // D: the filter.
    filter: "",

    init() {
      if (q.has("open")) this.open = true;
      if (q.get("f")) this.filter = q.get("f");
      document.documentElement.dataset.mode = this.mode;
      document.documentElement.classList.toggle("embed", this.embed);
      addEventListener("hashchange", () => this.go(location.hash.slice(1) || "home"));
      this.go(this.page);
      this.menu = q.has("menu");
      // Others in the mock-up (the phone frame) see pin changes.
      addEventListener("storage", () => { this.pins = store.get("pins", EVERYDAY); this.recent = store.get("recent", []); });
      this.$watch("dir", () => this.sync()); this.$watch("role", () => this.sync()); this.$watch("mode", () => this.sync()); this.$watch("subIcons", () => this.sync());
      this.$watch("pins", v => store.set("pins", v)); this.$watch("open", v => store.set("open", v));
      this.$watch("groupsOpen", v => store.set("groups", v));
      this.sync();
    },
    // Navigating within the mock-up: the page, and what C remembers.
    go(id) {
      this.page = id;
      this.menu = false;
      if (this.area(id)) {
        this.recent = [id, ...this.recent.filter(r => r !== id)].slice(0, 4);
        store.set("recent", this.recent);
        const g = this.area(id).group;
        if (!this.groupsOpen.includes(g)) this.groupsOpen = [...this.groupsOpen, g];
      }
      this.sync();
      if (!this.embed) scrollTo(0, 0);
      // As the real sidebar does, keep where you are in view.
      this.$nextTick(() => document.querySelector('#sidebar [aria-current]')?.scrollIntoView({ block: 'nearest' }));
    },
    sync() {
      document.documentElement.dataset.mode = this.mode;
      const u = new URL(location);
      u.searchParams.set("d", this.dir); u.searchParams.set("i", this.subIcons ? "1" : "0"); u.searchParams.set("r", this.role); u.searchParams.set("m", this.mode);
      for (const k of ["embed", "open", "menu", "f"]) u.searchParams.delete(k);
      if (!this.embed) history.replaceState(null, "", u);
      const f = this.$refs && this.$refs.phone;
      if (f && this.phone) { u.searchParams.set("embed", "1"); f.src = u; }
    },
    resetMock() {
      for (const k of ["pins", "open", "groups", "recent"]) try { localStorage.removeItem("settings-nav-" + k); } catch {}
      location.reload();
    },

    ic(name) { return `<svg class="icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${ICONS[name] || ""}</svg>`; },
    area(id) { return AREAS.find(a => a.id === id); },
    here(id) { return this.page === id ? "page" : null; },
    svc(id) { return SERVICES.find(x => x.id === id); },
    get inServices() { return this.page === "services" || !!this.svc(this.page); },
    get inSettings() { return this.page === "settings" || !!this.area(this.page); },
    get groups() {
      const gs = [];
      for (const a of AREAS) {
        if (!gs.length || gs[gs.length - 1].title !== a.group) gs.push({ title: a.group, areas: [] });
        gs[gs.length - 1].areas.push(a);
      }
      return gs;
    },
    // Today: the everyday set, or every page while you're in Settings.
    get todayList() { return this.inSettings ? AREAS : AREAS.filter(a => EVERYDAY.includes(a.id)); },
    // A: closed, the pins (in the overview's order) and the page you're
    // on, if it isn't pinned; open, every page.
    pinned(id) { return this.pins.includes(id); },
    togglePin(id) { this.pins = this.pinned(id) ? this.pins.filter(p => p !== id) : [...this.pins, id]; },
    get pinList() { return this.open ? AREAS : AREAS.filter(a => this.pinned(a.id) || a.id === this.page); },
    // F: every page under Settings while you're in Settings, none
    // otherwise (as Services lists its services while you're there).
    get fList() { return this.inSettings ? AREAS : []; },
    // G: F, with pinned pages always under Settings, first; the rest
    // only while you're in Settings.
    get gPinned() { return AREAS.filter(a => this.pinned(a.id)); },
    get gRest() { return this.inSettings ? AREAS.filter(a => !this.pinned(a.id)) : []; },
    // B: a group is open if you opened it, or you're on one of its pages.
    groupOpen(g) { return this.groupsOpen.includes(g.title); },
    toggleGroup(g) { this.groupsOpen = this.groupOpen(g) ? this.groupsOpen.filter(t => t !== g.title) : [...this.groupsOpen, g.title]; },
    groupHere(g) { return g.areas.some(a => a.id === this.page); },
    // C: closed, the last four opened (the current one among them).
    get recentList() { return this.open ? AREAS : this.recent.map(id => this.area(id)).filter(Boolean); },
    // D: empty, today's everyday set; typing, every page whose title,
    // group or words match each word typed.
    get filtered() {
      const words = this.filter.toLowerCase().split(/\s+/).filter(Boolean);
      if (!words.length) return AREAS.filter(a => EVERYDAY.includes(a.id) || a.id === this.page);
      return AREAS.filter(a => { const t = (a.title + " " + a.group + " " + a.words).toLowerCase(); return words.every(w => t.includes(w)); });
    },
    pickFirst() { const f = this.filtered[0]; if (f && this.filter) { this.filter = ""; location.hash = f.id; } },

    // The page beside the sidebar.
    get title() { return this.page === "settings" ? "Settings" : (this.area(this.page) || this.svc(this.page) || MAIN.find(m => m.id === this.page) || {}).title; },
    get lede() { return this.page === "settings" ? "How your lab is set up, area by area. Each card says how it is now." : (this.area(this.page) || this.svc(this.page) || MAIN.find(m => m.id === this.page) || {}).lede; },
    get overview() {
      const out = [];
      for (const g of this.groups) {
        const cards = g.areas.map(a => ({ ...a, s: SUMMARY[a.id] }));
        if (g.title === "Servers") cards.unshift({ id: "servers", title: "Servers", s: SUMMARY.servers });
        if (g.title === "Each server") cards.push({ id: "backups", title: "Backups", s: SUMMARY.backups });
        out.push({ title: g.title, cards });
      }
      return out;
    },
  };
}
