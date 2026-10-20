// Suggestions: what the editor offers for each field, and that taking one
// gives a value the checks accept.
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const R = require("./load.js");

const base = `schema: roostack/v1alpha1
name: notes
title: Notes
description: ""
version: "2.0"
about:
  source: https://github.com/example/notes
  licence: AGPL-3.0
containers:
  app:
    image: ghcr.io/example/notes:2.0
    env:
      SECRET_KEY: hunter2
      TZ: Europe/London
      APP_URL: http://localhost:3000
    mounts:
      - { storage: cache, path: /app/cache }
      - { storage: uploads, path: /app/uploads }
storage:
  cache: { class: critical }
  uploads: { class: replaceable }
routes:
  web: { container: app, port: 3000, subdomain: notes }
settings:
  - { key: admin_password, type: string }
`;

function suggestions(text) {
  const r = R.check.run(text, { local: true });
  assert.ok(r.pkg, JSON.stringify(r.problems));
  return R.suggest.suggestions(r.pkg, { version: "0.9.3" });
}
const at = (list, path) => list.filter((s) => s.path === path);

// apply does what the editor does with an action.
function apply(text, action) {
  const doc = YAML.parseDocument(text);
  for (const op of action.ops) {
    if (op.op === "set") doc.setIn(op.path, op.value);
    else if (op.op === "delete") doc.deleteIn(op.path);
    else if (op.op === "append") {
      const n = doc.getIn(op.path, true);
      if (YAML.isSeq(n)) n.add(doc.createNode(op.value));
      else doc.setIn(op.path, [op.value]);
    }
  }
  return doc.toString();
}

test("each part of a package gets what it's missing", () => {
  const s = suggestions(base);
  const labels = (path) => at(s, path).flatMap((x) => x.actions.map((a) => a.label));
  assert.deepEqual(labels("about.licence"), ["Use AGPL-3.0-or-later", "Use AGPL-3.0-only"]);
  assert.deepEqual(labels("about.releases"), ["Use tags with v", "Use tags without v"]);
  assert.ok(labels("about.categories").includes("Add Documents and notes"));
  assert.deepEqual(labels("requires.roostack"), ["Use 0.9"]);
  assert.deepEqual(labels("version"), ["Use 2.0-roostack.1"]);
  assert.ok(labels("routes.web.public.login_paths").includes("Add /login (Audiobookshelf, HedgeDoc, Miniflux, Wiki.js)"));
  assert.deepEqual(labels("storage.cache.class"), ["Mark it replaceable"]);
  assert.deepEqual(labels("storage.uploads.class"), ["Mark it critical"]);
  assert.deepEqual(labels("containers.app.env.SECRET_KEY"), ["Make it a secret Roostack generates"]);
  assert.deepEqual(labels("containers.app.env.TZ"), ["Take it out"]);
  assert.deepEqual(labels("containers.app.env.APP_URL"), ["Use {{ .Routes.web.URL }}"]);
  assert.deepEqual(labels("containers.app.health"), ["Use /health on port 3000", "Use / on port 3000"]);
  assert.deepEqual(labels("settings.admin_password.type"), ["Make it a secret"]);
  assert.equal(at(s, "containers.app.image").length, 1);
  assert.equal(at(s, "containers.app.memory").length, 1);
  // A Django app's environment (SECRET_KEY) offers Django's sign-in path.
  assert.ok(labels("routes.web.public.login_paths").some((l) => l.startsWith("Add /accounts/login (Django)")));
});

test("every action gives a package that's still valid, and fixes what it's about", () => {
  for (const s of suggestions(base)) {
    for (const a of s.actions) {
      const text = apply(base, a);
      const r = R.check.run(text, { local: true });
      const bad = r.problems.filter((p) => p.kind === "validation").map((p) => p.message);
      assert.deepEqual(bad, [], `${s.path}: ${a.label}`);
      const again = R.suggest.suggestions(r.pkg, { version: "0.9.3" }).filter((x) => x.path === s.path && x.message === s.message);
      if (s.path !== "routes.web.public.login_paths" && s.path !== "about.categories") assert.equal(again.length, 0, `${s.path}: ${a.label} didn't take`);
    }
  }
});

test("an app the registry has is named, with its facts", () => {
  const s = suggestions(base.replace("ghcr.io/example/notes:2.0", "docker.io/vaultwarden/server:1.34.3").replace("licence: AGPL-3.0", "licence: \"\""));
  assert.match(at(s, "name")[0].message, /This is Vaultwarden/);
  assert.equal(at(s, "about.licence")[0].actions[0].label, "Use AGPL-3.0-only (Vaultwarden's)");
  assert.ok(at(s, "routes.web.public.login_paths")[0].actions[0].label.startsWith("Add /identity/connect/token"));
  assert.ok(at(s, "routes.web.public.private_paths").length);
});

test("the test packages from the registry need nothing much", () => {
  for (const ex of R.examples) {
    const s = suggestions(ex.text);
    const blocking = s.filter((x) => !["name", "about.releases", "about.support", "routes.web.max_body_size"].includes(x.path));
    assert.deepEqual(blocking.map((x) => x.path + ": " + x.message), [], ex.name);
  }
});

test("single sign-on: offered where the app could take it, and what an OIDC part still needs", () => {
  const oidc = suggestions(base.replace("      TZ: Europe/London\n", "      TZ: Europe/London\n      OIDC_CLIENT_ID: notes\n"));
  assert.equal(at(oidc, "sso").length, 1);
  assert.match(at(oidc, "sso")[0].message, /OIDC_CLIENT_ID suggests the app can sign people in through OIDC/);

  const walled = suggestions(base.replace("ghcr.io/example/notes:2.0", "docker.io/stirlingtools/stirling-pdf:3.2.0"));
  assert.deepEqual(at(walled, "sso").flatMap((x) => x.actions.map((a) => a.label)), ["Put it behind the lab's sign-in"]);

  const part = base + "sso:\n  oidc: { route: web, redirect_paths: [/auth/callback] }\n";
  const s = suggestions(part);
  assert.deepEqual(at(s, "sso.secrets").flatMap((x) => x.actions.map((a) => a.label)), ["Give it as OIDC_CLIENT_SECRET"]);
  assert.equal(at(s, "sso.env").length, 1);
  assert.equal(at(s, "sso.note").length, 1);
  for (const x of s.filter((x) => x.path.startsWith("sso")).concat(at(walled, "sso"))) {
    for (const a of x.actions) {
      const text = apply(x.path === "sso" ? base.replace("ghcr.io/example/notes:2.0", "docker.io/stirlingtools/stirling-pdf:3.2.0") : part, a);
      const r = R.check.run(text, { local: true });
      assert.deepEqual(r.problems.filter((p) => p.kind === "validation").map((p) => p.message), [], `${x.path}: ${a.label}`);
    }
  }
});

test("single sign-on's templates: .SSO's fields pass, others are named", () => {
  const text = base + "sso:\n  oidc: { route: web, redirect_paths: [/auth/callback] }\n  env:\n    app: { A: \"{{ .SSO.DiscoveryURL }}\", B: \"{{ .SSO.Nope }}\" }\n";
  const r = R.check.run(text, { local: true });
  const msgs = r.problems.map((p) => p.message);
  assert.ok(msgs.some((m) => /sso\.env\.app\.B: \.SSO has no Nope/.test(m)), msgs.join("\n"));
  assert.ok(!msgs.some((m) => /sso\.env\.app\.A/.test(m)), msgs.join("\n"));
});
