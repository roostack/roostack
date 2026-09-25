# Working Assumptions & Concerns

Code was started before the Debian 13 rootless prototype (TASKS 1.2) was run. This file lists what the code **assumes** that prototype will show, so each item can be checked on a real VM and the code adjusted.

Status: **Unverified** until checked on Debian 13. Items marked ⚠️ could force a redesign if wrong.

## A. Rootless Podman & systemd

| # | Assumption | Code that depends on it |
|---|---|---|
| A1 | A dedicated `mylab` system user runs all rootless services, with `loginctl enable-linger mylab` so its systemd user manager starts at boot. | `internal/config` (paths), reconciler |
| A2 | Quadlet files for that user go in `$HOME/.config/containers/systemd/`, with the `mylab` home set to `/var/lib/mylab/home` (kept separate from desired state). | `config.Paths.QuadletDir` |
| A3 | The `mylab` daemon (a *system* unit running as `User=mylab`) can drive the user manager with `systemctl --user` if `XDG_RUNTIME_DIR=/run/user/<uid>` is set. | `internal/systemd` |
| A4 | Unit naming: `mylab-<svc>-<container>.container` → `mylab-<svc>-<container>.service`; `mylab-<svc>.network` → `mylab-<svc>-network.service`. | `internal/generate` |
| A5 | Debian uses AppArmor, not SELinux, so bind mounts need no `:z`/`:Z` relabel options. (Fedora support would need these.) | `internal/generate` |
| A6 | Podman 5.4 Quadlet supports `Notify=healthy`, so `After=` a database unit waits until the database passes its health check. | database units |
| A7 | `GroupAdd=keep-groups` + `AddDevice=/dev/dri` gives rootless containers Intel GPU access if `mylab` is in the `render` and `video` groups (needs crun, Debian's default). | `devices: gpu` |
| A8 ⚠️ | **`NoNewPrivileges=yes` can't be used on `mylab.service`.** Rootless Podman calls the setuid helpers `newuidmap`/`newgidmap`, which NoNewPrivileges disables. SPEC §6.1 listed it as planned sandboxing. Options: (a) drop it (current unit file); (b) have `mylab` never run `podman` directly, only `systemctl --user` and `systemd-run --user` (the user manager runs podman outside the sandbox); (c) give the helpers file capabilities instead of setuid. (b) is the cleanest, but `podman secret create` and `podman exec` (backups) would need to go through the user manager. | `packaging/systemd/mylab.service` |
| A9 | System users get no `/etc/subuid` range from `useradd --system`; the installer adds `200000-265535` explicitly. | `scripts/dev-install.sh` |

## B. UID mapping & storage

| # | Assumption |
|---|---|
| B1 | Root inside a rootless container = the `mylab` user on the host, so files written by container root appear owned by `mylab`. Files written by a non-root container user (e.g. Postgres uid 999) appear as `subuid_start + uid - 1` (e.g. `100998`). |
| B2 | Backups can read every file by running restic inside `podman unshare` as `mylab`. Restores must also go through `podman unshare` to keep ownership correct. |
| B3 | `keep-id` is **not** used by default; it will be a per-container option once a package needs it. |
| B4 | Bind-mounting subdirectories inside another bind mount (Immich `/data` + `/data/thumbs`) works; mounts are emitted parent-first. |

## C. Networking ⚠️

| # | Assumption |
|---|---|
| C1 | Each service gets its own Podman network `mylab-<svc>`. Containers find each other by container name (aardvark-dns). |
| C2 | A shared `mylab-proxy` network is joined by Caddy and by every container that has a route. Caddy reaches services by container name. |
| C3 | Caddy publishes 80/443 rootless once `net.ipv4.ip_unprivileged_port_start=80` is set (by `mylab-agent`). |
| C4 ⚠️ | **Rootless port forwarding (pasta, Podman 5 default) preserves the client's real source IP.** The "private" route check (`remote_ip`) and CrowdSec both depend on this. If it's wrong, every request looks like it comes from an internal address and **private routes become reachable from the internet**. Must be verified before anything is made public. |
| C5 | "Private" means Caddy's `private_ranges` (RFC1918, loopback, `fd00::/8`) + `100.64.0.0/10` (Tailscale/CGNAT). The CGNAT range can include ISP addresses on some connections; revisit when remote access is designed. The firewall's LAN set also includes `fc00::/7` and `fe80::/10`. |
| C6 | Caddy reaches the `mylab` UI on the host at `host.containers.internal:8480`. With pasta this may need `mylab` to listen on a non-loopback address; the firewall then keeps 8480 closed to the network (only 22/80/443 are open). |
| C7 | Containers resolve each other by container name on the shared `mylab-proxy` network (Caddy → `mylab-<svc>-<container>`). |
| C8 | Changing a `.network` file and restarting its unit doesn't recreate an existing network. Rare, but the reconciler may need to remove the network first. |

## D. TLS / Caddy

| # | Assumption |
|---|---|
| D1 ✅ | One wildcard site block (`*.domain`) with a `host` matcher per route, so every route shares the wildcard certificate. Site snippets are imported from `sites/*.caddy`. **Verified with `caddy adapt` (Caddy 2.11.4 + Cloudflare plugin):** routes are mutually exclusive, the catch-all is last, the wildcard is issued via DNS-01, and an empty or missing `sites/` directory is only a warning. (Syntax only; certificate issuance still needs a real test.) |
| D2 | DNS-01 provider config differs per provider; v1 code supports Cloudflare (single API token), plus a raw override directive for anything else. The MyLab Caddy image is defined in `build/caddy/Containerfile` but not yet built or published; the unit uses `localhost/mylab-caddy:latest`. |

## E. Secrets

| # | Assumption / concern |
|---|---|
| E1 | Secrets are Podman secrets named `mylab-<svc>-<name>` (DB passwords: `mylab-<svc>-db-<db>-password`), injected with `Secret=...,type=env` or `type=mount`. |
| E2 ⚠️ | Podman's default `file` secret driver stores secret values **unencrypted** under the user's container storage. That's acceptable only because the directory is readable just by `mylab`. Revisit (e.g. `pass` driver or a MyLab driver). |

## F. Health checks

| # | Assumption / concern |
|---|---|
| F1 | `health.http` in a package **cannot** simply become a Podman `HealthCmd`, because many images have no curl/wget. For now the generator only emits `HealthCmd` for `health.cmd`. HTTP checks will be done by MyLab itself (probably through Caddy). Needs design. |

## G. Settings

| # | Concern |
|---|---|
| G1 | `default: auto` (Immich hardware acceleration) currently means "true if the host has any GPU". It may need a more specific rule, e.g. `auto: gpu.intel`. |

## I. Databases

| # | Assumption / concern |
|---|---|
| I1 | Postgres data is mounted at `/var/lib/postgresql/data`. The official Postgres 18+ images changed this layout (`/var/lib/postgresql/<major>/docker`), so the path will need to depend on the image/version. |
| I2 | Postgres reads its password from a Podman secret mounted as a file (`POSTGRES_PASSWORD_FILE`); the app gets the same secret as an env var. Assumes the default secret mount mode is readable by the Postgres user in the container. |

## J. Firewall

| # | Assumption / concern |
|---|---|
| J1 ⚠️ | SSH defaults to **LAN-only**. On a VPS that would lock the user out. The installer must ask (or detect) before applying the firewall. |
| J2 | MyLab's `inet mylab` table drops anything it doesn't accept, even if other tables accept it. Ruleset verified with `nft -c` and applied twice in a network namespace. Not yet tested alongside Podman/pasta on a real host. |

## H. Dependencies chosen without discussion

Per PLAN.md, dependencies should be chosen collaboratively. These were picked overnight to keep moving and are open for review:

| Dependency | Why | Alternative |
|---|---|---|
| `go.yaml.in/yaml/v3` | The maintained continuation of `gopkg.in/yaml.v3` (now archived); same API | `sigs.k8s.io/yaml` |

Everything else so far uses the standard library. systemd is driven via `systemctl` (not D-Bus) to avoid a dependency and keep behaviour transparent; revisit if it proves slow or fragile.
