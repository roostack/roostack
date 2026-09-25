# MyLab Architecture

How the code fits together. For *why* things are the way they are, see [SPEC.md](SPEC.md).

## Processes

```
                ┌─────────────────────── host (Debian 13) ─────────────────────────┐
                │                                                                  │
  browser ──443─┼─▶ Caddy container ──mylab-proxy network──▶ service containers   │
                │        │                                     (rootless Podman)   │
                │        └─ host.containers.internal:8480 ─▶ mylab (user: mylab)   │
                │                                                │                 │
                │                     systemctl --user, podman ◀─┤                 │
                │                                                │ Unix socket     │
                │                                                ▼                 │
                │                                     mylab-agent (root)           │
                │                                     sysctl, nftables, apt        │
                └──────────────────────────────────────────────────────────────────┘
```

- **`mylab`** (unprivileged, user `mylab`): web UI, desired state, generating and applying config. It owns every rootless container through the `mylab` user's systemd manager.
- **`mylab-agent`** (root): a small, fixed set of privileged operations, reached over `/run/mylab-agent.sock`. Callers are identified by the kernel (`SO_PEERCRED`); only root and `mylab` are accepted.
- **Caddy** (rootless container): the only thing listening on 80/443. It routes subdomains to service containers over the shared `mylab-proxy` network.

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
| `cmd/mylab` | CLI and (soon) the web service: `apply`, `render`, `validate` |
| `cmd/mylab-agent` | The root helper |
| `internal/config` | Lab settings and **all default paths** in one place |
| `internal/pkgspec` | The package format (`schema: mylab/v1alpha1`) and its validation |
| `internal/instance` | A user's installed service, validated against its package |
| `internal/generate` | Desired state → Quadlets, Caddy config, secrets, dirs, backup plan |
| `internal/quadlet` | Writing Quadlet/systemd unit files, with escaping |
| `internal/caddy` | Writing the main Caddyfile and per-service snippets |
| `internal/reconcile` | Plan and apply changes to the host |
| `internal/state` | Load desired state from disk |
| `internal/systemd` | `systemctl --user` wrapper |
| `internal/podman` | The few direct `podman` calls (secrets) |
| `internal/agent` | Agent server, client, and firewall ruleset |

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

## Not built yet

Web UI and users, backups, the maintenance coordinator, host OS updates, packaging (.deb). See [TASKS.md](TASKS.md).
