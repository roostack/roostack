# Roostack Architecture

How the code fits together. For *why* things are the way they are, see [SPEC.md](SPEC.md).

## Processes

```
               ┌──────────────────────────── host (Debian 13) ─────────────────────────────┐
               │                                                                           │
 browser ─443──┼─▶ Caddy (own pasta netns, sees real client IPs)                           │
               │      │  169.254.1.3 = host loopback (pasta --map-host-loopback)           │
               │      ├──▶ 127.0.0.1:20000 ──▶ service container (own private network)   │
               │      └──▶ 127.0.0.1:8480  ──▶ roostack (user: roostack)                        │
               │                                  │                                        │
               │               systemctl --user,  │                                        │
               │               systemd-run --user ◀┤ (podman runs outside roostack's sandbox) │
               │                                  │ Unix socket                            │
               │                                  ▼                                        │
               │                          roostack-agent (root): sysctl, nftables, apt       │
               └───────────────────────────────────────────────────────────────────────────┘
```

- **`roostack`** (unprivileged, user `roostack`): web UI, desired state, generating and applying config. It owns every rootless container through the `roostack` user's systemd manager.
- **`roostack-agent`** (root): a small, fixed set of privileged operations, reached over `/run/roostack-agent/agent.sock`. Callers are identified by the kernel (`SO_PEERCRED`); only root and `roostack` are accepted, and only from the host's own user namespace (not a container). It runs in a systemd sandbox that lets it write only where its jobs need (SPEC §6.1).
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
2. **`internal/generate`** turns package + instance + lab config into output: Quadlet files, a Caddy snippet, required secrets, directories and a backup plan. It is **pure** (no disk access, no side effects), so it's tested with golden files and can be previewed with `roostack render`. `Summarize` uses it for the summary the install page shows before installing (what it keeps, addresses, ports, GPU, memory and the files it would write; SPEC.md §8.3.4).
3. **`internal/reconcile`** compares that output with what's on disk and builds a **plan** (files to write or remove, units to start, restart or stop, secrets to create, images to build or download). `roostack apply --dry-run` prints the plan; `roostack apply` carries it out. Image builds start in the background; new containers' images are pulled one at a time before their units start, with progress events the node turns into a service's install steps (SPEC.md §6.3.3).

The reconciler only touches files it owns (`roostack-*` Quadlets and `*.caddy` snippets) and **never deletes service data**.

## Packages

| Package | Purpose |
|---|---|
| `cmd/roostack` | Web service and CLI: `serve`, `apply`, `user`, `render`, `validate` |
| `cmd/roostack-agent` | The root helper |
| `internal/config` | Lab settings and **all default paths** in one place, including where the lab is (`network: home|internet`, SPEC.md §6.12) |
| `internal/registry` | The service registry (SPEC §8.3.3): the signed index and key statement, building them from the registry repository (`roostack registry`), and the controller's fetcher (`Updater`: at start, every 4 hours, on opening the catalog) keeping `CatalogDir`. `buildkeys` reports the root keys a build trusts, for `make build` to record and `scripts/build-deb.sh` to check |
| `internal/imagecheck` | Asks container registries which processors a pinned image has builds for (`make check-images`, the registry's index) |
| `internal/pkgspec` | The package format (`schema: roostack/v1alpha1`), its validation, and the package policy registry packages follow (SPEC §8.3.4; `Risks` is the part shown for the Owner's own) |
| `internal/instance` | A user's installed service, validated against its package |
| `internal/generate` | Desired state → Quadlets, Caddy config, secrets, dirs, backup plan; the install summary |
| `internal/quadlet` | Writing Quadlet/systemd unit files, with escaping |
| `internal/caddy` | Writing the main Caddyfile and per-service snippets |
| `internal/dns` | Supported DNS providers: credential fields, Caddy directive, and the create-and-delete-a-TXT-record check |
| `internal/dnsrecords` | Keeps `*.<domain>` pointing at the LAN IP (only records Roostack created) and checks home devices can resolve it; points each public address at the public entry's address once CrowdSec is up |
| `internal/public` | Public access (SPEC.md §6.11): the entry visitors come in through (port forwarding: the lab's public address, found over DNS), CrowdSec's health, check links opened from outside, and guessing whether the server is on the internet (§6.12) |
| `internal/setup` | First-run setup wizard logic: time zone, domain and DNS credentials, backup destination, recovery key |
| `internal/hosttime` | Reads the host's time zone and the valid zone names (tzdata); changing it is the agent's job |
| `internal/reconcile` | Plan and apply changes to the host |
| `internal/state` | Load desired state from disk; keep and prune the lab's copies of its packages (SPEC §8.3.1); read the catalog's newest version of a package; keep the Owner's own packages (SPEC §8.3.4) |
| `internal/pkgtest` | For tests: finds the package fixtures in `internal/testdata/packages` and copies them into a catalog |
| `internal/systemd` | `systemctl --user` wrapper |
| `internal/podman` | The few direct `podman` calls (secrets, `exec` for package hooks) |
| `internal/agent` | Agent server and client: firewall ruleset, time zone, OS updates, reboot, WireGuard interfaces, backup drives and folders, SSH's keys-only drop-in and key logins (`ssh.go`), and checks for host changes Roostack didn't make |
| `internal/web/static/css`, `docs/DESIGN.md` | Design system: tokens, built-in themes, components; gallery at `/design` and `docs/design/gallery.html` |
| `internal/nodeid` | This server's identity (node ID and name) |
| `authkit` | Passwords (argon2id), tokens and two-factor codes (TOTP), public so Roostack Cloud's repository uses the same code; Cloud also turns on a pepper and sealed two-factor secrets. Fuzz-tested (the inputs fuzzing found are kept in `testdata/fuzz`) |
| `awssig` | AWS Signature Version 4 with the standard library only (Route 53, S3-compatible storage; checked against the AWS SDK's signer): public, so Roostack Cloud signs Wasabi's STS and S3 requests with the same code |
| `cloudapi` | The API between a lab and Roostack Cloud (paths, bodies, the records and acme-dns conventions): public, not under `internal`, so Cloud's own repository imports the same contract |
| `internal/cloud` | The controller's link to Roostack Cloud (SPEC.md §6.6): linking (a link code, Cloud's page, polling), the lab's client certificate for mutual TLS (renewed with a new key), the daily check-in, unlinking, claiming a roost.ac name (then the lab's DNS provider is Roostack Cloud, `internal/dns/cloud.go`), Roostack Cloud backup's keys and the lab's use of its space, the recovery identity (registered with Cloud; used to restore from Cloud and to relink, `recovery.go`), and the request log, kept in `<state>/cloud/`. `cloudtest` is a fake of Cloud's lab API for Roostack's tests (dev VMs link to a development Cloud from its own repository) |
| `internal/labnet` | The network between a lab's servers (SPEC.md §10.2, §10.9): the controller's CA and each server's certificate, join codes, membership (role and overlay address), the hub (WireGuard peers, joining) and the mutual-TLS lab API server |
| `internal/node` | A server in the lab (SPEC.md §10): the `Node` interface and `Local`, which applies its Assignment (reconcile, firewall), runs package hooks, actions and secret reveals, and holds the server's maintenance jobs (backups, restores, OS updates and restarts), its own settings (time zone, SSH access and keys only, restart policy) and activity, and keeps its finished logs as history (`events`, SPEC.md §6.14). `Remote` is another server over the lab API (`Server` serves it on each node); `Fleet` keeps one per joined server |
| `internal/activity` | Live logs of host operations (OS updates, backups, applies, setup hooks) for the UI: a status line plus collapsible details |
| `internal/osupdate` | Nightly OS updates job, restart-needed detection, "restart now" |
| `internal/firewall` | Builds the firewall from the lab and its services; lists current SSH connections |
| `internal/store` | SQLite runtime data: users, sessions, audit log, settings, backup runs, the lab's servers and join codes, backup providers, targets and copies, and storage for other labs |
| `internal/auth` | Roles and usernames (passwords, tokens and two-factor codes are `authkit`) |
| `internal/web` | Web UI: handlers, templates, vendored htmx/Alpine, and the embedded help site at `/help/` (`help.go`, built from `help/` into `helpsite/site` by `make build`) |
| `internal/errpage` | The error scenes (generated by `assets/errors/generate.py`) and the services' error pages that Caddy serves (SPEC.md §6.3.4); Roostack's own error pages are in `internal/web/errors.go` |
| `internal/manager` | The controller: catalog (with the Owner's own packages), the package policy at install, install, remove, ports and settings (desired state), public addresses, recovery key changes, moving and restoring services across servers (SPEC §10.10), updating services to newer package versions (`update.go`, SPEC §7.1), and each server's Assignment |
| `internal/maintenance` | Maintenance coordinator: job queue and reboot policy |
| `internal/backup` | restic wrapper, prepare steps, backup plan and job |
| `internal/b2` | Backblaze B2 application keys limited to one server's folder, made and deleted over B2's native API |
| `internal/s3api` | The few S3 calls Roostack makes beside restic (path-style, signed with `awssig`): a bucket's versioning and lifecycle rules, the probe that shows a key can't erase old versions, and listing and copying back old versions to roll a server's folder back (Backblaze's S3-compatible API too); `s3api/s3fake` is an in-memory versioned bucket for tests |
| `internal/mail` | Sending email through the lab's SMTP server (SPEC.md §6.13): settings in lab.yaml, the password in its own 0600 file, STARTTLS or TLS with the standard library's client |
| `internal/units` | The one size formatter (decimal units: 1 GB = 1000³ bytes) |
| `internal/cloudkeys` | Roostack Cloud backup's short-lived keys on a server (SPEC.md §6.9): kept 0600 in the state folder, renewed with under an hour left, passed to restic as environment variables. The controller's come from Cloud, other servers' from the controller (`POST /v1/storage-keys` on the lab API) |
| `internal/labrestore` | Rebuilding a whole lab on a new server (SPEC.md §6.9): finding its backups (a folder, an S3-compatible bucket or any restic address) with the recovery key, restoring the controller's backup, then its database at the next start and the services' databases once they're set up again |

Rules that keep this maintainable:
- **Business logic stays out of HTTP handlers**, so the UI can change without touching the core (SPEC.md §6.3).
- **Side effects happen behind small interfaces** (`reconcile.Systemd`, `reconcile.Secrets`, `agent.Host`), so the logic is tested with fakes.
- **Commands shell out to standard tools** (`systemctl`, `podman`, `nft`), so every action matches something a person could run to debug it.

## Generated names

For a service `<svc>`:

| Thing | Name |
|---|---|
| Service network | `roostack-<svc>` (file `roostack-<svc>.network`) |
| Container | `roostack-<svc>-<container>` (unit `roostack-<svc>-<container>.service`) |
| Route port | `127.0.0.1:<port>`, from 20000–29999, stored as `routes.<name>.port` in the instance file |
| Database container | `roostack-<svc>-db-<name>` |
| Secret | `roostack-<svc>-<setting>`, or `roostack-<svc>-db-<name>-password` |
| Caddy snippet | `sites/<svc>.caddy` |
| Data | `/srv/roostack/services/<svc>/<storage>/` or `/srv/roostack/media/<svc>/<storage>/` |
| Database data | `/srv/roostack/services/<svc>/db/<name>/` |
| Database dumps (for backup) | `/srv/roostack/services/<svc>/db-dump/` |

## Package format

Packages live in the registry repository, [`roostack/services`](https://github.com/roostack/services) (SPEC.md §8.3), as `packages/<name>/<version>.yaml` beside `icon.png` and `registry.yaml`. The registry's CI builds a signed index from it (`roostack registry index`), published at `https://registry.roostack.cloud/v1/`; the controller fetches the index every few hours and when the catalog opens, checks its signatures, and keeps the version of each package it would install, with its icon, in `/var/lib/roostack/catalog` (`config.Paths.CatalogDir`), where the catalog reads them ([internal/registry](internal/registry); the keys: [docs/REGISTRY-KEYS.md](docs/REGISTRY-KEYS.md)). After each check the manager compares installed services' versions with the index's withdrawals ([internal/manager/withdrawals.go](internal/manager/withdrawals.go)): a stop marks the service in its server's assignment (`state.Service.Stop`), and `generate.Stop` leaves out its containers and site (SPEC.md §8.3.4). Updates to the offered versions ([internal/manager/update.go](internal/manager/update.go), SPEC.md §7.1) are driven by the controller: the service's server downloads the new images, backs up and holds its maintenance queue (`node.Updater`, [internal/node/update.go](internal/node/update.go); `/v1/updates` and `/v1/services/{service}/health` on the lab API), while the controller switches the instance's version, applies, checks the service and goes back to the previous version if the check fails. Copies of Vaultwarden (simple), Forgejo and Immich (complex) are the tests' fixtures, in [internal/testdata/packages](internal/testdata/packages) (found through `internal/pkgtest`). Field-by-field definitions are in [internal/pkgspec/pkgspec.go](internal/pkgspec/pkgspec.go), and the facts about the project (`about:`, `requires:`, upgrade notes) in [internal/pkgspec/about.go](internal/pkgspec/about.go).

Templates use Go `text/template` syntax with these values:

| Value | Example |
|---|---|
| `.Routes.<route>.Host` / `.URL` | `photos.lab.example.com` / `https://photos.lab.example.com` |
| `.Settings.<key>` | Non-secret settings only, so secrets can never end up in a unit file |
| `.Databases.<name>.Host` / `.Port` / `.User` / `.Name` | Connection details for Roostack-managed databases |
| `(index .Containers "<name>").Host` | Another container in the same service |
| `.Lab.Domain`, `.Service.Name` | Lab and service names |

## Web UI

Pages (top navigation; links hidden where the role can't use them):

| Page | Who | Contents |
|---|---|---|
| Home `/` | everyone | "Needs attention" list (problems gathered from setup, applying changes, backups, DNS and 2FA, filtered by role, each linking to where it's fixed), then service tiles. Shows "Everything's working" when there's nothing to do |
| Services `/services`, `/services/{name}` | view: everyone; manage: Admin+ | list, catalog/install, per-service page (incl. turning package ports on), remove |
| Backups `/backups` | Admin+ | status, "Back up now", destination, restore notes |
| Settings `/settings` | Owner | overview grid: each area's state in a line with a status dot; area pages `/settings/{people,remote-access,domain,home-network,updates,time-zone}` with a sidebar on desktop. The wizard's forms double as the change pages |
| Account `/account` | everyone | two-factor status, change password |

Shared pieces (attention list, backup and DNS cards) live in `templates/parts.html`, which every page can use. Live sections refresh via `/partials/*`.

`roostack serve` serves server-rendered pages (Go `html/template`), using htmx for live updates such as the service list refreshing every 10 seconds. Protections:

- **First run:** creating the Owner needs a one-time setup code, printed in the log and saved to `/var/lib/roostack/setup-token`.
- **Login:** a wrong password waits 500 ms; an unknown username costs the same password check as a known one, so timing doesn't reveal which usernames exist.
- **Sessions:** random tokens in an `HttpOnly`, `SameSite=Lax` cookie. Only a SHA-256 hash is stored.
- **Two-factor (TOTP):** Owners must enroll before using the UI. A login with 2FA first passes the password, then the code (`/login/totp`). Each code works once.
- **CSRF:** every non-GET request must carry an `Origin` header matching the host.
- **Headers:** a strict Content-Security-Policy, `nosniff`, and no framing.
- **Roles** are checked per route (`auth.Can`).

Installing or removing a service from the UI only changes desired state (an instance file); `internal/manager` then builds this server's Assignment (the services placed on it, plus any values typed into the install form, which the manager forgets once handed over) and gives it to the node, which applies it in the background, one apply at a time; the dashboard shows progress or the last error. Assignments are the seam for more servers: a remote node will receive the same thing over Roostack's network. Removing a service never deletes its data. For now, services installed from the UI are always private.

**Package hooks** run commands inside a service's containers with `podman exec`. After an apply succeeds, `install` hooks run once, retried every 10 s for up to 10 minutes while the app starts; their progress is kept in the settings table (`hooks.<service>.install`). `action` hooks are buttons on the service page. Values from users (settings, action inputs, secrets) reach the command only as environment variables, passed as an env file on stdin, so they never appear in a command line, a unit file or a process listing. `install_only` secrets (e.g. Forgejo's admin password) are deleted once the install hooks succeed.

## Not built yet

The main pieces still to come: public exposure (with CrowdSec), an SFTP backup destination, the apt repository and signing, moving the controller and failover between servers, and edge servers for public services at home. See [TASKS.md](TASKS.md) for everything, by phase.
