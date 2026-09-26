# Working Assumptions & Concerns

Code was started before the Debian 13 rootless prototype (TASKS 1.2) was run. This file lists what the code **assumes** that prototype will show, so each item can be checked on a real VM and the code adjusted.

Status: items are **unverified** unless marked. ✅ = verified on the Debian 13 dev VM (`scripts/dev-vm.sh`, Podman 5.4.2, 2026-09-26). ❌ = failed. ⚠️ = could force a redesign if wrong.

## A. Rootless Podman & systemd

| # | Assumption | Code that depends on it |
|---|---|---|
| A1 ✅ | A dedicated `mylab` system user runs all rootless services, with `loginctl enable-linger mylab` so its systemd user manager starts at boot. | `internal/config` (paths), reconciler **Verified:** linger starts the user manager at boot; units run under it. **Survives a reboot:** after `systemctl reboot` with nobody logged in, mylab, mylab-agent, Caddy and Vaultwarden were all running again and the port sysctl was persisted. |
| A2 | Quadlet files for that user go in `$HOME/.config/containers/systemd/`, with the `mylab` home set to `/var/lib/mylab/home` (kept separate from desired state). | `config.Paths.QuadletDir` |
| A3 ✅ | The `mylab` daemon (a *system* unit running as `User=mylab`) can drive the user manager with `systemctl --user` if `XDG_RUNTIME_DIR=/run/user/<uid>` is set. | `internal/systemd` **Verified:** works with `XDG_RUNTIME_DIR` **and** `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus`; podman needs the latter for systemd cgroups outside a login session. |
| A4 ✅ | Unit naming: `mylab-<svc>-<container>.container` → `mylab-<svc>-<container>.service`; `mylab-<svc>.network` → `mylab-<svc>-network.service`. | `internal/generate` **Verified:** `mylab-<svc>-network.service` etc. as expected. |
| A5 | Debian uses AppArmor, not SELinux, so bind mounts need no `:z`/`:Z` relabel options. (Fedora support would need these.) | `internal/generate` |
| A6 | Podman 5.4 Quadlet supports `Notify=healthy`, so `After=` a database unit waits until the database passes its health check. | database units |
| A7 | `GroupAdd=keep-groups` + `AddDevice=/dev/dri` gives rootless containers Intel GPU access if `mylab` is in the `render` and `video` groups (needs crun, Debian's default). | `devices: gpu` |
| A8 ✅ decided + verified | **Verified on the VM:** `mylab.service` runs with `NoNewPrivileges=yes`; `mylab apply` inside such a sandbox creates secrets via `systemd-run --user`. Direct podman under NoNewPrivileges is unreliable: `podman unshare` worked only because podman's pause process already held the namespace, while a fresh `podman run` failed. **Decided (option b, SPEC §6.1):** `mylab` never runs podman directly, only `systemctl --user` / `systemd-run --user`. Background: **`NoNewPrivileges=yes` can't be used on `mylab.service`** Rootless Podman calls the setuid helpers `newuidmap`/`newgidmap`, which NoNewPrivileges disables. SPEC §6.1 listed it as planned sandboxing. Options: (a) drop it (current unit file); (b) have `mylab` never run `podman` directly, only `systemctl --user` and `systemd-run --user` (the user manager runs podman outside the sandbox); (c) give the helpers file capabilities instead of setuid. (b) is the cleanest, but `podman secret create` and `podman exec` (backups) would need to go through the user manager. | `packaging/systemd/mylab.service` |
| A9 ✅ | System users get no `/etc/subuid` range from `useradd --system`; the installer adds `200000-265535` explicitly. | `scripts/dev-install.sh` **Verified:** `mylab:200000:65536` added; rootless podman works. |

## B. UID mapping & storage

| # | Assumption |
|---|---|
| B1 ✅ | Root inside a rootless container = the `mylab` user on the host, so files written by container root appear owned by `mylab`. Files written by a non-root container user (e.g. Postgres uid 999) appear as `subuid_start + uid - 1` (e.g. `100998`). **Verified:** container root → uid 999 (`mylab`) on the host. |
| B2 ✅ | Backups can read every file by running restic inside `podman unshare` as `mylab`. Restores must also go through `podman unshare` to keep ownership correct. **Verified:** restic runs via `systemd-run --user` + `podman unshare`; a restore inside `podman unshare` brought files back owned by `mylab` (uid 999), and the restored Vaultwarden database opened cleanly. Snapshot contents checked: live SQLite files, the backup key and container images excluded; dumps, state and Podman secrets included. |
| B3 | `keep-id` is **not** used by default; it will be a per-container option once a package needs it. |
| B4 | Bind-mounting subdirectories inside another bind mount (Immich `/data` + `/data/thumbs`) works; mounts are emitted parent-first. |

## C. Networking ⚠️

| # | Assumption |
|---|---|
| C1 ✅ | Each service gets its own Podman network `mylab-<svc>`. Containers find each other by container name (aardvark-dns). |
| C2 (superseded) | ~~A shared `mylab-proxy` network~~, replaced by loopback ports (SPEC §6.5). |
| C3 ✅ | Caddy publishes 80/443 rootless once `net.ipv4.ip_unprivileged_port_start=80` is set (by `mylab-agent`). **Verified:** Caddy listens on 80/443 rootless; HTTP redirects to HTTPS. |
| C4 ❌ **FAILED on Debian 13 (2026-09-26)** | Tested with a `whoami` container on Podman 5.4.2 (pasta). **Default pasta network:** the real client IP is preserved ✅. **Named bridge network (our Caddy design):** every client appears as `10.89.0.x` ❌. That address is inside `10.0.0.0/8`, so Caddy's "private only" check would **let the whole internet through**. **Host network (`--network host`):** the real IP is preserved ✅. Connections from the host itself arrive as `169.254.1.2`, outside the private ranges, so they are safely treated as external. See "Proposed fix" below. |
| C4 ✅ fixed (Option C, SPEC §6.5) | **Verified with the real Caddy on the VM:** Caddy on pasta sees the real client IP (`10.0.2.2`); it reaches Vaultwarden at `169.254.1.3:20000` and the UI at `169.254.1.3:8480`; ports 80/443 are now handled by pasta, not `rootlessport`; the UI isn't reachable from outside the VM. Original proposal: Run Caddy with `Network=host`. Each routed container publishes on `127.0.0.1:<allocated port>` (still on its own service network for database access), and Caddy proxies to `127.0.0.1:<port>`. **Verified in the VM:** host-network proxy sees the real client IP; loopback-published ports work from the host network and are not reachable from the LAN. Also lets the MyLab UI listen on `127.0.0.1:8480` only (fixes C6/K2). Cost: MyLab must allocate and store a stable port per route (e.g. in the instance file), and the `mylab-proxy` network goes away. |
| C5 | "Private" means Caddy's `private_ranges` (RFC1918, loopback, `fd00::/8`) + `100.64.0.0/10` (Tailscale/CGNAT). The CGNAT range can include ISP addresses on some connections; revisit when remote access is designed. The firewall's LAN set also includes `fc00::/7` and `fe80::/10`. |
| C6 ✅ (superseded) | Caddy reaches the `mylab` UI on the host at `host.containers.internal:8480`. With pasta this may need `mylab` to listen on a non-loopback address; the firewall then keeps 8480 closed to the network (only 22/80/443 are open). **Verified:** Caddy → `host.containers.internal:8480` reaches the UI. |
| C7 (superseded) | Caddy no longer resolves containers by name; it uses loopback ports. Containers in the same service still resolve each other by name (C1). |
| C9 | Removing `mylab-proxy.network` stopped its unit, but Podman doesn't delete the network itself (harmless leftover on existing installs). |
| C10 | Traffic from the host itself reaches Caddy with the host's own LAN address, so it counts as private. That's intended. |
| C8 | Changing a `.network` file and restarting its unit doesn't recreate an existing network. Rare, but the reconciler may need to remove the network first. |

## D. TLS / Caddy

| # | Assumption |
|---|---|
| D1 ✅ | One wildcard site block (`*.domain`) with a `host` matcher per route, so every route shares the wildcard certificate. Site snippets are imported from `sites/*.caddy`. **Verified with `caddy adapt` (Caddy 2.11.4 + Cloudflare plugin):** routes are mutually exclusive, the catch-all is last, the wildcard is issued via DNS-01, and an empty or missing `sites/` directory is only a warning. **Real issuance verified (2026-09-26):** the wizard with a real Cloudflare token got a Let's Encrypt wildcard certificate for `*.labtest.example.com` via DNS-01; Vaultwarden and the MyLab UI are served over verified HTTPS/2 through Caddy. |
| D2 | DNS-01: seven providers from the `internal/dns` registry (fields, Caddy directive, libdns client), plus a raw override directive for anything else. Checked: every provider's libdns library is on libdns v1.x (no version conflicts), and every Caddy plugin expands `{env.…}` placeholders anywhere in a value (so GoDaddy's `key:secret` works). The MyLab Caddy image is defined in `build/caddy/Containerfile` but not yet built or published; the unit uses `localhost/mylab-caddy:latest`. |

## E. Secrets

| # | Assumption / concern |
|---|---|
| E1 ✅ | Secrets are Podman secrets named `mylab-<svc>-<name>` (DB passwords: `mylab-<svc>-db-<db>-password`), injected with `Secret=...,type=env` or `type=mount`. **Verified:** Caddy received the DNS token from a Podman secret as an env var. |
| E2 ⚠️ | Podman's default `file` secret driver stores secret values **unencrypted** under the user's container storage. That's acceptable only because the directory is readable just by `mylab`. Revisit (e.g. `pass` driver or a MyLab driver). |

## F. Health checks

| # | Assumption / concern |
|---|---|
| F1 | `health.http` in a package **cannot** simply become a Podman `HealthCmd`, because many images have no curl/wget. For now the generator only emits `HealthCmd` for `health.cmd`. HTTP checks will be done by MyLab itself (probably through Caddy). Needs design. |

## G. Settings

| # | Concern |
|---|---|
| G1 | `default: auto` (Immich hardware acceleration) currently means "true if the host has any GPU". It may need a more specific rule, e.g. `auto: gpu.intel`. |

## L. Other findings from the VM (2026-09-26)

| # | Finding |
|---|---|
| L1 | `systemctl start` succeeds even if the container exits a moment later (Caddy did, with a bad token). The reconciler must check unit status after starting and report failures. |
| L2 | `systemctl --user reload` fails if the unit isn't active. Use `reload-or-restart`. (Fixed.) |
| L3 | The `mylab` user can't read its own user journal ("insufficient permissions"). To show service logs in the UI, `mylab` needs to be in `systemd-journal`, or logs must be read through the agent. |
| L4 | Stopping a `systemd-run --user` unit that wraps `podman run` does **not** stop the container (conmon leaves the unit's cgroup). Quadlet handles this correctly; one-off commands via `systemd-run` should use `podman exec`/`unshare`, not `podman run`. |
| L5 | The Cloudflare DNS plugin validates the token format when Caddy starts, so a bad token stops Caddy entirely, taking every service offline. The UI must validate the token before saving it. |
| L7 | restic excludes a directory's contents even when a path inside it is listed explicitly. To keep only the Podman secrets out of the mylab home directory, the backup uses a chain of `*` / `!` patterns (`backup.excludeAllBut`). |
| L8 | `VACUUM INTO` refuses to overwrite an existing file; the database copies go to a temporary file and are renamed. (Fixed.) |
| L9 | Debian 13 ships restic 0.18.0 (tested on the VM); the wrapper's tests also pass with 0.19.1. |
| L10 | ~~A local backup repository must be added to `ReadWritePaths=`~~: not needed. restic runs via `systemd-run --user`, outside mylab's sandbox. Verified: a backup inside the full sandbox wrote to `/srv/backup-test`. |
| L12 | Restoring with `--target /` also restores parent directories' metadata; inside `podman unshare` restic then fails to `lchown /srv`. Local tests missed it because restic only changes owners when running as root. Fixed by restoring each directory with `snapshot:dir --target dir`. The failed attempt was safely rolled back, and the rollback path is now verified too. |
| L13 | Restore drill on the VM: corrupted Vaultwarden's database and deleted a file, then ran `mylab backup restore vaultwarden`. Vaultwarden came back healthy with the file, the database owned by `mylab`, and the broken data kept in `*.before-restore-*`. |
| L11 | `ProtectHome=yes` also hides `/run/user`, so the sandboxed `mylab` couldn't reach its user manager (the dashboard showed every service as "unknown"). Fixed with `ProtectHome=tmpfs` + `BindPaths=/run/user/<uid>`. The earlier NoNewPrivileges test ran without the full sandbox; test with the real unit's options. |
| L14 | **Real record management test (Cloudflare):** enabling management from Settings created `*.labtest.example.com → 10.0.2.15`; public resolvers returned it. The home network's router answered with a *different* address (a local override on the user's network), which the check would have misreported as rebinding protection. It now has its own state, "overridden". |
| L15 ✅ | **Forgejo + MyLab Postgres:** the rootless Forgejo image (uid 1000) writes its bind-mounted data with `UserNS=keep-id:uid=1000,gid=1000`; files appear owned by `mylab` on the host. I2 holds (Postgres reads the password file). `pg_dump --format=custom` in the backup and `pg_restore --clean` on restore round-tripped a deleted repo. |
| L16 ✅ | **nftables with rootless Podman:** with MyLab's `inet mylab` table (policy drop), Caddy on 80/443 (pasta port forwarding) stays reachable, outbound traffic and DNS work (conntrack), and SSH follows the choice: with "nobody", new connections were refused while an open session kept working. Tested with a dead-man timer that would have removed the table. |
| L6 | Bridge-network ports go through Podman's `rootlessport` userspace proxy, which is why client IPs are lost (C4). |

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

## K. Web UI

| # | Assumption / concern |
|---|---|
| K1 | Alpine.js's standard build needs `'unsafe-eval'` in the Content-Security-Policy. Its CSP build avoids that but supports fewer expressions. Revisit once the UI uses Alpine more. |
| K2 ✅ | The UI listens on `127.0.0.1:8480` only; Caddy reaches it through pasta's loopback mapping. `ui_bind` in lab.yaml can override this for development. |
| K3 | TOTP 2FA is implemented (RFC 6238, standard library only; replay-protected; 5 wrong codes means entering the password again). Owners are forced to enroll; others get a reminder. **No QR code yet**: enrollment shows the key and an `otpauth://` link, since a QR library would be another dependency. Pending 2FA logins live in memory, so a restart means logging in again. There are no recovery codes yet: a lost phone means `mylab user reset-password` plus an Owner-side 2FA reset, which doesn't exist yet. |
| K4 | Failed logins only get a 500 ms delay; real rate limiting is expected from Caddy (SPEC §6.10) and isn't configured yet. |
| K5 | First-run setup requires a one-time setup code (logged, and saved to `/var/lib/mylab/setup-token` with mode 0600), so whoever reaches the UI first on the network can't claim the Owner account. Not in SPEC yet. |
| K6 | `mylab user add/reset-password` print a random temporary password instead of prompting, to avoid a terminal dependency. Users can't change their password in the UI yet. |

## M. Setup wizard

| # | Assumption / concern |
|---|---|
| M1 | Applying wizard settings restarts MyLab (`Restart=always`) instead of hot-reloading config everywhere. Verified on the VM: a self-exit is restarted within a second. The "restarting" page waits for a new boot ID before continuing. |
| M2 | The wizard's DNS check creates and deletes `_mylab-check.<domain>` (TXT), trying zones from the domain itself upwards. **Namecheap is read-only** (lists records): its API can only replace all of a domain's records at once, so a test write would rewrite the zone. Tested against fake APIs; **Cloudflare also verified with a real account** (the wizard check passed and the certificate was issued). The other six still need a real account test. |
| M5 | Provider notes in the wizard (GoDaddy and Namecheap only allow API access for some accounts; Namecheap needs this lab's public IP whitelisted) are from their public documentation at the time of writing and may change. |
| M6 | MyLab's DNS check uses each provider's public HTTP API directly (standard library only), not libdns or the AWS SDK, which had grown the binary from 22 to 31 MB. Route 53 needs AWS SigV4 signing, implemented in `internal/dns/sigv4.go` and checked to produce identical signatures to the AWS SDK's signer. Request formats follow the libdns implementations Caddy's plugins use. We maintain these ourselves; if a provider changes its API, only the wizard's check breaks, not certificates. |
| M3 ⚠️ | S3 credentials are stored in `lab.yaml` (mode 0640, owned by mylab). They're also inside the encrypted backups. Consider moving them to a separate 0600 file or a Podman secret. |
| M4 | A local backup folder must be writable by the `mylab` user (e.g. a disk mounted for it). Otherwise the wizard shows restic's error. Preparing disks belongs to the installer/agent later. |

## H. Dependencies

Picked overnight to keep moving; **approved by the user on 2026-09-26.**

| Dependency | Why | Alternative |
|---|---|---|
| `go.yaml.in/yaml/v3` | The maintained continuation of `gopkg.in/yaml.v3` (now archived); same API | `sigs.k8s.io/yaml` |
| `modernc.org/sqlite` | Pure Go SQLite, so the binary stays static with no C toolchain. Large (it's a C-to-Go translation) | `github.com/ncruces/go-sqlite3` (WASM-based, also pure Go); `mattn/go-sqlite3` (needs cgo) |
| `golang.org/x/crypto` | argon2id password hashing (SPEC §5). Maintained by the Go team | none realistic |
| htmx 2.0.11, Alpine.js 3.17.4 | Already agreed (SPEC §6.3); vendored into `internal/web/static` with checksums | — |

Everything else so far uses the standard library. systemd is driven via `systemctl` (not D-Bus) to avoid a dependency and keep behaviour transparent; revisit if it proves slow or fragile.
