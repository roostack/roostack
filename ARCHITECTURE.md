# Roostack Architecture

How the code fits together. For *why* things are the way they are, see [SPEC.md](SPEC.md).

## Processes

```
               ┌────────────────────────────── host (Debian 13) ───────────────────────────────┐
               │                                                                               │
 browser ─443──┼─▶ Caddy (own pasta netns, sees real client IPs)                               │
               │      │  169.254.1.3 = host loopback (pasta --map-host-loopback)               │
               │      ├──▶ 127.0.0.1:20000 ──▶ service container (own private network)         │
               │      └──▶ 127.0.0.1:8480  ──▶ roostack (user: roostack)                       │
               │                                  │                                            │
               │               systemctl --user,  │                                            │
               │               systemd-run --user ◀┤ (podman runs outside roostack's sandbox)  │
               │                                  │ Unix socket                                │
               │                                  ▼                                            │
               │       roostack-agent (root): firewall, WireGuard, OS updates, drives, mounts  │
               │                                                                               │
               └───────────────────────────────────────────────────────────────────────────────┘
```

- **`roostack`** (unprivileged, user `roostack`): web UI, desired state, generating and applying config. It owns every rootless container through the `roostack` user's systemd manager.
- **`roostack-agent`** (root): a small, fixed set of privileged operations, reached over `/run/roostack-agent/agent.sock`. Callers are identified by the kernel (`SO_PEERCRED`); only root and `roostack` are accepted, and only from the host's own user namespace (not a container). It runs in a systemd sandbox that lets it write only where its jobs need (SPEC §6.1).
- **The sign-in page for services** (single sign-on, SPEC §5.2): `roostack` also listens on `127.0.0.1:8482`, a listener of its own that serves nothing of Roostack's pages: Caddy sends `login.<domain>` there (the sign-in pages and the OIDC endpoints), asks it about each request to a walled service (`forward_auth` to `/check`), and passes it each walled service's `/.roostack/sign-in/` callback (`internal/web/signin.go`, `internal/sso`). Roostack's own pages sign in there too, once a service uses it: `/roostack` on that listener hands a one-time code back to the UI listener's `/login/sign-in/callback`, which makes a session linked to the sign-in (`internal/web/pagessignin.go`). A test-only build, `-tags ssoconformance`, also accepts the OpenID conformance suite's redirect address (docs/SSO-CONFORMANCE.md).
- **During first-run setup**, `roostack` also listens on `:8443` (HTTPS, with a self-signed certificate made at its first start) for the setup pages only, to the home network only, and stops when setup is finished (`internal/web/firstrun.go`, SPEC §6.3.2).
- **Caddy** (rootless container): the only thing listening on 80/443. It runs in its own pasta network namespace so it sees visitors' real IP addresses. It reaches routed containers, which are published on `127.0.0.1:<port>`, through pasta's loopback mapping (`169.254.1.3`). See SPEC.md §6.5 for why.

## Data flow: desired state → running services

```
lab.yaml ──────────────────────┐
services/<name>.yaml ──────────┼─▶ state ─▶ generate ─▶ reconcile ─▶ files + systemd
packages/<name>/<version>.yaml ┘             (pure)      (plan → apply)
      ▲
      └── copied at install from catalog/<name>/<version>.yaml
          (or local-packages/<name>/<version>.yaml, the Owner's own)
```

1. **`internal/state`** loads the lab config, every instance file, and the lab's own copy of each instance's package version (`packages/<name>/<version>.yaml` in the state folder, SPEC.md §8.3.1). The copy is taken from the catalog (or the Owner's own packages, `local-packages/`, SPEC.md §8.3.4) when a service is installed, so a newer catalog never changes a running service and a restore needs only the backup; the catalog is only what *can* be installed. Copies no service uses are deleted after each apply. It also checks lab-wide rules (e.g. unique hostnames).
2. **`internal/generate`** turns package + instance + lab config into output: Quadlet files, a Caddy snippet, required secrets, directories, and a backup plan. It is **pure** (no disk access, no side effects), so it's tested with golden files and can be previewed with `roostack render`. `Summarize` uses it for the summary the install page shows before installing (what it keeps, addresses, ports, GPU, memory, and the files it would write; SPEC.md §8.3.4).
3. **`internal/reconcile`** compares that output with what's on disk and builds a **plan** (files to write or remove, units to start, restart, or stop, secrets to create, images to build or download). `roostack apply --dry-run` prints the plan; `roostack apply` carries it out. Image builds start in the background; new containers' images are pulled one at a time before their units start, with progress events the node turns into a service's install steps (SPEC.md §6.3.3).

The reconciler only touches files it owns (`roostack-*` Quadlets and `*.caddy` snippets) and **never deletes service data**.

## Life of an install

What happens between the Owner clicking **Install** and the service running, with where to look. The apply is **asynchronous**: the click returns as soon as the desired state is saved, and the page follows the work by polling.

1. **The form.** `POST /install/{name}` reaches `installSubmit` ([internal/web/services.go](internal/web/services.go)), which turns the form into a `manager.InstallRequest` (settings, and typed-in secrets kept apart), calls `Manager.Install`, writes an audit entry, and redirects to Home.
2. **The desired state.** `Manager.Install` ([internal/manager/manager.go](internal/manager/manager.go)) finds the package (the Owner's own first, then the catalog), checks the package policy, drafts the instance (server, version, subdomain, ports), and does a dry run of `state.Desired` to catch clashes. Only then does it write: the lab's copy of the package version (`state.KeepPackage`, [internal/state/packages.go](internal/state/packages.go)) and the instance file. Typed-in secrets stay in memory, never on disk.
3. **The assignments.** `startApplyLocked` ([internal/manager/servers.go](internal/manager/servers.go)) builds every server's `node.Assignment` (its services, the typed-in secrets they need, the services whose install hooks should run, and on the controller the sites its Caddy fronts for other servers) and hands each to its `node.Node` with `Apply`. Then the manager forgets the secrets.
4. **Another server.** For a `node.Remote` ([internal/node/remote.go](internal/node/remote.go)), `Apply` encodes the assignment and pushes it in the background with `PUT /v1/assignment`, retrying until the server has it. There, `Server.Take` ([internal/node/api.go](internal/node/api.go)) applies only a newer version, hands it to that server's own `Local`, and saves it without its secrets (SPEC.md §10.3).
5. **One apply at a time.** `Local.Apply` ([internal/node/local.go](internal/node/local.go)) keeps the assignment and starts `applyLoop` in the background, or marks it to go again if an apply is already running, so the latest assignment always wins. This is where the request returns.
6. **Generate.** `applyOnce` starts the apply's activity log and calls `state.Desired`, which runs `generate` for each service: Quadlet files, a Caddy snippet, secrets, and folders. It's pure, so nothing has changed yet.
7. **Plan.** `reconcile.Plan` ([internal/reconcile/reconcile.go](internal/reconcile/reconcile.go)) compares that with the disk and Podman's secrets: folders and secrets to create, files to write or remove, images to pull, units to start, restart, or stop, and whether Caddy needs a reload. The node turns the plan into each new service's install steps ([internal/node/install.go](internal/node/install.go)).
8. **Apply.** `reconcile.Apply` creates folders and secrets, writes files atomically, runs `systemctl --user daemon-reload` (Podman's Quadlet generator turns the files into units), and restarts what changed. Each new container's image is pulled (`podman.Pull`, [internal/podman/pull.go](internal/podman/pull.go), through `systemd-run --user`) and its unit started, one at a time, with each step recorded for the page. Caddy is reloaded last ([internal/systemd](internal/systemd)).
9. **After the apply.** The firewall and links between servers are updated and the log ends. A failure is recorded in the install's steps, the activity log, and the server's status; finished logs that matter are kept as history ([internal/node/history.go](internal/node/history.go)).
10. **Install hooks,** also in the background, and only after the whole apply has succeeded: `startInstallHooks` ([internal/node/hooks.go](internal/node/hooks.go)) runs each package hook with `podman exec`, values passed as an env file on stdin, every 10 seconds for up to 10 minutes while the app starts. Their state is kept in the settings table (`hooks.<service>.install`), and `install_only` secrets are deleted once they succeed. Until then the instance has `setting_up: true` (set by `Manager.Install`), which keeps its public addresses and ports private everywhere they're worked out; the controller removes it and applies once the server says the hooks succeeded (`Manager.SetUpDone` for its own services, `Manager.WatchSetUps` asking the others, [internal/manager/setup.go](internal/manager/setup.go); SPEC.md §8.1).
11. **The page.** Home polls `/partials/home` every 10 seconds; a tile shows "Step N of M" from the install's steps, and the apply's log refreshes through `/activity/…` ([internal/web/activity.go](internal/web/activity.go)). For another server the controller reads the same over the lab API (`GET /v1/services/details`, `/v1/activity/{kind}`, `/v1/status`).

## Life of a backup run

Every server backs up its own services, the controller included, on its own schedule. The controller only sets up where each server backs up, passes **Back up now** on, and reads back each server's status (SPEC.md §6.9, §10.10).

1. **Set up in advance.** When the Owner chooses a destination, `Manager.SetBackupDestination` ([internal/manager/backups.go](internal/manager/backups.go)) creates the repository in the background and gives the server its settings (`SetBackupDestination` in [internal/node/settings.go](internal/node/settings.go): the key file and `lab.yaml`'s `backup:` section, then a restart). Extra backups work the same way ([internal/manager/copies.go](internal/manager/copies.go)).
2. **Each server's machinery.** At start, `serverJobs` ([cmd/roostack/jobs.go](cmd/roostack/jobs.go)) builds the server's maintenance coordinator and its `backup.Service` ([internal/backup/service.go](internal/backup/service.go)), with the services from its assignment.
3. **What starts a run.** The nightly timer at 02:00 plus the server's own few minutes (`maintenance.Offset`; `maintenance.Every`, [internal/maintenance/schedule.go](internal/maintenance/schedule.go)); a catch-up when no attempt was made in the last 26 hours; a planned retry of a destination that failed; or **Back up now**, `POST /backup/run` (`backupRun`, [internal/web/backup.go](internal/web/backup.go)). For another server the button posts to `/settings/servers/{server}/backups/run`, and `Remote` passes it on as `POST /v1/backups/run`. Each only queues the run; the page follows it by polling.
4. **One job at a time.** `Service.Submit` queues the job with the coordinator ([internal/maintenance/coordinator.go](internal/maintenance/coordinator.go)), which runs one maintenance job at a time (backups, restores, OS updates) and starts the activity log with "Waiting for other maintenance to finish". A backup interrupted by a reboot is run again afterwards.
5. **The plan.** `backup.LabPlan` ([internal/backup/job.go](internal/backup/job.go)) works out the snapshots at every run: Roostack's state folder and Podman secrets, then one per service, with the database steps `generate` gave each package.
6. **Prepare.** `backup.Run` checks the destination's drive, copies Roostack's own SQLite database, and runs each service's prepare steps ([internal/backup/prepare.go](internal/backup/prepare.go)): `pg_dump` or `mysqldump` inside the database's container, or a SQLite copy through `roostack backup-helper` inside `podman unshare`. Services keep running; the dumps are taken live.
7. **restic.** For each snapshot, `Repo.Backup` ([internal/backup/restic.go](internal/backup/restic.go)) runs `restic backup` through `systemd-run --user … podman unshare restic`, so it runs as the roostack user, outside Roostack's sandbox, and can read files that belong to containers' users. The repository's password is the recovery key, kept in `<state>/backup-key`; Roostack Cloud's short-lived keys come from [internal/cloudkeys](internal/cloudkeys), passed as environment variables; a command that fails once they've run out runs again at once with new ones (`keysRanOut`).
8. **Extra backups, in the same job.** Each extra backup gets its own drive check and its own `restic backup` of the same snapshots; they go ahead even if the main one failed.
9. **Recorded.** Each destination's result goes into the server's own store as a backup run, keyed by the repository's address so a copy promoted to main keeps its history ([internal/store](internal/store)); the activity log is finished, and a failed destination's next try is planned.
10. **The page.** `Service.Status` gathers the last runs, the coordinator's progress and the next run; the Backups page shows it and refreshes through `/partials/backups`, and Home's "Needs attention" checks the same for every server ([internal/attention/attention.go](internal/attention/attention.go)), which the notifier emails. For another server it's `GET /v1/backups`.

## Packages

Grouped by layer, from the commands down. Sub-packages that exist only for tests (`b2test`, `cloudtest`, `dnstest`, `nodetest`, `restictest`, `s3fake`, `storetest`) and small commands used by the build (`registry/buildkeys`, `editor/standalone`) are named in their parent's row.

### Commands

| Package | Purpose |
|---|---|
| `cmd/roostack` | The web service and the command line: `serve` (the controller's UI and lab API, or a node's lab API), `apply`, `user` (`add`, `reset-password`, `reset-2fa`), `backup` (`init`, `snapshots`, `run`, `restore`), `node join`, `registry` (`keygen`, `delegate`, `index`, `check`, `watch`, `images`, `bom-check`), `render`, and `validate`; `backup-helper` is internal, run inside `podman unshare` (SQLite copies for backups and restores). `serve` wires the packages below together: on the controller a step at a time, in order, each step a method of `controller` (`serve.go`, with `controller.go`); every server's maintenance jobs in `jobs.go`; another server's lab API in `nodeserve.go` |
| `cmd/roostack-agent` | The root helper (`internal/agent` is its code) |

### Desired state: what the lab should be

| Package | Purpose |
|---|---|
| `internal/config` | Lab settings and **all default paths** in one place, including where the lab is (`network: home|internet`, SPEC.md §6.12) |
| `internal/pkgspec` | The package format (`schema: roostack/v1alpha1`), its validation, and the package policy registry packages follow (SPEC §8.3.4; `Risks` is the part shown for the Owner's own); `Describe` gives the package editor what they use |
| `internal/instance` | A user's installed service, validated against its package |
| `internal/state` | Load desired state from disk; keep and prune the lab's copies of its packages (SPEC §8.3.1); read the catalog's newest version of a package; keep the Owner's own packages (SPEC §8.3.4) |
| `internal/registry` | The service registry (SPEC §8.3.3): the signed index and key statement, building them from the registry repository (`roostack registry`), and the controller's fetcher (`Updater`: at start, every 4 hours, on opening the catalog) keeping `CatalogDir`. `buildkeys` reports the root keys a build trusts, for `make build` to record and `scripts/build-deb.sh` to check. `watch` is the registry CI's watcher (`roostack registry watch`, docs/REGISTRY-WATCH.md): it reads free sources of security problems, matches them to the packages, and drafts advisories for a maintainer to approve; it also lists the images needing a bill of materials and compares the bills with `built_with:`. `updates` is the registry CI's update check (`roostack registry updates`, docs/REGISTRY-UPDATES.md): for each package's newest version, a new app version or a rebuilt image, and the revision written from the newest file |
| `internal/imagecheck` | Asks container registries which processors a pinned image has builds for (`make check-images`, the registry's index), a repository's tags, and the digest a tag points to now (the registry's update check) |
| `internal/nodeid` | This server's identity (node ID and name) |
| `internal/pkgtest` | For tests: finds the package fixtures in `internal/testdata/packages` and copies them into a catalog |
| `internal/testwait` | For tests: waits for what another goroutine does and fails the test when its deadline passes |

### From desired state to running services

| Package | Purpose |
|---|---|
| `internal/generate` | Desired state → Quadlets, Caddy config, secrets, dirs, backup plan; the install summary |
| `internal/quadlet` | Writing Quadlet/systemd unit files, with escaping |
| `internal/caddy` | Writing the main Caddyfile and per-service snippets |
| `internal/reconcile` | Plan and apply changes to the host |
| `internal/systemd` | The roostack user's systemd manager: `systemctl --user` (start, stop, reload, status) and `UserCommand`, which runs a command as a transient unit (`systemd-run --user`), the way Roostack runs podman, restic, and anything else that touches container-owned files from outside its own sandbox (SPEC.md §6.1) |
| `internal/podman` | The podman commands Roostack runs itself, all through `systemd.UserCommand`: secrets, and `exec` for package hooks with values passed as an env file on stdin |
| `internal/errpage` | The error scenes (generated by `assets/errors/generate.py`) and the services' error pages that Caddy serves (SPEC.md §6.3.4); Roostack's own error pages are in `internal/web/errors.go` |

### The lab: servers, the controller and the agent

| Package | Purpose |
|---|---|
| `internal/node` | A server in the lab (SPEC.md §10): the `Node` interface and `Local`, which applies its Assignment (reconcile, firewall), runs package hooks (install, actions, single sign-on's `on: sso` hooks whenever what they're given changes, `ssohooks.go`, and scheduled tasks, queued on its coordinator after the nightly backup, `scheduled.go`), saves keys hooks print and reveals secrets, and holds the server's maintenance jobs (backups, restores, OS updates, and restarts), its own settings (time zone, SSH access and keys only, restart policy) and activity, and keeps its finished logs as history (`events`, SPEC.md §6.14). `Remote` is another server over the lab API (`Server` serves it on each node); each feature's client methods and routes sit side by side in a `*wire.go` file (`labwire.go`, `backupwire.go`…), and `TestRoundTrip` calls every one; `Fleet` keeps one per joined server. It also brings data into its services from the person's own copy (`import*.go`, SPEC.md §8.4: the import's state, the check, the copy into staging folders, the switch's steps) and restarts services whose library, bound at a path of its own, came back (`libwatch.go`). Its photo migration and imports are types of their own (`localPhotos`, `localImports`), as `Remote`'s capabilities are. `nodetest.Base` is a server with nothing, for other packages' fakes to embed |
| `internal/manager` | The controller (SPEC.md §10): the lab's desired state and everything the pages ask for that isn't one server's own work. Services (the catalog with the Owner's own packages, the package policy at install, install, remove, ports and settings, updates, withdrawals, public addresses, the edge and the relay, single sign-on, bringing data in, photo migration, media libraries, monitoring's checks); servers (each one's Assignment, processors, links); backups (destinations, extra backups and the 3-2-1 checks, S3 checks and rollbacks, junk, recovery key changes); moving and restoring services across servers (one procedure, `transferJob` in `transfer.go`), lost servers, and holding a service stopped after a restore or move whose databases didn't load (`hold.go`, SPEC.md §6.9); and storage for other labs. Photo migration, storage for other labs, and recovery key changes are types of their own, `manager.Photos`, `manager.Guests`, and `manager.KeyChanges`, which reach the rest through a small interface (`core.go`). Its package doc maps files to features |
| `internal/labnet` | The network between a lab's servers (SPEC.md §10.2, §10.9): the controller's CA and each server's certificate, join codes, membership (role and overlay address), the hub (WireGuard peers, joining), and the mutual-TLS lab API server |
| `internal/trust` | Who a connection comes from (home, anyone, the lab's servers, other labs, edges, the relay) and what each may reach (SPEC.md §6.10): the one definition the agent's ruleset and Caddy's private list are made from, with tests that check both against it |
| `internal/firewall` | Builds the firewall from the lab, its services and the server's roles (store, guests, hub), each rule for a trust class; lists current SSH connections |
| `internal/agent` | Agent server and client: firewall ruleset, time zone, OS updates, reboot, WireGuard interfaces, backup drives and folders, media libraries and services' views of them, the read-only place to bring a service's data in from (`import.go`, `/mnt/roostack-import/<service>`), SSH's keys-only drop-in and key logins (`ssh.go`), and checks for host changes Roostack didn't make |
| `internal/maintenance` | Maintenance coordinator: job queue and reboot policy |
| `internal/activity` | Live logs of host operations (OS updates, backups, applies, setup hooks) for the UI: a status line plus collapsible details |
| `internal/osupdate` | Nightly OS updates job, restart-needed detection, "restart now"; Roostack's own release series: noticing a new one in its apt repository and moving to it once the Owner accepts (SPEC §7.2) |
| `internal/hosttime` | Reads the host's time zone and the valid zone names (tzdata); changing it is the agent's job |

### Backups and restores

| Package | Purpose |
|---|---|
| `internal/backup` | restic wrapper, prepare steps, backup plan and job; `backup/restictest` finds restic for tests and makes their repositories by copying one made once |
| `internal/kept` | Kept copies (SPEC.md §8.4): data set aside rather than deleted, before a restore, a move, bringing data in, or a put back, or left by one that didn't finish. Its folders' names (`<folder>.<why>-<time>`), why each is kept and its label, and finding a server's copies; `node.Local` lists, puts back, and deletes them (`kept.go`, `keptwire.go`) |
| `internal/destination` | A backup destination's reference (`server:<id>`, `drive:<id>`, `provider:<id>`, `cloud`…), parsed and written in one place |
| `internal/b2` | Backblaze B2 application keys limited to one server's folder, made and deleted over B2's native API; `b2test` fakes that API for tests |
| `internal/s3api` | The few S3 calls Roostack makes beside restic (path-style, signed with `awssig`): a bucket's versioning and lifecycle rules, the probe that shows a key can't erase old versions, and listing and copying back old versions to roll a server's folder back (Backblaze's S3-compatible API too); `s3api/s3fake` is an in-memory versioned bucket for tests |
| `internal/cloudkeys` | Roostack Cloud backup's short-lived keys on a server (SPEC.md §6.9): kept 0600 in the state folder, renewed once an hour old or with under an hour left, passed to restic as environment variables. The controller's come from Cloud, other servers' from the controller (`POST /v1/storage-keys` on the lab API) |
| `internal/labrestore` | Rebuilding a whole lab on a new server (SPEC.md §6.9): finding its backups (a folder, an S3-compatible bucket, or any restic address) with the recovery key, restoring the controller's backup, then its database at the next start and the services' databases once they're set up again |

### Addresses, DNS and public access

| Package | Purpose |
|---|---|
| `internal/dns` | Supported DNS providers: credential fields, Caddy directive, reading and writing records through each provider's API, and the check that creates and deletes a TXT record with that same code. `dnstest` fakes every provider's record API on one test server, for tests here and in `dnsrecords` |
| `internal/dnsrecords` | Keeps `*.<domain>` pointing at the LAN IP (only records Roostack created) and checks home devices can resolve it; points each public address at the public entry's address once CrowdSec is up, and deletes it when the address goes private; keeps an old address's records up to date while it works and deletes them when it ends (`old.go`, SPEC.md §6.6); deletes only its own records, as it left them |
| `internal/public` | Public access (SPEC.md §6.11): the entry visitors come in through (port forwarding: the lab's public address, found over DNS), CrowdSec's health (its local API and AppSec), what it saw and what AppSec refused (§6.10), check links opened from outside, and guessing whether the server is on the internet (§6.12) |
| `internal/cloud` | The controller's link to Roostack Cloud (SPEC.md §6.6): linking (a link code, Cloud's page, polling), the lab's client certificate for mutual TLS (renewed with a new key), the daily check-in, unlinking, claiming a roost.ac name (then the lab's DNS provider is Roostack Cloud, `internal/dns/cloud.go`), Roostack Cloud backup's keys and the lab's use of its space, the recovery identity (registered with Cloud; used to restore from Cloud and to relink, `recovery.go`), the request log (what each request sent, secrets left out by an allowlist a test keeps complete, `requestlog.go`), and what serves the lab's public addresses that come through Cloud, with Cloud's answer on which it doesn't allow (`public.go`; the manager's side is `cloudpublic.go`, SPEC.md §6.11), kept in `<state>/cloud/`. `cloudtest` is a fake of Cloud's lab API for Roostack's tests (dev VMs link to a development Cloud from its own repository) |

### Other features

| Package | Purpose |
|---|---|
| `internal/setup` | The first-run wizard's logic (SPEC.md §6.3.2): where the lab is, the time zone, SSH access, the lab's address with its DNS provider's login or a roost.ac name, the backup destination, and the recovery key, checked and saved step by step; Settings reuses it to change them |
| `internal/sso` | The identity provider for single sign-on (SPEC.md §5.2), standard library only: signing keys and ID tokens, the OIDC code flow (discovery, authorise, token, userinfo, refresh), and forward auth's check, hand-off, and per-address cookie. Its pages are `internal/web` (`signin.go`); people, groups, and who may use each service are `internal/store` (`sso.go`) |
| `internal/monitor` | Monitoring (SPEC.md §6.15): the controller's checks every minute (each route through its own Caddy, the servers, the lab's address, people's own: HTTPS, TCP, and ping with the standard library), their state (down after three failures, paused), uptime and day bars from the results and hourly summaries kept in `store`. The web layer adds the pages and the public status page it writes for Caddy; `internal/attention` puts what's down in "needs attention" |
| `internal/attention` | What needs attention (SPEC.md §6.13): the rules that make each "Needs attention" item, with its level, group, and key (backups, servers, disks running low, DNS, Roostack Cloud, the registry, services' updates, holds and withdrawals, monitoring), reading the lab through `Lab`, which `internal/web` fills from its features; and the notifier, which emails each person the groups they chose (a problem once it has lasted a few minutes, what waits for them, security events from the audit log, the weekly summary). The pages only show the items |
| `internal/mail` | Sending email through the lab's SMTP server (SPEC.md §6.13): settings in lab.yaml, the password in its own 0600 file, STARTTLS or TLS with the standard library's client |
| `internal/importer` | Bringing your own data into a service (SPEC.md §8.4), without state: reading an old setup (`docker inspect`; the Compose file is read in the browser), matching its folders to the package's storage by container path (`Match`, `GuessPrefixes`), the check's rules (dumps, images, settings, old paths in the data), and the copy (`Scan`, `Copy`: only what changed, links as links, nothing written outside through `os.Root`) |
| `internal/photoimport` | Migrating photos into Immich from other platforms (SPEC.md §8.5), without state: a quick look in an export (`ScanFS`: a Takeout's parts and any missing, counts, sizes, dates, albums, and Google's flags, from the zips' directories and the small sidecars), immich-go's command line for the person's choices (`Command`; the key only ever in `KeyEnv`), reading its JSON log as it goes (`Tally`), and asking Immich about a key from inside its own container (`AccountScript`, `ParseAccount`). `node/photos.go` keeps each person's steps on Immich's server and runs immich-go there, in its own container on Immich's network, in a transient unit (`systemd.RunDetached`) that outlives Roostack; the agent mounts the export read-only (`agent/photos.go`); `build/immich-go` is the tool's image, built on the server |

### The web UI

| Package | Purpose |
|---|---|
| `internal/web` | Web UI: handlers, templates, vendored htmx/Alpine, and the embedded help site at `/help/` (`help.go`, built from `help/` into `helpsite/site` by `make build`) |
| `internal/web/static/css`, `docs/DESIGN.md` | Design system: tokens, built-in themes, components; gallery at `/design` and `docs/design/gallery.html` |
| `internal/auth` | Roles and usernames (passwords, tokens, and two-factor codes are `authkit`) |
| `internal/store` | SQLite runtime data, a file per area: the schema (`schema.go`: one schema at version 37, squashed from the steps before the first release, and later changes as `migrations`), users and their notices (`users.go`), sessions, the audit log and history events (`audit.go`), settings, the lab's servers and join codes, backups (runs, providers, targets, copies, lost backups, store admins), storage for other labs (`guests.go`), single sign-on, monitoring, and media libraries; `storetest.Open` opens a new one for a test |
| `internal/editor` | The package editor (SPEC §8.3.7): plain HTML, CSS, and JavaScript that run in the browser, served at `/editor/` and copied to roostack.cloud; `static/schema.js` generated from `pkgspec.Describe` and `help.yaml`, `check.js` the same checks as `pkgspec`, `compose.js` the Compose import; `Standalone` writes it as one file. Tests: Go (`TestSchemaFile`, `TestCorpusFile`) and Node (`test/`, `make editor-test`) |

### Public packages, shared with Roostack Cloud

| Package | Purpose |
|---|---|
| `authkit` | Passwords (argon2id), tokens, and two-factor codes (TOTP), public so Roostack Cloud's repository uses the same code; Cloud also turns on a pepper and sealed two-factor secrets (`Sealer`; `SealFor` binds a secret to its owner as GCM's additional data). Fuzz-tested (the inputs fuzzing found are kept in `testdata/fuzz`) |
| `awssig` | AWS Signature Version 4 with the standard library only (Route 53, S3-compatible storage; checked against the AWS SDK's signer): public, so Roostack Cloud signs Wasabi's STS and S3 requests with the same code |
| `cloudapi` | The API between a lab and Roostack Cloud (paths, bodies, the records and acme-dns conventions, and `Routes`: each route's method, path, whether it needs the lab's certificate, whether a suspended account's lab may still use it, and whether it counts against the lab's limit on changes): public, not under `internal`, so Cloud's own repository imports the same contract, and Cloud's tests and Roostack's fake Cloud (`cloudtest`) follow the same table |
| `mailkit` | Emails in HTML beside their plain text (SPEC §6.13, "Email in HTML"): blocks (paragraphs, lists, sections of notices, buttons) written as text (`Text`) and as email-safe HTML in the default theme's colours (`HTML`, from `email.html`), plain text read back into blocks (`FromText`), and the MIME body with the brand's mark embedded (`Body`). Public, so Roostack Cloud's emails have the same look with its own mark. Golden files in `testdata` (`go test ./mailkit -update`) |
| `units` | The one size formatter (decimal units: 1 GB = 1000³ bytes): `Bytes` ("1.5 GB") is how Roostack shows sizes, `AsSold` ("1.5 TB", "under 1 MB") how Cloud shows storage as it's sold, and `Count` writes thousands separators. Public, so Cloud formats sizes with the same code |

### Small helpers and tools

| Package | Purpose |
|---|---|
| `internal/words` | The small pieces of sentences: a capital first letter, "a, b and c" and "a, b or c", counts with their nouns, an error's first line, and lengths of time, rough ("25 minutes") or exact ("1 h 5 min") |
| `internal/fsutil` | Writing a file atomically: a unique temporary file in the same folder, its mode set, synced, then renamed. `WritePrivate` (0600) is how secrets and private state are written, and `CopyFile` streams a copy the same way. `Within` is the one "is this path inside that folder?" |
| `internal/sleep` | The waits loops share: `For` (gives up when the context ends) and `Every`; `maintenance.Every` is for wall-clock schedules |
| `internal/usererr` | The one error a person can fix: a sentence saying what to change, which the web layer shows as it is (`usererr.Message`); any other error is a server error |
| `tools/maketakeout` | Writes a synthetic Google Photos Takeout, never real photos, for testing photo migration on a VM ([its README](tools/maketakeout/README.md)) |

Rules that keep this maintainable:
- **Business logic stays out of HTTP handlers** so that the UI can change without touching the core (SPEC.md §6.3).
- **Side effects happen behind small interfaces** (`reconcile.Systemd`, `reconcile.Secrets`, `agent.Host`) so that the logic is tested with fakes.
- **Commands shell out to standard tools** (`systemctl`, `podman`, `nft`) so that every action matches something a person could run to debug it.

## Generated names

For a service `<svc>`:

| Thing | Name |
|---|---|
| Service network | `roostack-<svc>` (file `roostack-<svc>.network`), isolated from every other network (`isolate=strict`, SPEC §6.4) |
| Container | `roostack-<svc>-<container>` (unit `roostack-<svc>-<container>.service`) |
| Route port | `127.0.0.1:<port>`, from 20000–29999, stored as `routes.<name>.port` in the instance file |
| Database container | `roostack-<svc>-db-<name>` |
| Secret | `roostack-<svc>-<setting>`, or `roostack-<svc>-db-<name>-password` |
| Caddy snippet | `sites/<svc>.caddy` |
| Data | `/srv/roostack/services/<svc>/<storage>/` or `/srv/roostack/media/<svc>/<storage>/` |
| Database data | `/srv/roostack/services/<svc>/db/<name>/` |
| Database dumps (for backup) | `/srv/roostack/services/<svc>/db-dump/` |

## Package format

Packages live in the registry repository, [`roostack/services`](https://github.com/roostack/services) (SPEC.md §8.3), as `packages/<name>/<version>.yaml` beside `icon.png` and `registry.yaml`. The registry's CI builds a signed index from it (`roostack registry index`), published at `https://registry.roostack.cloud/v1/`; the controller fetches the index every few hours and when the catalog opens, checks its signatures, and keeps the version of each package it would install, with its icon, in `/var/lib/roostack/catalog` (`config.Paths.CatalogDir`), where the catalog reads them ([internal/registry](internal/registry); the keys: [docs/REGISTRY-KEYS.md](docs/REGISTRY-KEYS.md)). After each check the manager compares installed services' versions with the index's withdrawals ([internal/manager/withdrawals.go](internal/manager/withdrawals.go)): a stop marks the service in its server's assignment (`state.Service.Stop`), and `generate.Stop` leaves out its containers and site (SPEC.md §8.3.4). Updates to the offered versions ([internal/manager/update.go](internal/manager/update.go), SPEC.md §7.1) are driven by the controller: the service's server downloads the new images, backs up, and holds its maintenance queue (`node.Updater`, [internal/node/update.go](internal/node/update.go); `/v1/updates` and `/v1/services/{service}/health` on the lab API), while the controller switches the instance's version, applies, checks the service, and goes back to the previous version if the check fails. Copies of Vaultwarden (simple), Forgejo and Immich (complex) are the tests' fixtures, in [internal/testdata/packages](internal/testdata/packages) (found through `internal/pkgtest`). Field-by-field definitions are in [internal/pkgspec/pkgspec.go](internal/pkgspec/pkgspec.go), and the facts about the project (`about:`, `requires:`, upgrade notes) in [internal/pkgspec/about.go](internal/pkgspec/about.go).

Templates use Go `text/template` syntax with these values:

| Value | Example |
|---|---|
| `.Routes.<route>.Host` / `.URL` | `photos.lab.example.com` / `https://photos.lab.example.com` |
| `.Settings.<key>` | Non-secret settings only, so secrets can never end up in a unit file |
| `.Databases.<name>.Host` / `.Port` / `.User` / `.Name` | Connection details for Roostack-managed databases |
| `(index .Containers "<name>").Host` | Another container in the same service |
| `.Lab.Domain`, `.Service.Name` | Lab and service names |

## Web UI

Pages (the sidebar; links hidden where the role can't use them). Admin+ means Admins and Owners:

| Page | Who | Contents |
|---|---|---|
| Home `/` | everyone | "Needs attention" list (problems gathered from setup, applying changes, backups, DNS, monitoring, and two-factor, filtered by role, each linking to where it's fixed), then service tiles. Shows "Everything's working" when there's nothing to do |
| Services `/services`, `/services/{name}` | view: everyone; manage: Admin+ | list and each service's page (settings, ports, actions, updates, single sign-on, bringing data in, remove); `/visitors` for the relay's visitor counts |
| Add a service `/catalog`, `/install/{name}` | Admin+; your own packages (`/catalog/add`, `/catalog/editor`): Owner | the catalog, then the install form with its summary |
| Backups `/backups` | Admin+; restores, the recovery key, and junk: Owner | status, "Back up now", destinations and extra backups, restores |
| Servers `/servers`, `/settings/servers/{server}` | Owner | the lab's servers, joining one, and each server's own pages (disk, kept data, a lost server) |
| Monitoring `/monitoring` | Admin+ | checks, uptime, and the public status page (SPEC.md §6.15) |
| Immich photos `/immich-photos` | everyone | migrating photos into Immich (SPEC.md §8.5) |
| Settings `/settings` | Owner | overview grid of areas, each with its state in a line and a status dot, and a sidebar on desktop. Lab: `lab` (name), `history`; people and access: `people`, `email`; network: `domain`, `home-network`, `public`, `cloud`; servers: `media`, `storage` (for other labs); each server: `updates`, `time-zone`, `remote-access`. The wizard's forms double as the change pages |
| Account `/account` | everyone | two-factor, password, email notifications, appearance (theme) |
| About `/about`, Design system `/design` | everyone signed in | Roostack's version and credits; the design system's gallery |
| Help `/help/` | everyone | the embedded help site |

Shared pieces (attention list, backup and DNS cards) live in `templates/parts.html`, which every page can use. Live sections refresh via `/partials/*`.

`roostack serve` serves server-rendered pages (Go `html/template`), using htmx for live updates such as the service list refreshing every 10 seconds. Protections:

- **First run:** creating the Owner needs a one-time setup code, printed in the log and saved to `/var/lib/roostack/setup-token`. The setup listener on the home network (`:8443`) serves only the first-run routes (`firstRunPaths`), answers only a `Host` that's an IP address (DNS rebinding), closes connections not between two home addresses, lets only the Owner signed in there past once the Owner exists (`setupGate`), keeps its sessions apart (`sessions.origin`), and shows **Setup is finished** instead of the wizard's last redirect, then stops and signs everything out; 24 hours after the Owner at most (SPEC §6.3.2).
- **Login:** a wrong password waits 500 ms; an unknown username costs the same password check as a known one, so timing doesn't reveal which usernames exist.
- **Sessions:** random tokens in an `HttpOnly`, `SameSite=Lax` cookie. Only a SHA-256 hash is stored.
- **Two-factor (TOTP):** Owners must enroll before using the UI. A login with 2FA first passes the password, then the code (`/login/totp`). Each code works once.
- **CSRF:** every non-GET request must carry an `Origin` header matching the host.
- **Headers:** a strict Content-Security-Policy, `nosniff`, and no framing.
- **Roles** are checked per route (`auth.Can`).

Installing or removing a service from the UI only changes desired state (an instance file); `internal/manager` then builds this server's Assignment (the services placed on it, plus any values typed into the install form, which the manager forgets once handed over) and gives it to the node, which applies it in the background, one apply at a time; the dashboard shows progress or the last error. Another server gets the same assignment over the lab API (`node.Remote`, SPEC.md §10.3); "Life of an install" above follows it step by step. Removing a service never deletes its data. A service starts private; the Owner can make it public from its page (SPEC.md §6.11).

**Package hooks** run commands inside a service's containers with `podman exec`. After an apply succeeds, `install` hooks run once, retried every 10 s for up to 10 minutes while the app starts; their progress is kept in the settings table (`hooks.<service>.install`). `action` hooks are buttons on the service page. Values from users (settings, action inputs, secrets) reach the command only as environment variables, passed as an env file on stdin, so they never appear in a command line, a unit file, or a process listing. `install_only` secrets (e.g. Forgejo's admin password) are deleted once the install hooks succeed.

## Not built yet

The main pieces still to come: an SFTP backup destination, and moving the controller and failover between servers. See [TASKS.md](TASKS.md) for everything, by phase.
