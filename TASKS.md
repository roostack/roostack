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

## Phase 1: MVP (single node), proposed breakdown

Each step is small and validated with the user before moving on.

- [ ] **1.1 Test environment:** Debian 13 VM (or NUC) to prototype on
- [ ] **1.2 Prototype: rootless storage.** Hand-written Quadlet + bind mounts on Debian 13:
  - [ ] UID mapping: how files appear on the host; effect of `keep-id`
  - [ ] Reading service data for backups (as `mylab` user / `podman unshare`)
  - [ ] Intel GPU (`/dev/dri`) passthrough with `keep-groups`
  - [ ] Linger + boot start; unprivileged port 80/443 sysctl
- [ ] **1.3 Project skeleton:** git repo, Go module layout, central defaults/paths package, README.md + ARCHITECTURE.md stubs
- [ ] **1.4 Package schema:** Go types, YAML parsing, validation; both examples parse
- [ ] **1.5 Generator:** package + instance → Quadlet files (golden-file tests, no execution)
- [ ] **1.6 Reconciler:** write Quadlets, systemd reload, start/stop/status via user D-Bus
- [ ] **1.7 Proxy:** MyLab Caddy image with DNS plugins; Caddyfile generation; wildcard DNS-01
- [ ] **1.8 mylab-agent:** local socket API skeleton; first ops (port sysctl, nftables)
- [ ] **1.9 Web UI skeleton:** Owner login + TOTP, dashboard, install/remove Vaultwarden
- [ ] **1.10 Backups:** restic interface, sqlite/postgres prepare, schedule, restore, recovery key
  - [ ] Measure restic RAM/time on a realistic photo/video dataset
- [ ] **1.11 Maintenance coordinator + host OS updates**
- [ ] **1.12 Packaging:** .deb, apt repo, installer flow
- [ ] **1.13 Packages:** Forgejo (Postgres) and Immich (complex)
