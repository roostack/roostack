// The Compose import: what each kind of Compose file becomes, and that
// what it makes is a package `roostack validate -local` accepts (bar the
// registry's rules, which are the person's to finish).
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const R = require("./load.js");

const fixture = (name) => fs.readFileSync(path.join(__dirname, "compose", name + ".yaml"), "utf8");
const imported = (name, opts) => {
  const r = R.compose.importCompose(fixture(name), Object.assign({ version: "0.9.0" }, opts));
  assert.equal(r.error, undefined, r.error);
  r.pkg = YAML.parse(r.yaml);
  r.check = R.check.run(r.yaml, { local: true });
  return r;
};
const has = (list, re) => list.some((t) => re.test(t));

// Whatever comes out is valid: only registry errors remain.
function valid(r) {
  const bad = r.check.problems.filter((p) => p.kind === "validation");
  assert.deepEqual(bad.map((p) => p.message), []);
}

test("an app with Postgres: the database is Roostack's, the app's variables point at it", () => {
  const r = imported("vikunja");
  valid(r);
  const app = r.pkg.containers.app;
  assert.equal(r.pkg.name, "vikunja");
  assert.equal(r.pkg.version, "0.24.6-roostack.1");
  assert.deepEqual(r.pkg.databases, { main: { type: "postgres" } });
  assert.equal(app.env.VIKUNJA_DATABASE_HOST, "{{ .Databases.main.Host }}");
  assert.equal(app.env.VIKUNJA_DATABASE_USER, "{{ .Databases.main.User }}");
  assert.equal(app.env.VIKUNJA_DATABASE_DATABASE, "{{ .Databases.main.Name }}");
  assert.equal(app.secrets.VIKUNJA_DATABASE_PASSWORD, "databases.main.password");
  assert.equal(app.env.VIKUNJA_SERVICE_PUBLICURL, "{{ .Routes.web.URL }}");
  // ${JWT_SECRET} becomes a secret Roostack makes, named after the variable.
  assert.equal(app.secrets.VIKUNJA_SERVICE_JWTSECRET, "jwt_secret");
  assert.deepEqual(r.pkg.settings, [{ key: "jwt_secret", title: "Signing key", type: "secret", generate: true }]);
  assert.deepEqual(app.mounts, [{ storage: "files", path: "/app/vikunja/files" }]);
  assert.deepEqual(app.depends_on, ["databases.main"]);
  assert.deepEqual(r.pkg.routes.web, { container: "app", port: 3456, subdomain: "vikunja", exposure: "private", public: {} });
  // The Docker socket is refused, and says why.
  assert.ok(has(r.report.notAllowed, /docker\.sock.*control of every container/));
  assert.ok(has(r.report.notNeeded, /restart/));
  assert.ok(has(r.report.overToYou, /skopeo inspect --raw docker:\/\/docker\.io\/vikunja\/vikunja:0\.24\.6/));
  // A variable given a value isn't reported as missing.
  assert.ok(!has(r.report.overToYou, /JWT_SECRET/));
  // Comments say what to check, beside the values.
  assert.match(r.yaml, /# Pin by digest: skopeo/);
  assert.match(r.yaml, /key: jwt_secret # was \$\{JWT_SECRET\}/);
});

test("several containers, a custom Postgres, Redis, and an .env", () => {
  const env = "UPLOAD_LOCATION=./library\nDB_DATA_LOCATION=./postgres\nIMMICH_VERSION=v2.1.0\nDB_PASSWORD=postgres\nDB_USERNAME=postgres\nDB_DATABASE_NAME=immich\n";
  const r = imported("immich", { env });
  valid(r);
  assert.deepEqual(Object.keys(r.pkg.containers).sort(), ["machine-learning", "server"]);
  assert.equal(r.pkg.version, "2.1.0-roostack.1");
  assert.match(r.pkg.databases.main.image, /^ghcr\.io\/immich-app\/postgres:14-vectorchord.*@sha256:[0-9a-f]{64}$/);
  assert.deepEqual(r.pkg.databases.cache, { type: "redis", backup: false });
  const server = r.pkg.containers.server;
  assert.equal(server.secrets.DB_PASSWORD, "databases.main.password");
  assert.equal(server.env.DB_USERNAME, "{{ .Databases.main.User }}");
  // The Compose file's own variables stay out of the app's environment.
  for (const k of ["UPLOAD_LOCATION", "DB_DATA_LOCATION", "IMMICH_VERSION"]) assert.equal(server.env[k], undefined, k);
  assert.deepEqual(server.depends_on.sort(), ["databases.cache", "databases.main"]);
  assert.equal(r.pkg.storage.library.class, "critical");
  assert.equal(r.pkg.storage.library.location, "media");
  assert.equal(r.pkg.storage["model-cache"].class, "replaceable");
  assert.ok(has(r.report.notNeeded, /\/etc\/localtime/));
  assert.ok(has(r.report.converted, /from the \.env you pasted/));
});

test("an .env file it can't read is over to the person", () => {
  const r = imported("immich");
  assert.ok(has(r.report.overToYou, /reads `\.env`, which the editor can't open/));
  assert.ok(has(r.report.overToYou, /Give the app's version/), "release isn't a version");
});

test("LinuxServer.io's shape: user IDs, the time zone, media and a GPU", () => {
  const r = imported("linuxserver");
  valid(r);
  const app = r.pkg.containers.app;
  assert.equal(app.env.PUID, undefined);
  assert.equal(app.env.TZ, undefined);
  assert.equal(app.env.JELLYFIN_PublishedServerUrl, "{{ .Routes.web.URL }}");
  assert.deepEqual(app.mounts, [{ storage: "config", path: "/config" }, { libraries: true, path: "/data/tvshows" }, { libraries: true, path: "/data/movies" }]);
  assert.equal(r.pkg.storage.config.title, "Its settings");
  assert.deepEqual(app.devices, [{ gpu: "intel" }]);
  assert.equal(r.pkg.routes.web.port, 8096);
  assert.equal(r.pkg.ports.ssdp.protocol, "udp");
  assert.ok(has(r.report.notNeeded, /PUID/));
});

test("MySQL, SSH on a low port, and what's refused", () => {
  const r = imported("gitea");
  valid(r);
  const app = r.pkg.containers.app;
  assert.equal(r.pkg.databases.main.type, "mysql");
  assert.equal(app.env.GITEA__database__HOST, "{{ .Databases.main.Host }}:{{ .Databases.main.Port }}");
  assert.equal(app.secrets.GITEA__database__PASSWD, "databases.main.password");
  // Port 22 published as 222 moves to 2222 on the server.
  assert.deepEqual(r.pkg.ports.ssh, { title: "Git or files over SSH", container: "app", port: 22, host_port: 2222 });
  assert.ok(has(r.report.notAllowed, /privileged/));
  assert.ok(has(r.report.overToYou, /MySQL 8\.4/));
  assert.ok(has(r.report.notNeeded, /networks/));
});

test("what isn't a Compose file says so", () => {
  assert.match(R.compose.importCompose("just: text").error, /no services/);
  assert.match(R.compose.importCompose("services: [\n").error, /isn't a Compose file Roostack can read/);
  assert.match(R.compose.importCompose("services:\n  db:\n    image: postgres:17\n").error, /Every service in that file is a database/);
});

test("Compose's small parts", () => {
  const missing = new Set();
  assert.equal(R.compose.interpolate("${A:-x}-${B-y}-$$-${C}", { B: "" }, missing), "x--$-${C}");
  assert.deepEqual([...missing], ["C"]);
  assert.deepEqual(R.compose.shellWords(`sh -c "echo 'hi there'" x\\ y`), ["sh", "-c", "echo 'hi there'", "x y"]);
  assert.equal(R.compose.memoryOf("512m"), "537 MB");
  assert.equal(R.compose.memoryOf("2g"), "2.1 GB");
  assert.equal(R.compose.memoryOf("bogus"), "");
  assert.equal(R.compose.versionFromTag("v1.2.3-alpine"), "1.2.3");
  assert.equal(R.compose.versionFromTag("latest"), "");
  assert.deepEqual(R.compose.imageParts("nginx:1.27"), { registry: "docker.io", repo: "library/nginx", tag: "1.27", digest: "", base: "nginx" });
  assert.equal(R.compose.imageParts("localhost:5000/x/y@sha256:ab").registry, "localhost:5000");
  assert.equal(R.compose.safeName("db-thing", "x"), "app-db-thing");
  assert.equal(R.compose.storageGuess("/var/cache/app", "x").class, "replaceable");
});

test("healthchecks, users, limits and Quadlet lines", () => {
  const r = R.compose.importCompose(`services:
  web:
    image: example/web:2.0.1
    user: "1000:1000"
    ports: ["8080:80"]
    mem_limit: 256m
    shm_size: 64m
    read_only: true
    init: true
    cap_drop: [ALL]
    stop_grace_period: 1m30s
    security_opt: [no-new-privileges:true, seccomp:unconfined]
    healthcheck:
      test: ["CMD", "wget", "--spider", "-q", "http://localhost:80/healthz"]
      interval: 30s
      retries: 3
    command: ["serve", "--port", "80"]
    tmpfs: [/tmp]
`, { version: "0.9.0" });
  const pkg = YAML.parse(r.yaml), c = pkg.containers.app;
  assert.deepEqual(c.health, { http: { port: 80, path: "/healthz" } });
  assert.equal(c.memory, "268 MB");
  assert.deepEqual(c.command, ["serve", "--port", "80"]);
  assert.deepEqual(c.quadlet.Container, { User: "1000:1000", UserNS: "keep-id:uid=1000,gid=1000", HealthInterval: "30s", HealthRetries: 3,
    ShmSize: "64m", ReadOnly: true, RunInit: true, StopTimeout: 90, DropCapability: "ALL", Tmpfs: "/tmp", NoNewPrivileges: true });
  assert.ok(has(r.report.notAllowed, /seccomp:unconfined/));
  const check = R.check.run(r.yaml, { local: true });
  assert.deepEqual(check.problems.filter((p) => p.kind === "validation").map((p) => p.message), []);
  // Every Quadlet line it writes is one the policy allows.
  assert.deepEqual(check.problems.filter((p) => /quadlet/.test(p.message)).map((p) => p.message), []);
});
