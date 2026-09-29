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
- **`roostack-agent`** (root): a small, fixed set of privileged operations, reached over `/run/roostack-agent.sock`. Callers are identified by the kernel (`SO_PEERCRED`); only root and `roostack` are accepted.
- **Caddy** (rootless container): the only thing listening on 80/443. It runs in its own pasta network namespace so it sees visitors' real IP addresses. It reaches routed containers, which are published on `127.0.0.1:<port>`, through pasta's loopback mapping (`169.254.1.3`). See SPEC.md §6.5 for why.

## Data flow: desired state → running services

```
lab.yaml ─────────────┐
services/<name>.yaml ─┼─▶ state ─▶ generate ─▶ reconcile ─▶ files + systemd
catalog/<name>/package.yaml ┘      (pure)      (plan → apply)
```

1. **`internal/state`** loads the lab config, every instance file, and each instance's package from the catalog. It also checks lab-wide rules (e.g. unique hostnames).
2. **`internal/generate`** turns package + instance + lab config into output: Quadlet files, a Caddy snippet, required secrets, directories and a backup plan. It is **pure** (no disk access, no side effects), so it's tested with golden files and can be previewed with `roostack render`.
3. **`internal/reconcile`** compares that output with what's on disk and builds a **plan** (files to write or remove, units to start, restart or stop, secrets to create). `roostack apply --dry-run` prints the plan; `roostack apply` carries it out.

The reconciler only touches files it owns (`roostack-*` Quadlets and `*.caddy` snippets) and **never deletes service data**.

## Packages

| Package | Purpose |
|---|---|
| `cmd/roostack` | Web service and CLI: `serve`, `apply`, `user`, `render`, `validate` |
| `cmd/roostack-agent` | The root helper |
| `internal/config` | Lab settings and **all default paths** in one place |
| `internal/pkgspec` | The package format (`schema: roostack/v1alpha1`) and its validation |
| `internal/instance` | A user's installed service, validated against its package |
| `internal/generate` | Desired state → Quadlets, Caddy config, secrets, dirs, backup plan |
| `internal/quadlet` | Writing Quadlet/systemd unit files, with escaping |
| `internal/caddy` | Writing the main Caddyfile and per-service snippets |
| `internal/dns` | Supported DNS providers: credential fields, Caddy directive, and the create-and-delete-a-TXT-record check |
| `internal/awssig` | AWS Signature Version 4, standard library only (Route 53 checks, listing S3-compatible buckets) |
| `internal/dnsrecords` | Keeps `*.<domain>` pointing at the LAN IP (only records Roostack created) and checks home devices can resolve it; points each public address at the public entry's address once CrowdSec is up |
| `internal/public` | Public access (SPEC.md §6.11): the entry visitors come in through (port forwarding: the home's public address, found over DNS), CrowdSec's health, and check links opened from a phone on mobile data |
| `internal/setup` | First-run setup wizard logic: time zone, domain and DNS credentials, backup destination, recovery key |
| `internal/hosttime` | Reads the host's time zone and the valid zone names (tzdata); changing it is the agent's job |
| `internal/reconcile` | Plan and apply changes to the host |
| `internal/state` | Load desired state from disk |
| `internal/systemd` | `systemctl --user` wrapper |
| `internal/podman` | The few direct `podman` calls (secrets, `exec` for package hooks) |
| `internal/agent` | Agent server and client: firewall ruleset, time zone, OS updates, reboot, WireGuard interfaces, backup drives and folders, and checks for host changes Roostack didn't make |
| `internal/web/static/css`, `docs/DESIGN.md` | Design system: tokens, built-in themes, components; gallery at `/design` and `docs/design/gallery.html` |
| `internal/nodeid` | This server's identity (node ID and name) |
| `internal/labnet` | The network between a lab's servers (SPEC.md §10.2, §10.9): the controller's CA and each server's certificate, join codes, membership (role and overlay address), the hub (WireGuard peers, joining) and the mutual-TLS lab API server |
| `internal/node` | A server in the lab (SPEC.md §10): the `Node` interface and `Local`, which applies its Assignment (reconcile, firewall), runs package hooks, actions and secret reveals, and holds the server's maintenance jobs (backups, restores, OS updates and restarts), its own settings (time zone, SSH access, restart policy) and activity. `Remote` is another server over the lab API (`Server` serves it on each node); `Fleet` keeps one per joined server |
| `internal/activity` | Live logs of host operations (OS updates, backups, applies, setup hooks) for the UI: a status line plus collapsible details |
| `internal/osupdate` | Nightly OS updates job, restart-needed detection, "restart now" |
| `internal/firewall` | Builds the firewall from the lab and its services; lists current SSH connections |
| `internal/store` | SQLite runtime data: users, sessions, audit log, settings, backup runs, the lab's servers and join codes, backup providers, targets and copies, and storage for other labs |
| `internal/auth` | Password hashing (argon2id), session tokens, roles |
| `internal/web` | Web UI: handlers, templates, vendored htmx/Alpine, and the embedded help site at `/help/` (`help.go`, built from `help/` into `helpsite/site` by `make build`) |
| `internal/manager` | The controller: catalog, install, remove, ports and settings (desired state), and each server's Assignment |
| `internal/maintenance` | Maintenance coordinator: job queue and reboot policy |
| `internal/backup` | restic wrapper, prepare steps, backup plan and job |
| `internal/b2` | Backblaze B2 application keys limited to one server's folder, made and deleted over B2's native API |
| `internal/units` | The one size formatter (decimal units: 1 GB = 1000³ bytes) |
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

See [docs/examples/vaultwarden.yaml](docs/examples/vaultwarden.yaml) (simple) and [docs/examples/immich.yaml](docs/examples/immich.yaml) (complex). Field-by-field definitions are in [internal/pkgspec/pkgspec.go](internal/pkgspec/pkgspec.go).

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
