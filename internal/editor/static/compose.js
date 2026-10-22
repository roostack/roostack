// Roostack's package editor: importing a Docker Compose file (SPEC.md
// §8.3.7, "The package editor"). It makes a package to start from and a
// report in four groups, each item saying why: what was converted, what
// Roostack doesn't allow, what Roostack does itself, and what's over to
// the person (the image's digest, memory, what the storage holds...).
// Nothing is fetched: the editor works offline.
//
// Text in the report marks code with backticks, which the page shows as
// <code>.
(function (R) {
  "use strict";

  // ---------- Small helpers ----------

  const lower = (s) => String(s).toLowerCase();

  // escapeRegExp makes a name safe to put in a regular expression: Compose
  // allows "." in service names, which would otherwise match any character.
  // The editor uses it too (R.compose.escapeRegExp).
  const escapeRegExp = (s) => String(s).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

  // safeName makes a name pkgspec accepts: lowercase letters, digits and
  // dashes, starting with a letter, at most 31 characters.
  function safeName(s, fallback) {
    let n = lower(s).replace(/[^a-z0-9-]+/g, "-").replace(/-+/g, "-").replace(/^[^a-z]+/, "").replace(/-+$/, "").slice(0, 31).replace(/-+$/, "");
    if (!n) n = fallback;
    if (n === "network" || n.startsWith("db-")) n = "app-" + n;
    return n.slice(0, 31);
  }
  const settingKey = (s) => lower(s).replace(/[^a-z0-9_]+/g, "_").replace(/^[^a-z]+/, "").replace(/_+$/, "").slice(0, 63) || "value";
  const titleOf = (s) => {
    const words = String(s).replace(/[_-]+/g, " ").trim().toLowerCase();
    return words.charAt(0).toUpperCase() + words.slice(1);
  };

  // imageParts splits an image reference: registry, repository, tag, digest.
  function imageParts(ref) {
    let rest = String(ref).trim(), digest = "", tag = "";
    const at = rest.indexOf("@");
    if (at >= 0) { digest = rest.slice(at + 1); rest = rest.slice(0, at); }
    const slash = rest.lastIndexOf("/"), colon = rest.lastIndexOf(":");
    if (colon > slash) { tag = rest.slice(colon + 1); rest = rest.slice(0, colon); }
    let registry = "docker.io", repo = rest;
    const first = rest.split("/")[0];
    if (rest.includes("/") && (first.includes(".") || first.includes(":") || first === "localhost")) {
      registry = first;
      repo = rest.slice(first.length + 1);
    }
    if (registry === "docker.io" && !repo.includes("/")) repo = "library/" + repo;
    return { registry, repo, tag, digest, base: repo.split("/").pop() };
  }
  const fullImage = (p) => `${p.registry}/${p.repo}${p.tag ? ":" + p.tag : ""}${p.digest ? "@" + p.digest : ""}`;

  // A version from an image's tag: 1.2.3 from v1.2.3, 1.2.3-alpine...
  function versionFromTag(tag) {
    const m = /^v?(\d+(?:\.\d+){0,3})(?:[-_.+][A-Za-z0-9.]*)?$/.exec(tag || "");
    return m ? m[1] : "";
  }

  // memoryOf reads Compose's sizes (512m, 1g, 1.5G, 268435456) as a
  // package memory figure, rounded: "512 MB".
  function memoryOf(v) {
    const m = /^\s*(\d+(?:\.\d+)?)\s*([bkmgt]?)i?b?\s*$/i.exec(String(v));
    if (!m) return "";
    const unit = { "": 1, b: 1, k: 1024, m: 1024 ** 2, g: 1024 ** 3, t: 1024 ** 4 }[lower(m[2])];
    const bytes = parseFloat(m[1]) * unit;
    if (bytes >= 1e9) return `${Math.round(bytes / 1e8) / 10} GB`;
    return `${Math.max(1, Math.round(bytes / 1e6))} MB`;
  }

  // seconds reads a Compose duration (30s, 1m30s, 1h) as whole seconds.
  function seconds(v) {
    let total = 0, any = false;
    for (const [, n, u] of String(v).matchAll(/(\d+(?:\.\d+)?)(ms|us|ns|h|m|s)/g)) {
      any = true;
      total += parseFloat(n) * { h: 3600, m: 60, s: 1, ms: 0.001, us: 1e-6, ns: 1e-9 }[u];
    }
    if (!any && /^\d+$/.test(String(v))) return Number(v);
    return any ? Math.max(1, Math.round(total)) : null;
  }

  // shellWords splits a command line as a shell would, for Compose's
  // string form of command.
  function shellWords(s) {
    const out = [];
    let cur = "", q = null, any = false;
    for (let i = 0; i < s.length; i++) {
      const c = s[i];
      if (q) {
        if (c === q) q = null;
        else if (c === "\\" && q === '"' && i + 1 < s.length) cur += s[++i];
        else cur += c;
      } else if (c === "'" || c === '"') { q = c; any = true; }
      else if (c === "\\" && i + 1 < s.length) { cur += s[++i]; any = true; }
      else if (/\s/.test(c)) { if (cur || any) out.push(cur); cur = ""; any = false; }
      else cur += c;
    }
    if (cur || any) out.push(cur);
    return out;
  }

  // ---------- Interpolation (${VAR:-default}) ----------

  function parseEnvFile(text) {
    const vars = {};
    for (const line of String(text || "").split(/\r?\n/)) {
      const m = /^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$/.exec(line);
      if (!m) continue;
      let v = m[2];
      if (/^(["']).*\1$/.test(v)) v = v.slice(1, -1);
      else v = v.replace(/\s+#.*$/, "");
      vars[m[1]] = v;
    }
    return vars;
  }

  // interpolate resolves ${VAR}, ${VAR:-default}, ${VAR-default} and
  // $VAR from the .env given, and $$; it lists the variables it couldn't.
  function interpolate(s, vars, missing) {
    return String(s).replace(/\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?+])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)/g, (all, name, op, arg, bare) => {
      if (all === "$$") return "$";
      const n = name || bare;
      const set = Object.prototype.hasOwnProperty.call(vars, n);
      const v = set ? vars[n] : undefined;
      if (op === ":-" || op === "-") return set && (op === "-" || v !== "") ? v : arg;
      if (op === ":+" || op === "+") return set && (op === "+" || v !== "") ? arg : "";
      if (set) return v;
      missing.add(n);
      return "${" + n + "}";
    });
  }

  // ---------- What things are ----------

  const dbKinds = [
    [/postgres|postgis|pgvecto|timescale/, "postgres"],
    [/mariadb/, "mariadb"],
    [/mysql/, "mysql"],
    [/redis|valkey|keydb|dragonfly/, "redis"],
  ];
  const officialDB = /^(library\/(postgres|mysql|mariadb|redis|valkey)|valkey\/valkey)$/;
  function databaseKind(image) {
    const p = imageParts(image);
    for (const [rx, kind] of dbKinds) if (rx.test(p.repo)) return kind;
    return "";
  }

  const webPorts = new Set([80, 81, 3000, 3001, 4000, 5000, 5001, 5005, 5006, 5055, 5230, 7878, 8000, 8001, 8008, 8065, 8080, 8081, 8082, 8083, 8085,
    8088, 8090, 8096, 8123, 8181, 8200, 8384, 8443, 8888, 8989, 9000, 9090, 9091, 9117, 13378, 2283, 3456, 6875, 9925, 8686, 7575, 2342, 5678]);
  const nonWeb = { 22: "ssh", 2222: "ssh", 53: "dns", 25: "smtp", 465: "smtps", 587: "submission", 143: "imap", 993: "imaps", 110: "pop3", 995: "pop3s",
    1883: "mqtt", 8883: "mqtts", 51820: "wireguard", 3478: "turn", 5349: "turns", 1900: "ssdp", 7359: "discovery", 389: "ldap", 636: "ldaps" };

  // storageGuess suggests whether a folder is backed up, and what it holds,
  // from where it's mounted and then its name: caches and thumbnails
  // aren't backed up; anything else is.
  function storageGuess(first, second) {
    const g = guessOne(first);
    return g.title === "Its data" && second ? guessOne(second) : g;
  }
  function guessOne(text) {
    const s = lower(text);
    if (/cache|tmp|temp|thumb|transcode|preview|\blogs?\b|encoded/.test(s)) return { class: "replaceable", title: /thumb|preview/.test(s) ? "Thumbnails and previews" : /transcode|encoded/.test(s) ? "Converted videos" : /log/.test(s) ? "Logs" : "Cache" };
    if (/photo|picture|video|movie|\btv\b|music|audio|media|librar|upload|book|download|document|files/.test(s)) {
      const media = /photo|picture|video|movie|\btv\b|music|audio|media|librar|upload/.test(s);
      return { class: "critical", media, title: /photo|picture/.test(s) ? "Your photos and videos" : /music|audio/.test(s) ? "Your music" : /document/.test(s) ? "Your documents" : /book/.test(s) ? "Your books" : "Uploaded files" };
    }
    if (/conf|setting/.test(s)) return { class: "critical", title: "Its settings" };
    if (/\bdb\b|database|sqlite/.test(s)) return { class: "critical", title: "Its database" };
    return { class: "critical", title: "Its data" };
  }

  const secretName = /(PASSWORD|PASSWD|PASS$|SECRET|TOKEN|(^|_)KEY$|_KEY_|SALT|PRIVATE|JWT|ENCRYPTION|PEPPER|CREDENTIAL)/;
  const adminSecret = /(ADMIN|INITIAL|ROOT|DEFAULT|SUPERUSER|OWNER|FIRST)[A-Z_]*(PASS|PASSWORD)/;
  const ownURLName = /(PUBLIC|PUBLISHED|BASE|EXTERNAL|SITE|ROOT|SERVER|APP|WEB|FRONTEND|SERVICE)_?[A-Z]*_?(URL|URI|ADDRESS|ORIGIN)$|^(URL|DOMAIN|ORIGIN|HOSTNAME)$|_DOMAIN$/;
  const userIDs = /^(PUID|PGID|UID|GID|USER_ID|GROUP_ID|USER_UID|USER_GID)$/;

  // Titles for well-known extra connections.
  const portTitles = { ssh: "Git or files over SSH", dns: "DNS", smtp: "Mail (SMTP)", smtps: "Mail (SMTPS)", submission: "Mail submission",
    imap: "Mail (IMAP)", imaps: "Mail (IMAPS)", pop3: "Mail (POP3)", pop3s: "Mail (POP3S)", mqtt: "MQTT", mqtts: "MQTT over TLS",
    wireguard: "WireGuard", turn: "TURN (calls)", turns: "TURN over TLS (calls)", ssdp: "Network discovery (SSDP)", discovery: "Network discovery",
    ldap: "LDAP", ldaps: "LDAPS" };

  // secretTitle names a setting for people, from its variable's name.
  function secretTitle(base) {
    if (/JWT|SIGN|SESSION|COOKIE|^SECRET$|SECRET_KEY|APP_KEY/.test(base)) return "Signing key";
    if (/ENCRYPT/.test(base)) return "Encryption key";
    if (adminSecret.test(base)) return "Administrator password";
    return titleOf(base);
  }

  // ---------- The import ----------

  // importCompose converts a Compose file's text. opts.env is the text of
  // an .env file, if pasted; opts.version is the Roostack release to
  // require. It returns { yaml, report, summary } or { error }.
  function importCompose(text, opts) {
    opts = opts || {};
    const doc = YAML.parseDocument(text, { merge: true, uniqueKeys: false });
    if (doc.errors.length) return { error: `That isn't a Compose file Roostack can read: ${doc.errors[0].message.split("\n")[0]}` };
    const compose = doc.toJS({ maxAliasCount: -1 }) || {};
    if (!compose || typeof compose !== "object" || !compose.services || typeof compose.services !== "object") {
      return { error: "That isn't a Compose file: it has no services:." };
    }
    const vars = parseEnvFile(opts.env);
    const missing = new Set();
    // Variables the Compose file itself uses (in images, folders, ports)
    // aren't the app's: they don't go into its environment from .env.
    const composeVars = new Set();
    const I = (s) => interpolate(s, vars, missing);
    const IC = (s) => {
      for (const m of String(s).matchAll(/\$\{?([A-Za-z_][A-Za-z0-9_]*)/g)) composeVars.add(m[1]);
      return I(s);
    };

    const report = { converted: [], notAllowed: [], notNeeded: [], overToYou: [] };
    const say = (group, text) => { if (!report[group].includes(text)) report[group].push(text); };

    // Which services are databases Roostack runs, and which are the app's.
    const services = Object.entries(compose.services).filter(([, s]) => s && typeof s === "object");
    for (const [, s] of services) {
      if (s.image) IC(s.image);
      for (const vol of [].concat(s.volumes || [])) volumeOf(vol, IC);
      for (const port of [].concat(s.ports || [])) portOf(port, IC);
    }
    const dbs = {}, apps = [];
    let sqlCount = 0, redisCount = 0;
    for (const [name, s] of services) {
      const kind = s.image ? databaseKind(I(s.image)) : "";
      if (kind) {
        const isSQL = kind !== "redis";
        const n = isSQL ? (sqlCount++ === 0 ? "main" : safeName(name, "db")) : (redisCount++ === 0 ? "cache" : safeName(name, "cache"));
        dbs[name] = { name: n, kind, service: s };
      } else apps.push([name, s]);
    }
    if (apps.length === 0) return { error: "Every service in that file is a database; there's no app to make a package for." };

    // Container names: app for one, otherwise each service's own.
    const containerName = {};
    for (const [name] of apps) containerName[name] = apps.length === 1 ? "app" : safeName(name, "app");

    // The package's own name and title.
    const mainEntry = apps.find(([, s]) => (s.ports || s.expose || []).length) || apps[0];
    const mainImage = mainEntry[1].image ? imageParts(IC(mainEntry[1].image)) : null;
    // A prefix every app container shares with the package (immich-server,
    // immich-machine-learning) is left out of their names.
    if (apps.length > 1) {
      const pre = safeName(compose.name || (mainImage ? mainImage.base.split("-")[0] : ""), "") + "-";
      if (pre.length > 1 && apps.every(([n]) => containerName[n].startsWith(pre) && containerName[n].length > pre.length)) {
        for (const [n] of apps) containerName[n] = containerName[n].slice(pre.length);
      }
    }
    const name = safeName(compose.name || (mainImage ? mainImage.base : mainEntry[0]), "myapp");
    const title = titleOf(compose.name || (mainImage ? mainImage.base : mainEntry[0])).replace(/\b\w/g, (c) => c.toUpperCase());
    let version = mainImage ? versionFromTag(mainImage.tag) : "";
    if (!version) {
      say("overToYou", `Give the app's version: the image's tag (\`${mainImage ? mainImage.tag || "latest" : "none"}\`) isn't one. Use a release's own tag, so every lab runs the same version.`);
      version = "0.1.0";
    }

    const pkg = {
      schema: "roostack/v1alpha1", name, title, description: "", version: version + "-roostack.1",
      about: {}, requires: { roostack: opts.version ? String(opts.version).split(".").slice(0, 2).join(".") : "0.1" },
      settings: [], containers: {}, storage: {}, databases: {}, routes: {}, ports: {},
    };
    const comments = []; // [path, comment, before]
    const setting = (s, comment) => {
      if (!pkg.settings.some((x) => x.key === s.key)) { pkg.settings.push(s); if (comment) comments.push([["settings", pkg.settings.length - 1, "key"], comment]); }
      return s.key;
    };
    // A setting's key and title from a variable's name, without the
    // app's own prefix (VIKUNJA_SERVICE_JWTSECRET: jwt_secret if its value
    // was ${JWT_SECRET}).
    const prefix = new RegExp("^(" + name.replace(/-/g, "_").toUpperCase() + "|" + title.replace(/\W+/g, "_").toUpperCase() + ")_");
    const named = (k, v) => {
      const m = /^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$/.exec(v);
      if (m) missing.delete(m[1]);
      const base = (m ? m[1] : k).toUpperCase().replace(prefix, "").replace(/^(SERVICE|APP|CONFIG)_/, "");
      return { key: settingKey(base), title: secretTitle(base) };
    };

    // Databases.
    for (const [svc, d] of Object.entries(dbs)) {
      const entry = { type: d.kind };
      const img = d.service.image ? imageParts(I(d.service.image)) : null;
      if (img && !officialDB.test(img.repo)) {
        entry.image = fullImage(img);
        say("converted", `\`${svc}\`'s image \`${I(d.service.image)}\` as the database's own image (Roostack runs its own unless a package says otherwise).`);
        if (!img.digest) say("overToYou", `Pin the database image \`${fullImage(img)}\` by its digest, as the app's.`);
      } else if (img && img.tag) {
        const want = { postgres: "17", mysql: "8.4", mariadb: "11.8" }[d.kind];
        if (want && !img.tag.startsWith(want)) say("overToYou", `The Compose file used \`${img.repo.replace("library/", "")}:${img.tag}\`; Roostack runs ${{ postgres: "Postgres", mysql: "MySQL", mariadb: "MariaDB" }[d.kind]} ${want}. Check the app supports it.`);
      }
      const limit = d.service.mem_limit || (d.service.deploy && d.service.deploy.resources && d.service.deploy.resources.limits && d.service.deploy.resources.limits.memory);
      if (limit && memoryOf(limit)) entry.memory = memoryOf(limit);
      if (d.kind === "redis") {
        entry.backup = false;
        comments.push([["databases", d.name, "backup"], " a queue or cache: check it's safe to lose"]);
      }
      pkg.databases[d.name] = entry;
      say("converted", `\`${svc}\` as the ${d.kind === "redis" ? "Redis" : d.kind === "postgres" ? "Postgres" : d.kind === "mysql" ? "MySQL" : "MariaDB"} database \`${d.name}\`, which Roostack runs${d.kind === "redis" ? "" : ", makes a password for and backs up"}.`);
      const dropped = ["volumes", "environment", "healthcheck", "ports", "command"].filter((k) => d.service[k]);
      if (dropped.length) say("notNeeded", `\`${svc}\`'s ${dropped.map((k) => "`" + k + "`").join(", ")}: Roostack keeps the database's data, makes its user and password, and checks it itself.`);
    }

    // What the databases' environment says, to recognise the app's
    // references to it: user, password, database name.
    const dbFacts = {};
    for (const [svc, d] of Object.entries(dbs)) {
      const env = envMap(d.service.environment, I);
      dbFacts[svc] = {
        user: env.POSTGRES_USER || env.MYSQL_USER || env.MARIADB_USER || (d.kind === "postgres" ? "postgres" : ""),
        password: env.POSTGRES_PASSWORD || env.MYSQL_PASSWORD || env.MARIADB_PASSWORD || env.REDIS_PASSWORD || "",
        db: env.POSTGRES_DB || env.MYSQL_DATABASE || env.MARIADB_DATABASE || "",
        port: { postgres: "5432", mysql: "3306", mariadb: "3306", redis: "6379" }[d.kind],
      };
    }

    // Named volumes shared between containers become one storage entry.
    const storageFor = {};
    const usedStorage = new Set();
    function storageName(source, target) {
      if (storageFor[source]) return storageFor[source];
      let n = safeName(String(source).split("/").filter((x) => x && x !== ".").pop() || target.split("/").pop(), "data");
      if (!n || n === "app") n = "data";
      let k = n, i = 2;
      while (usedStorage.has(k)) k = `${n}-${i++}`;
      usedStorage.add(k);
      storageFor[source] = k;
      return k;
    }

    let firstRoute = null;
    const known = new Set(["image", "build", "command", "entrypoint", "environment", "env_file", "ports", "expose", "volumes", "depends_on", "healthcheck",
      "user", "devices", "deploy", "mem_limit", "mem_reservation", "shm_size", "ulimits", "cap_drop", "cap_add", "read_only", "init", "stop_signal",
      "stop_grace_period", "working_dir", "hostname", "extra_hosts", "dns", "sysctls", "security_opt", "pids_limit", "tmpfs", "restart", "container_name",
      "networks", "labels", "logging", "stdin_open", "tty", "platform", "pull_policy", "profiles", "privileged", "network_mode", "pid", "ipc", "userns_mode", "group_add"]);

    for (const [svc, s] of apps) {
      const cname = containerName[svc];
      const c = {};
      const at = (k) => ["containers", cname, k];
      const quadlet = {};
      const ql = (key, value) => {
        if (quadlet[key] === undefined) quadlet[key] = value;
        else quadlet[key] = [].concat(quadlet[key], value);
      };

      // The image.
      if (s.image) {
        const img = imageParts(IC(s.image));
        c.image = fullImage(img);
        if (!img.digest) {
          comments.push([at("image"), ` Pin by digest: skopeo inspect --raw docker://${fullImage(img)} | sha256sum`, true]);
          say("overToYou", `Pin \`${fullImage(img)}\` by its digest (the editor can't look it up offline): \`skopeo inspect --raw docker://${fullImage(img)} | sha256sum\`, then add \`@sha256:\` and the result after the tag.`);
        }
        say("converted", `\`${svc}\`'s image, as \`${fullImage(img)}\`${svc === mainEntry[0] && versionFromTag(img.tag) ? `; the version \`${pkg.version}\` from its tag` : ""}.`);
      } else {
        c.image = "";
        say("notAllowed", `\`${svc}\` is built from source (\`build:\`): a package runs an image the project publishes. Find the project's published image and put it in.`);
      }

      // Its command.
      if (s.command !== undefined && s.command !== null) {
        c.command = (Array.isArray(s.command) ? s.command.map((x) => I(x)) : shellWords(I(s.command))).map(String);
        say("converted", `\`${svc}\`'s command.`);
      }
      if (s.entrypoint !== undefined && s.entrypoint !== null) {
        const ep = Array.isArray(s.entrypoint) ? s.entrypoint.map((x) => I(x)) : shellWords(I(s.entrypoint));
        ql("Entrypoint", ep.length === 1 ? ep[0] : JSON.stringify(ep));
        say("converted", `\`${svc}\`'s entrypoint, as the Quadlet line \`Entrypoint\`.`);
      }

      // Its environment: its env_file's (the .env pasted, if any), then
      // its own.
      let env = envMap(s.environment, I);
      if (s.env_file) {
        const files = [].concat(s.env_file).map((f) => (typeof f === "object" ? f.path : f)).join("`, `");
        if (opts.env && Object.keys(vars).length) {
          const fromFile = {};
          for (const [k, v] of Object.entries(vars)) if (!composeVars.has(k)) fromFile[k] = v;
          env = Object.assign(fromFile, env);
          say("converted", `\`${svc}\`'s \`${files}\`, from the .env you pasted (but the variables the Compose file uses itself, such as \`${[...composeVars][0] || "folders"}\`).`);
        } else {
          say("overToYou", `\`${svc}\` reads \`${files}\`, which the editor can't open: paste what's in it into the .env box and import again, or add its variables yourself.`);
        }
      }
      const cenv = {}, secrets = {};
      for (const [k, raw] of Object.entries(env)) {
        const v = raw === null || raw === undefined ? "" : String(raw);
        const K = k.toUpperCase();
        // A database the app connects to.
        const dbHit = Object.entries(dbs).find(([dsvc]) => v === dsvc || new RegExp(`^${escapeRegExp(dsvc)}:\\d+$`).test(v) || new RegExp(`^[a-z]+://([^@/]*@)?${escapeRegExp(dsvc)}([:/]|$)`, "i").test(v));
        const factHit = Object.entries(dbFacts).find(([dsvc, f]) => dbs[dsvc] && (
          (f.password && v === f.password && /PASS/.test(K)) || (f.user && v === f.user && /USER/.test(K)) || (f.db && v === f.db && /(NAME|DATABASE|_DB)$/.test(K))));
        if (dbHit) {
          const [dsvc, d] = dbHit;
          if (v === dsvc) { cenv[k] = `{{ .Databases.${d.name}.Host }}`; say("converted", `\`${k}\` as the database's address, \`{{ .Databases.${d.name}.Host }}\`.`); continue; }
          if (new RegExp(`^${escapeRegExp(dsvc)}:\\d+$`).test(v)) {
            cenv[k] = `{{ .Databases.${d.name}.Host }}:{{ .Databases.${d.name}.Port }}`;
            say("converted", `\`${k}\` as the database's address and port.`);
            continue;
          }
          if (d.kind === "postgres" && /:\/\/[^@/]*:[^@/]*@/.test(v)) {
            secrets[k] = `databases.${d.name}.url`;
            say("converted", `\`${k}\`, the database's whole address with its password, as \`databases.${d.name}.url\`, which Roostack makes from the password it keeps.`);
            continue;
          }
          if (d.kind === "redis") {
            cenv[k] = v.replace(new RegExp(`(://(?:[^@/]*@)?)${escapeRegExp(dsvc)}`), `$1{{ .Databases.${d.name}.Host }}`);
            say("converted", `\`${k}\`, with the Redis database's address.`);
            continue;
          }
          say("overToYou", `\`${k}\` is the database's address with its password in it: Roostack can only make that for Postgres. Check whether the app takes the parts separately instead.`);
          cenv[k] = v;
          continue;
        }
        if (factHit) {
          const [dsvc, f] = factHit, d = dbs[dsvc];
          if (f.password && v === f.password && /PASS/.test(K)) {
            secrets[k] = `databases.${d.name}.password`;
            say("converted", `\`${k}\` as the password Roostack makes for the database.`);
          } else if (f.user && v === f.user && /USER/.test(K)) {
            cenv[k] = `{{ .Databases.${d.name}.User }}`;
            say("converted", `\`${k}\` as the database's user.`);
          } else {
            cenv[k] = `{{ .Databases.${d.name}.Name }}`;
            say("converted", `\`${k}\` as the database's name.`);
          }
          continue;
        }
        if (/PORT$/.test(K) && Object.values(dbFacts).some((f) => f.port === v) && Object.keys(dbs).length) {
          const d = Object.values(dbs).find((x) => dbFacts[Object.keys(dbs).find((n) => dbs[n] === x)].port === v);
          cenv[k] = `{{ .Databases.${d.name}.Port }}`;
          continue;
        }
        // Another of the app's containers.
        const other = apps.find(([o]) => o !== svc && (v === o || new RegExp(`^[a-z]+://${escapeRegExp(o)}([:/]|$)`, "i").test(v) || new RegExp(`^${escapeRegExp(o)}:\\d+$`).test(v)));
        if (other) {
          const host = `{{ (index .Containers "${containerName[other[0]]}").Host }}`;
          cenv[k] = v === other[0] ? host : v.replace(other[0], host);
          say("converted", `\`${k}\`, with the address of the container \`${containerName[other[0]]}\`.`);
          continue;
        }
        if (K === "TZ") { say("notNeeded", "`TZ`: containers already run in the server's time zone."); continue; }
        if (/TIME_?ZONE$/.test(K)) { cenv[k] = "{{ .Lab.TimeZone }}"; say("converted", `\`${k}\` as the server's time zone, \`{{ .Lab.TimeZone }}\`.`); continue; }
        if (userIDs.test(K)) {
          say("notNeeded", `\`${k}\`: Roostack runs the app as its own user. If the app's files must belong to a user inside the container, add \`UserNS=keep-id:uid=…,gid=…\` under its Quadlet lines.`);
          continue;
        }
        if (ownURLName.test(K) || /^https?:\/\/(localhost|127\.0\.0\.1|0\.0\.0\.0|example\.(com|org)|[^/]*\.example\.com)(:\d+)?\/?$/.test(v)) {
          const hostOnly = /(DOMAIN|HOSTNAME)$/.test(K) && !/^https?:/.test(v);
          cenv[k] = hostOnly ? "{{ .Routes.web.Host }}" : "{{ .Routes.web.URL }}";
          say("converted", `\`${k}\` as the service's own address, \`${cenv[k]}\`.`);
          continue;
        }
        if (secretName.test(K) && !/_FILE$/.test(K)) {
          const nm = named(k, v);
          const key = setting(adminSecret.test(K)
            ? { key: nm.key, title: nm.title, type: "secret", min_length: 8, install_only: true }
            : { key: nm.key, title: nm.title, type: "secret", generate: true }, v.startsWith("${") ? ` was ${v}` : ` was ${k}`);
          secrets[k] = key;
          say("converted", adminSecret.test(K)
            ? `\`${k}\` as a secret asked for at install (\`${key}\`): it looks like the first administrator's password, which Roostack deletes once setup has run. It only works if the app reads it on its first start; if not, a setup step (\`hooks\`) can make the account.`
            : `\`${k}\` as a secret Roostack generates, \`${key}\`${v && !v.startsWith("${") ? " (the Compose file's own value is left out)" : ""}.`);
          continue;
        }
        const unresolved = /\$\{([A-Za-z_][A-Za-z0-9_]*)\}/.exec(v);
        if (unresolved && v === unresolved[0]) {
          const nm = named(k, v);
          const key = setting({ key: nm.key, title: nm.title, type: "string", required: true }, ` was ${v}`);
          cenv[k] = `{{ .Settings.${key} }}`;
          say("converted", `\`${k}\` as a setting people fill in at install, \`${key}\` (it was \`${v}\`, with no default).`);
          continue;
        }
        cenv[k] = v;
      }
      if (Object.keys(cenv).length) c.env = cenv;
      if (Object.keys(secrets).length) c.secrets = secrets;

      // Its folders.
      const mounts = [];
      for (const vol of [].concat(s.volumes || [])) {
        const m = volumeOf(vol, I);
        if (!m) continue;
        const { source, target, ro, type } = m;
        if (type === "tmpfs") { ql("Tmpfs", target); say("converted", `the tmpfs at \`${target}\`, as the Quadlet line \`Tmpfs\`.`); continue; }
        if (/^\/(var\/)?run\/(docker|podman)[^/]*\.sock$|docker\.sock$/.test(source)) {
          say("notAllowed", `\`${source}\`: it would give the app control of every container on the server. Roostack never mounts it; if the app needs it for something, that part won't work.`);
          continue;
        }
        if (/^\/etc\/(localtime|timezone)$/.test(source)) { say("notNeeded", `\`${source}\`: containers already run in the server's time zone.`); continue; }
        if (/^\/(proc|sys|dev|boot|root|etc)(\/|$)|^\/$/.test(source)) {
          say("notAllowed", `\`${source}\`: a package can't reach the server's own files. If the app needs what's there, it can't run in Roostack yet.`);
          continue;
        }
        const absolute = source.startsWith("/");
        if (absolute && (ro || /^\/(media|mnt|music|movies|tv|photos|pictures|videos|books|data\/media)/.test(source)) && /media|mnt|music|movie|tv|photo|picture|video|book|librar/.test(lower(source + target))) {
          mounts.push({ libraries: true, path: target });
          say("converted", `\`${source}\` as the media libraries the Owner chooses for the service, under \`${target}\` (read-only, never backed up).`);
          continue;
        }
        if (source === "") { ql("Volume", target); say("converted", `the anonymous volume at \`${target}\`, as the Quadlet line \`Volume\` (it isn't kept: use storage for what should be).`); continue; }
        const sname = storageName(absolute || /^[.~]?\/?$|^\.\/?$/.test(source) ? target : source, target);
        const guess = storageGuess(target, sname);
        if (!pkg.storage[sname]) {
          pkg.storage[sname] = { title: guess.title, class: guess.class };
          if (guess.media) pkg.storage[sname].location = "media";
          comments.push([["storage", sname, "title"], " check: what it holds, for people"]);
        }
        const mount = { storage: sname, path: target };
        if (ro) mount.readonly = true;
        mounts.push(mount);
        say("converted", absolute
          ? `\`${source}\`, a folder on the server, as the service's own storage \`${sname}\` (${guess.class === "critical" ? "backed up" : "not backed up"}): Roostack keeps each service's data in its own folders.`
          : `\`${source}\` as the storage \`${sname}\`, ${guess.class === "critical" ? "backed up" : "not backed up (it looks like a cache)"}${guess.media ? ", on the media disk" : ""}.`);
      }
      if (mounts.length) c.mounts = mounts;

      // Its devices.
      const devices = [];
      for (const d of [].concat(s.devices || [])) {
        const src = String(typeof d === "object" ? d.source || "" : d).split(":")[0];
        if (src.startsWith("/dev/dri")) {
          if (!devices.length) devices.push({ gpu: "intel" });
          say("converted", "`/dev/dri` as an Intel GPU (`devices: [{gpu: intel}]`).");
        } else say("notAllowed", `the device \`${src}\`: Roostack gives containers Intel GPUs only, for now.`);
      }
      if (devices.length) c.devices = devices;

      // Its health check.
      const h = s.healthcheck;
      if (h && !h.disable && !(Array.isArray(h.test) && h.test[0] === "NONE")) {
        const test = Array.isArray(h.test) ? (h.test[0] === "CMD-SHELL" ? h.test.slice(1).join(" ") : h.test[0] === "CMD" ? h.test.slice(1).map((a) => (/\s/.test(a) ? `"${a}"` : a)).join(" ") : h.test.join(" ")) : String(h.test || "");
        const url = /https?:\/\/(?:localhost|127\.0\.0\.1|0\.0\.0\.0)(?::(\d+))?(\/[^\s"'|;&]*)?/.exec(test);
        if (url && /curl|wget/.test(test)) {
          c.health = { http: { port: Number(url[1] || 80), path: url[2] || "/" } };
          say("converted", `\`${svc}\`'s health check, as asking \`${url[2] || "/"}\` on port ${url[1] || 80}.`);
        } else if (test) {
          c.health = { cmd: I(test) };
          say("converted", `\`${svc}\`'s health check, as the command \`${I(test)}\`.`);
        }
        for (const [k, q] of [["interval", "HealthInterval"], ["timeout", "HealthTimeout"], ["start_period", "HealthStartPeriod"]]) if (h[k]) ql(q, String(h[k]));
        if (h.retries) ql("HealthRetries", Number(h.retries));
      }

      // What it starts after.
      const deps = Array.isArray(s.depends_on) ? s.depends_on : Object.keys(s.depends_on || {});
      if (deps.length) {
        c.depends_on = deps.map((d) => (dbs[d] ? `databases.${dbs[d].name}` : containerName[d] ? `containers.${containerName[d]}` : null)).filter(Boolean);
        if (!c.depends_on.length) delete c.depends_on;
        say("converted", `\`depends_on\`, as what each container starts after.`);
        if (!Array.isArray(s.depends_on)) say("notNeeded", "`depends_on`'s conditions: Roostack starts what a container depends on first, and waits for databases.");
      }

      // Its user.
      if (s.user !== undefined) {
        const u = String(I(s.user));
        if (/^(root|0)(:|$)/.test(u)) say("notAllowed", `\`user: ${u}\`: containers don't run as root. The image's own user is used.`);
        else {
          ql("User", u);
          const [uid, gid] = u.split(":");
          if (/^\d+$/.test(uid)) ql("UserNS", `keep-id:uid=${uid},gid=${/^\d+$/.test(gid || "") ? gid : uid}`);
          say("converted", `\`user: ${u}\`, as the Quadlet lines \`User\` and \`UserNS=keep-id\`, so the app's files belong to Roostack's user on the server.`);
        }
      }

      // Memory: a limit or reservation is the best guess there is.
      const limit = s.mem_limit || s.mem_reservation || (s.deploy && s.deploy.resources && ((s.deploy.resources.limits || {}).memory || (s.deploy.resources.reservations || {}).memory));
      if (limit && memoryOf(limit)) {
        c.memory = memoryOf(limit);
        comments.push([at("memory"), " the Compose file's limit: measure what it really uses"]);
        say("converted", `\`${svc}\`'s memory limit (${limit}), as its memory estimate, \`${c.memory}\`. Measure what it really uses once it runs.`);
      } else say("overToYou", `Say how much memory \`${cname}\` typically uses (\`memory\`): measure it once it runs.`);

      // Quadlet lines from the rest.
      if (s.shm_size) { ql("ShmSize", String(s.shm_size)); say("converted", "`shm_size`, as the Quadlet line `ShmSize`."); }
      if (s.read_only) { ql("ReadOnly", true); say("converted", "`read_only`, as the Quadlet line `ReadOnly`."); }
      if (s.init) { ql("RunInit", true); say("converted", "`init`, as the Quadlet line `RunInit`."); }
      if (s.stop_signal) ql("StopSignal", String(s.stop_signal));
      if (s.stop_grace_period && seconds(s.stop_grace_period) !== null) ql("StopTimeout", seconds(s.stop_grace_period));
      if (s.working_dir) ql("WorkingDir", I(s.working_dir));
      if (s.hostname) ql("HostName", I(s.hostname));
      if (s.pids_limit) ql("PidsLimit", Number(s.pids_limit));
      for (const cap of [].concat(s.cap_drop || [])) ql("DropCapability", String(cap));
      for (const t of [].concat(s.tmpfs || [])) ql("Tmpfs", String(t));
      for (const hst of [].concat(s.extra_hosts || [])) {
        if (/host-gateway/.test(hst)) { say("notAllowed", `\`extra_hosts: ${hst}\`: a container doesn't reach the server itself.`); continue; }
        ql("AddHost", String(hst));
      }
      for (const d of [].concat(s.dns || [])) ql("DNS", String(d));
      const sysctls = Array.isArray(s.sysctls) ? s.sysctls : Object.entries(s.sysctls || {}).map(([k, v]) => `${k}=${v}`);
      for (const x of sysctls) ql("Sysctl", String(x));
      for (const [k, v] of Object.entries(s.ulimits || {})) ql("Ulimit", typeof v === "object" ? `${k}=${v.soft}:${v.hard}` : `${k}=${v}`);
      for (const o of [].concat(s.security_opt || [])) {
        if (/^no-new-privileges(:true)?$/.test(o)) ql("NoNewPrivileges", true);
        else say("notAllowed", `\`security_opt: ${o}\`: containers keep Podman's protections.`);
      }

      // What Roostack doesn't allow, or does itself.
      if (s.privileged) say("notAllowed", `\`${svc}\`'s \`privileged\`: it would give the container the whole server. Containers keep Podman's protections; if the app needs this, it can't run in Roostack yet.`);
      if (s.cap_add) say("notAllowed", `\`cap_add\` (${[].concat(s.cap_add).join(", ")}): containers keep Podman's default capabilities.`);
      if (s.network_mode) say("notAllowed", `\`network_mode: ${s.network_mode}\`: each service has its own network. An app that needs the server's network (to find devices at home) can't run in Roostack yet.`);
      if (s.pid || s.ipc || s.userns_mode) say("notAllowed", "`pid`, `ipc` or `userns_mode`: each container keeps its own.");
      if (s.group_add) say("notAllowed", "`group_add`: use `devices` for a GPU.");
      for (const k of ["restart", "container_name", "networks", "logging", "stdin_open", "tty", "platform", "pull_policy"]) {
        if (s[k] !== undefined) say("notNeeded", {
          restart: "`restart`: Roostack restarts services that stop.", container_name: "`container_name`: Roostack names the containers.",
          networks: "`networks`: each service has a network of its own, and its containers reach each other by name.", logging: "`logging`: Roostack keeps each container's log.",
          stdin_open: "`stdin_open` and `tty`: services run in the background.", tty: "`stdin_open` and `tty`: services run in the background.",
          platform: "`platform`: a package's image covers both processors Roostack runs on (amd64 and arm64).", pull_policy: "`pull_policy`: the image is pinned, so it's pulled once.",
        }[k]);
      }
      if (s.profiles) say("overToYou", `\`${svc}\` is in the profiles ${[].concat(s.profiles).join(", ")}: if it's optional, give it a \`when:\` and a setting that turns it on.`);
      for (const k of Object.keys(s)) if (!known.has(k) && !k.startsWith("x-")) say("overToYou", `\`${svc}\`'s \`${k}\` isn't something the import knows; it's left out.`);

      // Its ports: the first web port is the web address.
      const traefik = traefikPort(s.labels);
      const portList = [].concat(s.ports || []).map((p) => portOf(p, I)).filter(Boolean);
      if (traefik) portList.unshift({ target: traefik, published: null, protocol: "tcp", fromLabel: true });
      for (const e of [].concat(s.expose || [])) portList.push({ target: Number(String(e).split("/")[0]), published: null, protocol: "tcp", exposed: true });
      if (s.labels && Object.keys(labelMap(s.labels)).length) {
        say("notNeeded", traefik ? "Labels: Roostack's own web server gives the app its address and HTTPS (Traefik's port was read from them)." : "Labels: Roostack doesn't need them.");
      }
      const seen = new Set();
      for (const p of portList) {
        if (!p.target || seen.has(p.target + "/" + p.protocol)) continue;
        seen.add(p.target + "/" + p.protocol);
        const looksWeb = p.protocol === "tcp" && !nonWeb[p.target] && (webPorts.has(p.target) || p.fromLabel || (!p.exposed && p.target >= 1024));
        if (looksWeb && !firstRoute) {
          firstRoute = "web";
          pkg.routes.web = { container: cname, port: p.target, subdomain: safeName(name, "app").slice(0, 63), exposure: "private" };
          say("converted", `Port ${p.target}${p.published && p.published !== p.target ? ` (published as ${p.published})` : ""} as the web address \`web\`, at \`${pkg.routes.web.subdomain}.\`*your lab*. Roostack gives it HTTPS.`);
          if (p.target === 443) say("overToYou", "The web address is the app's port 443: Roostack gives the address HTTPS itself, so use the app's plain HTTP port if it has one.");
          continue;
        }
        if (looksWeb) {
          if (!p.exposed) say("overToYou", `Port ${p.target} looked like another web page: add a second web address under \`routes\` if people open it.`);
          continue;
        }
        if (p.exposed) continue;
        const pname = safeName(nonWeb[p.target] || `port-${p.target}`, "port");
        const host = p.published && p.published >= 1024 ? p.published : p.target >= 1024 ? p.target : p.target === 22 ? 2222 : 10000 + p.target;
        pkg.ports[pname] = { title: portTitles[nonWeb[p.target]] || `Port ${p.target}`, container: cname, port: p.target, host_port: host };
        if (p.protocol === "udp") pkg.ports[pname].protocol = "udp";
        say("converted", `Port ${p.target}${p.protocol === "udp" ? "/udp" : ""} as the extra connection \`${pname}\` on the server's port ${host}, off until an Admin turns it on (home network only).`);
      }

      if (Object.keys(quadlet).length) c.quadlet = { Container: quadlet };
      pkg.containers[cname] = c;
    }

    for (const n of missing) {
      if (!pkg.settings.some((s) => s.key === settingKey(n))) say("overToYou", `\`\${${n}}\` has no value: paste the app's .env into the .env box and import again, or fill it in.`);
    }
    for (const k of ["networks", "volumes", "configs", "secrets"]) {
      if (compose[k] && Object.keys(compose[k]).length) {
        if (k === "networks") say("notNeeded", "The top-level `networks`: each service has a network of its own.");
        else if (k === "volumes") say("notNeeded", "The top-level `volumes`: storage entries replace them.");
        else say("overToYou", `The top-level \`${k}\` aren't imported: give their values as settings (secrets for anything private).`);
      }
    }

    // Routes: what's safe once public, for the person to say.
    if (firstRoute) {
      pkg.routes.web.public = {};
      say("overToYou", "Say where people sign in (`login_paths`), so the address can be made public safely, or why it shouldn't be (`unsafe`).");
      for (const [cn, c] of Object.entries(pkg.containers)) {
        if (!c.health && pkg.routes.web.container === cn) say("overToYou", `Give \`${cn}\` a health check: an address that answers when the app works, such as \`/health\`.`);
      }
    } else say("overToYou", "No port looked like a web page: add a web address under `routes` if the app has one.");
    say("overToYou", "The facts about the project: who makes it, where its code is, its licence, logo and categories.");
    if (mainImage && /^ghcr\.io$/.test(mainImage.registry)) {
      pkg.about.source = `https://github.com/${mainImage.repo.split("/").slice(0, 2).join("/")}`;
      comments.push([["about", "source"], " a guess from the image's address: check it"]);
    }

    // Tidy: drop what's empty, keep the order the guide uses.
    for (const k of ["settings", "storage", "databases", "routes", "ports"]) {
      if (Array.isArray(pkg[k]) ? !pkg[k].length : !Object.keys(pkg[k]).length) delete pkg[k];
    }
    if (!Object.keys(pkg.about).length) delete pkg.about;

    const out = new YAML.Document(pkg);
    out.commentBefore = ` Imported from a Docker Compose file by Roostack's package editor${opts.date ? ", " + opts.date : ""}.\n Check every value; the editor lists what's left to do.`;
    for (const [p, comment, before] of comments) {
      const parent = out.getIn(p.slice(0, -1), true);
      const pair = parent && YAML.isMap(parent) && parent.items.find((it) => YAML.isScalar(it.key) && it.key.value === p[p.length - 1]);
      if (before && pair) { pair.key.commentBefore = comment; continue; }
      const node = out.getIn(p, true);
      if (node && YAML.isNode(node)) node.comment = comment;
      else if (pair && YAML.isScalar(pair.value)) pair.value.comment = comment;
    }
    for (const top of ["about", "requires", "settings", "containers", "storage", "databases", "routes", "ports"]) {
      const pair = out.contents.items.find((it) => it.key && it.key.value === top);
      if (pair && pair.key) pair.key.spaceBefore = true;
    }
    for (const m of out.getIn(["containers"], true).items) {
      const mounts = m.value && YAML.isMap(m.value) ? m.value.get("mounts", true) : null;
      if (mounts) for (const it of mounts.items) it.flow = true;
      const deps = m.value && YAML.isMap(m.value) ? m.value.get("depends_on", true) : null;
      if (deps) deps.flow = true;
    }

    const apps1 = apps.map(([n]) => `\`${n}\``), dbs1 = Object.keys(dbs).map((n) => `\`${n}\``);
    const summary = `${services.length === 1 ? "One service" : `${services.length} services`}: ${apps1.join(", ")} became ${apps.length === 1 ? "the container" : "the containers"} ${apps.map(([n]) => "`" + containerName[n] + "`").join(", ")}${dbs1.length ? `, and ${dbs1.join(", ")} ${dbs1.length === 1 ? "a database" : "databases"} Roostack runs for it and backs up` : ""}.`;
    return { yaml: out.toString({ flowCollectionPadding: false, lineWidth: 0 }), report, summary, name: pkg.name, version: pkg.version };
  }

  // ---------- Reading an old setup (SPEC.md §8.4) ----------

  // readSetup reads a Compose file (with its .env) as the old setup of a
  // service whose data is being brought into Roostack, in the shape
  // Roostack's importer.Setup takes: each service's image, mounts (host
  // path or named volume, and where in the container) and environment.
  // Values of variables named like secrets are masked here, so they never
  // leave the browser.
  const secretLike = /pass|secret|token|key|private|credential|auth|salt|cookie|jwt|dsn|_url$/i;
  function readSetup(text, opts) {
    opts = opts || {};
    const doc = YAML.parseDocument(String(text || ""), { merge: true, uniqueKeys: false });
    if (doc.errors.length) return { error: `That isn't a Compose file Roostack can read: ${doc.errors[0].message.split("\n")[0]}` };
    const compose = doc.toJS({ maxAliasCount: -1 }) || {};
    if (!compose || typeof compose !== "object" || !compose.services || typeof compose.services !== "object") {
      return { error: "That isn't a Compose file: it has no services:." };
    }
    const vars = parseEnvFile(opts.env);
    const missing = new Set();
    const I = (s) => interpolate(s, vars, missing);
    const project = compose.name ? I(compose.name) : "";
    const containers = [];
    for (const [name, s] of Object.entries(compose.services)) {
      if (!s || typeof s !== "object" || !s.image) continue;
      // An env_file (Immich's .env) gives the container every variable in
      // it; the .env pasted stands for it.
      const env = Object.assign(s.env_file ? Object.assign({}, vars) : {}, envMap(s.environment, I));
      for (const k of Object.keys(env)) if (secretLike.test(k) && env[k] !== "") env[k] = "•••";
      const mounts = [];
      for (const v of [].concat(s.volumes || [])) {
        const m = volumeOf(v, I);
        if (!m || !m.target) continue;
        if (m.type === "volume" && m.source) {
          // Docker keeps a named volume's files here, named after the project.
          mounts.push({ source: `/var/lib/docker/volumes/${project ? project + "_" : ""}${m.source}/_data`, target: m.target, volume: m.source, ro: m.ro });
        } else if (m.type === "bind") {
          mounts.push({ source: m.source, target: m.target, ro: m.ro });
        }
      }
      const nvidia = s.runtime === "nvidia" || /nvidia/.test(JSON.stringify(s.deploy || {}));
      containers.push({ name: s.container_name ? I(s.container_name) : name, service: name, image: I(s.image), mounts, env, nvidia });
    }
    if (!containers.length) return { error: "None of its services has an image Roostack can read." };
    return { setup: { from: "compose", project, containers }, missing: [...missing].sort() };
  }

  // envMap reads Compose's environment, a map or a list of KEY=VALUE.
  function envMap(e, I) {
    const out = {};
    if (Array.isArray(e)) {
      for (const item of e) {
        const s = String(item), i = s.indexOf("=");
        if (i < 0) out[s] = "";
        else out[s.slice(0, i)] = I(s.slice(i + 1));
      }
    } else if (e && typeof e === "object") {
      for (const [k, v] of Object.entries(e)) out[k] = v === null || v === undefined ? "" : I(String(v));
    }
    return out;
  }

  // volumeOf reads one of Compose's volumes, short or long syntax.
  function volumeOf(v, I) {
    if (typeof v === "string") {
      const parts = I(v).split(":");
      if (parts.length === 1) return { source: "", target: parts[0], ro: false, type: "volume" };
      const ro = parts.length > 2 && /(^|,)ro(,|$)/.test(parts[2]);
      return { source: parts[0], target: parts[1], ro, type: parts[0].startsWith("/") || parts[0].startsWith(".") || parts[0].startsWith("~") ? "bind" : "volume" };
    }
    if (v && typeof v === "object") {
      return { source: v.source ? I(v.source) : "", target: I(v.target || ""), ro: !!v.read_only, type: v.type || (v.source && /^[./~]/.test(v.source) ? "bind" : "volume") };
    }
    return null;
  }

  // portOf reads one of Compose's ports: "8080:80", "127.0.0.1:8080:80",
  // "80", "53:53/udp", or the long syntax.
  function portOf(p, I) {
    if (p && typeof p === "object") return { target: Number(p.target), published: p.published ? Number(String(p.published).split("-")[0]) : null, protocol: p.protocol || "tcp" };
    const s = I(String(p));
    const [spec, proto] = s.split("/");
    const parts = spec.split(":");
    const target = Number(parts[parts.length - 1].split("-")[0]);
    const published = parts.length > 1 ? Number(parts[parts.length - 2].split("-")[0]) : null;
    if (!target) return null;
    return { target, published: published || null, protocol: proto || "tcp" };
  }

  function labelMap(labels) {
    if (Array.isArray(labels)) return Object.fromEntries(labels.map((l) => { const s = String(l), i = s.indexOf("="); return i < 0 ? [s, ""] : [s.slice(0, i), s.slice(i + 1)]; }));
    return labels && typeof labels === "object" ? labels : {};
  }

  function traefikPort(labels) {
    for (const [k, v] of Object.entries(labelMap(labels))) {
      if (/^traefik\.http\.services\.[^.]+\.loadbalancer\.server\.port$/.test(k) && /^\d+$/.test(String(v))) return Number(v);
    }
    return null;
  }

  R.compose = { escapeRegExp, importCompose, readSetup, imageParts, memoryOf, interpolate, shellWords, storageGuess, safeName, versionFromTag };
})(globalThis.RoostackEditor = globalThis.RoostackEditor || {});
