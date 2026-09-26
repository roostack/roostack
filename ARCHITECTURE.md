# MyLab Architecture

How the code fits together. For *why* things are the way they are, see [SPEC.md](SPEC.md).

## Processes

```
               ┌──────────────────────────── host (Debian 13) ─────────────────────────────┐
               │                                                                           │
 browser ─443──┼─▶ Caddy (own pasta netns, sees real client IPs)                           │
               │      │  169.254.1.3 = host loopback (pasta --map-host-loopback)           │
               │      ├──▶ 127.0.0.1:20000 ──▶ service container (own private network)   │
               │      └──▶ 127.0.0.1:8480  ──▶ mylab (user: mylab)                        │
               │                                  │                                        │
               │               systemctl --user,  │                                        │
               │               systemd-run --user ◀┤ (podman runs outside mylab's sandbox) │
               │                                  │ Unix socket                            │
               │                                  ▼                                        │
               │                          mylab-agent (root): sysctl, nftables, apt       │
               └───────────────────────────────────────────────────────────────────────────┘
```

- **`mylab`** (unprivileged, user `mylab`): web UI, desired state, generating and applying config. It owns every rootless container through the `mylab` user's systemd manager.
- **`mylab-agent`** (root): a small, fixed set of privileged operations, reached over `/run/mylab-agent.sock`. Callers are identified by the kernel (`SO_PEERCRED`); only root and `mylab` are accepted.
- **Caddy** (rootless container): the only thing listening on 80/443. It runs in its own pasta network namespace so it sees visitors' real IP addresses. It reaches routed containers, which are published on `127.0.0.1:<port>`, through pasta's loopback mapping (`169.254.1.3`). See SPEC.md §6.5 for why.

## Data flow: desired state → running services

```
lab.yaml ─────────────┐
services/<name>.yaml ─┼─▶ state ─▶ generate ─▶ reconcile ─▶ files + systemd
catalog/<name>/package.yaml ┘      (pure)      (plan → apply)
```

1. **`internal/state`** loads the lab config, every instance file, and each instance's package from the catalog. It also checks lab-wide rules (e.g. unique hostnames).
2. **`internal/generate`** turns package + instance + lab config into output: Quadlet files, a Caddy snippet, required secrets, directories and a backup plan. It is **pure** (no disk access, no side effects), so it's tested with golden files and can be previewed with `mylab render`.
3. **`internal/reconcile`** compares that output with what's on disk and builds a **plan** (files to write or remove, units to start, restart or stop, secrets to create). `mylab apply --dry-run` prints the plan; `mylab apply` carries it out.

The reconciler only touches files it owns (`mylab-*` Quadlets and `*.caddy` snippets) and **never deletes service data**.

## Packages

| Package | Purpose |
|---|---|
| `cmd/mylab` | Web service and CLI: `serve`, `apply`, `user`, `render`, `validate` |
| `cmd/mylab-agent` | The root helper |
| `internal/config` | Lab settings and **all default paths** in one place |
| `internal/pkgspec` | The package format (`schema: mylab/v1alpha1`) and its validation |
| `internal/instance` | A user's installed service, validated against its package |
| `internal/generate` | Desired state → Quadlets, Caddy config, secrets, dirs, backup plan |
| `internal/quadlet` | Writing Quadlet/systemd unit files, with escaping |
| `internal/caddy` | Writing the main Caddyfile and per-service snippets |
| `internal/dns` | Supported DNS providers: credential fields, Caddy directive, and the create-and-delete-a-TXT-record check |
| `internal/setup` | First-run setup wizard logic: domain and DNS credentials, backup destination, recovery key |
| `internal/reconcile` | Plan and apply changes to the host |
| `internal/state` | Load desired state from disk |
| `internal/systemd` | `systemctl --user` wrapper |
| `internal/podman` | The few direct `podman` calls (secrets) |
| `internal/agent` | Agent server, client, and firewall ruleset |
| `internal/store` | SQLite runtime data: users, sessions, audit log |
| `internal/auth` | Password hashing (argon2id), session tokens, roles |
| `internal/web` | Web UI: handlers, templates, vendored htmx/Alpine |
| `internal/manager` | Business logic behind the UI: catalog, install, remove, background apply |
| `internal/maintenance` | Maintenance coordinator: job queue and reboot policy |
| `internal/backup` | restic wrapper, prepare steps, backup plan and job |

Rules that keep this maintainable:
- **Business logic stays out of HTTP handlers**, so the UI can change without touching the core (SPEC.md §6.3).
- **Side effects happen behind small interfaces** (`reconcile.Systemd`, `reconcile.Secrets`, `agent.Host`), so the logic is tested with fakes.
- **Commands shell out to standard tools** (`systemctl`, `podman`, `nft`), so every action matches something a person could run to debug it.

## Generated names

For a service `<svc>`:

| Thing | Name |
|---|---|
| Service network | `mylab-<svc>` (file `mylab-<svc>.network`) |
| Container | `mylab-<svc>-<container>` (unit `mylab-<svc>-<container>.service`) |
| Route port | `127.0.0.1:<port>`, from 20000–29999, stored as `routes.<name>.port` in the instance file |
| Database container | `mylab-<svc>-db-<name>` |
| Secret | `mylab-<svc>-<setting>`, or `mylab-<svc>-db-<name>-password` |
| Caddy snippet | `sites/<svc>.caddy` |
| Data | `/srv/mylab/services/<svc>/<storage>/` or `/srv/mylab/media/<svc>/<storage>/` |
| Database data | `/srv/mylab/services/<svc>/db/<name>/` |
| Database dumps (for backup) | `/srv/mylab/services/<svc>/db-dump/` |

## Package format

See [docs/examples/vaultwarden.yaml](docs/examples/vaultwarden.yaml) (simple) and [docs/examples/immich.yaml](docs/examples/immich.yaml) (complex). Field-by-field definitions are in [internal/pkgspec/pkgspec.go](internal/pkgspec/pkgspec.go).

Templates use Go `text/template` syntax with these values:

| Value | Example |
|---|---|
| `.Routes.<route>.Host` / `.URL` | `photos.lab.example.com` / `https://photos.lab.example.com` |
| `.Settings.<key>` | Non-secret settings only, so secrets can never end up in a unit file |
| `.Databases.<name>.Host` / `.Port` / `.User` / `.Name` | Connection details for MyLab-managed databases |
| `(index .Containers "<name>").Host` | Another container in the same service |
| `.Lab.Domain`, `.Service.Name` | Lab and service names |

## Web UI

`mylab serve` serves server-rendered pages (Go `html/template`), using htmx for live updates such as the service list refreshing every 10 seconds. Protections:

- **First run:** creating the Owner needs a one-time setup code, printed in the log and saved to `/var/lib/mylab/setup-token`.
- **Sessions:** random tokens in an `HttpOnly`, `SameSite=Lax` cookie. Only a SHA-256 hash is stored.
- **Two-factor (TOTP):** Owners must enroll before using the UI. A login with 2FA first passes the password, then the code (`/login/totp`). Each code works once.
- **CSRF:** every non-GET request must carry an `Origin` header matching the host.
- **Headers:** a strict Content-Security-Policy, `nosniff`, and no framing.
- **Roles** are checked per route (`auth.Can`).

Installing or removing a service from the UI only changes desired state (an instance file); `internal/manager` then applies it in the background, one apply at a time, and the dashboard shows progress or the last error. Removing a service never deletes its data. For now, services installed from the UI are always private.

## Not built yet

2FA QR codes and recovery codes, managing services and users from the UI, backups, the maintenance coordinator, host OS updates, packaging (.deb). See [TASKS.md](TASKS.md).
