# Working Assumptions & Concerns

Code was started before the Debian 13 rootless prototype (TASKS 1.2) was run. This file lists what the code **assumes** that prototype will show, so each item can be checked on a real VM and the code adjusted.

Status: items are **unverified** unless marked. ✅ = verified on the Debian 13 dev VM (`scripts/dev-vm.sh`, Podman 5.4.2, 2026-09-26). ❌ = failed. ⚠️ = could force a redesign if wrong.

## A. Rootless Podman & systemd

| # | Assumption | Code that depends on it |
|---|---|---|
| A1 ✅ | A dedicated `roostack` system user runs all rootless services, with `loginctl enable-linger roostack` so its systemd user manager starts at boot. | `internal/config` (paths), reconciler **Verified:** linger starts the user manager at boot; units run under it. **Survives a reboot:** after `systemctl reboot` with nobody logged in, roostack, roostack-agent, Caddy and Vaultwarden were all running again and the port sysctl was persisted. |
| A2 | Quadlet files for that user go in `$HOME/.config/containers/systemd/`, with the `roostack` home set to `/var/lib/roostack/home` (kept separate from desired state). | `config.Paths.QuadletDir` |
| A3 ✅ | The `roostack` daemon (a *system* unit running as `User=roostack`) can drive the user manager with `systemctl --user` if `XDG_RUNTIME_DIR=/run/user/<uid>` is set. | `internal/systemd` **Verified:** works with `XDG_RUNTIME_DIR` **and** `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus`; podman needs the latter for systemd cgroups outside a login session. |
| A4 ✅ | Unit naming: `roostack-<svc>-<container>.container` → `roostack-<svc>-<container>.service`; `roostack-<svc>.network` → `roostack-<svc>-network.service`. | `internal/generate` **Verified:** `roostack-<svc>-network.service` etc. as expected. |
| A5 | Debian uses AppArmor, not SELinux, so bind mounts need no `:z`/`:Z` relabel options. (Fedora support would need these.) | `internal/generate` |
| A6 | Podman 5.4 Quadlet supports `Notify=healthy`, so `After=` a database unit waits until the database passes its health check. | database units |
| A7 | `GroupAdd=keep-groups` + `AddDevice=/dev/dri` gives rootless containers Intel GPU access if `roostack` is in the `render` and `video` groups (needs crun, Debian's default). | `devices: gpu` |
| A8 ✅ decided + verified | **Verified on the VM:** `roostack.service` runs with `NoNewPrivileges=yes`; `roostack apply` inside such a sandbox creates secrets via `systemd-run --user`. Direct podman under NoNewPrivileges is unreliable: `podman unshare` worked only because podman's pause process already held the namespace, while a fresh `podman run` failed. **Decided (option b, SPEC §6.1):** `roostack` never runs podman directly, only `systemctl --user` / `systemd-run --user`. Background: **`NoNewPrivileges=yes` can't be used on `roostack.service`** Rootless Podman calls the setuid helpers `newuidmap`/`newgidmap`, which NoNewPrivileges disables. SPEC §6.1 listed it as planned sandboxing. Options: (a) drop it (current unit file); (b) have `roostack` never run `podman` directly, only `systemctl --user` and `systemd-run --user` (the user manager runs podman outside the sandbox); (c) give the helpers file capabilities instead of setuid. (b) is the cleanest, but `podman secret create` and `podman exec` (backups) would need to go through the user manager. | `packaging/systemd/roostack.service` |
| A10 ✅ | **Caddy's image build runs in the background from the first start (2026-09-30).** The Caddy container file waits for the domain step (it needs the DNS login's secret), so before this nothing started the build until after the setup wizard, and the first service install waited for it (8½ minutes on a 1-CPU Linode). Now an apply that writes a `.build` file starts it with `systemctl --user start --no-block`. Its output goes to `/var/lib/roostack/caddy-build.log` (`StandardOutput=truncate:…` in the Quadlet `[Service]` section, passed through): the `roostack` user is a system user (uid < 1000), so journald puts its units' output in the system journal, which the sandboxed daemon can't read (`journalctl --user` in the unit's sandbox: "insufficient permissions"). The build unit is a oneshot that goes back to `inactive` on success (`failed` on failure). **Verified** on a fresh Debian 13 VM: the build started at install, a restart mid-build (the wizard's end) picked it up again, the Services page showed it live, and afterwards Caddy read "waiting for your domain". |
| A11 ✅ | **Pulling images before starting new containers (2026-09-30):** `podman pull` run through `systemd-run --user --pipe` as `roostack` (like backups) streams its output (a "Copying blob …" line per layer, then "Writing manifest", no byte progress without a terminal) into the apply log; `podman image exists` skips images already here. The unit then starts without pulling. Quadlet's own pull inside systemd has a 5-minute timeout ("setting pull timeout to 5m0s"), which a large image on a slow connection could hit; pulling first avoids it. **Verified** on a Debian 13 VM installing Forgejo (Postgres 19 s, Forgejo 20 s) and Vaultwarden. |
| A9 ✅ | System users get no `/etc/subuid` range from `useradd --system`; the installer adds `200000-265535` explicitly. | `scripts/dev-install.sh` **Verified:** `roostack:200000:65536` added; rootless podman works. |

## B. UID mapping & storage

| # | Assumption |
|---|---|
| B1 ✅ | Root inside a rootless container = the `roostack` user on the host, so files written by container root appear owned by `roostack`. Files written by a non-root container user (e.g. Postgres uid 999) appear as `subuid_start + uid - 1` (e.g. `100998`). **Verified:** container root → uid 999 (`roostack`) on the host. |
| B2 ✅ | Backups can read every file by running restic inside `podman unshare` as `roostack`. Restores must also go through `podman unshare` to keep ownership correct. **Verified:** restic runs via `systemd-run --user` + `podman unshare`; a restore inside `podman unshare` brought files back owned by `roostack` (uid 999), and the restored Vaultwarden database opened cleanly. Snapshot contents checked: live SQLite files, the backup key and container images excluded; dumps, state and Podman secrets included. |
| B3 | `keep-id` is **not** used by default; it will be a per-container option once a package needs it. |
| B4 | Bind-mounting subdirectories inside another bind mount (Immich `/data` + `/data/thumbs`) works; mounts are emitted parent-first. |

## C. Networking ⚠️

| # | Assumption |
|---|---|
| C1 ✅ | Each service gets its own Podman network `roostack-<svc>`. Containers find each other by container name (aardvark-dns). |
| C2 (superseded) | ~~A shared `roostack-proxy` network~~, replaced by loopback ports (SPEC §6.5). |
| C3 ✅ | Caddy publishes 80/443 rootless once `net.ipv4.ip_unprivileged_port_start=80` is set (by `roostack-agent`). **Verified:** Caddy listens on 80/443 rootless; HTTP redirects to HTTPS. |
| C4 ❌ **FAILED on Debian 13 (2026-09-26)** | Tested with a `whoami` container on Podman 5.4.2 (pasta). **Default pasta network:** the real client IP is preserved ✅. **Named bridge network (our Caddy design):** every client appears as `10.89.0.x` ❌. That address is inside `10.0.0.0/8`, so Caddy's "private only" check would **let the whole internet through**. **Host network (`--network host`):** the real IP is preserved ✅. Connections from the host itself arrive as `169.254.1.2`, outside the private ranges, so they are safely treated as external. See "Proposed fix" below. |
| C4 ✅ fixed (Option C, SPEC §6.5) | **Verified with the real Caddy on the VM:** Caddy on pasta sees the real client IP (`10.0.2.2`); it reaches Vaultwarden at `169.254.1.3:20000` and the UI at `169.254.1.3:8480`; ports 80/443 are now handled by pasta, not `rootlessport`; the UI isn't reachable from outside the VM. Original proposal: Run Caddy with `Network=host`. Each routed container publishes on `127.0.0.1:<allocated port>` (still on its own service network for database access), and Caddy proxies to `127.0.0.1:<port>`. **Verified in the VM:** host-network proxy sees the real client IP; loopback-published ports work from the host network and are not reachable from the LAN. Also lets the Roostack UI listen on `127.0.0.1:8480` only (fixes C6/K2). Cost: Roostack must allocate and store a stable port per route (e.g. in the instance file), and the `roostack-proxy` network goes away. |
| C5 | "Private" means Caddy's `private_ranges` (RFC1918, loopback, `fd00::/8`) + `100.64.0.0/10` (Tailscale/CGNAT). The CGNAT range can include ISP addresses on some connections; revisit when remote access is designed. The firewall's LAN set also includes `fc00::/7` and `fe80::/10`. |
| C6 ✅ (superseded) | Caddy reaches the `roostack` UI on the host at `host.containers.internal:8480`. With pasta this may need `roostack` to listen on a non-loopback address; the firewall then keeps 8480 closed to the network (only 22/80/443 are open). **Verified:** Caddy → `host.containers.internal:8480` reaches the UI. |
| C7 (superseded) | Caddy no longer resolves containers by name; it uses loopback ports. Containers in the same service still resolve each other by name (C1). |
| C9 | Removing `roostack-proxy.network` stopped its unit, but Podman doesn't delete the network itself (harmless leftover on existing installs). |
| C10 | Traffic from the host itself reaches Caddy with the host's own LAN address, so it counts as private. That's intended. |
| C11 ✅ | **The network at boot isn't always ready at network-online.target (2026-10-01).** On Linode's Debian 13 image (ifupdown with dhcpcd), `network-online.target` was reached at 7.8 s but the DHCP lease (the server's IPv4 address and route) only at 9.5 s, when Roostack and the containers start. CrowdSec's pasta then failed to bind `127.0.0.1:8473` twice, and systemd's restart brought it up 5 s later; Roostack's first apply saw the first failure ("the last change didn't finish"), and the DNS check failed with "network is unreachable". Now Roostack waits (up to a minute) for a route before anything else, and a failed start that becomes active within 30 s (systemd restarting it) counts as started. |
| C12 ✅ | **Removing a Podman secret a running container uses (2026-10-07, VM3, Podman as the roostack user):** `podman secret rm` succeeds while the container runs, and the container then can't restart ("no secret with name or id"). So the old address's DNS credentials are removed only after Roostack has restarted without them in Caddy's unit (SPEC §6.6). |
| C13 ✅ | **Caddy's acmedns module (caddy-dns/acmedns v0.7.0) replaces placeholders only in `password` (2026-10-07):** with `{env.…}` in the other fields it posted to the literal "{env.DNS_SERVER_URL}/update". So Roostack Cloud's non-secret fields (server, username, subdomain) are written into the Caddyfile quoted, from `lab.yaml`, and only the password comes from a Podman secret. Then, on VM3 against a development Cloud with roost.ac's real DNS: Let's Encrypt issued `smiths.test.roost.ac` and `*.smiths.test.roost.ac`, and both served over HTTPS, verified. |
| C8 | Changing a `.network` file and restarting its unit doesn't recreate an existing network. Rare, but the reconciler may need to remove the network first. |

## D. TLS / Caddy

| # | Assumption |
|---|---|
| D1 ✅ | One wildcard site block (`*.domain`) with a `host` matcher per route, so every route shares the wildcard certificate. Site snippets are imported from `sites/*.caddy`. **Verified with `caddy adapt` (Caddy 2.11.4 + Cloudflare plugin):** routes are mutually exclusive, the catch-all is last, the wildcard is issued via DNS-01, and an empty or missing `sites/` directory is only a warning. **Real issuance verified (2026-09-26):** the wizard with a real Cloudflare token got a Let's Encrypt wildcard certificate for `*.labtest.example.com` via DNS-01; Vaultwarden and the Roostack UI are served over verified HTTPS/2 through Caddy. |
| D2 | DNS-01: seven providers from the `internal/dns` registry (fields, Caddy directive, libdns client), plus a raw override directive for anything else. Checked: every provider's libdns library is on libdns v1.x (no version conflicts), and every Caddy plugin expands `{env.…}` placeholders anywhere in a value (so GoDaddy's `key:secret` works). The Roostack Caddy image is defined in `build/caddy/Containerfile` but not yet built or published; the unit uses `localhost/roostack-caddy:latest`. |

## E. Secrets

| # | Assumption / concern |
|---|---|
| E1 ✅ | Secrets are Podman secrets named `roostack-<svc>-<name>` (DB passwords: `roostack-<svc>-db-<db>-password`), injected with `Secret=...,type=env` or `type=mount`. **Verified:** Caddy received the DNS token from a Podman secret as an env var. |
| E2 ⚠️ | Podman's default `file` secret driver stores secret values **unencrypted** under the user's container storage. That's acceptable only because the directory is readable just by `roostack`. Revisit (e.g. `pass` driver or a Roostack driver). |

## F. Health checks

| # | Assumption / concern |
|---|---|
| F1 | `health.http` in a package **cannot** simply become a Podman `HealthCmd`, because many images have no curl/wget. For now the generator only emits `HealthCmd` for `health.cmd`. HTTP checks will be done by Roostack itself (probably through Caddy). Needs design. |

## G. Settings

| # | Concern |
|---|---|
| G1 | `default: auto` (Immich hardware acceleration) currently means "true if the host has any GPU". It may need a more specific rule, e.g. `auto: gpu.intel`. |

## L. Other findings from the VM (2026-09-26)

| # | Finding |
|---|---|
| L1 | `systemctl start` succeeds even if the container exits a moment later (Caddy did, with a bad token). The reconciler must check unit status after starting and report failures. |
| L2 | `systemctl --user reload` fails if the unit isn't active. Use `reload-or-restart`. (Fixed.) |
| L3 | The `roostack` user can't read its own user journal ("insufficient permissions"). To show service logs in the UI, `roostack` needs to be in `systemd-journal`, or logs must be read through the agent. |
| L4 | Stopping a `systemd-run --user` unit that wraps `podman run` does **not** stop the container (conmon leaves the unit's cgroup). Quadlet handles this correctly; one-off commands via `systemd-run` should use `podman exec`/`unshare`, not `podman run`. |
| L5 | The Cloudflare DNS plugin validates the token format when Caddy starts, so a bad token stops Caddy entirely, taking every service offline. The UI must validate the token before saving it. |
| L7 | restic excludes a directory's contents even when a path inside it is listed explicitly. To keep only the Podman secrets out of the roostack home directory, the backup uses a chain of `*` / `!` patterns (`backup.excludeAllBut`). |
| L8 | `VACUUM INTO` refuses to overwrite an existing file; the database copies go to a temporary file and are renamed. (Fixed.) |
| L9 | Debian 13 ships restic 0.18.0 (tested on the VM); the wrapper's tests also pass with 0.19.1. |
| L10 | ~~A local backup repository must be added to `ReadWritePaths=`~~: not needed. restic runs via `systemd-run --user`, outside roostack's sandbox. Verified: a backup inside the full sandbox wrote to `/srv/backup-test`. |
| L11 ✅ | `systemd-run --setenv=NAME` (no value) takes the value from systemd-run's own environment, so secrets (backup store passwords, online storage keys) reach a `--user` unit without appearing in its command line, which any local user can read with `ps`. **Verified** on Debian 13 (systemd 257), and during a real backup on VM1 with a copy on VM2: no value in `ps`, the backup worked (2026-10-04). |
| L12 | Restoring with `--target /` also restores parent directories' metadata; inside `podman unshare` restic then fails to `lchown /srv`. Local tests missed it because restic only changes owners when running as root. Fixed by restoring each directory with `snapshot:dir --target dir`. The failed attempt was safely rolled back, and the rollback path is now verified too. |
| L13 | Restore drill on the VM: corrupted Vaultwarden's database and deleted a file, then ran `roostack backup restore vaultwarden`. Vaultwarden came back healthy with the file, the database owned by `roostack`, and the broken data kept in `*.before-restore-*`. |
| L11 | `ProtectHome=yes` also hides `/run/user`, so the sandboxed `roostack` couldn't reach its user manager (the dashboard showed every service as "unknown"). Fixed with `ProtectHome=tmpfs` + `BindPaths=/run/user/<uid>`. The earlier NoNewPrivileges test ran without the full sandbox; test with the real unit's options. |
| L14 | **Real record management test (Cloudflare):** enabling management from Settings created `*.labtest.example.com → 10.0.2.15`; public resolvers returned it. The home network's router answered with a *different* address (a local override on the user's network), which the check would have misreported as rebinding protection. It now has its own state, "overridden". |
| L15 ✅ | **Forgejo + Roostack Postgres:** the rootless Forgejo image (uid 1000) writes its bind-mounted data with `UserNS=keep-id:uid=1000,gid=1000`; files appear owned by `roostack` on the host. I2 holds (Postgres reads the password file). `pg_dump --format=custom` in the backup and `pg_restore --clean` on restore round-tripped a deleted repo. |
| L16 ✅ | **nftables with rootless Podman:** with Roostack's `inet roostack` table (policy drop), Caddy on 80/443 (pasta port forwarding) stays reachable, outbound traffic and DNS work (conntrack), and SSH follows the choice: with "nobody", new connections were refused while an open session kept working. Tested with a dead-man timer that would have removed the table. |
| L17 ✅ | **OS updates via the agent:** `systemd-run --wait --pipe` from the sandboxed agent runs `apt-get update && unattended-upgrade` outside the sandbox; it upgraded packages from both the main (point release) and stable-updates origins. The coordinator's reboot through the agent (`systemd-run --on-active=5 systemctl reboot`) worked, and all rootless services came back with nobody logged in. |
| L6 | Bridge-network ports go through Podman's `rootlessport` userspace proxy, which is why client IPs are lost (C4). |

## P. Public access (SPEC §6.11)

Prototyped on VM2 (2026-09-30) with the Caddy image's new plugins and CrowdSec 1.8.1 as throwaway containers, network options as Roostack's Caddy unit (pasta, `--map-host-loopback`). An "outside" visitor was a network namespace with `203.0.113.10` (a documentation address, not private), and a "home" device one with `192.168.77.10`.

| # | Assumption |
|---|---|
| P1 ✅ | The outside visitor reaches Caddy with its own address (`203.0.113.10`), so private checks, rate limits and CrowdSec see real visitors. |
| P2 ✅ | A private path on a public address (`not remote_ip private_ranges …` + `abort`) blocks outside visitors and lets home devices in. **It must be the first thing inside a `route` block:** Caddy runs `route` before `abort`, so an `abort` next to a `route` is skipped (seen in the first try: `/admin` answered from outside). |
| P3 ✅ | `caddy-ratelimit` v0.1.0 works with Caddy 2.11.4: a zone per login path (5 a minute answered 5, then 429) and a general zone per visitor address. |
| P4 ✅ | CrowdSec reads Caddy's JSON access log from a shared folder (`labels: type: caddy`, collection `crowdsecurity/caddy`): 53 lines read, all parsed. 40 requests for missing `.php` pages got the visitor banned for 4 hours (`http-crawl-non_statics`); the bouncer then answered 403. A home device kept working (CrowdSec whitelists private addresses). |
| P5 ✅ | The bouncer registers with `BOUNCER_KEY_<name>` on the CrowdSec container and an API key from the environment in Caddy. Caddy starts, and keeps serving, while CrowdSec isn't up yet (it retries every 10 s), so a slow CrowdSec start doesn't take services down. With `DISABLE_ONLINE_API=true` nothing is sent to CrowdSec's central API (the opt-out). |
| P6 | Rootless containers started from an SSH session stop when it ends (no linger for the login user): tests run within one session. Roostack's own units are fine (the `roostack` user lingers). |
| P8 ✅ | **End to end through Roostack on VM1 (2026-09-30),** Vaultwarden made public by its instance file (as the switch does) with sharing off: the apply started CrowdSec (online API off: no CAPI configured), the bouncer registered and pulled; the first DNS sync waited, and the next set `vault.labtest.example.com` to the home's public address (found over DNS) once CrowdSec's health check answered; Google's resolver had it at once, Cloudflare's still had the wildcard cached. With the real certificate, an outside visitor (network namespace, 203.0.113.10) got 200 on `/`, nothing on `/admin`, 429 after 10 logins, Roostack's own page for a check link, and 403 once CrowdSec banned it for scanning; a home device (192.168.77.10) got 200 on `/admin` throughout. Made private again: CrowdSec stopped, Caddy's config lost the bouncer, and the record pointed home (10.0.2.15) two seconds after the restart. Not tested: a real router forward (the user's port 443 goes elsewhere), and a check link's success through Caddy (it needs the UI; unit-tested). |
| P9 ✅ | **What Caddy's pasta network sees for connections made on the host (2026-09-30):** a throwaway container with Roostack's network options (`pasta:--map-host-loopback,<addr>`, port published) answered with the client address it saw. A connection to the host's `127.0.0.1` (where an SSH tunnel ends) arrives from **the host's own address** (10.0.2.15 on the VM); one to the host's own address arrives from **the map-host-loopback address**. So on a lab on the internet, "private" (this server only) is loopback, the host's address and `169.254.1.3` (SPEC §6.12). At home both are private addresses anyway, which is why VM1's private UI route answered through the host's 127.0.0.1. |
| P7 | Requests from the host itself to its LAN address reached the test Caddy from `169.254.1.2` (pasta's address for the host), which Caddy's `private_ranges` doesn't include, so the server itself can't reach a private path through Caddy. Nothing in Roostack does. |

## Q. The edge (SPEC §10.5)

Prototyped on VM2 (2026-10-10) with throwaway containers: Roostack's Caddy image plus `github.com/mholt/caddy-l4@v0.1.2`, both on pasta as Roostack's Caddy. An "edge" Caddy on 9443 with the `layer4` listener wrapper (`tls sni` → `proxy { proxy_protocol v2; upstream … }`, then `tls` for its own sites), and a "home" Caddy on 8443 with the `proxy_protocol` wrapper (`allow` the overlay, `fallback_policy reject`). The overlay was a dummy interface with `10.88.0.1` (the WireGuard hop itself not yet: Q6); the outside visitor a network namespace with `203.0.113.10` (VM2's firewall opened for it, then closed).

| # | Result |
|---|---|
| Q1 ✅ | **A visitor through the edge reaches home with their real address:** home's `{remote_host}` was `203.0.113.10`, and a private path answered "refused: 203.0.113.10 is not private". TLS passed through: the certificate was home's. |
| Q2 ✅ | **The edge's own sites still work** behind the layer-4 wrapper (names it doesn't route fall through to its `tls` and HTTP server), and **a name it neither routes nor serves fails the TLS handshake**. |
| Q3 ✅ | **A faked PROXY header is refused:** a visitor sending one straight to home's port, claiming `10.0.0.5`, had the connection closed (`fallback_policy reject`: an untrusted source may not send one). |
| Q4 ⚠️ | **`fallback_policy reject` doesn't require a header**, it only refuses one from an untrusted source: a visitor connecting straight to home's 8443 without a header was served (with their real, public address, so nothing private opened). So the edge port must be reachable only over WireGuard (the firewall), and `allow` lists only the edges' overlay addresses, never the whole overlay. A trusted source that sends no header counts as its own overlay address (private): the edge always sends one, and an edge that didn't could claim any address anyway. |
| Q5 ✅ | **Inside pasta, the host's own address is the container's:** the edge dialling `10.0.2.15` (VM2's address) reached itself ("connection refused"). The edge dials home's overlay address, which leaves through the host (`10.88.0.1` worked), as WireGuard addresses will. |
| Q6 | **To check over real WireGuard** with the implementation: VM3 at home, `roostack-test` (a Linode) as the edge, a real visitor from the internet. |
| Q7 | **No HTTP/3 through the edge:** Caddy's listener wrappers (`proxy_protocol` among them) wrap a TCP listener, and HTTP/3 uses a UDP packet connection, so home couldn't learn a visitor's address over it (Caddy's docs say nothing either way; this follows from the interfaces). The edge forwards TCP only, and home doesn't advertise HTTP/3 for names routed through an edge. |

## I. Databases

| # | Assumption / concern |
|---|---|
| I1 | Postgres data is mounted at `/var/lib/postgresql/data`. The official Postgres 18+ images changed this layout (`/var/lib/postgresql/<major>/docker`), so the path will need to depend on the image/version. |
| I2 | Postgres reads its password from a Podman secret mounted as a file (`POSTGRES_PASSWORD_FILE`); the app gets the same secret as an env var. Assumes the default secret mount mode is readable by the Postgres user in the container. |

## J. Firewall

| # | Assumption / concern |
|---|---|
| J1 ✅ | SSH defaults to **LAN-only**. On a VPS that would lock the user out. **Resolved:** the Owner chooses in setup step 2, which shows current SSH connections and asks before cutting one off; Roostack applies no firewall until then. |
| J2 | Roostack's `inet roostack` table drops anything it doesn't accept, even if other tables accept it. Ruleset verified with `nft -c` and applied twice in a network namespace. Not yet tested alongside Podman/pasta on a real host. |

## K. Web UI

| # | Assumption / concern |
|---|---|
| K1 | Alpine.js's standard build needs `'unsafe-eval'` in the Content-Security-Policy. Its CSP build avoids that but supports fewer expressions. Revisit once the UI uses Alpine more. |
| K2 ✅ | The UI listens on `127.0.0.1:8480` only; Caddy reaches it through pasta's loopback mapping. `ui_bind` in lab.yaml can override this for development. |
| K3 | TOTP 2FA is implemented (RFC 6238, standard library only; replay-protected; wrong codes limited per account, see K4). Owners are forced to enroll; others get a reminder. **No QR code yet**: enrollment shows the key and an `otpauth://` link, since a QR library would be another dependency. Pending 2FA logins live in memory, so a restart means logging in again. There are no recovery codes yet: a lost phone means `roostack user reset-password` plus an Owner-side 2FA reset, which doesn't exist yet. |
| K4 | Failed logins only get a 500 ms delay; real rate limiting is expected from Caddy (SPEC §6.10) and isn't configured yet. Wrong 2FA codes are limited per account (2026-09-29): every fifth wrong code in a row locks codes for 15 minutes, doubling up to a day, kept in the database so new logins and restarts don't reset it. A lock is a nuisance someone with the password can cause for the account's owner; `roostack user reset-2fa` ends it. |
| K5 | First-run setup requires a one-time setup code (logged, and saved to `/var/lib/roostack/setup-token` with mode 0600), so whoever reaches the UI first on the network can't claim the Owner account. The installer also shows it (2026-09-30), to whoever ran it as root, who could read the file anyway; like the log, apt's `/var/log/apt/term.log` (root and `adm`) keeps a copy. It stops working once the Owner exists. Not in SPEC yet. |
| K7 ✅ | **The install message comes last** (2026-09-30): `postinst` activates a trigger on its own package (`dpkg-trigger --no-await roostack-installed`, `interest-noawait` in the package's `triggers`), and dpkg runs it after the other packages' triggers (libc-bin, man-db, initramfs-tools), so the message ends the output. An apt hook (`DPkg::Post-Invoke`) was rejected: apt reads its configuration before the package installs the hook, so it wouldn't run on the first install. **Verified** on a fresh Debian 13 VM with `apt install ./roostack_….deb`: the message was the last thing printed, with colour on a terminal and none when piped or with `NO_COLOR`; the code shown matched the file; reinstalling showed nothing. |
| K6 | `roostack user add/reset-password` print a random temporary password instead of prompting, to avoid a terminal dependency. Users can't change their password in the UI yet. |

## M. Setup wizard

| # | Assumption / concern |
|---|---|
| M1 | Applying wizard settings restarts Roostack (`Restart=always`) instead of hot-reloading config everywhere. Verified on the VM: a self-exit is restarted within a second. The "restarting" page waits for a new boot ID before continuing. |
| M2 | The wizard's DNS check creates and deletes `_roostack-check.<domain>` (TXT), trying zones from the domain itself upwards. **Namecheap is read-only** (lists records): its API can only replace all of a domain's records at once, so a test write would rewrite the zone. Tested against fake APIs; **Cloudflare also verified with a real account** (the wizard check passed and the certificate was issued). The other six still need a real account test. |
| M5 | Provider notes in the wizard (GoDaddy and Namecheap only allow API access for some accounts; Namecheap needs this lab's public IP whitelisted) are from their public documentation at the time of writing and may change. |
| M6 | Roostack's DNS check uses each provider's public HTTP API directly (standard library only), not libdns or the AWS SDK, which had grown the binary from 22 to 31 MB. Route 53 needs AWS SigV4 signing, implemented in `internal/dns/sigv4.go` and checked to produce identical signatures to the AWS SDK's signer. Request formats follow the libdns implementations Caddy's plugins use. We maintain these ourselves; if a provider changes its API, only the wizard's check breaks, not certificates. |
| M3 ⚠️ | S3 credentials are stored in `lab.yaml` (mode 0640, owned by roostack). They're also inside the encrypted backups. Consider moving them to a separate 0600 file or a Podman secret. |
| M4 | A local backup folder must be writable by the `roostack` user (e.g. a disk mounted for it). Otherwise the wizard shows restic's error. Preparing disks belongs to the installer/agent later. |

## H. Dependencies

Picked overnight to keep moving; **approved by the user on 2026-09-26.**

| Dependency | Why | Alternative |
|---|---|---|
| `go.yaml.in/yaml/v3` | The maintained continuation of `gopkg.in/yaml.v3` (now archived); same API | `sigs.k8s.io/yaml` |
| `modernc.org/sqlite` | Pure Go SQLite, so the binary stays static with no C toolchain. Large (it's a C-to-Go translation) | `github.com/ncruces/go-sqlite3` (WASM-based, also pure Go); `mattn/go-sqlite3` (needs cgo) |
| `golang.org/x/crypto` | argon2id password hashing (SPEC §5). Maintained by the Go team | none realistic |
| htmx 2.0.11, Alpine.js 3.17.4 | Already agreed (SPEC §6.3); vendored into `internal/web/static` with checksums | — |

Everything else so far uses the standard library. systemd is driven via `systemctl` (not D-Bus) to avoid a dependency and keep behaviour transparent; revisit if it proves slow or fragile.
