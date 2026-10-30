// Roostack's package editor: the page (SPEC.md §8.3.7, "The package
// editor"). The YAML is the document: the form edits it in place, keeping
// comments and order, and typing in the YAML refreshes the form whenever
// it reads as YAML. Problems come from check.js (what `roostack validate`
// says) and suggest.js; each opens its field and its line.
//
// Plain DOM, no framework: the same file runs in Roostack, on the website
// (whose CSP has no 'unsafe-eval') and opened from a file. Nothing typed
// goes into innerHTML.
(function (R) {
  "use strict";

  const root = document.querySelector("[data-editor]");
  if (!root || !R.check) return;
  const S = R.schema;
  const $ = (sel, el) => (el || root).querySelector(sel);
  const $$ = (sel, el) => Array.from((el || root).querySelectorAll(sel));

  const opt = {
    where: root.dataset.where || "website",
    version: root.dataset.version || "",
    help: root.dataset.help || "https://help.roostack.cloud/",
    check: root.dataset.check || "",
    add: root.dataset.add || "",
  };
  root.classList.add("pe-where-" + opt.where);

  const state = {
    doc: null, text: "", problems: [], suggestions: [], pkg: null, lc: null,
    local: opt.where === "roostack", icon: null, imported: null, stale: false, showAll: false, focus: "",
  };

  // ---------- Small helpers ----------

  // h builds an element: h("p", {class: "x"}, "text", child).
  function h(tag, attrs, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === null || v === undefined || v === false) continue;
      if (k === "class") el.className = v;
      else if (k === "text") el.textContent = v;
      else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
      else if (v === true) el.setAttribute(k, "");
      else el.setAttribute(k, v);
    }
    for (const kid of kids.flat()) if (kid !== null && kid !== undefined && kid !== false) el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
    return el;
  }

  // codeText shows text with `code` marked, as the import's report has it.
  function codeText(s) {
    const out = [];
    String(s).split(/(`[^`]*`)/).forEach((part) => {
      if (part.startsWith("`") && part.endsWith("`") && part.length > 1) out.push(h("code", {}, part.slice(1, -1)));
      else if (part) part.split(/(\*[^*]+\*)/).forEach((p) => out.push(p.startsWith("*") && p.endsWith("*") && p.length > 2 ? h("em", {}, p.slice(1, -1)) : p));
    });
    return out;
  }

  // The icons are Roostack's (internal/web/icons.go; a test there keeps
  // these the same).
  const icons = {
    import: '<path d="M12 3v12M7 10l5 5 5-5"/><path d="M4 15v4a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-4"/>',
    spark: '<path d="M12 3v4M12 17v4M3 12h4M17 12h4M6 6l2.5 2.5M15.5 15.5 18 18M6 18l2.5-2.5M15.5 8.5 18 6"/>',
    documents: '<path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z"/><path d="M14 3v5h5M9 13h6M9 17h6"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    warn: '<path d="M12 4 2.5 20h19z"/><path d="M12 10v4M12 17h.01"/>',
    passwords: '<circle cx="8" cy="15" r="4"/><path d="m11 12 8-8M16 7l2 2M14 9l2 2"/>',
    development: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="8" r="2"/><path d="M6 7v10M18 10c0 4-6 3-11 7"/>',
    photos: '<rect x="3" y="5" width="18" height="14" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="m4 17 5-5 4 4 2.5-2.5L20 17"/>',
  };
  function icon(name) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    for (const [k, v] of Object.entries({ class: "pe-svg", viewBox: "0 0 24 24", "aria-hidden": "true", focusable: "false", fill: "none", stroke: "currentColor",
      "stroke-width": "1.8", "stroke-linecap": "round", "stroke-linejoin": "round" })) svg.setAttribute(k, v);
    // The paths are this file's own constants, never page or user text.
    svg.innerHTML = icons[name] || "";
    return svg;
  }

  function helpURL(link) {
    const [page, anchor] = String(link || "").split("#");
    const base = opt.help.endsWith("/") ? opt.help : opt.help + "/";
    return base + (page ? page.replace(/\/?$/, "/") : "") + (anchor ? "#" + anchor : "");
  }

  const store = {
    get(k) { try { return window.localStorage.getItem("roostack-package-editor-" + k); } catch { return null; } },
    set(k, v) { try { window.localStorage.setItem("roostack-package-editor-" + k, v); } catch { /* private window */ } },
  };

  // ---------- Paths ----------

  // pathKey writes a path as the checks do: containers.app.image,
  // settings[0].key.
  function pathKey(path) {
    let s = "";
    for (const seg of path) s += typeof seg === "number" ? `[${seg}]` : (s ? "." : "") + seg;
    return s;
  }

  // helpKey is the path as help.yaml names it: containers.*.image.
  function helpKey(path) {
    let s = "", f = S.fields;
    for (const seg of path) {
      if (!f) return "";
      if (f.type === "object") {
        f = f.fields.find((x) => x.name === seg);
        s += (s ? "." : "") + seg;
      } else if (f.type === "list") { f = f.item; s += "[]"; }
      else if (f.type === "map") { f = f.item; s += ".*"; }
      else return s;
    }
    return s;
  }

  // fieldOf is the format's field at a path.
  function fieldOf(path) {
    let f = S.fields;
    for (const seg of path) {
      if (!f) return null;
      if (f.type === "object") f = f.fields.find((x) => x.name === seg);
      else if (f.type === "list" || f.type === "map") f = f.item;
      else return null;
    }
    return f;
  }
  const help = (path) => {
    const e = S.help[helpKey(path)];
    return e || {};
  };

  // resolve turns a problem's path into the document's path, with list
  // indexes (settings.admin_token becomes settings[2]); what isn't written
  // yet stays as the path names it.
  function resolve(pathText) {
    if (!pathText) return [];
    const segs = R.check.splitPath(pathText);
    const out = [];
    let node = state.doc ? state.doc.contents : null;
    for (const seg of segs) {
      if (YAML.isMap(node)) {
        const pair = node.items.find((it) => YAML.isScalar(it.key) && String(it.key.value) === String(seg));
        out.push(typeof seg === "number" ? seg : String(seg));
        node = pair ? pair.value : null;
      } else if (YAML.isSeq(node)) {
        const i = typeof seg === "number" ? seg : node.items.findIndex((it) => YAML.isMap(it) && it.get("key") === seg);
        out.push(i < 0 ? seg : i);
        node = i >= 0 ? node.items[i] : null;
      } else {
        out.push(seg);
        node = null;
      }
    }
    return out;
  }

  // lineOf is the line a path starts on in the YAML.
  function lineOf(path) {
    if (!state.doc || !state.lc) return 0;
    let node = state.doc.contents, keyNode = null, found = 0;
    for (const seg of path) {
      if (YAML.isMap(node)) {
        const pair = node.items.find((it) => YAML.isScalar(it.key) && String(it.key.value) === String(seg));
        if (!pair) break;
        keyNode = pair.key;
        node = pair.value;
      } else if (YAML.isSeq(node)) {
        if (!node.items[seg]) break;
        keyNode = null;
        node = node.items[seg];
      } else break;
      found++;
    }
    if (!found) return 0;
    const n = keyNode || node;
    return n && n.range ? state.lc.linePos(n.range[0]).line : 0;
  }

  // ---------- Reading and changing the document ----------

  const get = (path) => {
    if (!state.doc) return undefined;
    const v = state.doc.getIn(path, false);
    return v;
  };
  const node = (path) => (state.doc ? state.doc.getIn(path, true) : undefined);
  const textOf = (v) => (v === null || v === undefined ? "" : YAML.isNode(v) ? "" : String(v));
  const keysOf = (path) => {
    const n = node(path);
    return YAML.isMap(n) ? n.items.map((it) => String(it.key && it.key.value)) : [];
  };
  const itemsOf = (path) => {
    const n = node(path);
    return YAML.isSeq(n) ? n.items : [];
  };

  // change sets a value (an empty one takes the field out), then tidies
  // what's left empty and writes the YAML again. A value that's already
  // there is changed in the text itself, so the rest of the file stays
  // exactly as it was written; anything else prints the document again
  // (comments kept, their alignment not).
  function change(path, value) {
    if (!state.doc) return;
    const empty = value === "" || value === null || value === undefined || value === false;
    if (!empty && typeof value !== "object" && !state.stale) {
      const n = state.doc.getIn(path, true);
      if (YAML.isScalar(n) && n.range) {
        const inFlow = path.some((_, i) => { const c = state.doc.getIn(path.slice(0, i), true); return c && c.flow; });
        const t = scalarText(value, inFlow, n);
        if (t !== null) return splice(n.range[0], n.range[1], t);
      }
    }
    if (empty) remove(path);
    else state.doc.setIn(path, value);
    written();
  }

  // scalarText is how a value is written in place of another: as YAML
  // would print it, quoted inside a flow map ({ a: b }) where a comma or
  // bracket would end it, and double-quoted if it was. null if it needs
  // more than one line.
  function scalarText(value, inFlow, old) {
    if (typeof value === "number" || typeof value === "boolean") return String(value);
    const s = String(value);
    let t = new YAML.Document(s).toString({ lineWidth: 0 }).replace(/\n$/, "");
    if (t.includes("\n")) return null;
    const quoted = /^["']/.test(t);
    if ((inFlow && /[,[\]{}#]/.test(s) && !quoted) || (old.type === "QUOTE_DOUBLE" && !t.startsWith('"'))) t = JSON.stringify(s);
    return t;
  }

  function splice(start, end, t) {
    state.text = state.text.slice(0, start) + t + state.text.slice(end);
    showText();
    check();
    saveDraft();
  }

  function showText() {
    const src = $("[data-source]");
    const top = src.scrollTop;
    src.value = state.text;
    src.scrollTop = top;
  }

  function remove(path) {
    if (!state.doc || !state.doc.hasIn(path)) return;
    state.doc.deleteIn(path);
    prune(path.slice(0, -1));
  }

  // prune takes out an emptied field: a part of an object (health,
  // env, public...), never a named entry or a list's item.
  function prune(path) {
    while (path.length) {
      const n = node(path);
      const parent = fieldOf(path.slice(0, -1));
      const empty = (YAML.isMap(n) || YAML.isSeq(n)) && n.items.length === 0;
      if (!empty || !parent || parent.type !== "object") return;
      state.doc.deleteIn(path);
      path = path.slice(0, -1);
    }
  }

  function append(path, value) {
    const n = node(path);
    if (YAML.isSeq(n)) n.add(state.doc.createNode(value));
    else state.doc.setIn(path, [value]);
  }

  const printOpts = { flowCollectionPadding: false, lineWidth: 0 };

  // written is after the form changed the document: the YAML follows.
  // The document is parsed again at once, so its positions always match
  // the text.
  function written(structural) {
    state.text = state.doc.toString(printOpts);
    showText();
    check();
    if (structural) renderForm();
    saveDraft();
  }

  // ---------- Checking ----------

  function check() {
    const r = R.check.run(state.text, { local: state.local });
    state.lc = r.lc;
    const yamlBroken = r.problems.some((p) => p.source === "yaml") && r.doc.errors.length;
    if (!yamlBroken) state.doc = r.doc;
    state.stale = !!yamlBroken;
    state.pkg = r.pkg;
    state.problems = r.problems;
    state.suggestions = r.pkg ? R.suggest.suggestions(r.pkg, { version: opt.version }) : [];
    for (const s of state.suggestions) s.line = s.path ? lineOf(resolve(s.path)) : 0;
    $("[data-stale]").hidden = !state.stale;
    renderProblems();
    renderGutter();
    markFields();
    for (const el of $$("[data-file]")) el.textContent = fileName();
  }

  function fileName() {
    const name = state.pkg ? state.pkg.name : textOf(get(["name"]));
    const version = state.pkg ? state.pkg.version : textOf(get(["version"]));
    return `${name || "package"}${version ? "-" + version : ""}.yaml`;
  }

  // ---------- The problems ----------

  const kinds = {
    validation: { pill: "bad", one: "validation error", many: "validation errors", sev: "Validation" },
    registry: { pill: "warn", one: "registry error", many: "registry errors", sev: "Registry" },
    suggestion: { pill: "", one: "suggestion", many: "suggestions", sev: "Suggestion" },
  };

  function allProblems() {
    return state.problems.concat(state.suggestions);
  }

  function renderProblems() {
    const all = allProblems();
    const tally = $("[data-tally]");
    tally.replaceChildren();
    let errors = 0;
    for (const kind of ["validation", "registry", "suggestion"]) {
      const n = all.filter((p) => p.kind === kind).length;
      if (kind !== "suggestion") errors += n;
      if (!n && kind !== "validation") continue;
      const k = kinds[kind];
      tally.append(h("span", { class: "pe-pill" + (k.pill ? " pe-pill-" + k.pill : "") + (n === 0 ? " pe-pill-ok" : "") },
        n === 0 ? "No validation errors" : `${n} ${n === 1 ? k.one : k.many}`));
    }
    const count = $("[data-tab-count]");
    count.hidden = errors === 0;
    count.textContent = String(errors);

    const list = $("[data-problem-list]");
    list.replaceChildren();
    const order = { validation: 0, registry: 1, suggestion: 2 };
    const shown = [...all].sort((a, b) => order[a.kind] - order[b.kind] || (a.line || 1e9) - (b.line || 1e9));
    const limit = state.showAll ? shown.length : 6;
    for (const p of shown.slice(0, limit)) list.append(problemItem(p));
    const more = $("[data-act=more]");
    more.hidden = shown.length <= 6;
    more.textContent = state.showAll ? "Show fewer" : `${shown.length - 6} more`;
    if (!shown.length) list.append(h("li", { class: "pe-problem pe-none" }, "Nothing to fix: roostack validate would accept it."));
  }

  function problemItem(p) {
    const k = kinds[p.kind];
    const warnOnly = p.kind === "registry" && state.local;
    const hasPath = p.path && p.message.startsWith(p.path);
    const li = h("li", { class: `pe-problem pe-sev-${p.kind}${warnOnly ? " pe-sev-allowed" : ""}` },
      h("span", { class: "pe-sev" }, warnOnly ? "Allowed" : k.sev),
      h("span", { class: "pe-msg" },
        hasPath ? h("button", { type: "button", class: "pe-link pe-path", onclick: () => goTo(p) }, p.path) : null,
        hasPath ? p.message.slice(p.path.length).replace(/^:\s*/, ": ") : p.message,
        actionsFor(p)),
      p.line ? h("button", { type: "button", class: "pe-line", onclick: () => goTo(p), "aria-label": `Line ${p.line}` }, `line ${p.line}`) : h("span"));
    return li;
  }

  function actionsFor(p) {
    if (!p.actions || !p.actions.length) return null;
    return h("span", { class: "pe-chips" }, p.actions.map((a) => h("button", { type: "button", class: "pe-chip", onclick: () => act(a) }, a.label)));
  }

  // act applies a suggestion's action.
  function act(a) {
    for (const op of a.ops) {
      if (op.op === "set") state.doc.setIn(op.path, op.value);
      else if (op.op === "append") append(op.path, op.value);
      else if (op.op === "delete") remove(op.path);
    }
    written(true);
  }

  // goTo opens a problem's field in the form and its line in the YAML.
  function goTo(p) {
    const path = resolve(p.path);
    if (root.dataset.tab === "problems") setTab("form");
    let key = pathKey(path), el = null;
    for (let n = path.length; n >= 0 && !el; n--) {
      key = pathKey(path.slice(0, n));
      el = $(`[data-path="${CSS.escape(key)}"]`, $("[data-form]"));
    }
    if (el) {
      const details = el.closest("details");
      if (details) details.open = true;
      for (let d = el.parentElement; d; d = d.parentElement) if (d.tagName === "DETAILS") d.open = true;
      const input = el.matches("input, select, textarea") ? el : $("input, select, textarea, button", el);
      el.scrollIntoView({ block: "center", behavior: "smooth" });
      if (input) input.focus({ preventScroll: true });
    }
    showLine(p.line || lineOf(path), true);
  }

  // ---------- The YAML pane ----------

  function renderGutter() {
    const g = $("[data-gutter]");
    const lines = state.text.split("\n").length;
    const marks = {};
    for (const p of allProblems()) {
      if (!p.line) continue;
      const sev = p.kind === "validation" ? "bad" : p.kind === "registry" ? "warn" : "hint";
      if (!marks[p.line] || sev === "bad" || (sev === "warn" && marks[p.line] === "hint")) marks[p.line] = sev;
    }
    const frag = document.createDocumentFragment();
    for (let i = 1; i <= lines; i++) frag.append(h("span", { class: marks[i] ? "pe-mark-" + marks[i] : null, "data-line": i }, String(i)));
    g.replaceChildren(frag);
    g.scrollTop = $("[data-source]").scrollTop;
    markLine(state.focusLine);
  }

  function markLine(line) {
    state.focusLine = line;
    for (const el of $$(".pe-gutter .is-focus")) el.classList.remove("is-focus");
    if (line) { const el = $(`.pe-gutter [data-line="${line}"]`); if (el) el.classList.add("is-focus"); }
  }

  // showLine brings a line into view in the YAML, and with select puts
  // the cursor there (without moving the page).
  function showLine(line, select) {
    if (!line) return;
    markLine(line);
    const src = $("[data-source]");
    const lh = parseFloat(getComputedStyle(src).lineHeight) || 20;
    const want = (line - 1) * lh;
    if (want < src.scrollTop || want > src.scrollTop + src.clientHeight - lh * 2) src.scrollTop = Math.max(0, want - src.clientHeight / 3);
    if (select && document.activeElement !== src) {
      const lines = state.text.split("\n");
      let start = 0;
      for (let i = 0; i < line - 1 && i < lines.length; i++) start += lines[i].length + 1;
      try { src.setSelectionRange(start, start + (lines[line - 1] || "").length); } catch { /* not focusable yet */ }
    }
  }

  // ---------- The form ----------

  // The parts, in Write a package's order; each lists its fields, those
  // after "more" behind More options.
  const parts = [
    { id: "name", title: "Name and version", fields: [["title"], ["name"], ["description"], ["version"]] },
    { id: "about", title: "The project", fields: [["about", "maker"], ["about", "source"], ["about", "website"], ["about", "licence"], "logo", ["about", "categories"],
      ["about", "keywords"], ["built_with"], ["about", "releases"], ["about", "support"], ["requires", "roostack"]] },
    { id: "containers", title: "Containers", map: ["containers"], add: "Add a container",
      fields: ["image", "memory", "env", "secrets", "mounts", "health", "depends_on"], more: ["command", "devices", "when", "quadlet"] },
    { id: "storage", title: "Data", map: ["storage"], add: "Add storage", fields: ["title", "class", "location"] },
    { id: "databases", title: "Databases", map: ["databases"], add: "Add a database", fields: ["type", "path", "image", "backup", "memory"], more: ["schemas", "quadlet"] },
    { id: "routes", title: "Web addresses", map: ["routes"], add: "Add a web address",
      fields: ["container", "port", "subdomain", ["public", "login_paths"], ["public", "private_paths"], ["public", "unsafe"]],
      more: ["max_body_size", "client_ip_header", ["monitor", "path"], ["monitor", "statuses"], "exposure"] },
    { id: "settings", title: "Settings and setup", lists: [
      { path: ["settings"], add: "Add a setting", title: (v) => v.get && v.get("key"), fields: ["key", "title", "type", "default", "description"],
        more: ["generate", "required", "min_length", "install_only", "reveal", "install_value", "from_hook"] },
      { path: ["hooks"], add: "Add a setup step, action or single sign-on step", title: (v) => v.get && (v.get("title") || v.get("on")),
        fields: ["on", "name", "title", "container", "command", "env", "secrets"], more: ["description", "inputs", "saves"] },
      { path: ["links"], add: "Add a link into the app", title: (v) => v.get && v.get("title"), fields: ["title", "route", "path", "description"] },
    ] },
    { id: "sso", title: "Single sign-on", fields: [["sso", "oidc", "route"], ["sso", "oidc", "redirect_paths"], ["sso", "oidc", "redirect_uris"],
      ["sso", "forward_auth", "route"], ["sso", "forward_auth", "bypass_paths"], ["sso", "env"], ["sso", "secrets"], ["sso", "note"]],
      more: [["sso", "oidc", "admins"], ["sso", "oidc", "existing_accounts"], ["sso", "secret_env"], ["sso", "oidc", "groups", "users"], ["sso", "oidc", "groups", "admins"]] },
    { id: "more", title: "More", map: ["ports"], add: "Add an extra connection", fields: ["title", "container", "port", "host_port", "protocol", "enabled"], more: ["description", "exposure"],
      lists: [{ path: ["upgrade_notes"], add: "Add an upgrade note", title: (v) => v.get && v.get("version"), fields: ["version", "note"] }], extra: [["schema"]] },
  ];

  const enums = {
    "settings[].type": S.lists.settingTypes, "hooks[].inputs[].type": ["string", "secret"], "storage.*.class": S.lists.classes,
    "storage.*.location": S.lists.locations, "databases.*.type": S.lists.databaseTypes, "routes.*.exposure": S.lists.exposures,
    "ports.*.exposure": S.lists.exposures, "ports.*.protocol": ["tcp", "udp"], "hooks[].on": S.lists.hookEvents, "containers.*.devices[].gpu": S.lists.gpuKinds,
    "sso.oidc.existing_accounts": S.lists.ssoExisting,
  };
  // The value a field has when it's left out, shown beside its choice.
  const defaults = { "storage.*.location": "service", "ports.*.protocol": "tcp" };
  const references = {
    "routes.*.container": ["containers"], "ports.*.container": ["containers"], "hooks[].container": ["containers"],
    "links[].route": ["routes"], "containers.*.mounts[].storage": ["storage"],
    "sso.oidc.route": ["routes"], "sso.forward_auth.route": ["routes"],
  };
  const long = new Set(["upgrade_notes[].note", "routes.*.public.unsafe", "routes.*.public.private_paths[].reason", "containers.*.health.cmd", "settings[].description", "hooks[].description",
    "sso.note", "sso.forward_auth.bypass_paths[].reason"]);

  function renderForm() {
    const form = $("[data-form]");
    const scroll = window.scrollY;
    const focus = document.activeElement && root.contains(document.activeElement) ? document.activeElement.closest("[data-path]") : null;
    const focusPath = focus ? focus.dataset.path : "";
    form.replaceChildren();
    const index = $("[data-index]");
    index.replaceChildren();
    if (!state.doc || (state.doc.contents && !YAML.isMap(state.doc.contents))) {
      form.append(h("p", { class: "pe-note" }, "The package isn't a map of fields yet, so there's nothing to show here: fix it in the YAML."));
      return;
    }
    for (const part of parts) {
      index.append(h("a", { href: "#pe-" + part.id, "data-part": part.id }, part.title));
      form.append(renderPart(part));
    }
    markFields();
    if (focusPath) {
      const el = $(`[data-path="${CSS.escape(focusPath)}"]`, form);
      const input = el && (el.matches("input, select, textarea") ? el : $("input, select, textarea", el));
      if (input) input.focus({ preventScroll: true });
    }
    window.scrollTo(window.scrollX, scroll);
  }

  function renderPart(part) {
    const sec = h("section", { class: "pe-card pe-section", id: "pe-" + part.id, "data-part": part.id },
      h("h2", {}, part.title, h("span", { class: "pe-pill pe-part-count", hidden: true })));
    const sectionHelp = part.map ? help(part.map) : part.id === "about" || part.id === "sso" ? S.help[part.id] : null;
    if (sectionHelp && sectionHelp.help) sec.append(h("p", { class: "pe-hint" }, sectionHelp.help, " ", helpLink(sectionHelp.link)));
    if (part.fields && !part.map) {
      for (const f of part.fields) sec.append(f === "logo" ? logoField() : fieldEl(f));
      if (part.more && part.more.length) {
        const more = h("details", { class: "pe-more-options" }, h("summary", {}, "More options"));
        for (const f of part.more) more.append(fieldEl(f));
        if (part.more.some((f) => get(f) !== undefined)) more.open = true;
        sec.append(more);
      }
    }
    if (part.map) {
      for (const name of keysOf(part.map)) sec.append(entryCard(part, part.map.concat(name), name));
      sec.append(h("button", { type: "button", class: "pe-button secondary small", onclick: () => addEntry(part.map) }, part.add));
    }
    for (const l of part.lists || []) {
      const box = h("div", { class: "pe-list-part" }, h("h3", {}, help(l.path).label || l.path[0]));
      const hp = help(l.path);
      if (hp.help) box.append(h("p", { class: "pe-hint" }, hp.help, " ", helpLink(hp.link)));
      itemsOf(l.path).forEach((item, i) => box.append(itemCard(l, l.path.concat(i), item, i)));
      box.append(h("button", { type: "button", class: "pe-button secondary small", onclick: () => addItem(l.path) }, l.add));
      sec.append(box);
    }
    for (const f of part.extra || []) sec.append(fieldEl(f));
    return sec;
  }

  function helpLink(link) {
    return link ? h("a", { href: helpURL(link), target: "_blank", rel: "noopener" }, "Tell me more") : null;
  }

  function entryCard(part, path, name) {
    const v = node(path);
    const sum = h("summary", {}, h("strong", {}, name), summaryOf(part.id, v));
    const card = h("details", { class: "pe-item", open: true, "data-path": pathKey(path) }, sum);
    const nameField = h("div", { class: "pe-field pe-name-field" },
      h("label", { class: "pe-label", for: "pe-n-" + pathKey(path) }, "Name"),
      h("span", { class: "pe-inline" },
        h("input", { id: "pe-n-" + pathKey(path), class: "pe-input mono", value: name, spellcheck: "false", onchange: (e) => rename(part.map, name, e.target.value.trim()) }),
        h("button", { type: "button", class: "pe-button quiet small", onclick: () => { remove(path); written(true); } }, "Remove")));
    card.append(nameField);
    const fields = (part.fields || []).map((f) => [].concat(f));
    for (const f of fields) card.append(fieldEl(path.concat(f)));
    if (part.more && part.more.length) {
      const more = h("details", { class: "pe-more-options" }, h("summary", {}, "More options"));
      for (const f of part.more) more.append(fieldEl(path.concat([].concat(f))));
      if (part.more.some((f) => get(path.concat([].concat(f))) !== undefined)) more.open = true;
      card.append(more);
    }
    return card;
  }

  function summaryOf(id, v) {
    if (!YAML.isMap(v)) return null;
    const t = id === "containers" ? v.get("image") : id === "storage" ? v.get("title") : id === "databases" ? v.get("type") : id === "routes" ? `${v.get("container") || ""} : ${v.get("port") || ""}` : v.get("title");
    return t ? h("code", {}, String(t)) : null;
  }

  function itemCard(l, path, item, i) {
    const title = (l.title && l.title(item)) || `#${i + 1}`;
    const card = h("details", { class: "pe-item", open: true, "data-path": pathKey(path) },
      h("summary", {}, h("strong", {}, String(title))));
    card.append(h("div", { class: "pe-item-tools" }, h("button", { type: "button", class: "pe-button quiet small", onclick: () => { remove(path); written(true); } }, "Remove")));
    for (const f of l.fields) card.append(fieldEl(path.concat([].concat(f))));
    if (l.more) {
      const more = h("details", { class: "pe-more-options" }, h("summary", {}, "More options"));
      for (const f of l.more) more.append(fieldEl(path.concat([].concat(f))));
      if (l.more.some((f) => get(path.concat([].concat(f))) !== undefined)) more.open = true;
      card.append(more);
    }
    return card;
  }

  let idn = 0;

  // fieldEl draws one field: its label, its input by type, and a place for
  // its problems and help.
  function fieldEl(path) {
    const f = fieldOf(path);
    const hk = helpKey(path);
    const hp = S.help[hk] || {};
    const id = "pe-f" + (++idn);
    const key = pathKey(path);
    const wrap = h("div", { class: "pe-field", "data-path": key, "data-help": hk });
    if (!f) return wrap;
    const label = h("label", { class: "pe-label", for: id }, hp.label || path[path.length - 1]);
    let input;
    const val = get(path);

    if (hk === "about.categories") {
      input = categoriesInput(path, val);
      wrap.append(h("span", { class: "pe-label" }, hp.label), input);
    } else if (f.type === "bool") {
      input = h("input", { id, type: "checkbox", role: "switch", checked: val === true || val === "true", onchange: (e) => change(path, e.target.checked ? true : null) });
      wrap.append(h("label", { class: "pe-check", for: id }, input, " ", hp.label || path[path.length - 1]));
    } else if (enums[hk] && enums[hk].length >= 2 && enums[hk].length <= 4 && (!textOf(val) || enums[hk].includes(textOf(val)))) {
      // A small fixed choice: a row of buttons (radios), as Roostack's
      // segmented control; nothing chosen while it's unset.
      const lid = id + "-l";
      input = h("div", { class: "pe-seg", role: "radiogroup", "aria-labelledby": lid },
        enums[hk].map((o, i) => h("label", {}, h("input", { type: "radio", name: id, value: o, id: i ? undefined : id, checked: textOf(val) === o,
          onchange: () => change(path, o) }), h("span", {}, o + (defaults[hk] === o ? " (the default)" : "")))));
      wrap.append(h("span", { class: "pe-label", id: lid }, hp.label || path[path.length - 1]), input);
    } else if (enums[hk] || references[hk]) {
      const options = enums[hk] || keysOf(references[hk]);
      input = h("select", { id, class: "pe-input", onchange: (e) => change(path, e.target.value) },
        h("option", { value: "" }, hk === "storage.*.location" ? "service (the default)" : hk === "ports.*.protocol" ? "tcp (the default)" : "Choose…"),
        options.map((o) => h("option", { value: o, selected: textOf(val) === o }, o)));
      if (textOf(val) && !options.includes(textOf(val))) input.append(h("option", { value: textOf(val), selected: true }, textOf(val)));
      wrap.append(label, input);
    } else if (f.type === "string" || f.type === "int" || f.type === "any") {
      const attrs = { id, class: "pe-input" + (/\.(image|name|version|path|key|subdomain|memory|port)$|_header$|\.when$/.test(key) ? " mono" : ""), value: textOf(val), placeholder: hp.example || "", spellcheck: "false",
        onchange: (e) => change(path, typed(f, e.target.value)) };
      if (f.type === "int") { attrs.inputmode = "numeric"; }
      input = long.has(hk) ? h("textarea", Object.assign(attrs, { rows: 2 }), textOf(val)) : h("input", attrs);
      wrap.append(label, input);
    } else if (f.type === "list" && f.item.type !== "object") {
      input = listInput(path, f, hp);
      wrap.append(h("span", { class: "pe-label" }, hp.label || path[path.length - 1]), input);
    } else if (f.type === "map" && f.item.type === "string") {
      input = mapInput(path, hk);
      wrap.append(h("span", { class: "pe-label" }, hp.label || path[path.length - 1]), input);
    } else if (f.type === "map" && f.item.type === "map") {
      input = containerMapsInput(path, hk);
      wrap.append(h("span", { class: "pe-label" }, hp.label || path[path.length - 1]), input);
    } else if (f.type === "list" && f.item.type === "object") {
      input = rowsInput(path, f, hk);
      wrap.append(h("span", { class: "pe-label" }, hp.label || path[path.length - 1]), input);
    } else if (f.type === "object") {
      wrap.classList.add("pe-group");
      wrap.append(h("span", { class: "pe-label" }, hp.label || path[path.length - 1]));
      if (hp.help) wrap.append(h("p", { class: "pe-hint" }, hp.help));
      for (const c of f.fields) {
        if (c.type === "object") for (const cc of c.fields) wrap.append(fieldEl(path.concat(c.name, cc.name)));
        else wrap.append(fieldEl(path.concat(c.name)));
      }
      return wrap;
    } else if (f.type === "quadlet") {
      input = quadletInput(path);
      wrap.append(h("span", { class: "pe-label" }, hp.label || "Quadlet lines"), input);
    }
    wrap.append(h("div", { class: "pe-messages" }), h("div", { class: "pe-help", hidden: true }));
    return wrap;
  }

  // typed reads what's typed as the field's type: a number for an int,
  // and for any (a setting's default) true, false or a number as such.
  function typed(f, s) {
    s = s.trim() === "" ? "" : s;
    if (s === "") return "";
    if (f.type === "int" && /^-?\d+$/.test(s.trim())) return Number(s.trim());
    if (f.type === "any") {
      if (s === "true" || s === "false") return s === "true";
      if (/^-?\d+$/.test(s)) return Number(s);
    }
    return s;
  }

  function categoriesInput(path, val) {
    const chosen = YAML.isSeq(val) ? val.items.map((x) => String(x.value !== undefined ? x.value : x)) : [];
    return h("div", { class: "pe-chips pe-cats", role: "group", "data-path": pathKey(path) },
      S.categories.map((c) => h("button", { type: "button", class: "pe-chip" + (chosen.includes(c.key) ? " is-on" : ""), "aria-pressed": String(chosen.includes(c.key)),
        onclick: () => {
          const next = chosen.includes(c.key) ? chosen.filter((k) => k !== c.key) : chosen.concat(c.key);
          change(path, next.length ? next : null);
          written(true);
        } }, c.title)));
  }

  // listInput is a list of words or paths: each as a chip to take out, and
  // a box to add one.
  function listInput(path, f, hp) {
    const items = itemsOf(path).map((x) => (YAML.isScalar(x) ? x.value : x));
    const box = h("div", { class: "pe-list" });
    const chips = h("div", { class: "pe-chips" });
    items.forEach((v, i) => chips.append(h("span", { class: "pe-chip is-on pe-value" }, h("code", {}, String(v)),
      h("button", { type: "button", class: "pe-x", "aria-label": `Take out ${v}`, onclick: () => { remove(path.concat(i)); written(true); } }, "×"))));
    const input = h("input", { class: "pe-input mono", placeholder: hp.example || "", spellcheck: "false", "aria-label": `Add to ${hp.label || path[path.length - 1]}`,
      onkeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); addIt(); } } });
    function addIt() {
      const v = input.value.trim();
      if (!v) return;
      for (const part of f.item.type === "string" && /keywords|categories|built_with/.test(pathKey(path)) ? v.split(/\s*,\s*/) : [v]) {
        if (part) append(path, f.item.type === "int" && /^\d+$/.test(part) ? Number(part) : part);
      }
      written(true);
    }
    box.append(chips, h("span", { class: "pe-inline" }, input, h("button", { type: "button", class: "pe-button secondary small", onclick: addIt }, "Add")));
    return box;
  }

  // mapInput is env or secrets: a variable and its value per row.
  // containerMapsInput draws a map of containers, each with its own
  // variables (sso.env, sso.secrets): a block per container, and a way to
  // add one of the package's other containers.
  function containerMapsInput(path, hk) {
    const box = h("div", { class: "pe-container-maps" });
    const present = keysOf(path);
    for (const c of present) {
      box.append(h("div", { class: "pe-container-map", "data-path": pathKey(path.concat(c)) },
        h("span", { class: "pe-inline" }, h("strong", {}, c),
          h("button", { type: "button", class: "pe-button quiet small", "aria-label": `Take out ${c}`, onclick: () => { remove(path.concat(c)); written(true); } }, "Remove")),
        mapInput(path.concat(c), hk)));
    }
    const others = keysOf(["containers"]).filter((c) => !present.includes(c));
    if (others.length) {
      box.append(h("select", { class: "pe-input", "aria-label": "Add a container", onchange: (e) => {
        if (!e.target.value) return;
        state.doc.setIn(path.concat(e.target.value), state.doc.createNode({}));
        written(true);
      } }, h("option", { value: "" }, "Add a container…"), others.map((c) => h("option", { value: c }, c))));
    }
    return box;
  }

  function mapInput(path, hk) {
    const box = h("div", { class: "pe-rows" });
    const isSecrets = /\.secrets$/.test(hk);
    const ssoSecret = hk === "sso.secrets";
    const secretEnv = hk === "sso.secret_env";
    const listId = "pe-dl" + (++idn);
    const refs = ssoSecret ? S.lists.ssoClientSecret : isSecrets ? secretRefs(hk) : secretEnv ? ["{{ json .SSO.ClientSecret }}"].concat(templateRefs()) : templateRefs();
    box.append(h("datalist", { id: listId }, refs.map((r) => h("option", { value: r }))));
    for (const k of keysOf(path)) {
      const v = textOf(get(path.concat(k)));
      box.append(h("div", { class: "pe-row", "data-path": pathKey(path.concat(k)) },
        h("input", { class: "pe-input mono", value: k, "aria-label": "Variable", spellcheck: "false", onchange: (e) => renameKey(path, k, e.target.value.trim()) }),
        h("input", { class: "pe-input mono", value: v, "aria-label": `${k}'s value`, list: listId, spellcheck: "false", onchange: (e) => { state.doc.setIn(path.concat(k), e.target.value); written(); } }),
        h("button", { type: "button", class: "pe-button quiet small", "aria-label": `Take out ${k}`, onclick: () => { remove(path.concat(k)); written(true); } }, "Remove")));
    }
    const nk = h("input", { class: "pe-input mono", placeholder: "NAME", "aria-label": "New variable", spellcheck: "false" });
    const nv = h("input", { class: "pe-input mono", placeholder: ssoSecret ? S.lists.ssoClientSecret[0] : isSecrets ? "a secret setting, or databases.main.password" :
      secretEnv ? '{"secret": {{ json .SSO.ClientSecret }}}' : "value or {{ template }}", "aria-label": "Its value", list: listId, spellcheck: "false" });
    box.append(h("div", { class: "pe-row pe-row-new" }, nk, nv, h("button", { type: "button", class: "pe-button secondary small", onclick: () => {
      const k = nk.value.trim();
      if (!k) return;
      state.doc.setIn(path.concat(k), nv.value);
      written(true);
    } }, "Add")));
    return box;
  }

  function secretRefs(hk) {
    const out = [];
    // A hook's secrets: the client secret too, for an sso hook.
    if (hk === "hooks[].secrets" && get(["sso", "oidc"]) !== undefined) out.push(S.lists.ssoClientSecret[0]);
    for (const s of itemsOf(["settings"])) if (YAML.isMap(s) && s.get("type") === "secret" && s.get("key")) out.push(String(s.get("key")));
    for (const d of keysOf(["databases"])) {
      const t = get(["databases", d, "type"]);
      if (["postgres", "mysql", "mariadb"].includes(t)) out.push(`databases.${d}.password`);
      if (t === "postgres") out.push(`databases.${d}.url`);
    }
    return out;
  }

  function templateRefs() {
    const out = [];
    for (const r of keysOf(["routes"])) out.push(`{{ .Routes.${r}.URL }}`, `{{ .Routes.${r}.Host }}`);
    for (const s of itemsOf(["settings"])) if (YAML.isMap(s) && s.get("type") !== "secret" && s.get("key")) out.push(`{{ .Settings.${s.get("key")} }}`);
    for (const d of keysOf(["databases"])) if (get(["databases", d, "type"]) !== "sqlite") out.push(`{{ .Databases.${d}.Host }}`, `{{ .Databases.${d}.Port }}`, `{{ .Databases.${d}.User }}`, `{{ .Databases.${d}.Name }}`);
    for (const c of keysOf(["containers"])) out.push(`{{ (index .Containers "${c}").Host }}`);
    if (get(["sso"]) !== undefined) {
      for (const k of ["DiscoveryURL", "Issuer", "ClientID", "AdminGroup", "UserGroup", "ButtonText", "ProviderName", "AuthorizeURL", "TokenURL", "UserinfoURL", "Enabled"]) out.push(`{{ .SSO.${k} }}`);
    }
    out.push("{{ .Lab.TimeZone }}", "{{ .Lab.Domain }}", "{{ .Service.Name }}");
    return out;
  }

  // rowsInput is a list of small objects (mounts, devices, private paths,
  // inputs), one row of their fields each.
  function rowsInput(path, f, hk) {
    const box = h("div", { class: "pe-rows" });
    itemsOf(path).forEach((item, i) => {
      const row = h("div", { class: "pe-subitem", "data-path": pathKey(path.concat(i)) });
      for (const c of f.item.fields) {
        if (hk === "hooks[].inputs" && ["default", "generate", "install_only", "reveal", "install_value"].includes(c.name)) continue;
        row.append(fieldEl(path.concat(i, c.name)));
      }
      row.append(h("button", { type: "button", class: "pe-button quiet small", onclick: () => { remove(path.concat(i)); written(true); } }, "Remove"));
      box.append(row);
    });
    const blank = { "containers.*.mounts": { storage: keysOf(["storage"])[0] || "data", path: "/data" }, "containers.*.devices": { gpu: "intel" },
      "routes.*.public.private_paths": { path: "/admin", reason: "" }, "hooks[].inputs": { key: "", type: "string" } }[hk] || {};
    box.append(h("button", { type: "button", class: "pe-button secondary small", onclick: () => { append(path, blank); written(true); } }, "Add"));
    return box;
  }

  // quadletInput lists Quadlet lines as section, key and value.
  function quadletInput(path) {
    const box = h("div", { class: "pe-rows" });
    for (const section of keysOf(path)) {
      for (const k of keysOf(path.concat(section))) {
        const v = get(path.concat(section, k));
        const values = YAML.isSeq(v) ? v.items.map((x) => (YAML.isScalar(x) ? x.value : x)) : [v];
        values.forEach((one, i) => box.append(h("div", { class: "pe-row", "data-path": pathKey(path.concat(section, k)) },
          h("code", { class: "pe-quadlet-key" }, `[${section}] ${k}=`),
          h("input", { class: "pe-input mono", value: textOf(one), "aria-label": `${k}'s value`, spellcheck: "false",
            onchange: (e) => change(YAML.isSeq(v) ? path.concat(section, k, i) : path.concat(section, k), e.target.value) }),
          h("button", { type: "button", class: "pe-button quiet small", onclick: () => { remove(YAML.isSeq(v) ? path.concat(section, k, i) : path.concat(section, k)); written(true); } }, "Remove"))));
      }
    }
    const sel = h("select", { class: "pe-input", "aria-label": "Section" }, S.lists.quadletSections.filter((s) => s !== "Install").map((s) => h("option", { value: s }, s)));
    const listId = "pe-dl" + (++idn);
    const keys = h("datalist", { id: listId });
    const fill = () => keys.replaceChildren(...(S.quadlet.allowed[sel.value] || []).map((k) => h("option", { value: k })));
    sel.addEventListener("change", fill);
    fill();
    const nk = h("input", { class: "pe-input mono", placeholder: "UserNS", list: listId, "aria-label": "Key", spellcheck: "false" });
    const nv = h("input", { class: "pe-input mono", placeholder: "keep-id:uid=1000,gid=1000", "aria-label": "Value", spellcheck: "false" });
    box.append(keys, h("div", { class: "pe-row pe-row-new pe-row-quadlet" }, sel, nk, nv, h("button", { type: "button", class: "pe-button secondary small", onclick: () => {
      if (!nk.value.trim()) return;
      const at = path.concat(sel.value, nk.value.trim());
      if (state.doc.hasIn(at)) {
        const cur = get(at);
        if (YAML.isSeq(cur)) append(at, nv.value);
        else state.doc.setIn(at, [textOf(cur), nv.value]);
      } else state.doc.setIn(at, nv.value);
      written(true);
    } }, "Add")));
    box.append(h("p", { class: "pe-hint" }, "Only the keys the package policy allows: they'll be offered as you type."));
    return box;
  }

  // logoField chooses the project's logo, checked in the browser.
  function logoField() {
    const hp = S.help["about.icon"];
    const id = "pe-logo";
    const input = h("input", { id, type: "file", accept: "image/png", class: "pe-file", onchange: (e) => chooseLogo(e.target.files[0]) });
    const wrap = h("div", { class: "pe-field pe-logo", "data-path": "about.icon", "data-help": "about.icon" },
      h("label", { class: "pe-label", for: id }, hp.label));
    const row = h("div", { class: "pe-inline" });
    if (state.icon) row.append(h("img", { src: state.icon.url, alt: "", width: 48, height: 48, class: "pe-logo-img" }), h("span", {}, state.icon.name));
    else if (textOf(get(["about", "icon"]))) row.append(h("span", { class: "pe-hint" }, `The package names ${textOf(get(["about", "icon"]))}: choose it so the editor can check it${opt.where === "roostack" ? " and add it with the package" : ""}.`));
    row.append(input);
    wrap.append(row, h("div", { class: "pe-messages" }), h("div", { class: "pe-help", hidden: true }));
    return wrap;
  }

  function chooseLogo(file) {
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      const bytes = new Uint8Array(reader.result);
      const err = R.check.checkIcon(bytes);
      if (err) {
        state.iconError = `The logo: ${err}. It must be a square PNG, at least ${S.limits.iconMinSize}×${S.limits.iconMinSize} pixels and at most ${S.limits.iconMaxBytes / 1000} KB.`;
        state.icon = null;
      } else {
        state.iconError = "";
        // A data: address, which the pages' CSP allows for images (blob: it doesn't).
        let bin = "";
        for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
        state.icon = { file, bytes, name: file.name, url: "data:image/png;base64," + btoa(bin) };
        if (!textOf(get(["about", "icon"]))) state.doc.setIn(["about", "icon"], "icon.png");
      }
      written(true);
    };
    reader.readAsArrayBuffer(file);
  }

  // ---------- Adding, removing and renaming ----------

  function unique(base, taken) {
    let n = base, i = 2;
    while (taken.includes(n)) n = `${base}-${i++}`;
    return n;
  }

  function addEntry(mapPath) {
    const first = (p) => keysOf(p)[0] || "";
    const name = unique({ containers: "app", storage: "data", databases: "main", routes: "web", ports: "port" }[mapPath[0]] || "new", keysOf(mapPath));
    const blank = {
      containers: { image: "" },
      storage: { title: "", class: "critical" },
      databases: { type: "postgres" },
      routes: { container: first(["containers"]), port: 80, subdomain: textOf(get(["name"])) || "app", exposure: "private" },
      ports: { title: "", container: first(["containers"]), port: 22, host_port: 2222 },
    }[mapPath[0]] || {};
    state.doc.setIn(mapPath.concat(name), blank);
    written(true);
  }

  function addItem(listPath) {
    const first = (p) => keysOf(p)[0] || "";
    const blank = {
      settings: { key: "", title: "", type: "string" },
      hooks: { on: "install", container: first(["containers"]), command: ["sh", "-c", ""] },
      links: { title: "", route: first(["routes"]), path: "/" },
      upgrade_notes: { version: textOf(get(["version"])), note: "" },
    }[listPath[0]] || {};
    append(listPath, blank);
    written(true);
  }

  // renameKey changes a variable's name, keeping its value and place.
  function renameKey(path, from, to) {
    if (!to || to === from) return;
    const m = node(path);
    const pair = YAML.isMap(m) && m.items.find((it) => it.key && String(it.key.value) === from);
    if (pair) pair.key = state.doc.createNode(to);
    written(true);
  }

  // rename changes an entry's name, and what refers to it.
  function rename(mapPath, from, to) {
    if (!to || to === from) return;
    if (keysOf(mapPath).includes(to)) { written(true); return; }
    renameKey(mapPath, from, to);
    const kind = mapPath[0];
    const swapText = (re, rep) => {
      const visit = (n) => {
        if (YAML.isScalar(n) && typeof n.value === "string" && re.test(n.value)) n.value = n.value.replace(re, rep);
        else if (YAML.isMap(n) || YAML.isSeq(n)) for (const it of n.items) { if (YAML.isPair(it)) visit(it.value); else visit(it); }
      };
      visit(state.doc.contents);
    };
    const esc = R.compose.escapeRegExp(from);
    const setWhere = (paths) => { for (const p of paths) if (get(p) === from) state.doc.setIn(p, to); };
    if (kind === "containers") {
      setWhere(keysOf(["routes"]).map((r) => ["routes", r, "container"]).concat(keysOf(["ports"]).map((r) => ["ports", r, "container"]), itemsOf(["hooks"]).map((_, i) => ["hooks", i, "container"])));
      swapText(new RegExp(`^containers\\.${esc}$`), `containers.${to}`);
      swapText(new RegExp(`(index \\.Containers ")${esc}(")`, "g"), `$1${to}$2`);
    } else if (kind === "storage") {
      for (const c of keysOf(["containers"])) itemsOf(["containers", c, "mounts"]).forEach((_, i) => setWhere([["containers", c, "mounts", i, "storage"]]));
      for (const d of keysOf(["databases"])) { const p = get(["databases", d, "path"]); if (typeof p === "string" && p.startsWith(from + "/")) state.doc.setIn(["databases", d, "path"], to + p.slice(from.length)); }
    } else if (kind === "databases") {
      swapText(new RegExp(`^databases\\.${esc}(\\.|$)`), `databases.${to}$1`);
      swapText(new RegExp(`\\.Databases\\.${esc}\\b`, "g"), `.Databases.${to}`);
    } else if (kind === "routes") {
      itemsOf(["links"]).forEach((_, i) => setWhere([["links", i, "route"]]));
      swapText(new RegExp(`\\.Routes\\.${esc}\\b`, "g"), `.Routes.${to}`);
    }
    written(true);
  }

  // ---------- Problems on fields, and help ----------

  function markFields() {
    const form = $("[data-form]");
    for (const el of $$(".pe-field", form)) {
      el.classList.remove("has-bad", "has-warn", "has-hint");
      const msgs = $(".pe-messages", el);
      if (msgs) msgs.replaceChildren();
    }
    const counts = {};
    for (const p of allProblems()) {
      const path = resolve(p.path);
      let el = null;
      for (let n = path.length; n > 0 && !el; n--) el = $(`.pe-field[data-path="${CSS.escape(pathKey(path.slice(0, n)))}"]`, form);
      const top = path[0];
      const part = parts.find((pt) => (pt.map && pt.map[0] === top) || (pt.lists || []).some((l) => l.path[0] === top) ||
        (pt.fields || []).some((f) => Array.isArray(f) && f[0] === top) || (pt.extra || []).some((f) => f[0] === top));
      if (part && p.kind !== "suggestion") counts[part.id] = Math.max(counts[part.id] || 0, p.kind === "validation" ? 2 : 1);
      if (!el) continue;
      const cls = p.kind === "validation" ? "has-bad" : p.kind === "registry" ? "has-warn" : "has-hint";
      el.classList.add(cls);
      if (p.kind === "suggestion") continue;
      const msgs = $(".pe-messages", el);
      if (msgs) msgs.append(h("p", { class: "pe-msg-" + (p.kind === "validation" ? "bad" : "warn") }, p.message.startsWith(p.path) ? p.message.slice(p.path.length).replace(/^[:.]\s*/, "") : p.message));
    }
    if (state.iconError) {
      const logo = $(".pe-logo .pe-messages", form);
      if (logo) logo.append(h("p", { class: "pe-msg-bad" }, state.iconError));
    }
    for (const a of $$("[data-index] a")) a.className = counts[a.dataset.part] === 2 ? "has-bad" : counts[a.dataset.part] === 1 ? "has-warn" : "";
    if (state.focus) showHelp($(`.pe-field[data-path="${CSS.escape(state.focus)}"]`, form));
  }

  // showHelp opens a field's help: what it's for, its suggestions, and a
  // link to the help site.
  function showHelp(el) {
    for (const other of $$(".pe-field.is-focus")) if (other !== el) { other.classList.remove("is-focus"); const hp = $(":scope > .pe-help", other); if (hp) hp.hidden = true; }
    if (!el) return;
    const box = $(":scope > .pe-help", el);
    if (!box) return;
    const hp = S.help[el.dataset.help] || {};
    const key = el.dataset.path;
    const mine = state.suggestions.filter((s) => { const k = pathKey(resolve(s.path)); return k === key || k.startsWith(key + ".") || k.startsWith(key + "["); });
    // Drawn again only when it says something else, so a button in it
    // isn't replaced while it's being pressed.
    const sig = JSON.stringify([key, mine.map((s) => [s.message, s.actions.map((a) => a.label)])]);
    if (el.classList.contains("is-focus") && box.dataset.sig === sig) return;
    box.dataset.sig = sig;
    box.replaceChildren();
    if (hp.help) box.append(h("p", {}, hp.help));
    for (const s of mine) box.append(h("div", { class: "pe-suggest" }, h("p", {}, icon("spark"), " ", s.message), actionsFor(s)));
    if (hp.link) box.append(h("p", { class: "pe-hint" }, helpLink(hp.link)));
    box.hidden = !box.childNodes.length;
    el.classList.add("is-focus");
    state.focus = key;
  }

  // ---------- Views ----------

  function setView(v) {
    root.dataset.view = v;
    for (const s of $$(".pe-view")) s.hidden = s.dataset.viewName !== v;
    if (v === "start") showDraft();
  }

  function setTab(t) {
    root.dataset.tab = t;
    for (const b of $$("[data-tab]")) b.setAttribute("aria-selected", String(b.dataset.tab === t));
  }

  function load(text, imported) {
    state.text = text;
    state.icon = null;
    state.iconError = "";
    state.imported = imported || null;
    state.focus = "";
    $("[data-source]").value = text;
    const r = R.check.run(text, { local: state.local });
    state.doc = r.doc.errors.length ? new YAML.Document({}) : r.doc;
    $("[data-imported]").hidden = !imported;
    if (imported) $("[data-imported-groups]").replaceChildren(reportGroups(imported.report));
    $("[data-server]").hidden = true;
    renderForm();
    check();
    setView("edit");
    setTab("form");
    saveDraft();
    window.scrollTo(0, 0);
  }

  function saveDraft() {
    if (state.text) store.set("draft", JSON.stringify({ text: state.text, at: Date.now() }));
  }

  function showDraft() {
    const box = $("[data-draft]");
    let d = null;
    try { d = JSON.parse(store.get("draft") || "null"); } catch { d = null; }
    box.hidden = !(d && d.text);
    if (d && d.text) {
      const name = /^name:\s*(\S+)/m.exec(d.text), version = /^version:\s*(\S+)/m.exec(d.text);
      $("[data-draft-what]").textContent = `${name ? name[1] : "A package"}${version ? " " + version[1] : ""}, from ${new Date(d.at).toLocaleString()}.`;
      state.draft = d.text;
    }
  }

  const scratch = `schema: roostack/v1alpha1
name: myapp
title: My App
description: What it does, in one line
version: 1.0.0-roostack.1

containers:
  app:
    image: ""
    memory: ""

storage:
  data:
    title: ""
    class: critical

routes:
  web:
    container: app
    port: 8080
    subdomain: myapp
    exposure: private
`;

  // ---------- The import ----------

  function reportGroups(report) {
    const frag = document.createDocumentFragment();
    const groups = [["converted", "Converted", "ok"], ["notAllowed", "Not allowed", "warn"], ["notNeeded", "Not needed in Roostack", ""], ["overToYou", "Over to you", "hi"]];
    for (const [k, title, pill] of groups) {
      const items = report[k] || [];
      if (!items.length) continue;
      frag.append(h("h3", { class: "pe-report-head" }, h("span", { class: "pe-pill" + (pill ? " pe-pill-" + pill : "") }, String(items.length)), " ", title),
        h("ul", { class: "pe-report-list" }, items.map((t) => h("li", {}, codeText(t)))));
    }
    return frag;
  }

  function runImport() {
    const err = $("[data-import-error]");
    const r = R.compose.importCompose($("[data-compose]").value, { env: $("[data-env]").value, version: opt.version, date: new Date().toISOString().slice(0, 10) });
    err.hidden = !r.error;
    if (r.error) { err.textContent = r.error; $("[data-report]").hidden = true; return; }
    state.pendingImport = r;
    $("[data-report-summary]").replaceChildren(...codeText(r.summary));
    $("[data-report-groups]").replaceChildren(reportGroups(r.report));
    $("[data-report]").hidden = false;
    $("[data-report]").scrollIntoView({ block: "start", behavior: "smooth" });
  }

  // ---------- Saving, checking with Roostack, adding ----------

  function download() {
    const blob = new Blob([state.text], { type: "application/yaml" });
    const a = h("a", { href: URL.createObjectURL(blob), download: fileName() });
    document.body.append(a);
    a.click();
    setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
  }

  async function checkWithRoostack() {
    const box = $("[data-server]");
    box.hidden = false;
    box.replaceChildren(h("p", {}, "Checking with this Roostack…"));
    try {
      const resp = await fetch(opt.check, { method: "POST", headers: { "Content-Type": "application/yaml" }, body: state.text, credentials: "same-origin" });
      if (!resp.ok) throw new Error(resp.status === 403 ? "Only the Owner can check packages here." : `the lab answered ${resp.status}`);
      const r = await resp.json();
      const errs = r.errors || [], pol = state.local ? r.risks || [] : r.policy || [];
      box.replaceChildren(h("h2", {}, "Roostack's check"));
      if (!errs.length) box.append(h("p", { class: "pe-ok" }, `Roostack can set it up: it's valid, and its templates render${pol.length ? "" : ", and it follows the package policy"}.`));
      for (const m of errs) box.append(h("p", { class: "pe-msg-bad" }, m));
      if (pol.length) box.append(h("p", {}, state.local ? "What it asks for that registry packages can't have, which you'll confirm when you install it:" : "What the registry's package policy refuses:"),
        h("ul", {}, pol.map((m) => h("li", {}, m))));
    } catch (e) {
      box.replaceChildren(h("p", { class: "pe-msg-bad" }, `Roostack couldn't check it: ${e.message}.`));
    }
  }

  function addToLab() {
    const form = $("[data-add-form]");
    form.action = opt.add;
    $("textarea", form).value = state.text;
    const file = $("input[type=file]", form);
    if (state.icon) {
      try {
        const dt = new DataTransfer();
        dt.items.add(new File([state.icon.bytes], textOf(get(["about", "icon"])) || "icon.png", { type: "image/png" }));
        file.files = dt.files;
      } catch { /* an older browser: the add page asks for the logo */ }
    }
    form.submit();
  }

  // ---------- Starting up ----------

  function readFile(input, then) {
    const f = input.files && input.files[0];
    if (!f) return;
    const reader = new FileReader();
    reader.onload = () => { then(String(reader.result), f.name); input.value = ""; };
    reader.readAsText(f);
  }

  function start() {
    for (const el of $$("[data-icon]")) el.replaceChildren(icon(el.dataset.icon));
    for (const a of $$("[data-help-link]")) a.href = helpURL(a.dataset.helpLink);
    const examples = $("[data-examples]");
    for (const ex of R.examples || []) {
      examples.append(h("button", { type: "button", class: "pe-button secondary", onclick: () => load(ex.text) },
        icon({ vaultwarden: "passwords", forgejo: "development", immich: "photos" }[ex.name] || "documents"), ex.title, " ", h("small", {}, ex.about)));
    }
    const local = $("[data-local]");
    const saved = store.get("local");
    if (saved !== null) state.local = saved === "1";
    local.checked = state.local;
    local.addEventListener("change", () => { state.local = local.checked; store.set("local", local.checked ? "1" : "0"); check(); });

    root.addEventListener("click", (e) => {
      const b = e.target.closest("[data-act]");
      if (b && root.contains(b)) {
        const a = b.dataset.act;
        if (a === "start") setView("start");
        else if (a === "import-view") { setView("import"); $("[data-compose]").focus(); }
        else if (a === "open") $("[data-open-file]").click();
        else if (a === "open-compose") $("[data-compose-file]").click();
        else if (a === "scratch") load(scratch);
        else if (a === "draft") load(state.draft);
        else if (a === "import") runImport();
        else if (a === "imported") load(state.pendingImport.yaml, state.pendingImport);
        else if (a === "download") download();
        else if (a === "check") checkWithRoostack();
        else if (a === "add") addToLab();
        else if (a === "more") { state.showAll = !state.showAll; renderProblems(); }
      }
      const tab = e.target.closest("[data-tab]");
      if (tab) setTab(tab.dataset.tab);
    });
    $("[data-open-file]").addEventListener("change", (e) => readFile(e.target, (t) => load(t)));
    $("[data-compose-file]").addEventListener("change", (e) => readFile(e.target, (t) => { $("[data-compose]").value = t; }));

    const src = $("[data-source]");
    let typing = 0;
    src.addEventListener("input", () => {
      clearTimeout(typing);
      typing = setTimeout(() => {
        state.text = src.value;
        const before = state.doc;
        check();
        if (state.doc !== before) renderForm();
        saveDraft();
      }, 300);
    });
    src.addEventListener("scroll", () => { $("[data-gutter]").scrollTop = src.scrollTop; });
    // Tab indents in the YAML instead of leaving it; Escape then Tab leaves.
    let escaped = false;
    src.addEventListener("keydown", (e) => {
      if (e.key === "Escape") { escaped = true; return; }
      if (e.key === "Tab" && !e.shiftKey && !escaped) {
        e.preventDefault();
        const s = src.selectionStart;
        src.setRangeText("  ", s, src.selectionEnd, "end");
        src.dispatchEvent(new Event("input"));
      }
      escaped = false;
    });

    root.addEventListener("focusin", (e) => {
      const field = e.target.closest(".pe-field");
      if (!field || !$("[data-form]").contains(field)) return;
      // Within the field you're on (its help's buttons too), nothing is
      // drawn again: a button redrawn as it's pressed never gets the click.
      if (field.classList.contains("is-focus")) return;
      showHelp(field);
      const path = resolve(field.dataset.path.replace(/\[(\d+)\]/g, ".[$1]").replace(/^\./, ""));
      showLine(lineOf(path), false);
    });

    setTab("form");
    setView("start");
  }

  start();
})(globalThis.RoostackEditor = globalThis.RoostackEditor || {});
