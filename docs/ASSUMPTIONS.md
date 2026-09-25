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
| C5 | "Private" means RFC1918 + loopback + IPv6 ULA/link-local + `100.64.0.0/10` (Tailscale/CGNAT). The CGNAT range can include ISP addresses on some connections; revisit when remote access is designed. |

## D. TLS / Caddy

| # | Assumption |
|---|---|
| D1 | One wildcard site block (`*.domain`) with a `host` matcher per route, so every route shares the wildcard certificate. Site snippets are imported from `sites/*.caddy`. |
| D2 | DNS-01 provider config differs per provider; v1 code supports Cloudflare, plus a raw override directive for anything else. The MyLab Caddy image (with plugins) isn't built yet. |

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

## H. Dependencies chosen without discussion

Per PLAN.md, dependencies should be chosen collaboratively. These were picked overnight to keep moving and are open for review:

| Dependency | Why | Alternative |
|---|---|---|
| `go.yaml.in/yaml/v3` | The maintained continuation of `gopkg.in/yaml.v3` (now archived); same API | `sigs.k8s.io/yaml` |

Everything else so far uses the standard library. systemd is driven via `systemctl` (not D-Bus) to avoid a dependency and keep behaviour transparent; revisit if it proves slow or fragile.
