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
  - [x] D1: real wildcard certificate via Cloudflare DNS-01, set up through the wizard; services served over verified HTTPS
  - [x] Install Vaultwarden end-to-end with `mylab apply` (running; Caddy up)
- [x] **1.3 Project skeleton:** git repo, Go module, central defaults (`internal/config`), README.md, ARCHITECTURE.md, Makefile
- [x] **1.4 Package schema:** types, parsing, validation; both examples parse (`internal/pkgspec`, `internal/instance`)
- [x] **1.5 Generator:** package + instance → Quadlets, Caddy snippet, secrets, dirs, backup plan; golden-file tests (`internal/generate`)
- [ ] 🟡 **1.6 Reconciler:** plan/apply, `mylab apply [--dry-run]` (`internal/reconcile`, `internal/state`)
- [ ] 🟡 **1.7 Proxy:** Caddyfile generation ✅ (checked with real Caddy); Caddy image Containerfile written, not built
- [ ] 🟡 **1.8 mylab-agent:** socket API with peer-credential checks; port sysctl; nftables firewall (ruleset checked with `nft -c`)
  - [x] Firewall applied: SSH choice in the wizard (step 2) and Settings, package ports opened after each apply (tested on the VM: SSH off blocks new connections, HTTPS and outbound traffic keep working)
  - [x] `mylab apply` (CLI) applies the firewall too
- [ ] 🟡 **1.9 Web UI skeleton**
  - [x] First-run Owner setup (setup code), login/logout, sessions, roles, CSRF, security headers
  - [x] Read-only dashboard with live status (htmx polling)
  - [x] `mylab user add` / `reset-password`
  - [x] TOTP 2FA: Owners must enroll; replay protection; attempt limit
  - [x] 2FA: QR code (rsc.io/qr, drawn as SVG; checked with zbarimg); recovery codes dropped in favour of the CLI reset
  - [x] UI review pass (2026-09-27): temporary passwords must be changed at first login; phone layout of fact lists; dark-mode form controls; shorter "Needs attention" DNS items; service page layout (technical details collapsed); backups status shows what the last backup did; clearer end states for applies
  - [x] People page for the Owner (add, role, new temporary password, turn off 2FA, remove; last Owner protected); `mylab user reset-2fa`
  - [x] Service settings: links into the app's admin pages, editable MyLab settings and web address, revealable secrets (Vaultwarden admin token)
  - [x] Theme toggle (auto/light/dark, saved per user)
  - [ ] Mark untested catalog packages (Immich) before release (not needed while in development)
  - [x] Split the UI into pages: Home (needs attention + services), Services, Backups, Settings, Account; role-aware navigation; works on phones
  - [x] Change your own password (logs out other devices)
  - [x] Install/remove services from the UI (catalog, settings form, remove keeps data, background apply with status)
  - [ ] Public exposure option (after firewall + CrowdSec)
  - [x] First-run setup wizard (Owner): domain + Cloudflare token (checked with Cloudflare before saving), backup destination (local folder or S3-compatible), recovery key shown with "I've saved it" confirmation; "later" options with dashboard reminders
  - [ ] Wizard: SFTP destination; changing settings later from a Settings page
  - [x] **More DNS providers**: Cloudflare, Route 53, Porkbun, Gandi, GoDaddy, Namecheap, Spaceship (`internal/dns` registry; TXT-record check over each provider's HTTP API, no SDKs; all plugins in the Caddy image)
  - [ ] Test each DNS provider with a real account (Cloudflare done; the other six only against fake APIs)
  - [ ] **DNS records (SPEC §6.5.1)**
    - [x] Stage 1: wildcard record → LAN IP via the provider API; ownership tracking (never overwrites others' records, adopts matching ones); router rebinding check (home resolver vs 1.1.1.1); Owner dashboard card; wizard checkbox (on by default); Namecheap opt-in with warning
    - [x] Take over a conflicting record (Owner, confirmation, audit log); switch management on/off from the dashboard
    - [x] Stage 1 real test with Cloudflare: record created and visible in public DNS; home DNS override detected
    - [ ] Stage 2: public records → public IP; dynamic DNS via qdm12/ddns-updater; NAT loopback check (with public exposure)
    - [ ] Stage 3: Cloudflare proxy option (CF-Connecting-IP from Cloudflare ranges only; package proxy warnings)
    - [ ] Later: CGNAT detection → Cloudflare Tunnel / MyLab Cloud relay; optional local DNS (Pi-hole)
  - [ ] Maybe later: deSEC, DigitalOcean, Hetzner, OVH, DuckDNS
- [ ] 🟡 **1.10 Backups**
  - [x] restic wrapper (tested against real restic), recovery key, `mylab backup init|run|snapshots`
  - [x] Prepare steps: SQLite copy (concurrent-write tested), Postgres dump (tested with Forgejo)
  - [x] Lab plan: state + Podman secrets included; images, live DBs, key excluded (verified on the VM)
  - [x] Nightly schedule (02:00) and weekly prune + 5% check (Sun 03:00) through the coordinator, running in `mylab serve`
  - [x] Dashboard backup card: last success, failures, staleness (>2 days), local-only warning, next run, "Back up now"
  - [x] `mylab backup restore <service>`: moves current data aside, restores, puts the database back, restarts. Drill passed on the VM (SQLite)
  - [x] Restore from the UI (Owner only): choose a backup that contains the service, confirm, runs through the coordinator with a live log; kept copies listed and deletable
  - [ ] Full-lab restore onto a new machine (with the installer, 1.12)
  - [ ] 3-2-1 guidance and multiple destinations (SPEC §6.9): suggestions in the UI, primary + `restic copy` to extra destinations, per-destination status
  - [x] Postgres restore (pg_restore): Forgejo repo deleted, restored, back with its git data
  - [ ] Measure restic RAM/time on a realistic photo/video dataset
- [ ] 🟡 **1.11 Maintenance coordinator + host OS updates**
  - [x] Coordinator: reboot policy, job chains, interrupt/resume (unit-tested)
  - [x] Running in `mylab serve` with a file journal (interrupted backups resume after reboot)
  - [x] OS updates: apt config, agent runs unattended-upgrade after the nightly backup, results in Settings, restart-needed detection, automatic restart in the window or notify-only, "Check for updates now" / "Restart now". Tested on the VM: downgraded curl/libcurl/tzdata from snapshot.debian.org were upgraded by the agent; a flagged reboot happened in the window through the coordinator and every service came back
  - [x] Live activity logs (status line + collapsed details) for OS updates (streamed from the agent), backups (steps and upload progress), applying service changes, and setup hooks
  - [ ] Unknown host changes (extra apt sources, held packages, changed firewall) warning (SPEC §3.2)
  - [x] Host time zone: wizard step 1, Settings card, agent `timedatectl`, banner when the browser's clock differs
  - [x] Containers use the host's time zone (`Timezone=<zone>` in every unit; a change restarts them)
- [ ] **1.14 Design system** (agreed 2026-09-27)
  - [x] Brand colours: "Teal and violet" is the default (changed from Ocean and magenta, which stays as a theme)
  - [x] Built-in themes, chosen per user (Account → Appearance): Teal, Ocean, Catppuccin, Gruvbox, Nord, Solarized, Everforest, Rosé Pine, Tokyo Night; a test checks every theme's contrast in both modes. Catppuccin offers its named accent colours for actions; choices preview and save at once
  - [x] Tokens: palette → purpose → scales (tokens.css, themes/ocean.css); app.css uses only tokens
  - [x] Components: buttons, notice, status, badge, back, confirm, card/heading, facts, forms, rows, attention, activity, technical details, secret (docs/DESIGN.md)
  - [x] Component gallery: /design in the UI and docs/design/gallery.html (same templates; a test keeps the file current)
  - [x] Move existing pages onto the components: back links, status pills, notices and confirm dialogs all use the shared templates; headings without ampersands
- [x] **1.15 Settings structure**
  - [x] /settings as a grid of overview cards (current state + status per area)
  - [x] Area pages with a sidebar on desktop, back link on phones: People and access (People, Remote access), Network (Domain and certificates, Home network), This machine (Updates and restarts, Time zone)
  - [x] Wizard forms double as the area "change" pages
- [ ] **1.12 Packaging:** .deb, apt repo, installer flow (`scripts/dev-install.sh` is a stand-in)
  - [ ] Ship full licence texts for bundled code (Debian `copyright` file), from THIRD_PARTY.md's list
- [ ] **1.13 Packages:** Forgejo (Postgres) and Immich (complex)
  - [x] Forgejo v15 LTS: rootless image, MyLab Postgres, HTTPS clone; backup/restore tested on the VM
  - [x] Package `ports:` option; Forgejo git over SSH (tested on the VM: clone via LAN IP on 2222)
  - [x] Package hooks (`install`, `action`); Forgejo creates its admin at install and has "Reset a password" (hook scripts tested on the VM)
  - [ ] Forgejo: fresh install through the UI (user to test: remove Forgejo, install again)
  - [ ] If an install hook fails because of what was typed (e.g. an invalid username), "Try again" repeats it; offer to edit those settings instead
  - [ ] Immich on the VM
  - [x] `mylab validate` renders every template (default and flipped settings, with and without GPU, hooks too)

## Phase 2: Multiple machines (SPEC §10, draft)

- [x] Review the §10 design with the user (locked 2026-09-27)
- [ ] Seams
  - [x] Stable node ID and name (`internal/nodeid`, `node.json`; never part of the replica)
  - [x] `host` on every instance, set explicitly (existing ones stamped on first apply); only this node's services are generated, applied, backed up and shown; ports are per node; invalid hosts are rejected
  - [x] `Node` interface between the manager (controller) and machine operations: the manager hands each machine an Assignment (its services, typed-in secrets, services to set up); `node.Local` applies it, runs the firewall, install hooks, actions and secret reveals
  - [x] Backups, restores, OS updates and restarts behind the node (`node.Maintenance`); the web reaches them through the node. Their construction still lives in `serve` (`startMaintenance`)
  - [x] Machine settings behind the node (`node.Settings`: time zone, SSH access, restart policy); the wizard only records its progress (`setup.Done`)
  - [x] The web reaches a service's machine by its host
  - [x] Per-node status and activity in the UI: activity is read from the node; Settings → Machines lists each machine (role, services, apply status); problems name the machine once there are several. Per-machine URLs (`/settings/machines/<id>/…`) come with the second machine
- [x] Two-VM prototype (SPEC.md §10.9): second dev VM joins over MyLab-managed WireGuard (join code with a one-time key, mTLS lab API), runs a service placed on it; the controller's Caddy forwards its routes; setup, actions, secrets, backups, updates, restarts and settings work remotely (`/settings/machines/<id>/…`); `MYLAB_VM=2 scripts/dev-vm.sh`
- [ ] Multi-machine follow-ups
  - [x] Remove a machine (only once it runs no services: it leaves the network and runs on its own); rename a machine
  - [ ] Revoke a removed machine's certificate (today it loses its WireGuard peer, so it can't reach the lab API)
  - [ ] Backups across machines (SPEC.md §10.10)
    - [x] Port forwarding explained on the join page (UDP 51820, which machine, how to check)
    - [x] Backup providers: S3-compatible providers set up once, usable by any machine; any machine's disk
    - [x] The setup wizard's destination follows the scheme (own folder; an S3 account becomes a provider; pruned by the controller); an existing one is recorded and shown as it is
    - [x] rest-server on a machine (pinned container) offering its disk over MyLab's network, a user per machine
    - [x] The controller creates each machine's repository (recovery key + the machine's own key) and hands over only the machine's key and credentials
    - [x] Each machine's Backups page chooses its provider; status per machine
    - [x] Weekly forget/prune by the controller (admin store access or provider credentials, recovery key); machines only check
    - [x] B2: an application key per machine limited to its folder, deleted when the machine leaves (UNTESTED against Backblaze: built from the API docs, tested with a fake server)
  - [ ] Storage for other labs: storage invites with quotas, `mylab-guests` WireGuard interface (UDP 51821), usage on both sides, port-forwarding guide and reachability check
  - [ ] Move a service between machines (SPEC.md §10.10)
    - [x] Copy directly: copy while running, stop, copy what changed (and deletions), bring its secrets, switch; the old copy is set aside
    - [ ] Through a backup (large data, slow links); the move page suggests a way from the size
    - [ ] Delete kept copies (moved-away and before-move folders) from the UI
  - [x] Needs-attention items for other machines' backups, updates and restarts
  - [ ] Certificate renewal; nodes behind a hub that isn't the controller
  - [x] A machine without an SSH choice (firewall off) is flagged in "needs attention"
- [x] A settings change that restarts MyLab waits for the running maintenance job and starts no new ones (`Coordinator.Hold`); the agent also lets an update finish if MyLab goes away
- [ ] Controller move (planned hand-over) and promotion from a node's replica
- [ ] Edge: public service at home routed through a cloud node (TLS passthrough over WireGuard, PROXY protocol)
- [ ] Failover: cold standby first

## To discuss with the user


- K5: setup-code approach for first-run setup (add to SPEC?)
- Go module path is a placeholder (`github.com/mylab/mylab`); repo location not decided yet
