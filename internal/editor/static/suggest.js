// Roostack's package editor: suggestions (SPEC.md §8.3.7, "The package
// editor"). Each is something to consider, with what to do about it and,
// where there's an obvious value, actions that put it in: a login path,
// a storage class, a licence that says -only or -or-later, a category, a
// health check. Suggestions never block anything; the checks (check.js)
// say what Roostack or the registry would refuse.
(function (R) {
  "use strict";

  const S = R.schema;

  // What the service registry's packages use (roostack/services,
  // 2026-10-20), by image: for apps packaged again, and as examples. It
  // only suggests; the registry's own package is the one to start from.
  // sso is how the app takes its OIDC settings (docs/SSO-RESEARCH.md):
  // command (its admin command), api (only its API), json (a JSON value
  // holding the secret).
  const known = {
    "actualbudget/actual-server": { app: "Actual Budget", licence: "MIT", categories: ["productivity"], login: ["/account/login", "/account/bootstrap"] },
    "advplyr/audiobookshelf": { app: "Audiobookshelf", licence: "GPL-3.0-only", categories: ["media"], login: ["/login"], sso: "api" },
    "forgejo/forgejo": { app: "Forgejo", licence: "GPL-3.0-or-later", categories: ["development"], login: ["/user/login", "/user/sign_up"], private: ["/admin"], sso: "command" },
    "library/ghost": { app: "Ghost", licence: "MIT", categories: ["publishing"], login: ["/ghost/api/admin/session", "/ghost/api/admin/authentication"] },
    "gitea/gitea": { app: "Gitea", licence: "MIT", categories: ["development"], login: ["/user/login", "/user/sign_up"], private: ["/admin"], sso: "command" },
    "hedgedoc/hedgedoc": { app: "HedgeDoc", licence: "AGPL-3.0-only", categories: ["documents"], login: ["/login", "/register"] },
    "immich-app/immich-server": { app: "Immich", licence: "AGPL-3.0-only", categories: ["photos"], login: ["/api/auth/login"], sso: "api" },
    "jellyfin/jellyfin": { app: "Jellyfin", licence: "GPL-2.0-only", categories: ["media"], login: ["/Users/AuthenticateByName"] },
    "linkwarden/linkwarden": { app: "Linkwarden", licence: "AGPL-3.0-only", categories: ["documents"], login: ["/api/v1/auth/callback/credentials", "/api/v1/users"] },
    "lycheeorg/lychee": { app: "Lychee", licence: "MIT", categories: ["photos"], login: ["/api/v2/Auth::login"], private: ["/install"] },
    "mealie-recipes/mealie": { app: "Mealie", licence: "AGPL-3.0-only", categories: ["home"], login: ["/api/auth/token"], private: ["/admin", "/api/admin"] },
    "miniflux/miniflux": { app: "Miniflux", licence: "Apache-2.0", categories: ["productivity"], login: ["/login"] },
    "deluan/navidrome": { app: "Navidrome", licence: "GPL-3.0-only", categories: ["media"], login: ["/auth/login"] },
    "binwiederhier/ntfy": { app: "ntfy", licence: "Apache-2.0 OR GPL-2.0-only", categories: ["productivity"], login: ["/v1/account/token"] },
    "paperless-ngx/paperless-ngx": { app: "Paperless-ngx", licence: "GPL-3.0-only", categories: ["documents"], login: ["/accounts/login", "/api/token"], private: ["/admin"], sso: "json" },
    "papra-hq/papra": { app: "Papra", licence: "AGPL-3.0-or-later", categories: ["documents"], login: ["/api/auth/sign-in/email", "/api/auth/sign-up/email"], sso: "json" },
    "photoprism/photoprism": { app: "PhotoPrism", licence: "AGPL-3.0-only", categories: ["photos"], login: ["/api/v1/session"] },
    "plexinc/pms-docker": { app: "Plex", licence: "closed", categories: ["media"], login: ["/myplex"] },
    "kozea/radicale": { app: "Radicale", licence: "GPL-3.0-only", categories: ["productivity"], unsafe: true },
    "stirlingtools/stirling-pdf": { app: "Stirling-PDF", licence: "MIT", categories: ["documents"], unsafe: true },
    "louislam/uptime-kuma": { app: "Uptime Kuma", licence: "MIT", categories: ["network"], unsafe: true },
    "vaultwarden/server": { app: "Vaultwarden", licence: "AGPL-3.0-only", categories: ["passwords"], login: ["/identity/connect/token", "/identity/accounts/prelogin"], private: ["/admin"] },
    "requarks/wiki": { app: "Wiki.js", licence: "AGPL-3.0-only", categories: ["documents"], login: ["/login"], sso: "api" },
  };

  // Where people sign in, by the framework an app is built with, which its
  // environment often gives away; then the paths registry apps use most.
  const frameworks = [
    { test: /^DJANGO_|PAPERLESS_|^SECRET_KEY$/, name: "Django", login: ["/accounts/login", "/admin/login"], private: ["/admin"] },
    { test: /^RAILS_|^SECRET_KEY_BASE$/, name: "Rails", login: ["/users/sign_in"], private: ["/admin"] },
    { test: /^APP_KEY$|^LARAVEL/, name: "Laravel", login: ["/login"] },
    { test: /^NEXTAUTH_|^AUTH_SECRET$/, name: "NextAuth", login: ["/api/auth/callback/credentials"] },
    { test: /^BETTER_AUTH_/, name: "Better Auth", login: ["/api/auth/sign-in/email", "/api/auth/sign-up/email"] },
    { test: /^WORDPRESS_/, name: "WordPress", login: ["/wp-login.php"], private: ["/wp-admin"] },
  ];
  const commonLogins = [
    ["/login", "Audiobookshelf, HedgeDoc, Miniflux, Wiki.js"], ["/api/auth/login", "Immich"], ["/user/login", "Forgejo, Gitea"],
    ["/accounts/login", "Paperless-ngx (Django)"], ["/auth/login", "Navidrome"], ["/api/v1/session", "PhotoPrism"],
  ];

  // Category hints in names, descriptions and images.
  const categoryHints = [
    ["photos", /photo|gallery|picture|immich|image|album|camera roll/],
    ["media", /jellyfin|plex|emby|music|audio|video|movie|film|\btv\b|podcast|stream|media|audiobook|navidrome|radio/],
    ["documents", /document|paperless|\bdocs?\b|note|wiki|pdf|book(?!mark)|ebook|markdown|writing|scan/],
    ["productivity", /task|todo|kanban|calendar|plan|project|budget|finance|bookmark|feed|rss|read|time track|contact|notif/],
    ["home", /home ?assistant|smart home|camera|nvr|mqtt|zigbee|frigate|recipe|grocer|inventory|household/],
    ["passwords", /password|vault|bitwarden|\bauth|2fa|otp|secret|identity|sso/],
    ["development", /\bgit\b|gitea|forge|\bcode\b|\bci\b|registry|devops|ide\b|developer|snippet/],
    ["publishing", /blog|ghost|wordpress|website|\bcms\b|static site|newsletter|forum/],
    ["network", /\bdns\b|vpn|proxy|adguard|pi-?hole|wireguard|uptime|monitor|speedtest|network|firewall/],
  ];

  const isNil = (x) => x === null || x === undefined;
  const sorted = (m) => Object.keys(m || {}).sort();
  const imageKey = (img) => {
    const p = R.compose.imageParts(img || "");
    return p.repo;
  };

  // A suggestion: the field it's about, what to consider, and actions
  // that put a value in (ops: set, append, delete, at a path).
  const make = (path, message, actions) => ({ kind: "suggestion", path, message, actions: actions || [] });
  const set = (label, path, value) => ({ label, ops: [{ op: "set", path, value }] });
  const append = (label, path, value) => ({ label, ops: [{ op: "append", path, value }] });

  // suggestions looks at a decoded package (check.run's pkg) and says what
  // to consider. ctx: { version } (the Roostack the editor came with).
  function suggestions(pkg, ctx) {
    ctx = ctx || {};
    if (!pkg) return [];
    const out = [];
    const add = (s) => out.push(s);
    const containers = pkg.containers || {};
    const firstImage = Object.values(containers).map((c) => c && c.image).find(Boolean) || "";
    const app = known[imageKey(firstImage)];
    const words = [pkg.name, pkg.title, pkg.description, ...(pkg.about.keywords || []), ...Object.values(containers).map((c) => (c && c.image) || "")].join(" ").toLowerCase();
    const envNames = Object.values(containers).flatMap((c) => Object.keys((c && c.env) || {}).concat(Object.keys((c && c.secrets) || {})));

    if (app) {
      add(make("name", `This is ${app.app}, which the service registry has a package for already. To change it, start from the registry's package (it's checked and kept up to date), or give yours another name: a package of your own can't share the registry's.`));
    }

    // Name and version.
    if (pkg.description === "") add(make("description", "Say what the app does, in one line, for the list of services: what people get, not how it works."));
    if (pkg.version && !/-roostack\.\d+$/.test(pkg.version)) {
      add(make("version", "A package's version is the app's own, then -roostack. and the package's revision.", [set(`Use ${pkg.version}-roostack.1`, ["version"], pkg.version + "-roostack.1")]));
    }

    // The project.
    const a = pkg.about;
    const lic = a.licence;
    const gnu = /^(A?GPL|LGPL|GFDL)-\d\.\d\+?$/.exec(lic);
    if (gnu) {
      const base = lic.replace(/\+$/, "");
      add(make("about.licence", `${lic} doesn't say whether later versions apply. Read the project's licence notice: "or (at your option) any later version" means -or-later; without it, -only.`,
        [set(`Use ${base}-or-later`, ["about", "licence"], base + "-or-later"), set(`Use ${base}-only`, ["about", "licence"], base + "-only")]));
    } else if (lic === "") {
      const acts = [];
      if (app) acts.push(set(`Use ${app.licence} (${app.app}'s)`, ["about", "licence"], app.licence));
      for (const l of ["AGPL-3.0-only", "GPL-3.0-or-later", "MIT", "Apache-2.0"]) if (!app || app.licence !== l) acts.push(set(`Use ${l}`, ["about", "licence"], l));
      add(make("about.licence", "Take the licence from the project's own LICENSE file or notice, as its SPDX identifier. Closed source, with only an image to run? Use closed, and give the website instead.", acts));
    }
    const src = /^https:\/\/(github\.com|codeberg\.org|gitlab\.com)\/([^/]+)\/([^/#?]+)/.exec(a.source);
    if (src && a.releases === "") {
      const base = a.source.replace(/\/+$/, "").replace(/\.git$/, "");
      const rel = src[1] === "gitlab.com" ? "/-/releases/" : "/releases/tag/";
      add(make("about.releases", "Where the project says what's new in each version: a service's update will link to it. Check whether the project's tags start with v.",
        [set("Use tags with v", ["about", "releases"], `${base}${rel}v{version}`), set("Use tags without v", ["about", "releases"], `${base}${rel}{version}`)]));
    }
    if (src && src[1] === "github.com" && a.support === "") {
      add(make("about.support", `If the project takes donations, link its page: often GitHub Sponsors (https://github.com/sponsors/${src[2]}), Open Collective, or Liberapay. Check it exists first.`));
    }
    if (a.source === "" && a.licence !== S.words.licenceClosed) {
      const img = R.compose.imageParts(firstImage);
      if (img.registry === "ghcr.io") {
        const guess = `https://github.com/${img.repo.split("/").slice(0, 2).join("/")}`;
        add(make("about.source", "Where the app's code is published. Images on ghcr.io usually come from GitHub: check this is the project's own repository.", [set(`Use ${guess}`, ["about", "source"], guess)]));
      }
    }
    if ((a.categories || []).length === 0) {
      const hits = categoryHints.filter(([, rx]) => rx.test(words)).map(([k]) => k);
      const picks = (app ? app.categories : []).concat(hits).filter((k, i, xs) => xs.indexOf(k) === i).slice(0, 3);
      if (picks.length) {
        add(make("about.categories", "One to three, for the catalog's filters; suggested from the name, description and image.",
          picks.map((k) => append(`Add ${S.categories.find((c) => c.key === k).title}`, ["about", "categories"], k))));
      }
    }
    if (a.icon === "") add(make("about.icon", "Add the project's logo: a square PNG, at least 256×256 pixels and at most 100 KB, from the project's own site or repository, where its terms allow a catalog to show it."));
    if (pkg.requires.roostack === "" && ctx.version && /^\d+\.\d+/.test(ctx.version)) {
      const v = ctx.version.split(".").slice(0, 2).join(".");
      add(make("requires.roostack", "The oldest Roostack the package works with: the one you tried it on, unless you know better.", [set(`Use ${v}`, ["requires", "roostack"], v)]));
    }

    // Containers.
    for (const name of sorted(containers)) {
      const c = containers[name];
      if (!c) continue;
      const at = ["containers", name];
      if (c.image && !/@sha256:[0-9a-f]{64}$/.test(c.image)) {
        add(make(`containers.${name}.image`, `Pin it by its multi-architecture digest so that every lab runs the image you tried: run skopeo inspect --raw docker://${c.image.replace(/@.*$/, "")} | sha256sum (or docker buildx imagetools inspect, the Digest line), then add @sha256: and the result after the tag. Not podman image inspect: that's only your processor's build.`));
      }
      if (c.memory === "") {
        add(make(`containers.${name}.memory`, "Measure what it uses once it's running (podman stats as the roostack user, on a lab). As a rough start: a small Go or Rust app 30–100 MB, a Node or Python app 150–400 MB, Java 500 MB or more."));
      }
      const route = Object.entries(pkg.routes || {}).find(([, r]) => r && r.container === name);
      if (isNil(c.health) && route) {
        add(make(`containers.${name}.health`, `How Roostack will tell it's working: an address that answers when the app works. Many apps have one (/health, /api/health, /healthz, /api/status); / works for most.`,
          ["/health", "/"].map((p) => set(`Use ${p} on port ${route[1].port}`, at.concat("health"), { http: { port: route[1].port, path: p } }))));
      }
      for (const [k, v] of Object.entries(c.env || {})) {
        const K = k.toUpperCase();
        if (/(PASSWORD|PASSWD|SECRET|TOKEN|(^|_)KEY$|SALT|PRIVATE)/.test(K) && !/_FILE$/.test(K) && !String(v).includes("{{")) {
          const key = K.toLowerCase().replace(/[^a-z0-9_]+/g, "_").replace(/^[^a-z]+/, "") || "secret";
          add(make(`containers.${name}.env.${k}`, `${k} looks like a password or key. As a secret, Roostack will keep it as a Podman secret, never in a file, and can make it for you.`,
            [{ label: "Make it a secret Roostack will generate", ops: [
              { op: "append", path: ["settings"], value: { key, title: k.replace(/_/g, " ").toLowerCase().replace(/^./, (x) => x.toUpperCase()), type: "secret", generate: true } },
              { op: "delete", path: at.concat("env", k) },
              { op: "set", path: at.concat("secrets", k), value: key },
            ] }]));
        }
        if (K === "TZ") add(make(`containers.${name}.env.${k}`, "Containers already run in the server's time zone, so TZ isn't needed. An app with its own time-zone setting can use {{ .Lab.TimeZone }}.", [{ label: "Take it out", ops: [{ op: "delete", path: at.concat("env", k) }] }]));
        if (/^https?:\/\/(localhost|127\.0\.0\.1)(:\d+)?\/?$/.test(String(v)) && Object.keys(pkg.routes || {}).length) {
          const r = Object.keys(pkg.routes)[0];
          add(make(`containers.${name}.env.${k}`, `${v} looks like the app's own address. {{ .Routes.${r}.URL }} is the address people use, once the lab has one.`, [set(`Use {{ .Routes.${r}.URL }}`, at.concat("env", k), `{{ .Routes.${r}.URL }}`)]));
        }
        if (/^(PUID|PGID|UID|GID)$/.test(K)) add(make(`containers.${name}.env.${k}`, `${k} chooses which user the app runs as in images that support it. If its files must belong to that user, use the Quadlet line UserNS=keep-id:uid=…,gid=… (as Forgejo's package does).`));
      }
    }

    // Data.
    for (const name of sorted(pkg.storage)) {
      const s = pkg.storage[name];
      if (!s) continue;
      const mount = Object.values(containers).flatMap((c) => (c && c.mounts) || []).find((m) => m.storage === name);
      const guess = R.compose.storageGuess(name + " " + s.title, mount ? mount.path : "");
      const at = ["storage", name];
      if (s.title === "") add(make(`storage.${name}.title`, "Say what it holds, in a few words for people: the summary before installing reads \"<title>: backed up\".", [set(`Use "${guess.title}"`, at.concat("title"), guess.title)]));
      if (s.class === "critical" && guess.class === "replaceable") {
        add(make(`storage.${name}.class`, `"${name}" sounds like something the app makes again (a cache, thumbnails, converted files). If nobody would miss it, it needn't be backed up.`, [set("Mark it replaceable", at.concat("class"), "replaceable")]));
      }
      if (s.class === "replaceable" && guess.class === "critical" && guess.title !== "Its data") {
        add(make(`storage.${name}.class`, `"${name}" sounds like something people would miss (${guess.title.toLowerCase()}). Anything people would miss must be critical so that it's backed up.`, [set("Mark it critical", at.concat("class"), "critical")]));
      }
      if (s.location !== "media" && guess.media) {
        add(make(`storage.${name}.location`, "Large data such as photos, videos, and uploads can go on the media disk, if the server has one.", [set("Put it on the media disk", at.concat("location"), "media")]));
      }
    }

    // Web addresses.
    for (const name of sorted(pkg.routes)) {
      const r = pkg.routes[name];
      if (!r) continue;
      const at = ["routes", name, "public"];
      const pub = r.public;
      if ((pub.login_paths || []).length === 0 && pub.unsafe === "") {
        const acts = [];
        const seen = new Set();
        const offer = (p, why) => { if (!seen.has(p)) { seen.add(p); acts.push(append(`Add ${p}${why ? " (" + why + ")" : ""}`, at.concat("login_paths"), p)); } };
        if (app && app.login) app.login.forEach((p) => offer(p, app.app + "'s"));
        for (const f of frameworks) if (envNames.some((n) => f.test.test(n))) f.login.forEach((p) => offer(p, f.name));
        for (const [p, who] of commonLogins) offer(p, who);
        const unsafe = app && app.unsafe ? `${app.app}'s registry package says it shouldn't be public. ` : "";
        add(make(`routes.${name}.public.login_paths`, `${unsafe}Name the addresses that check a password: outside visitors will get a strict limit on tries there. To be sure, open the app's sign-in page with the browser's developer tools open (Network), sign in, and note where the form is sent. If the address shouldn't be public at all (an API with no login), say why under unsafe instead.`, acts.slice(0, 7)));
      }
      const privs = (pub.private_paths || []).map((p) => p.path);
      const wantPrivate = (app && app.private) || frameworks.filter((f) => f.private && envNames.some((n) => f.test.test(n))).flatMap((f) => f.private);
      for (const p of wantPrivate || []) {
        if (!privs.includes(p)) add(make(`routes.${name}.public.private_paths`, `Keep ${p} home-only? It's usually the app's administration, which changes every account.`,
          [append(`Keep ${p} home-only`, at.concat("private_paths"), { path: p, reason: "The app's administration, which changes every account." })]));
      }
      if (r.max_body_size === "" && /photo|video|upload|document|file|media|book|music/.test(words)) {
        add(make(`routes.${name}.max_body_size`, "People upload files to this app: Roostack's web server will take requests of any size unless told otherwise, so set the largest the app accepts if it has a limit.", [set("Use 100MB", ["routes", name, "max_body_size"], "100MB")]));
      }
    }

    // Single sign-on (SPEC.md §5.2): whether the app could take it, and
    // what an OIDC part still needs.
    const sso = pkg.sso;
    const firstRoute = sorted(pkg.routes)[0];
    const firstContainer = sorted(containers)[0];
    const oidcEnv = envNames.find((n) => /OIDC|OAUTH|OPENID/i.test(n));
    const ssoHooks = (pkg.hooks || []).filter((h) => h && h.on === "sso");
    const how = {
      command: (a) => `${a} takes its OIDC settings from its admin command: an sso part (oidc), and an on: sso hook that will run the command when single sign-on is turned on, changed, or off (ROOSTACK_SSO=on or off), safe to run again.`,
      api: (a) => `${a} takes its OIDC settings only through its API: an sso part (oidc), and an on: sso hook that will call it with a key that ${a} will make at install and Roostack will keep (a from_hook secret setting, an install hook that saves it, and an action that makes it again for older installs).`,
      json: (a) => `${a} takes its OIDC provider as a JSON value holding the client secret: an sso part (oidc) with that variable in sso.secret_env, a template using {{ json .SSO.ClientSecret }}, which Roostack will make as a Podman secret.`,
    };
    if (isNil(sso) && firstRoute && app && how[app.sso]) {
      add(make("sso", how[app.sso](app.app)));
    } else if (isNil(sso) && firstRoute) {
      if (oidcEnv) {
        // No action: only the app's own settings or docs say its callback
        // path, and the part isn't valid without one.
        add(make("sso", `${oidcEnv} suggests the app can sign people in through OIDC. With a single sign-on part (oidc, on the ${firstRoute} address), the Owner can let people use the lab's own sign-in for it: give the app's callback path, which its OIDC settings or docs name, then its client ID, discovery address and secret.`));
      } else if (app && app.unsafe) {
        add(make("sso", `${app.app} has no sign-in of its own. Behind the lab's sign-in wall (forward_auth), everyone will sign in on the lab's page before reaching it once the Owner turns single sign-on on.`,
          [set("Put it behind the lab's sign-in", ["sso"], { forward_auth: { route: firstRoute } })]));
      }
    }
    if (!isNil(sso) && !isNil(sso.oidc)) {
      // The secret and the app's settings reach it as env, inside a value
      // (secret_env), through its sso hook, or in a config file.
      const hookGetsSecret = ssoHooks.some((h) => Object.values(h.secrets || {}).includes(S.lists.ssoClientSecret[0]));
      const configs = Object.values(pkg.config_files || {}).map((f) => (f ? String(f.content) : ""));
      const configGetsSecret = configs.some((c) => c.includes(".SSO.ClientSecret"));
      if (Object.keys(sso.secrets || {}).length === 0 && Object.keys(sso.secret_env || {}).length === 0 && !hookGetsSecret && !configGetsSecret && firstContainer) {
        add(make("sso.secrets", `An OIDC app needs its client secret, which Roostack will make: give it to the container under the variable the app reads (often OIDC_CLIENT_SECRET), or to its on: sso hook if a command or its API sets the app.`,
          [set("Give it as OIDC_CLIENT_SECRET", ["sso", "secrets", firstContainer], { OIDC_CLIENT_SECRET: S.lists.ssoClientSecret[0] })]));
      }
      const usesSSO = [sso.env, sso.secret_env, ...ssoHooks.map((h) => ({ hook: h.env }))].some((byContainer) =>
        Object.values(byContainer || {}).some((m) => Object.values(m || {}).some((v) => String(v).includes(".SSO.")))) ||
        configs.some((c) => c.includes(".SSO."));
      if (app && app.sso === "api" && ssoHooks.length > 0 && !(pkg.settings || []).some((s) => s.from_hook)) {
        add(make("hooks", how.api(app.app)));
      }
      if (!usesSSO) {
        add(make("sso.env", "Tell the app where the lab's sign-in is, in the variables it reads while single sign-on is on: usually {{ .SSO.DiscoveryURL }} (or {{ .SSO.Issuer }}) and {{ .SSO.ClientID }}; {{ .SSO.AdminGroup }} if it can make the lab's Owners and Admins its admins, and {{ .SSO.ButtonText }} for its sign-in button."));
      }
      if (sso.note === "") add(make("sso.note", "Say what still uses the app's own sign-in, such as its phone apps or API tokens: it will be shown beside the switch."));
    }

    // Settings.
    for (const [i, s] of (pkg.settings || []).entries()) {
      if (s.from_hook && !(pkg.hooks || []).some((h) => h && h.on === "install" && h.saves === s.key)) {
        add(make(`settings.${s.key}.from_hook`, "Make it at install too, with an install hook that saves it: a new service will then need nobody to type the app's password, as the action does."));
      }
      if (s.type === "string" && /pass(word)?|secret|token|_key$/.test(s.key)) {
        add(make(`${s.key ? "settings." + s.key : `settings[${i}]`}.type`, "It looks like a password or key: as a secret, Roostack will keep it as a Podman secret, never in a file, and the install form will hide it.", [set("Make it a secret", ["settings", i, "type"], "secret")]));
      }
      if (s.type === "secret" && /admin.*pass|pass.*admin|initial/.test(s.key) && !s.install_only) {
        add(make(`${s.key ? "settings." + s.key : `settings[${i}]`}.install_only`, "The first administrator's password is needed only to set the app up: install_only will ask for it at install, and Roostack will delete it once setup has run.", [set("Ask for it only at install", ["settings", i, "install_only"], true)]));
      }
    }
    return out;
  }

  R.suggest = { suggestions, known };
})(globalThis.RoostackEditor = globalThis.RoostackEditor || {});
