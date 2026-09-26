# MyLab Tasks

## Phase 0: Requirements & Decisions ✅

- [x] Agree guiding principles and high-level requirements
- [x] Technical decisions (recorded in SPEC.md)
  - [x] Base distro(s) → Debian 13 (Ubuntu 26.04 planned)
  - [x] MyLab process architecture → host .deb: `mylab` (unprivileged) + `mylab-agent` (root)
  - [x] Host OS update & reboot policy → automatic updates, reboot window, maintenance coordinator
  - [x] Language → Go
  - [x] Web UI → Go templates + htmx + Alpine.js
  - [x] How services are run under podman → Quadlets
  - [x] Reverse proxy and TLS → Caddy, DNS-01, wildcard cert, domain required
  - [x] Data volume layout → bind mounts under /srv/mylab, configurable, central defaults
  - [x] Databases → one instance per service, standardised pattern
  - [x] Backup mechanism → restic behind an interface
  - [x] Host security → nftables, private admin UI, SSH hardening, CrowdSec when public
  - [x] Users and roles → Owner/Admin/Member, TOTP, CLI reset; YAML state + SQLite runtime data
  - [x] Service definition format (initial) → own YAML schema + Quadlet passthrough; package/instance split
  - [x] Stress-test package format with an Immich draft → `docs/examples/`
- Deferred (post-MVP): see SPEC.md "Deferred Decisions"

## Phase 1: MVP (single node)

Order changed on 2026-09-25: coding started before the Debian 13 prototype, based on the assumptions in `docs/ASSUMPTIONS.md`. Everything marked 🟡 is written and unit-tested, but **not yet run on Debian 13**.

- [x] **1.1 Test environment:** `scripts/dev-vm.sh` (Debian 13 under QEMU/KVM); `dev-install.sh` runs cleanly
- [ ] **1.2 Verify assumptions on Debian 13** (see `docs/ASSUMPTIONS.md`; ⚠️ items first)
  - [x] Run `scripts/dev-install.sh` and fix what breaks
  - [x] C4: client IP was lost on bridge networks; fixed with Option C (Caddy on pasta, services on 127.0.0.1 ports) and verified with the real Caddy
  - [x] A8: podman via `systemd-run --user`; `NoNewPrivileges=yes` restored and verified
  - [x] B1/B2: UID mapping; backups and restores via `podman unshare`
  - [ ] A7: Intel GPU (`/dev/dri`) passthrough with `keep-groups`
  - [x] A1/A3: linger, `systemctl --user` from a system context (needs the D-Bus address too)
  - [x] A1: everything comes back after a reboot with nobody logged in
  - [x] C3/C6: ports 80/443 rootless; Caddy → `host.containers.internal:8480`
  - [ ] D1: real wildcard certificate via Cloudflare DNS-01 (needs a real domain and token)
  - [x] Install Vaultwarden end-to-end with `mylab apply` (running; Caddy up)
- [x] **1.3 Project skeleton:** git repo, Go module, central defaults (`internal/config`), README.md, ARCHITECTURE.md, Makefile
- [x] **1.4 Package schema:** types, parsing, validation; both examples parse (`internal/pkgspec`, `internal/instance`)
- [x] **1.5 Generator:** package + instance → Quadlets, Caddy snippet, secrets, dirs, backup plan; golden-file tests (`internal/generate`)
- [ ] 🟡 **1.6 Reconciler:** plan/apply, `mylab apply [--dry-run]` (`internal/reconcile`, `internal/state`)
- [ ] 🟡 **1.7 Proxy:** Caddyfile generation ✅ (checked with real Caddy); Caddy image Containerfile written, not built
- [ ] 🟡 **1.8 mylab-agent:** socket API with peer-credential checks; port sysctl; nftables firewall (ruleset checked with `nft -c`)
  - [ ] Installer must ask about SSH access before applying the firewall (ASSUMPTIONS J1)
- [ ] 🟡 **1.9 Web UI skeleton**
  - [x] First-run Owner setup (setup code), login/logout, sessions, roles, CSRF, security headers
  - [x] Read-only dashboard with live status (htmx polling)
  - [x] `mylab user add` / `reset-password`
  - [x] TOTP 2FA: Owners must enroll; replay protection; attempt limit
  - [ ] 2FA: QR code, recovery codes, CLI reset of a user's 2FA
  - [x] Install/remove services from the UI (catalog, settings form, remove keeps data, background apply with status)
  - [ ] Public exposure option (after firewall + CrowdSec)
  - [ ] Collect user-provided secrets (DNS token) in the UI instead of env vars
- [ ] 🟡 **1.10 Backups**
  - [x] restic wrapper (tested against real restic), recovery key, `mylab backup init|run|snapshots`
  - [x] Prepare steps: SQLite copy (concurrent-write tested), Postgres dump (command tested; no Postgres service run yet)
  - [x] Lab plan: state + Podman secrets included; images, live DBs, key excluded (verified on the VM)
  - [ ] Nightly schedule through the coordinator; results in the UI
  - [ ] Restore command (per service and full lab)
  - [ ] Measure restic RAM/time on a realistic photo/video dataset
- [ ] 🟡 **1.11 Maintenance coordinator + host OS updates**
  - [x] Coordinator: reboot policy, job chains, interrupt/resume (unit-tested)
  - [ ] Wire into `mylab serve`; agent `reboot` and OS update operations; reboot-required detection
- [ ] **1.12 Packaging:** .deb, apt repo, installer flow (`scripts/dev-install.sh` is a stand-in)
- [ ] **1.13 Packages:** Forgejo (Postgres) and Immich (complex)

## To discuss with the user

- K5: setup-code approach for first-run setup (add to SPEC?)
- Go module path is a placeholder (`github.com/mylab/mylab`); repo location not decided yet
