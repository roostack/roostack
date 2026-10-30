// Roostack's package editor: checking a package as `roostack validate`
// does (SPEC.md §8.3.7, "The package editor"). Decoding follows Go's YAML
// decoder (go.yaml.in/yaml/v3, unknown fields refused); validate, policy
// and risks are internal/pkgspec's Validate, Policy and Risks, message for
// message, reading the values they use from schema.js (pkgspec.Describe).
// A test runs both on the same packages (test/corpus.test.js), so a change
// on either side that isn't made on the other fails.
//
// What Go does that this can't: render the templates (generate.Check).
// templates() checks what they refer to; Roostack's own check and
// `roostack validate` render them.
(function (R) {
  "use strict";

  const S = R.schema;
  const re = {};
  for (const [k, v] of Object.entries(S.patterns)) re[k] = new RegExp(v);
  const L = S.lists;

  // ---------- Go's formatting ----------

  // goQuote is Go's %q of a string (strconv.Quote).
  function goQuote(s) {
    let out = '"';
    for (const ch of String(s)) {
      const c = ch.codePointAt(0);
      if (ch === '"') out += '\\"';
      else if (ch === "\\") out += "\\\\";
      else if (c === 7) out += "\\a";
      else if (c === 8) out += "\\b";
      else if (c === 12) out += "\\f";
      else if (c === 10) out += "\\n";
      else if (c === 13) out += "\\r";
      else if (c === 9) out += "\\t";
      else if (c === 11) out += "\\v";
      else if (c < 0x20 || c === 0x7f) out += "\\x" + c.toString(16).padStart(2, "0");
      else if (!printable(c)) out += c > 0xffff ? "\\U" + c.toString(16).padStart(8, "0") : "\\u" + c.toString(16).padStart(4, "0");
      else out += ch;
    }
    return out + '"';
  }

  // printable approximates Go's strconv.IsPrint: letters, marks, numbers,
  // punctuation, symbols and the ASCII space.
  function printable(c) {
    if (c === 0x20) return true;
    return /[\p{L}\p{M}\p{N}\p{P}\p{S}]/u.test(String.fromCodePoint(c));
  }

  // goFloat is Go's %v of a float64.
  function goFloat(f) {
    if (Number.isNaN(f)) return "NaN";
    if (f === Infinity) return "+Inf";
    if (f === -Infinity) return "-Inf";
    if (f === 0) return Object.is(f, -0) ? "-0" : "0";
    // The shortest digits, as %e below 1e-4 and from 1e6 (strconv's 'g'
    // with the shortest precision decides at 6 digits).
    const [mant, ex] = f.toExponential().split("e");
    const x = Number(ex);
    if (x < -4 || x >= 6) return mant + "e" + (x < 0 ? "-" : "+") + String(Math.abs(x)).padStart(2, "0");
    return String(f);
  }

  // goSprint is Go's fmt.Sprint of a decoded value (Val).
  function goSprint(v) {
    if (v === null || v === undefined) return "<nil>";
    switch (v.k) {
      case "null": return "<nil>";
      case "str": return v.v;
      case "int": return String(v.v);
      case "float": return goFloat(v.v);
      case "bool": return v.v ? "true" : "false";
      case "list": return "[" + v.v.map(goSprint).join(" ") + "]";
      case "map": return "map[" + goSorted(Object.keys(v.v)).map((k) => k + ":" + goSprint(v.v[k])).join(" ") + "]";
    }
    return String(v);
  }

  // goSorted sorts strings as Go's slices.Sorted does: by UTF-8 bytes.
  function goSorted(keys) {
    const enc = new TextEncoder();
    return [...keys].sort((a, b) => {
      const x = enc.encode(a), y = enc.encode(b);
      for (let i = 0; i < Math.min(x.length, y.length); i++) if (x[i] !== y[i]) return x[i] - y[i];
      return x.length - y.length;
    });
  }
  const sortedKeys = (m) => goSorted(Object.keys(m || {}));
  const bytes = (s) => new TextEncoder().encode(s).length;
  const list = (xs) => "[" + xs.join(" ") + "]";

  // ---------- Decoding, as Go's YAML decoder does ----------

  // A Val is a value decoded into Go's `any`: k is int, float, bool, str,
  // null, list or map.
  const val = (k, v) => ({ k, v });

  // resolvePlain resolves a plain (unquoted) scalar as go-yaml v3 does.
  function resolvePlain(s) {
    if (s === "" || s === "~" || s === "null" || s === "Null" || s === "NULL") return val("null", null);
    if (s === "true" || s === "True" || s === "TRUE") return val("bool", true);
    if (s === "false" || s === "False" || s === "FALSE") return val("bool", false);
    if (/^[-+]?\.(inf|Inf|INF)$/.test(s)) return val("float", s[0] === "-" ? -Infinity : Infinity);
    if (/^\.(nan|NaN|NAN)$/.test(s)) return val("float", NaN);
    const c = s[0];
    if (/[0-9+\-.]/.test(c)) {
      const plain = s.replace(/_/g, "");
      const n = parseGoInt(plain);
      if (n !== null) return val("int", n);
      if (/^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$/.test(plain)) return val("float", parseFloat(plain));
    }
    return val("str", s);
  }

  // parseGoInt is strconv.ParseInt(s, 0, 64): decimal, 0x, 0o, 0b and a
  // leading 0 for octal; null if it isn't one.
  function parseGoInt(s) {
    const m = /^([-+]?)(0[xX][0-9a-fA-F]+|0[oO][0-7]+|0[bB][01]+|0[0-7]*|[1-9][0-9]*)$/.exec(s);
    if (!m) return null;
    let body = m[2], n;
    if (/^0[xX]/.test(body)) n = parseInt(body.slice(2), 16);
    else if (/^0[oO]/.test(body)) n = parseInt(body.slice(2), 8);
    else if (/^0[bB]/.test(body)) n = parseInt(body.slice(2), 2);
    else if (body.length > 1 && body[0] === "0") n = parseInt(body.slice(1), 8);
    else n = parseInt(body, 10);
    if (!Number.isSafeInteger(n)) return null;
    return m[1] === "-" ? -n : n;
  }

  // scalarVal is what a scalar node decodes to in Go's `any`.
  function scalarVal(node) {
    const text = node.source !== undefined ? node.source : String(node.value);
    if (node.tag === "tag:yaml.org,2002:str" || (node.type && node.type !== "PLAIN")) return val("str", text);
    return resolvePlain(text);
  }

  function yamlKind(v) {
    switch (v.k) {
      case "int": return "!!int";
      case "float": return "!!float";
      case "bool": return "!!bool";
      case "null": return "!!null";
    }
    return "!!str";
  }

  // decode turns a parsed YAML document into a package the way pkgspec.Parse
  // does: Go's zero values for what's missing, nil for missing pointers,
  // and an error for each field Go wouldn't decode (unknown fields, wrong
  // types). It returns { pkg, errors }; errors have line numbers.
  function decode(doc, lineCounter) {
    const errors = [];
    const lineOf = (node) => (node && node.range && lineCounter ? lineCounter.linePos(node.range[0]).line : 0);
    const fail = (node, msg) => errors.push({ line: lineOf(node), message: msg });

    function deref(node) {
      let n = node, guard = 0;
      while (n && YAML.isAlias(n) && guard++ < 100) n = n.resolve(doc);
      return n;
    }

    // pairsOf lists a map's pairs, merge keys (<<) applied as Go does:
    // explicit keys win over merged ones.
    function pairsOf(map) {
      const out = [], seen = new Set();
      const merged = [];
      for (const pair of map.items) {
        const k = YAML.isScalar(pair.key) ? scalarVal(pair.key) : null;
        if (k && k.k === "str" && k.v === "<<" && (!pair.key.type || pair.key.type === "PLAIN")) {
          const v = deref(pair.value);
          const sources = YAML.isSeq(v) ? v.items.map(deref) : [v];
          for (const src of sources) if (YAML.isMap(src)) merged.push(...pairsOf(src));
          continue;
        }
        out.push(pair);
        seen.add(k ? goSprint(k) : "");
      }
      for (const pair of merged) {
        const k = YAML.isScalar(pair.key) ? goSprint(scalarVal(pair.key)) : "";
        if (!seen.has(k)) { out.push(pair); seen.add(k); }
      }
      return out;
    }

    function keyText(key) {
      const k = deref(key);
      if (YAML.isScalar(k)) { const v = scalarVal(k); return v.k === "null" ? "" : goSprint(v); }
      return null;
    }

    function isNullNode(n) {
      n = deref(n);
      return n === null || n === undefined || (YAML.isScalar(n) && scalarVal(n).k === "null");
    }

    // keepsNull says whether go-yaml sets a null for a list item or map
    // value of this field, rather than leaving it out: pointers,
    // interfaces, maps and lists.
    const keepsNull = (f) => f.ptr || ["any", "list", "map", "quadlet"].includes(f.type);

    function zero(field) {
      if (field.ptr) return null;
      switch (field.type) {
        case "string": return "";
        case "int": return 0;
        case "bool": return false;
        case "any": return null;
        case "list": case "map": case "quadlet": return null;
        case "object": {
          const o = {};
          for (const f of field.fields) o[f.name] = zero(f);
          return o;
        }
      }
      return null;
    }

    function cannot(node, v, into) {
      fail(node, `cannot unmarshal ${v ? yamlKind(v) : node && YAML.isSeq(node) ? "!!seq" : "!!map"}${v && v.k !== "null" ? " `" + goSprint(v) + "`" : ""} into ${into}`);
    }

    function anyVal(node) {
      node = deref(node);
      if (node === null || node === undefined) return val("null", null);
      if (YAML.isScalar(node)) return scalarVal(node);
      if (YAML.isSeq(node)) return val("list", node.items.map(anyVal));
      if (YAML.isMap(node)) {
        const m = {};
        for (const p of pairsOf(node)) m[keyText(p.key) ?? ""] = anyVal(p.value);
        return val("map", m);
      }
      return val("null", null);
    }

    function dec(node, field, goType) {
      node = deref(node);
      const isNull = node === null || node === undefined || (YAML.isScalar(node) && scalarVal(node).k === "null");
      if (isNull) {
        if (field.type === "any") return val("null", null);
        return zero(field);
      }
      switch (field.type) {
        case "string":
          if (YAML.isScalar(node)) {
            const v = scalarVal(node);
            return v.k === "str" ? v.v : node.source !== undefined ? node.source : goSprint(v);
          }
          cannot(node, null, "string");
          return zero(field);
        case "int":
          if (YAML.isScalar(node)) {
            const v = scalarVal(node);
            if (v.k === "int") return v.v;
            if (v.k === "float" && Number.isFinite(v.v) && Math.abs(v.v) < 2 ** 63) return Math.trunc(v.v);
            cannot(node, v, "int");
            return 0;
          }
          cannot(node, null, "int");
          return 0;
        case "bool":
          if (YAML.isScalar(node)) {
            const v = scalarVal(node);
            if (v.k === "bool") return v.v;
            const t = goSprint(v);
            // go-yaml's YAML 1.1 compatibility, for typed bools only.
            if (["y", "Y", "yes", "Yes", "YES", "on", "On", "ON"].includes(t)) return true;
            if (["n", "N", "no", "No", "NO", "off", "Off", "OFF"].includes(t)) return false;
            cannot(node, v, "bool");
            return false;
          }
          cannot(node, null, "bool");
          return false;
        case "any":
          return anyVal(node);
        case "list":
          // go-yaml leaves out a null item of a list of plain values
          // (strings, numbers, objects); it keeps it as nil in a list of
          // pointers.
          if (YAML.isSeq(node)) return node.items.filter((n) => keepsNull(field.item) || !isNullNode(n)).map((n) => dec(n, field.item, goType));
          cannot(node, YAML.isScalar(node) ? scalarVal(node) : null, "[]" + goType);
          return null;
        case "map":
          if (YAML.isMap(node)) {
            const m = {};
            for (const p of pairsOf(node)) {
              const k = keyText(p.key);
              if (k === null) { cannot(deref(p.key), null, "string"); continue; }
              if (!keepsNull(field.item) && isNullNode(p.value)) continue;
              m[k] = dec(p.value, field.item, goType);
            }
            return m;
          }
          cannot(node, YAML.isScalar(node) ? scalarVal(node) : null, "map[string]" + goType);
          return null;
        case "quadlet":
          if (YAML.isMap(node)) {
            const q = {};
            for (const p of pairsOf(node)) {
              const k = keyText(p.key) ?? "";
              const sec = deref(p.value);
              if (sec === null || sec === undefined || (YAML.isScalar(sec) && scalarVal(sec).k === "null")) { q[k] = null; continue; }
              if (!YAML.isMap(sec)) { cannot(sec, YAML.isScalar(sec) ? scalarVal(sec) : null, "map[string]interface {}"); continue; }
              const m = {};
              for (const sp of pairsOf(sec)) m[keyText(sp.key) ?? ""] = anyVal(sp.value);
              q[k] = m;
            }
            return q;
          }
          cannot(node, YAML.isScalar(node) ? scalarVal(node) : null, "pkgspec.Quadlet");
          return null;
        case "object": {
          if (!YAML.isMap(node)) {
            cannot(node, YAML.isScalar(node) ? scalarVal(node) : null, "pkgspec." + goType);
            return zero(field);
          }
          const o = zero(Object.assign({}, field, { ptr: false }));
          const byName = Object.fromEntries(field.fields.map((f) => [f.name, f]));
          for (const p of pairsOf(node)) {
            const k = keyText(p.key);
            const f = k !== null ? byName[k] : undefined;
            if (!f) { fail(deref(p.key), `field ${k} not found in type pkgspec.${goType}`); continue; }
            o[f.name] = dec(p.value, f, goTypeOf(goType, f.name));
          }
          return o;
        }
      }
      return null;
    }

    const root = doc.contents;
    let pkg;
    if (root === null || root === undefined || (YAML.isScalar(root) && scalarVal(root).k === "null")) pkg = zero(S.fields);
    else pkg = dec(root, S.fields, "Package");
    return { pkg, errors };
  }

  // goTypeOf names Go's struct types, for decoding errors only.
  const structNames = {
    "Package.about": "About", "Package.requires": "Requires", "Package.upgrade_notes": "UpgradeNote",
    "Package.settings": "Setting", "Package.containers": "Container", "Package.storage": "Storage",
    "Package.databases": "Database", "Package.routes": "Route", "Package.ports": "Port", "Package.hooks": "Hook",
    "Package.links": "Link", "Container.mounts": "Mount", "Container.devices": "Device", "Container.health": "Health",
    "Health.http": "HTTPCheck", "Route.monitor": "RouteMonitor", "Route.public": "PublicRules",
    "PublicRules.private_paths": "PrivatePath", "Hook.inputs": "Setting",
    "Package.sso": "SSO", "SSO.oidc": "SSOOIDC", "SSO.forward_auth": "SSOForwardAuth", "SSOForwardAuth.bypass_paths": "PrivatePath",
    "SSOOIDC.groups": "SSOGroups",
  };
  function goTypeOf(parent, name) { return structNames[parent + "." + name] || "string"; }

  // applyDefaults is Package.applyDefaults.
  function applyDefaults(p) {
    for (const s of Object.values(p.storage || {})) if (s && s.location === "") s.location = "service";
    for (const r of Object.values(p.routes || {})) if (r && r.exposure === "") r.exposure = "private";
  }

  // ---------- Validate (pkgspec/validate.go, about.go) ----------

  const isNil = (x) => x === null || x === undefined;
  const has = (m, k) => !!m && Object.prototype.hasOwnProperty.call(m, k) && !isNil(m[k]);
  const settingOf = (p, key) => (p.settings || []).find((s) => s.key === key) || null;
  const hasInstallHook = (p) => (p.hooks || []).some((h) => h && h.on === "install");
  const hasSavingAction = (p, key) => (p.hooks || []).some((h) => h && h.on === "action" && h.saves === key);
  const validRedirectPath = (rp) => re.ssoRedirectPath.test(rp) && !rp.includes("//");
  const validPort = (n) => n > 0 && n < 65536;
  const isAbs = (s) => s.startsWith("/");
  const validPath = (s) => re.path.test(s);

  function validate(p) {
    const errs = [];
    const add = (msg) => errs.push(msg);

    if (p.schema !== S.schema) add(`schema: must be ${goQuote(S.schema)}, got ${goQuote(p.schema)}`);
    if (!re.name.test(p.name)) add(`name: ${goQuote(p.name)} must be lowercase letters, digits, and dashes, starting with a letter`);
    if (p.title === "") add("title: required");
    if (p.version === "") add("version: required");
    else if (!re.version.test(p.version)) add(`version: ${goQuote(p.version)} must be letters, digits, dots, dashes, underscores, and plus signs, starting with a letter or digit`);
    if (Object.keys(p.containers || {}).length === 0) add("containers: at least one container is required");

    validateAbout(p, add);
    validateSettings(p, add);
    for (const n of sortedKeys(p.storage)) validateStorage(add, n, p.storage[n]);
    for (const n of sortedKeys(p.databases)) validateDatabase(p, add, n, p.databases[n]);
    for (const n of sortedKeys(p.containers)) validateContainer(p, add, n, p.containers[n]);
    for (const n of sortedKeys(p.routes)) validateRoute(p, add, n, p.routes[n]);
    for (const n of sortedKeys(p.ports)) validatePortDef(p, add, n, p.ports[n]);
    validateHooks(p, add);
    validateSSO(p, add);
    (p.links || []).forEach((l, i) => {
      const at = `links[${i}]`;
      if (l.title === "") add(`${at}.title: required`);
      if (!has(p.routes, l.route)) add(`${at}.route: unknown route ${goQuote(l.route)}`);
      if (!l.path.startsWith("/") || l.path.startsWith("//")) add(`${at}.path: must start with a single /`);
    });
    return errs;
  }

  // validateSSO is Package.validateSSO (pkgspec/sso.go).
  function validateSSO(p, add) {
    const s = p.sso;
    if (isNil(s)) return;
    if (isNil(s.oidc) && isNil(s.forward_auth)) add("sso: needs oidc or forward_auth");
    else if (!isNil(s.oidc) && !isNil(s.forward_auth)) add("sso: oidc or forward_auth, not both");
    const o = s.oidc;
    if (!isNil(o)) {
      if (!has(p.routes, o.route)) add(`sso.oidc.route: unknown route ${goQuote(o.route)}`);
      if ((o.redirect_paths || []).length + (o.redirect_uris || []).length === 0) add("sso.oidc: needs redirect_paths (or redirect_uris)");
      (o.redirect_paths || []).forEach((rp, i) => {
        // A templated path is checked once it's rendered (generate).
        const templated = rp.includes("{{");
        if ((templated && !rp.startsWith("/")) || (!templated && !validRedirectPath(rp))) add(`sso.oidc.redirect_paths[${i}]: ${goQuote(rp)} must be a path starting with /, with only letters, digits, and - _ . : ~ /`);
      });
      const g = o.groups;
      if (!isNil(g)) {
        if (!re.ssoGroupName.test(g.users) || !re.ssoGroupName.test(g.admins)) add("sso.oidc.groups: users and admins must each be a lowercase name (letters, digits, -, and _)");
        else if (g.users === g.admins) add("sso.oidc.groups: users and admins must differ");
      }
      if (!isNil(o.existing_accounts) && o.existing_accounts !== "" && !S.lists.ssoExisting.includes(o.existing_accounts)) add(`sso.oidc.existing_accounts: ${goQuote(o.existing_accounts)} must be one of ${S.lists.ssoExisting.join(", ")}`);
      (o.redirect_uris || []).forEach((u, i) => {
        if (!re.ssoAppScheme.test(u) || u.startsWith("http:") || u.startsWith("https:")) add(`sso.oidc.redirect_uris[${i}]: ${goQuote(u)} must be a phone app's own address (like app.example:/callback), not a web address`);
      });
    }
    const f = s.forward_auth;
    if (!isNil(f)) {
      if (!has(p.routes, f.route)) add(`sso.forward_auth.route: unknown route ${goQuote(f.route)}`);
      const seen = new Set();
      (f.bypass_paths || []).forEach((bp, i) => {
        if (!validPath(bp.path)) add(`sso.forward_auth.bypass_paths[${i}].path: ${goQuote(bp.path)} must start with / and have only letters, digits, and - _ . : / (no * or trailing /)`);
        if (goTrim(bp.reason) === "") add(`sso.forward_auth.bypass_paths[${i}].reason: say why it's safe without signing in`);
        if (seen.has(bp.path)) add(`sso.forward_auth.bypass_paths[${i}].path: ${goQuote(bp.path)} is listed twice`);
        seen.add(bp.path);
      });
    }
    for (const c of sortedKeys(s.env)) {
      if (!has(p.containers, c)) add(`sso.env.${c}: unknown container`);
      for (const k of sortedKeys(s.env[c])) if (!re.envName.test(k)) add(`sso.env.${c}.${k}: invalid variable name`);
    }
    const secret = L.ssoClientSecret[0];
    for (const c of sortedKeys(s.secrets)) {
      if (!has(p.containers, c)) add(`sso.secrets.${c}: unknown container`);
      for (const k of sortedKeys(s.secrets[c])) {
        if (!re.envName.test(k)) add(`sso.secrets.${c}.${k}: invalid variable name`);
        if (s.secrets[c][k] !== secret) add(`sso.secrets.${c}.${k}: must be ${secret}`);
      }
    }
    for (const c of sortedKeys(s.secret_env)) {
      if (!has(p.containers, c)) add(`sso.secret_env.${c}: unknown container`);
      for (const k of sortedKeys(s.secret_env[c])) {
        if (!re.envName.test(k)) add(`sso.secret_env.${c}.${k}: invalid variable name`);
        if (s.env && s.env[c] && Object.prototype.hasOwnProperty.call(s.env[c], k)) add(`sso.secret_env.${c}.${k}: also set in sso.env`);
        if (s.secrets && s.secrets[c] && Object.prototype.hasOwnProperty.call(s.secrets[c], k)) add(`sso.secret_env.${c}.${k}: also set in sso.secrets`);
        if (!s.secret_env[c][k].includes(".SSO.ClientSecret")) add(`sso.secret_env.${c}.${k}: must use {{ .SSO.ClientSecret }} (otherwise put it in sso.env)`);
      }
    }
    if (!isNil(f) && (Object.keys(s.secrets || {}).length > 0 || Object.keys(s.secret_env || {}).length > 0)) add("sso.secrets: only an oidc app has a client secret");
  }

  function validateSettings(p, add) {
    const seen = new Set();
    (p.settings || []).forEach((s, i) => {
      let at = `settings[${i}]`;
      if (!re.settingKey.test(s.key)) add(`${at}.key: ${goQuote(s.key)} must be lowercase letters, digits, and underscores`);
      else at = "settings." + s.key;
      if (seen.has(s.key)) add(`${at}: duplicate key`);
      seen.add(s.key);

      const d = s.default;
      switch (s.type) {
        case "bool":
          if (!(d && d.k === "bool") && !isNil(d) && d.k !== "null" && !(d.k === "str" && d.v === S.words.defaultAuto)) add(`${at}.default: must be true, false, or ${goQuote(S.words.defaultAuto)}`);
          break;
        case "string":
          if (!(d && d.k === "str") && !isNilVal(d)) add(`${at}.default: must be a string`);
          break;
        case "int":
          if (!(d && d.k === "int") && !isNilVal(d)) add(`${at}.default: must be an integer`);
          break;
        case "secret":
          if (!isNilVal(d)) add(`${at}.default: secrets cannot have a default; use generate: true`);
          break;
        default:
          add(`${at}.type: must be one of bool, string, int, secret; got ${goQuote(s.type)}`);
      }
      if (s.generate && s.type !== "secret") add(`${at}.generate: only valid for secret settings`);
      if (!isNilVal(s.install_value)) {
        if (s.type === "secret" || s.install_only) add(`${at}.install_value: only for settings that are kept and aren't secrets`);
        else if (!installValueFits(s.type, s.install_value)) add(`${at}.install_value: must be a ${s.type}, like the setting`);
        else if (!hasInstallHook(p)) add(`${at}.install_value: used only until the install hooks succeed, and there are none`);
      }
      if (s.reveal && (s.type !== "secret" || s.install_only)) add(`${at}.reveal: only for secrets that are kept (not install_only)`);
      if (s.generate && s.install_only) add(`${at}: a generated secret can't be install_only (nobody would know it)`);
      if (s.from_hook) {
        if (s.type !== "secret") add(`${at}.from_hook: only for secrets`);
        else if (s.generate || s.install_only || s.reveal || s.min_length !== 0) add(`${at}.from_hook: a hook will make it, so it takes no generate, install_only, reveal, or min_length`);
        else if (!hasSavingAction(p, s.key)) add(`${at}.from_hook: needs an action that saves it (saves: ${s.key}), for services installed before it, or whose key was lost`);
      }
      if (s.required && s.type !== "string") add(`${at}.required: only for strings (secrets are always required)`);
      if (s.min_length !== 0 && s.type !== "string" && s.type !== "secret") add(`${at}.min_length: only for strings and secrets`);
    });
  }

  const isNilVal = (v) => isNil(v) || v.k === "null";

  function installValueFits(typ, v) {
    switch (typ) {
      case "bool": return v.k === "bool";
      case "string": return v.k === "str";
      case "int": return v.k === "int";
    }
    return false;
  }

  function validateHooks(p, add) {
    const actions = new Set();
    (p.hooks || []).forEach((h, i) => {
      const at = `hooks[${i}]`;
      if (isNil(h)) { add(`${at}: empty hook`); return; }
      switch (h.on) {
        case "install":
          if ((h.inputs || []).length > 0) add(`${at}.inputs: only actions take inputs; install hooks use settings`);
          break;
        case "action":
          if (!re.name.test(h.name)) add(`${at}.name: ${goQuote(h.name)} must be lowercase letters, digits, and dashes`);
          if (actions.has(h.name)) add(`${at}.name: duplicate action ${goQuote(h.name)}`);
          actions.add(h.name);
          if (h.title === "") add(`${at}.title: required (the button's label)`);
          break;
        case "sso":
          if (isNil(p.sso)) add(`${at}.on: an sso hook needs the package's sso part`);
          else if (h.name !== "" || (h.inputs || []).length > 0) add(`${at}: an sso hook takes no name or inputs`);
          break;
        default:
          add(`${at}.on: must be "install", "action", or "sso"`);
      }
      if (h.saves !== "") {
        const s = settingOf(p, h.saves);
        if (h.on === "sso") add(`${at}.saves: only install and action hooks save a secret`);
        else if (!s || !s.from_hook) add(`${at}.saves: ${goQuote(h.saves)} is not a from_hook secret setting`);
      }
      if (!has(p.containers, h.container)) add(`${at}.container: unknown container ${goQuote(h.container)}`);
      if ((h.command || []).length === 0) add(`${at}.command: required`);
      const inputs = new Set();
      (h.inputs || []).forEach((inp, j) => {
        const iat = `${at}.inputs[${j}]`;
        if (!re.settingKey.test(inp.key) || inputs.has(inp.key)) add(`${iat}.key: ${goQuote(inp.key)} must be a unique lowercase key`);
        inputs.add(inp.key);
        if (inp.type !== "string" && inp.type !== "secret") add(`${iat}.type: inputs are string or secret`);
        if (!isNilVal(inp.default) || inp.generate || inp.install_only) add(`${iat}: inputs take no default, generate, or install_only`);
      });
      for (const k of sortedKeys(h.env)) if (!re.envName.test(k)) add(`${at}.env: invalid variable name ${goQuote(k)}`);
      for (const k of sortedKeys(h.secrets)) {
        if (!re.envName.test(k)) add(`${at}.secrets: invalid variable name ${goQuote(k)}`);
        if (h.env && Object.prototype.hasOwnProperty.call(h.env, k)) add(`${at}.secrets.${k}: also set in env`);
        const ref = h.secrets[k];
        if (ref === L.ssoClientSecret[0]) {
          if (h.on !== "sso" || isNil(p.sso) || isNil(p.sso.oidc)) add(`${at}.secrets.${k}: ${ref} is only for an sso hook of an oidc app`);
          continue;
        }
        if (ref.startsWith("inputs.")) {
          const key = ref.slice("inputs.".length);
          const inp = (h.inputs || []).find((x) => x.key === key);
          if (!inp || inp.type !== "secret") add(`${at}.secrets.${k}: ${goQuote(ref)} is not a secret input`);
          continue;
        }
        const s = settingOf(p, ref);
        if (!s || s.type !== "secret") add(`${at}.secrets.${k}: ${goQuote(ref)} is not a secret setting`);
        else if (s.install_only && h.on !== "install") add(`${at}.secrets.${k}: ${goQuote(ref)} is install_only and will be deleted after install`);
      }
    });
  }

  function validateStorage(add, name, s) {
    const at = "storage." + name;
    if (!re.name.test(name)) add(`${at}: invalid name`);
    if (isNil(s)) { add(`${at}: class is required`); return; }
    if (s.class !== "critical" && s.class !== "replaceable") add(`${at}.class: must be "critical" or "replaceable"`);
    if (s.location !== "service" && s.location !== "media") add(`${at}.location: must be "service" or "media"`);
    if ([...s.title].length > S.limits.maxStorageTitle || /[\n\r]/.test(s.title)) add(`${at}.title: must be one line of at most ${S.limits.maxStorageTitle} characters`);
  }

  function validateDatabase(p, add, name, d) {
    const at = "databases." + name;
    if (!re.name.test(name)) add(`${at}: invalid name`);
    if (isNil(d)) { add(`${at}: type is required`); return; }
    switch (d.type) {
      case "sqlite": {
        const i = d.path.indexOf("/");
        const storage = i < 0 ? d.path : d.path.slice(0, i), rel = i < 0 ? "" : d.path.slice(i + 1);
        if (i < 0 || rel === "" || isAbs(d.path) || d.path.includes("..")) add(`${at}.path: must be "<storage>/<file>", got ${goQuote(d.path)}`);
        else if (!has(p.storage, storage)) add(`${at}.path: unknown storage ${goQuote(storage)}`);
        if (d.image !== "" || Object.keys(d.quadlet || {}).length > 0 || d.memory !== "") add(`${at}: sqlite runs inside the app; image, memory, and quadlet are not allowed`);
        break;
      }
      case "postgres": case "redis": case "mysql": case "mariadb":
        if (d.path !== "") add(`${at}.path: only valid for sqlite`);
        validateQuadlet(add, at + ".quadlet", d.quadlet);
        validateMemory(add, at + ".memory", d.memory);
        break;
      default:
        add(`${at}.type: must be one of sqlite, postgres, mysql, mariadb, redis; got ${goQuote(d.type)}`);
    }
  }

  function validateContainer(p, add, name, c) {
    const at = "containers." + name;
    if (!re.name.test(name)) add(`${at}: invalid name`);
    if (name === "network" || name.startsWith("db-")) add(`${at}: name is reserved`);
    if (isNil(c)) { add(`${at}: image is required`); return; }
    if (c.image === "") add(`${at}.image: required`);
    for (const k of sortedKeys(c.env)) if (!re.envName.test(k)) add(`${at}.env: invalid variable name ${goQuote(k)}`);
    for (const k of sortedKeys(c.secrets)) {
      if (!re.envName.test(k)) add(`${at}.secrets: invalid variable name ${goQuote(k)}`);
      if (c.env && Object.prototype.hasOwnProperty.call(c.env, k)) add(`${at}.secrets.${k}: also set in env`);
      const err = checkSecretRef(p, c.secrets[k]);
      if (err) add(`${at}.secrets.${k}: ${err}`);
      else {
        const s = settingOf(p, c.secrets[k]);
        if (s && s.install_only) add(`${at}.secrets.${k}: ${goQuote(c.secrets[k])} is install_only, so only install hooks can use it`);
        else if (s && s.from_hook) add(`${at}.secrets.${k}: ${goQuote(c.secrets[k])} is from_hook, so only hooks can use it`);
      }
    }
    (c.mounts || []).forEach((m, i) => {
      if (m.libraries && m.storage !== "") add(`${at}.mounts[${i}]: set storage or libraries, not both`);
      else if (!m.libraries && !has(p.storage, m.storage)) add(`${at}.mounts[${i}]: unknown storage ${goQuote(m.storage)}`);
      if (!isAbs(m.path)) add(`${at}.mounts[${i}].path: must be absolute, got ${goQuote(m.path)}`);
    });
    (c.devices || []).forEach((d, i) => {
      if (!L.gpuKinds.includes(d.gpu)) add(`${at}.devices[${i}].gpu: must be one of ${list(L.gpuKinds)}, got ${goQuote(d.gpu)}`);
    });
    const h = c.health;
    if (!isNil(h)) {
      if (isNil(h.http) === (h.cmd === "")) add(`${at}.health: set exactly one of http or cmd`);
      if (!isNil(h.http) && (!validPort(h.http.port) || !h.http.path.startsWith("/"))) add(`${at}.health.http: needs a valid port and a path starting with /`);
    }
    for (const dep of c.depends_on || []) {
      const i = dep.indexOf(".");
      const kind = i < 0 ? dep : dep.slice(0, i), target = i < 0 ? "" : dep.slice(i + 1);
      if (kind === "databases" && has(p.databases, target)) {
        if (p.databases[target].type === "sqlite") add(`${at}.depends_on: ${goQuote(dep)} is sqlite and has no container`);
      } else if (kind === "containers" && has(p.containers, target)) {
        if (target === name) add(`${at}.depends_on: cannot depend on itself`);
      } else add(`${at}.depends_on: unknown reference ${goQuote(dep)} (use databases.<name> or containers.<name>)`);
    }
    validateMemory(add, at + ".memory", c.memory);
    validateQuadlet(add, at + ".quadlet", c.quadlet);
  }

  // parseMemory is pkgspec.ParseMemory: bytes, or an error message.
  function parseMemory(s) {
    const m = re.memory.exec(s);
    if (!m) return { err: `${goQuote(s)} must be a number and MB or GB, e.g. 300 MB or 1.5 GB` };
    const n = parseFloat(m[1]);
    const b = Math.trunc(n * (m[2] === "GB" ? 1e9 : 1e6));
    if (b < S.limits.minMemory || b > S.limits.maxMemory) return { err: `${goQuote(s)} must be between 1 MB and 1000 GB` };
    return { bytes: b };
  }

  function validateMemory(add, at, memory) {
    if (memory === "") return;
    const r = parseMemory(memory);
    if (r.err) add(`${at}: ${r.err}`);
  }

  function validateRoute(p, add, name, r) {
    const at = "routes." + name;
    if (!re.name.test(name)) add(`${at}: invalid name`);
    if (isNil(r)) { add(`${at}: container and port are required`); return; }
    if (!has(p.containers, r.container)) add(`${at}.container: unknown container ${goQuote(r.container)}`);
    if (!validPort(r.port)) add(`${at}.port: must be 1-65535`);
    if (!re.subdomain.test(r.subdomain)) add(`${at}.subdomain: ${goQuote(r.subdomain)} is not a valid DNS label`);
    if (r.exposure !== "private" && r.exposure !== "public") add(`${at}.exposure: must be "private" or "public"`);
    if (r.client_ip_header !== "" && !validClientIPHeader(r.client_ip_header)) add(`${at}.client_ip_header: ${goQuote(r.client_ip_header)} must be an X- header name other than X-Forwarded-* (e.g. X-Real-IP)`);
    const m = r.monitor;
    if (!isNil(m)) {
      if (m.path !== "" && m.path !== "/" && !validPath(m.path)) add(`${at}.monitor.path: ${goQuote(m.path)} must start with / and have only letters, digits, and - _ . : /`);
      (m.statuses || []).forEach((st, i) => { if (st < 100 || st > 599) add(`${at}.monitor.statuses[${i}]: ${st} isn't an HTTP status`); });
    }
    const seen = new Set();
    (r.public.private_paths || []).forEach((pp, i) => {
      if (!validPath(pp.path)) add(`${at}.public.private_paths[${i}].path: ${goQuote(pp.path)} must start with / and have only letters, digits, and - _ . : / (no * or trailing /)`);
      if (goTrim(pp.reason) === "") add(`${at}.public.private_paths[${i}].reason: say why it shouldn't be public`);
      if (seen.has(pp.path)) add(`${at}.public.private_paths[${i}].path: ${goQuote(pp.path)} is listed twice`);
      seen.add(pp.path);
    });
    (r.public.login_paths || []).forEach((lp, i) => {
      if (!validPath(lp)) add(`${at}.public.login_paths[${i}]: ${goQuote(lp)} must start with / and have only letters, digits, and - _ . : / (no * or trailing /)`);
    });
  }

  const validClientIPHeader = (h) => re.clientIPHeader.test(h) && !h.toLowerCase().startsWith("x-forwarded-");

  function validatePortDef(p, add, name, r) {
    const at = "ports." + name;
    if (!re.name.test(name)) add(`${at}: invalid name`);
    if (isNil(r)) { add(`${at}: title, container, port, and host_port are required`); return; }
    if (r.title === "") add(`${at}.title: required (shown in the UI)`);
    if (!has(p.containers, r.container)) add(`${at}.container: unknown container ${goQuote(r.container)}`);
    if (!validPort(r.port)) add(`${at}.port: must be 1-65535`);
    if (r.host_port < S.limits.minHostPort || r.host_port > 65535) add(`${at}.host_port: must be ${S.limits.minHostPort}-65535`);
    const proto = r.protocol === "" ? "tcp" : r.protocol;
    if (proto !== "tcp" && proto !== "udp") add(`${at}.protocol: must be tcp or udp`);
    const e = r.exposure === "" ? "private" : r.exposure;
    if (e !== "private" && e !== "public") add(`${at}.exposure: must be "private" or "public"`);
  }

  function checkSecretRef(p, ref) {
    if (ref.startsWith("databases.")) {
      const rest = ref.slice("databases.".length);
      const i = rest.indexOf(".");
      const name = i < 0 ? rest : rest.slice(0, i), field = i < 0 ? "" : rest.slice(i + 1);
      const d = has(p.databases, name) ? p.databases[name] : null;
      const hasPassword = d && ["postgres", "mysql", "mariadb"].includes(d.type);
      if (!hasPassword || (field !== "password" && field !== "url") || (field === "url" && d.type !== "postgres")) {
        return `${goQuote(ref)}: only databases.<postgres, mysql, or mariadb db>.password, or .url for postgres, is supported`;
      }
      return null;
    }
    const s = settingOf(p, ref);
    if (!s || s.type !== "secret") return `${goQuote(ref)} is not a secret setting`;
    return null;
  }

  function validateQuadlet(add, at, q) {
    for (const section of sortedKeys(q)) {
      if (!L.quadletSections.includes(section)) { add(`${at}: section ${goQuote(section)} not allowed (use one of ${list(L.quadletSections)})`); continue; }
      for (const key of sortedKeys(q[section])) {
        const kerr = checkQuadletKey(key);
        if (kerr) { add(`${at}.${section}.${goQuote(key)}: ${kerr}`); continue; }
        const v = q[section][key];
        if (!isScalarOrList(v)) { add(`${at}.${section}.${key}: must be a scalar or a list of scalars`); continue; }
        for (const value of quadletValues(v)) {
          const verr = checkQuadletValue(value);
          if (verr) add(`${at}.${section}.${key}: ${verr}`);
        }
      }
    }
  }

  const checkQuadletKey = (key) => (re.quadletKey.test(key) ? null : "a key is letters and digits only, starting with a letter");
  function checkQuadletValue(value) {
    if (/[\n\r]/.test(value)) return "value contains a line break";
    if (value.replace(/[ \t]+$/, "").endsWith("\\")) return "value ends in a backslash, which would join the next line to it";
    return null;
  }

  function isScalarOrList(v) {
    if (isNil(v)) return false;
    switch (v.k) {
      case "str": case "int": case "bool": case "float": return true;
      case "list": return v.v.every((e) => isScalarOrList(e) && e.k !== "list");
    }
    return false;
  }

  function quadletValues(v) {
    if (v && v.k === "list") return v.v.map(goSprint);
    return [goSprint(v)];
  }

  // validHTTPS is Go's url.Parse(s), then an https scheme, a host and no
  // user: written out, as JavaScript's URL reads addresses differently.
  function validHTTPS(s) {
    if (/[\x00-\x1f\x7f]/.test(s)) return false;
    let rest = s;
    const hash = rest.indexOf("#");
    if (hash >= 0) rest = rest.slice(0, hash);
    const m = /^([A-Za-z][A-Za-z0-9+.-]*):/.exec(rest);
    if (!m || m[1].toLowerCase() !== "https") return false;
    rest = rest.slice(m[0].length);
    const q = rest.indexOf("?");
    if (q >= 0) rest = rest.slice(0, q);
    if (!rest.startsWith("//")) return false;
    rest = rest.slice(2);
    const slash = rest.indexOf("/");
    const authority = slash < 0 ? rest : rest.slice(0, slash);
    if (authority.includes("@")) return false;
    let host = authority;
    if (host.startsWith("[")) {
      const end = host.lastIndexOf("]");
      if (end < 0) return false;
      const after = host.slice(end + 1);
      if (after !== "" && !/^:[0-9]*$/.test(after)) return false;
      return end > 1;
    }
    const colon = host.lastIndexOf(":");
    if (colon >= 0) {
      if (!/^[0-9]*$/.test(host.slice(colon + 1))) return false;
      host = host.slice(0, colon);
    }
    for (let i = 0; i < host.length; i++) {
      const ch = host[i];
      if (ch === "%") {
        if (!/^[0-9a-fA-F]{2}$/.test(host.slice(i + 1, i + 3))) return false;
        i += 2;
        continue;
      }
      if (ch.charCodeAt(0) < 0x80 && !/[A-Za-z0-9\-_.~!$&'()*+,;=:[\]<>"]/.test(ch)) return false;
    }
    return host !== "";
  }

  function checkSPDX(expr) {
    const toks = expr.replace(/\(/g, " ( ").replace(/\)/g, " ) ").split(/\s+/).filter(Boolean);
    let pos = 0;
    const isID = (t) => !["AND", "OR", "WITH", "(", ")"].includes(t) && re.spdxID.test(t);
    function term() {
      if (pos >= toks.length) return "it ends too soon";
      const t = toks[pos++];
      if (t === "(") {
        const e = expression();
        if (e) return e;
        if (pos >= toks.length || toks[pos] !== ")") return "a ( isn't closed";
        pos++;
        return null;
      }
      if (!isID(t)) return `${goQuote(t)} isn't an identifier`;
      if (pos < toks.length && toks[pos] === "WITH") {
        pos++;
        if (pos >= toks.length || !isID(toks[pos])) return "WITH needs an exception identifier";
        pos++;
      }
      return null;
    }
    function expression() {
      const e = term();
      if (e) return e;
      while (pos < toks.length && (toks[pos] === "AND" || toks[pos] === "OR")) {
        pos++;
        const e2 = term();
        if (e2) return e2;
      }
      return null;
    }
    const e = expression();
    if (e) return e;
    if (pos < toks.length) return `unexpected ${goQuote(toks[pos])}`;
    return null;
  }

  function validateAbout(p, add) {
    const a = p.about;
    if (bytes(a.maker) > S.limits.maxMakerBytes) add("about.maker: at most 100 characters; say who makes it in a few words");
    for (const [at, v] of [["about.website", a.website], ["about.source", a.source], ["about.support", a.support]]) {
      if (v !== "" && !validHTTPS(v)) add(`${at}: ${goQuote(v)} must be an https:// address`);
    }
    if (a.releases !== "") {
      const u = a.releases.split(S.words.releasesPlaceholder).join("1.2.3");
      if (!validHTTPS(u) || /[{}]/.test(u)) add(`about.releases: ${goQuote(a.releases)} must be an https:// address, with ${S.words.releasesPlaceholder} where the version goes if it has one place per version`);
    }
    if (a.licence !== "" && a.licence !== S.words.licenceClosed) {
      const err = checkSPDX(a.licence);
      if (err) add(`about.licence: ${goQuote(a.licence)} isn't an SPDX licence expression (${err}); use the project's SPDX identifier, e.g. AGPL-3.0-only, or ${goQuote(S.words.licenceClosed)} if its code isn't published`);
    }
    if (a.licence === S.words.licenceClosed && a.source !== "") add("about.source: a closed-source app has no published source; remove it or give the licence");
    if (a.icon !== "" && !re.icon.test(a.icon)) add(`about.icon: ${goQuote(a.icon)} must be the name of a .png file beside the package file`);
    const cats = a.categories || [];
    if (cats.length > S.limits.maxCategories) add(`about.categories: at most ${S.limits.maxCategories}`);
    const keys = S.categories.map((c) => c.key).join(", ");
    cats.forEach((c, i) => {
      if (!S.categories.some((x) => x.key === c)) add(`about.categories[${i}]: ${goQuote(c)} must be one of ${keys}`);
      if (cats.indexOf(c) !== i) add(`about.categories[${i}]: ${goQuote(c)} is listed twice`);
    });
    const kws = a.keywords || [];
    if (kws.length > S.limits.maxKeywords) add(`about.keywords: at most ${S.limits.maxKeywords}`);
    const seen = new Set();
    kws.forEach((k, i) => {
      if (k === "" || k !== goTrim(k) || bytes(k) > S.limits.maxKeywordBytes) add(`about.keywords[${i}]: ${goQuote(k)} must be a word or short phrase (up to 40 characters, no spaces around it)`);
      if (seen.has(k.toLowerCase())) add(`about.keywords[${i}]: ${goQuote(k)} is listed twice`);
      seen.add(k.toLowerCase());
    });
    const bw = p.built_with || [];
    if (bw.length > S.limits.maxBuiltWith) add(`built_with: at most ${S.limits.maxBuiltWith}; name what the app is built on, not every library it uses`);
    bw.forEach((b, i) => {
      if (!re.builtWith.test(b)) add(`built_with[${i}]: ${goQuote(b)} must be a short name such as php or log4j: lowercase letters, digits, and dashes, starting with a letter`);
      if (bw.indexOf(b) !== i) add(`built_with[${i}]: ${goQuote(b)} is listed twice`);
    });
    const rv = p.requires.roostack;
    if (rv !== "" && !re.roostack.test(rv)) add(`requires.roostack: ${goQuote(rv)} must be a Roostack release such as "0.6" or "0.6.1"`);
    const notes = new Set();
    (p.upgrade_notes || []).forEach((n, i) => {
      const at = `upgrade_notes[${i}]`;
      if (!re.version.test(n.version)) add(`${at}.version: ${goQuote(n.version)} must be the package version the note arrived in`);
      else if (notes.has(n.version)) add(`${at}.version: ${goQuote(n.version)} has two notes; put them in one`);
      notes.add(n.version);
      if (goTrim(n.note) === "") add(`${at}.note: required`);
    });
  }

  // goTrim is strings.TrimSpace (Unicode white space).
  const goTrim = (s) => s.replace(/^[\s\u0085]+|[\s\u0085]+$/gu, "");

  // ---------- Policy and Risks (pkgspec/policy.go) ----------

  function policy(p) {
    const out = [];
    const add = (m) => out.push(m);
    aboutPolicy(p, add);
    for (const name of sortedKeys(p.containers)) {
      if (p.containers[name].memory === "") add(`containers.${name}.memory: say how much memory it typically uses (e.g. 300 MB), for the summary shown before installing.`);
    }
    return out.concat(risks(p));
  }

  function aboutPolicy(p, add) {
    const a = p.about, closed = S.words.licenceClosed;
    if (a.maker === "") add("about.maker: say who makes the app, in a few words, so that the catalog credits them.");
    if (a.licence === "") add(`about.licence: give the app's licence as an SPDX identifier (e.g. AGPL-3.0-only), or ${goQuote(closed)} if its code isn't published.`);
    else if (a.licence === closed) {
      if (a.website === "") add("about.website: an app whose code isn't published needs its website so that people can see who makes it.");
    } else {
      for (const id of ambiguousLicences(a.licence)) {
        const base = id.replace(/\+$/, "");
        add(`about.licence: ${id} doesn't say whether later versions apply; use ${base}-only or ${base}-or-later, as the project's licence notice says.`);
      }
      if (a.source === "") add("about.source: give the address of the app's published code.");
    }
    if (a.icon === "") add(`about.icon: add the project's logo as a PNG beside the package (at least ${S.limits.iconMinSize}×${S.limits.iconMinSize} pixels, square).`);
    if ((a.categories || []).length === 0) add("about.categories: choose at least one, for the catalog's filters.");
    if (p.requires.roostack === "") add('requires.roostack: give the oldest Roostack release the package works with (e.g. "0.6").');
  }

  function ambiguousLicences(expr) {
    return expr.replace(/[()]/g, " ").split(/\s+/).filter(Boolean)
      .filter((t) => L.deprecatedLicences.includes(t.replace(/\+$/, "")));
  }

  function risks(p) {
    const out = [];
    const add = (m) => out.push(m);
    const pin = "pin the image by digest (name:tag@sha256:…) so that every lab runs exactly the image that was reviewed.";
    for (const name of sortedKeys(p.containers)) {
      const c = p.containers[name], at = "containers." + name;
      if (!re.pinned.test(c.image)) add(`${at}.image: ${pin}`);
      for (const msg of quadletPolicy(c.quadlet)) add(`${at}.quadlet: ${msg}`);
    }
    for (const name of sortedKeys(p.databases)) {
      const d = p.databases[name], at = "databases." + name;
      if (d.image !== "" && !re.pinned.test(d.image)) add(`${at}.image: ${pin}`);
      for (const msg of quadletPolicy(d.quadlet)) add(`${at}.quadlet: ${msg}`);
    }
    for (const name of sortedKeys(p.routes)) {
      const r = p.routes[name], at = "routes." + name;
      if (r.exposure !== "private") add(`${at}.exposure: must be private; only the Owner can make an address public.`);
      if (r.public.unsafe === "" && (r.public.login_paths || []).length === 0) add(`${at}.public: name the pages where people sign in (login_paths), or say why the address shouldn't be public at all (unsafe).`);
    }
    for (const name of sortedKeys(p.ports)) {
      const e = p.ports[name].exposure === "" ? "private" : p.ports[name].exposure;
      if (e !== "private") add(`ports.${name}.exposure: must be private; only the Owner can open a connection to the internet.`);
    }
    return out;
  }

  // valueTests are pkgspec's valueTests, by the names schema.js gives.
  const valueTests = {
    volumeHasSource(v) {
      const t = v.trim(), i = t.indexOf(":");
      return i >= 0 && t.slice(0, i) !== "";
    },
    notTmpfsMount(v) { return !isTmpfsMount(v); },
    logPath(v) { return v.toLowerCase().includes("path") || v.includes("\\"); },
    healthLogFolder(v) { return v !== "local" && v !== "events_logger"; },
    keepGroups(v) { return v.toLowerCase().includes("keep-groups") || v.includes("\\"); },
    rootUser(v) {
      let user = v.trim().replace(/^["']+|["']+$/g, "");
      const i = user.indexOf(":");
      if (i >= 0) user = user.slice(0, i);
      user = user.trim();
      if (user.toLowerCase() === "root") return true;
      const digits = user.replace(/^[+-]+/, "");
      return digits !== "" && digits.replace(/^0+|0+$/g, "") === "";
    },
    sharedUserNS(v) {
      const t = v.trim(), i = t.indexOf(":");
      const mode = i < 0 ? t : t.slice(0, i);
      return !["keep-id", "auto", "nomap", "private"].includes(mode);
    },
    roostackLabel(v) {
      if (v.includes("\\")) return true;
      return v.replace(/["']/g, " ").split(/\s+/).filter(Boolean).some((w) => w.toLowerCase().startsWith("io.roostack."));
    },
  };

  function isTmpfsMount(v) {
    if (/[ \t"'\\]/.test(v)) return false;
    let types = 0;
    for (const opt of v.split(",")) {
      const i = opt.indexOf("=");
      const k = (i < 0 ? opt : opt.slice(0, i)).toLowerCase(), val2 = i < 0 ? "" : opt.slice(i + 1);
      if (k === "type") { types++; if (val2 !== "tmpfs") return false; }
      else if (k === "source" || k === "src") return false;
    }
    return types === 1;
  }

  function ruleMatches(rule, key) {
    key = key.toLowerCase();
    for (let k of rule.keys) {
      k = k.toLowerCase();
      if (k === "*") return true;
      if (k.length > 2 && k[0] === "*" && k[k.length - 1] === "*") { if (key.includes(k.slice(1, -1))) return true; }
      else if (k.endsWith("*")) { if (key.startsWith(k.slice(0, -1))) return true; }
      else if (key === k) return true;
    }
    return false;
  }

  function quadletPolicy(q) {
    const out = [];
    for (const section of sortedKeys(q)) {
      for (const key of sortedKeys(q[section])) {
        const kerr = checkQuadletKey(key);
        if (kerr) { out.push(`${section}.${goQuote(key)} isn't allowed: ${kerr}.`); continue; }
        const rule = S.quadlet.rules.find((r) => r.section === section && ruleMatches(r, key)) || null;
        let refused = false;
        for (const value of quadletValues(q[section][key])) {
          const verr = checkQuadletValue(value);
          if (verr) { out.push(`${section}.${key}=${goQuote(value)} isn't allowed: ${verr}.`); refused = true; continue; }
          if (!rule || (rule.denies && !valueTests[rule.denies](value))) continue;
          out.push(`${section}.${key}=${value} isn't allowed: ${rule.instead}.`);
          refused = true;
        }
        const allowed = S.quadlet.allowed[section] || [];
        if (!refused && !allowed.includes(key)) {
          const same = allowed.find((k) => k.toLowerCase() === key.toLowerCase());
          out.push(`${section}.${key} isn't allowed: ${same ? `write it as ${same}, as systemd and Quadlet only read it that way`
            : "it isn't one of the keys Roostack has checked stay inside the service; a package that needs it can ask for it to be added to the package policy"}.`);
        }
      }
    }
    return out;
  }

  // ---------- Templates (what generate.Check renders) ----------

  // templates checks what each template refers to: the settings, web
  // addresses, connections, databases and containers it names exist, and
  // secrets aren't among them. Rendering them is Go's (generate.Check).
  function templates(p) {
    const out = [];
    const where = [];
    for (const name of sortedKeys(p.containers)) {
      const c = p.containers[name], at = "containers." + name;
      if (!c) continue;
      (c.command || []).forEach((a, i) => where.push([`${at}.command[${i}]`, a, null]));
      for (const k of sortedKeys(c.env)) where.push([`${at}.env.${k}`, c.env[k], null]);
      if (c.when !== "") where.push([`${at}.when`, c.when, null, true]);
      (c.devices || []).forEach((d, i) => { if (d.when !== "") where.push([`${at}.devices[${i}].when`, d.when, null, true]); });
    }
    (p.hooks || []).forEach((h, i) => {
      if (!h) return;
      for (const k of sortedKeys(h.env)) where.push([`hooks[${i}].env.${k}`, h.env[k], h]);
    });
    const sso = p.sso || {};
    for (const c of sortedKeys(sso.env)) for (const k of sortedKeys(sso.env[c])) where.push([`sso.env.${c}.${k}`, sso.env[c][k], null]);
    if (sso.oidc) (sso.oidc.redirect_paths || []).forEach((rp, i) => where.push([`sso.oidc.redirect_paths[${i}]`, rp, null]));
    const secretEnv = new Set();
    for (const c of sortedKeys(sso.secret_env)) for (const k of sortedKeys(sso.secret_env[c])) {
      where.push([`sso.secret_env.${c}.${k}`, sso.secret_env[c][k], null]);
      secretEnv.add(`sso.secret_env.${c}.${k}`);
    }
    for (const [at, text, hook, isWhen] of where) {
      for (const msg of checkTemplate(p, text, hook, secretEnv.has(at))) out.push(`${at}: ${msg}`);
      if (isWhen && !text.includes("{{") && !["true", "false"].includes(text.trim())) out.push(`${at}: a condition must be true or false, or a template that gives one, such as {{ .Settings.x }}`);
    }
    return out;
  }

  const fields = {
    Service: { Name: 1 }, Lab: { Domain: 1, TimeZone: 1 },
    route: { Subdomain: 1, Host: 1, URL: 1, Exposure: 1, Port: 1, PublicPaths: 1 },
    port: { Enabled: 1, HostPort: 1, Protocol: 1, Exposure: 1 },
    database: { Host: 1, Port: 1, User: 1, Name: 1 }, container: { Host: 1 },
    // generate.SSOContext.
    SSO: { Enabled: 1, Issuer: 1, DiscoveryURL: 1, AuthorizeURL: 1, TokenURL: 1, UserinfoURL: 1, ClientID: 1, ProviderName: 1, ButtonText: 1, UserGroup: 1, AdminGroup: 1 },
  };
  const names = (m) => sortedKeys(m).join(", ") || "none";

  function checkTemplate(p, text, hook, secretEnv) {
    const out = [];
    if (!text.includes("{{")) return out;
    const opens = (text.match(/\{\{/g) || []).length, closes = (text.match(/\}\}/g) || []).length;
    if (opens !== closes) return [`${goQuote(text)} has a {{ without its }}, or the other way round`];
    // Inside {{ with }} and {{ range }} the dot changes: only the action
    // itself is checked there.
    const scoped = /\{\{-?\s*(with|range)\b/.test(text);
    for (const [, action] of text.matchAll(/\{\{-?([\s\S]*?)-?\}\}/g)) {
      if (scoped && !/^\s*(with|range|if)\b/.test(action)) continue;
      for (const m of action.matchAll(/index\s+\$?\.Containers\s+"([^"]*)"/g)) {
        if (!has(p.containers, m[1])) out.push(`the container ${goQuote(m[1])} isn't in the package (it has ${names(p.containers)})`);
      }
      for (const m of action.matchAll(/(?:^|[\s(|$])\$?(\.[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)*)/g)) {
        const parts = m[1].slice(1).split(".");
        const msg = checkChain(p, parts, hook, secretEnv);
        if (msg) out.push(msg);
      }
    }
    return [...new Set(out)];
  }

  function checkChain(p, parts, hook, secretEnv) {
    const [top, a, b] = parts;
    const leaf = (set, k, what) => (k === undefined || set[k] ? null : `${what} has no ${k} (it has ${Object.keys(set).join(", ")})`);
    switch (top) {
      case "Service": return leaf(fields.Service, a, ".Service");
      case "Lab": return leaf(fields.Lab, a, ".Lab");
      case "SSO":
        // generate.SSOContext.ClientSecret: never into a file Roostack writes.
        if (a === "ClientSecret") return secretEnv ? null : ".SSO.ClientSecret is only for sso.secret_env (or an sso hook's secrets: sso.client_secret)";
        return leaf(fields.SSO, a, ".SSO");
      case "Settings": {
        if (a === undefined) return null;
        const s = settingOf(p, a);
        if (!s) return `.Settings.${a}: the package has no setting ${goQuote(a)}`;
        if (s.type === "secret") return `.Settings.${a} is a secret, and secrets never go into templates: give it to the container with secrets: instead`;
        return null;
      }
      case "Routes":
        if (a === undefined) return null;
        if (!has(p.routes, a)) return `.Routes.${a}: the package has no web address ${goQuote(a)} (it has ${names(p.routes)})`;
        return leaf(fields.route, b, `.Routes.${a}`);
      case "Ports":
        if (a === undefined) return null;
        if (!has(p.ports, a)) return `.Ports.${a}: the package has no extra connection ${goQuote(a)} (it has ${names(p.ports)})`;
        return leaf(fields.port, b, `.Ports.${a}`);
      case "Databases": {
        if (a === undefined) return null;
        const d = has(p.databases, a) ? p.databases[a] : null;
        if (!d) return `.Databases.${a}: the package has no database ${goQuote(a)} (it has ${names(p.databases)})`;
        if (d.type === "sqlite") return `.Databases.${a}: a SQLite database is a file in the app's own storage, with nothing to connect to`;
        return leaf(fields.database, b, `.Databases.${a}`);
      }
      case "Containers":
        if (a === undefined) return null;
        if (!has(p.containers, a)) return `.Containers.${a}: the package has no container ${goQuote(a)} (it has ${names(p.containers)})`;
        return leaf(fields.container, b, `.Containers.${a}`);
      case "Inputs": {
        if (!hook || hook.on !== "action") return ".Inputs is only for action hooks";
        if (a === undefined) return null;
        const inp = (hook.inputs || []).find((x) => x.key === a);
        if (!inp) return `.Inputs.${a}: the action has no input ${goQuote(a)}`;
        if (inp.type === "secret") return `.Inputs.${a} is a secret, and secrets never go into templates: use the hook's secrets: instead`;
        return null;
      }
    }
    return `.${top} isn't something a template can use (try .Settings, .Routes, .Databases, .Ports, .Containers, .Lab, .Service or .SSO)`;
  }

  // ---------- Finding a problem's place ----------

  // pathOf splits a problem's path ("containers.app.mounts[0].path",
  // 'containers.app.quadlet.Container."Bad"') into segments.
  function splitPath(path) {
    const segs = [];
    const rx = /\.?"((?:[^"\\]|\\.)*)"|\.?\[(\d+)\]|\.?([^.[\]"]+)/g;
    let m;
    while ((m = rx.exec(path))) {
      if (m[1] !== undefined) segs.push(JSON.parse('"' + m[1] + '"'));
      else if (m[2] !== undefined) segs.push(Number(m[2]));
      else segs.push(m[3]);
    }
    return segs;
  }

  // locate finds the YAML node a problem's path names, as far down as it
  // exists: { node, keyNode, found } (found: how many segments matched).
  function locate(doc, path) {
    let node = doc.contents, keyNode = null, found = 0;
    const segs = splitPath(path);
    for (const seg of segs) {
      while (node && YAML.isAlias(node)) node = node.resolve(doc);
      if (YAML.isMap(node)) {
        const pair = node.items.find((it) => YAML.isScalar(it.key) && String(it.key.value) === String(seg));
        if (!pair) break;
        keyNode = pair.key;
        node = pair.value;
      } else if (YAML.isSeq(node)) {
        let item = null;
        if (typeof seg === "number") item = node.items[seg];
        else item = node.items.find((it) => YAML.isMap(it) && it.items.some((pr) => YAML.isScalar(pr.key) && pr.key.value === "key" && YAML.isScalar(pr.value) && String(pr.value.value) === seg));
        if (item === undefined || item === null) break;
        keyNode = null;
        node = item;
      } else break;
      found++;
    }
    return { node, keyNode, found, total: segs.length };
  }

  // problemPath is the path a message starts with ("about.licence: …").
  function problemPath(message) {
    const m = /^((?:[A-Za-z0-9_-]+|\[\d+\]|"(?:[^"\\]|\\.)*")(?:(?:\.(?:[A-Za-z0-9_-]+|"(?:[^"\\]|\\.)*"))|\[\d+\])*)(?=[:.=]| isn't)/.exec(message);
    return m ? m[1] : "";
  }

  // ---------- Everything at once ----------

  // run checks a package's text, as `roostack validate` would, plus the
  // policy (as errors, or with local, risks only). It returns problems:
  // { kind: "validation" | "registry", path, message, line }.
  function run(text, opts) {
    opts = opts || {};
    const lc = new YAML.LineCounter();
    const doc = YAML.parseDocument(text, { lineCounter: lc, merge: true, uniqueKeys: true, prettyErrors: false });
    const problems = [];
    const lineAt = (node) => (node && node.range ? lc.linePos(node.range[0]).line : 0);
    const place = (kind, message, source) => {
      let path = problemPath(message);
      // A Quadlet line: its section and key too.
      const q = /\.quadlet: ([A-Za-z]+)\.("(?:[^"\\]|\\.)*"|[A-Za-z0-9]+)/.exec(message);
      if (q && path.endsWith(".quadlet")) path += "." + q[1] + "." + q[2];
      const loc = path ? locate(doc, path) : null;
      // A field that isn't written yet has no line: its part may not be either.
      const line = loc && loc.found > 0 ? lineAt(loc.keyNode || loc.node) : 0;
      problems.push({ kind, path, message, line, source: source || kind });
    };
    if (doc.errors.length) {
      for (const e of doc.errors) problems.push({ kind: "validation", path: "", message: "YAML: " + e.message.split("\n")[0], line: e.linePos ? e.linePos[0].line : 0, source: "yaml" });
      return { doc, lc, problems, pkg: null };
    }
    // An empty file is nothing Go can decode (it reads no document).
    if (doc.contents === null || doc.contents === undefined) {
      problems.push({ kind: "validation", path: "", message: "The package is empty.", line: 1, source: "yaml" });
      return { doc, lc, problems, pkg: null };
    }
    const { pkg, errors } = decode(doc, lc);
    if (errors.length) {
      for (const e of errors) problems.push({ kind: "validation", path: "", message: `line ${e.line}: ${e.message}`, line: e.line, source: "decode" });
      return { doc, lc, problems, pkg: null };
    }
    applyDefaults(pkg);
    const verrs = validate(pkg);
    for (const m of verrs) place("validation", m);
    for (const m of templates(pkg)) place("validation", m, "template");
    if (verrs.length === 0) {
      for (const m of opts.local ? risks(pkg) : policy(pkg)) place("registry", m);
    }
    return { doc, lc, problems, pkg };
  }

  // checkIcon is pkgspec.CheckIcon on a PNG's bytes: an error, or null.
  function checkIcon(data) {
    if (data.length > S.limits.iconMaxBytes) return `${data.length} bytes is too large (at most ${S.limits.iconMaxBytes / 1000} KB)`;
    const sig = [137, 80, 78, 71, 13, 10, 26, 10];
    if (data.length < 24 || sig.some((b, i) => data[i] !== b) || String.fromCharCode(...data.slice(12, 16)) !== "IHDR") return "not a PNG image";
    const u32 = (o) => ((data[o] << 24) | (data[o + 1] << 16) | (data[o + 2] << 8) | data[o + 3]) >>> 0;
    const w = u32(16), h = u32(20), min = S.limits.iconMinSize;
    if (w !== h) return `${w}×${h} isn't square`;
    if (w < min) return `${w}×${h} is too small (at least ${min}×${min})`;
    return null;
  }

  R.check = {
    run, decode, applyDefaults, validate, policy, risks, templates, checkIcon, locate, splitPath, problemPath,
    parseMemory, goQuote, goFloat, resolvePlain, valueTests, validHTTPS, checkSPDX,
  };
})(globalThis.RoostackEditor = globalThis.RoostackEditor || {});
