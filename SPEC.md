# MyLab Specification

This document records decisions (locked and pending) and the reasoning behind them. See PLAN.md for the project brief and TASKS.md for progress.

Status legend: **Locked** = agreed, **Pending** = under discussion, **Open** = not yet discussed.

---

## 1. Guiding Principles — Locked

1. **Recoverable by default.** A user can rebuild their entire lab on new hardware from a single backup. This is the main test for design decisions.
2. **Declarative state.** MyLab stores the *desired* state (services, domains, settings) as files; podman is reconciled to match. This simplifies backups, workers, and debugging.
3. **Local-first.** Everything works without MyLab Cloud or internet access, except public TLS issuance and image pulls.
4. **Transparent output.** Generated artifacts (container definitions, proxy config) are human-readable and inspectable. A comfortable homelabber can always find where things live and what MyLab did.
5. **Simplicity.** Small codebase, minimal dependencies, each chosen collaboratively for maintainability.
6. **Secure by default.** Rootless where possible, nothing public unless explicitly opted in.

## 2. Target Users — Locked

- **Primary:** less technical users who manage a lab after someone else has installed it (e.g. a NUC set up by a family member). Day-to-day management, including installing new services, must be possible entirely from the web UI.
- **Secondary:** comfortable homelabbers, who must be able to inspect and understand everything on disk.
- **Rationale:** Installing Linux and MyLab is an acceptable technical barrier; managing the lab afterwards must not be.

## 3. Deployment Targets — Locked

- **v1:** a dedicated, freshly installed machine. MyLab assumes it owns the host.
- **Reference hardware:** x86_64 Intel NUC-style device.
- **Later:** ARM64 (Raspberry Pi, ARM VPS).
- **Rationale:** A dedicated host avoids conflicts with existing software (ports, podman config, firewall) and greatly reduces the support burden.

### 3.1 Supported Distros — Locked

| Tier | Distros | Meaning |
|---|---|---|
| **Fully supported** | Debian 13 | Tested; installer supported; full host OS management (§3.2) |
| **Planned** | Ubuntu 26.04 LTS | Add when practical; avoid Debian-only assumptions that would make it hard |
| **Unsupported** | Anything else with Podman 5+ | May work; no host OS management, no support |

- **Rationale:** Debian 13 ships Podman 5.4, has a ~5-year support cycle, rarely changes, and has built-in unattended security updates, which makes it well suited to machines nobody logs into. Supporting a single distro keeps the installer, testing and docs simple.
- **Rejected:** Fedora Server (a major upgrade every ~13 months doesn't suit unattended machines); Ubuntu 24.04 (Podman 4.9 has no `.pod` Quadlets); Fedora CoreOS (unfamiliar and harder to troubleshoot).
- Distro-specific logic (package management, update tooling) should sit behind a small internal interface so Ubuntu can be added cleanly.

### 3.2 Host OS Management — Locked

- **Look — Locked (2026-09-27):** a design system with built-in colour themes (docs/DESIGN.md). The default is Teal and violet; each user can choose another (Ocean and magenta, or themes from popular open-source palettes: Catppuccin, Gruvbox, Nord, Solarized, Everforest, Rosé Pine, Tokyo Night) and light, dark or follow the device. Every theme must pass WCAG contrast checks in both modes (a test enforces it).
- **Feedback on host operations — Locked (2026-09-27):** anything that acts on the host (OS updates, backups, applying service changes, setup hooks, restores) shows a one-line status that updates live ("OS updates: Installing updates…", then "✓ … finished: Installed 3 updates"), with the full step-by-step log and command output under a collapsed **Details** for technical users. Pages refresh their results when the operation finishes. Logs are kept in memory (latest run of each kind); results that matter later are stored separately.

- On fully supported distros, MyLab manages the host OS: security/package updates, reboot handling, and eventually major version upgrades.
- Users must be told clearly (docs and UI) that major manual changes to the host OS may limit MyLab's ability to manage it.
- **Rationale:** The primary user (§2) cannot maintain a Linux host. An unpatched host undermines everything else.

#### Time zone — Locked (2026-09-26)

- MyLab shows and schedules everything (nightly backups, maintenance windows) in the **host's** time zone.
- The setup wizard's first step confirms it, pre-filled from the browser's zone; the Owner can change it in Settings. `mylab-agent` applies it with `timedatectl set-timezone`, then MyLab restarts, because Go reads the local zone once at startup.
- If the browser's UTC offset differs from the host's, every page shows a prominent banner saying times are the machine's, with a link for the Owner to change it.
- **Rationale:** cloud images and VMs default to UTC, which silently moved the "02:00" backup to midday for a user in Melbourne.

#### Update & reboot policy — Locked

| What | Policy |
|---|---|
| Security updates | Automatic, daily (`unattended-upgrades`, managed by `mylab-agent`) |
| Other Debian stable updates | Automatic (stable only receives bug and security fixes) |
| Reboots | "Reboot pending" shown in the UI; automatic reboot in a maintenance window (default 04:00). The Owner can change the window or choose notify-only |
| After reboot | Services return through systemd; MyLab health-checks them and flags failures |
| Major upgrades (e.g. 13 → 14) | Never automatic. Owner-initiated, guided, only once MyLab supports the new release, and only after a fresh successful backup |
| MyLab itself | Patch releases automatic; minor/major releases notify and need approval (same as services, §7) |
| Unknown host changes | Basic detection (extra apt sources, held packages, a changed firewall) triggers a warning; more thorough checks later |

- `unattended-upgrades`' own automatic-reboot option is **disabled**; only MyLab decides when to reboot.
- **How it runs (2026-09-27):** MyLab ships `/etc/apt/apt.conf.d/52mylab-unattended-upgrades` (adds stable-updates, no automatic reboot, removes old kernels). Every night after the 02:00 backup, the coordinator runs an "OS updates" job: `mylab-agent` runs `apt-get update && unattended-upgrade` in a transient unit (outside its own sandbox). The job is queued separately from the backup, so a failing backup doesn't hold back security fixes. Debian's own apt timers stay enabled as a fallback. `/run/reboot-required` (written by the kernel's postinst hook) is checked after each run and every 30 minutes; the coordinator then reboots in the window through the agent.
- The Owner sees the last run, any pending restart and why it waits, and can **check for updates now**, **restart now** (refused while a maintenance job runs), and choose automatic restarts at a chosen hour or notify-only (`lab.yaml`: `maintenance.restart`, `maintenance.restart_hour`).
- **Rationale for automatic reboots:** otherwise the primary user may run an unpatched kernel indefinitely. A brief overnight interruption is the better trade-off.

#### Maintenance coordinator — Locked

A single coordinator in `mylab` sequences every disruptive operation (backups, prunes, restores, service updates, package upgrades, reboots) so that conflicting operations never overlap.

- **Nightly order:** backup → automatic service updates → reboot if needed. This means there's always a fresh backup just before any automatic change.
- **Reboots wait** for running backups, restores and updates, then happen once they finish, within the window.
- **Never interrupted:** restores, major OS upgrades, and a backup's prepare step (services stopped or database dumps being written).
- **Resumable long jobs** (e.g. a multi-day first off-site upload) may be paused for a reboot that has been pending for longer than a deadline (default 24h, configurable), and they resume automatically afterwards. restic's repository stays consistent when interrupted.
- The UI explains why a reboot is waiting (e.g. "waiting for backup to finish, 62%").
- **Rationale:** fixed time windows can't handle jobs of unpredictable length. Delaying kernel security fixes for days is worse than pausing a resumable upload.

## 4. MyLab Cloud — Locked (scope), Open (design)

- A future paid hosting service for: resilience (critical services away from home internet), off-site backup targets, and users without their own hardware who still want to own their data.
- **Out of scope for v1**, but worker communication and backup formats must not preclude it.
- Implication: data portability is essential. A user must be able to move services and backups between home, VPS, and Cloud freely.

## 5. Users & Access — Locked

- **MyLab accounts are separate from service accounts in v1.** MyLab manages who can log in to MyLab; each service (Immich, Vaultwarden, …) manages its own users. Unifying them is part of the deferred SSO decision, and the design should allow MyLab to become the identity source later.
- **Fixed roles (not customisable in v1):**

| Role | Permissions |
|---|---|
| **Owner** | Everything: host settings, OS updates, backups and restore, recovery key, user management. At least one Owner must exist. |
| **Admin** | Install, configure, update and remove services; run backups; view logs. No host-level settings, no user management. |
| **Member** | View the dashboard (service links, status) only. |

  - Rationale: the person who sets up the lab (Owner) keeps control of risky host settings, while the day-to-day user (Admin) can still manage services freely.
- **Authentication:** local accounts, passwords hashed with argon2id. **TOTP 2FA** in v1, required for Owners and for any public admin UI. Passkeys later.
- **Password recovery:** host command line only (`sudo mylab user reset-password <name>`, `reset-2fa <name>` for a lost phone). No email reset (it would need mail setup and adds attack surface), and no recovery codes: the command line is simpler for a home lab.
- **People page (2026-09-27):** the Owner adds people (a temporary password is shown once and must be changed at first login), changes roles, gives someone a new temporary password, turns off someone's two-factor, and removes people. Not on their own account (use Account), and the last Owner can't be demoted or removed. An Owner who forgot their own password uses the command line.
- **Audit log:** records who did what and when, to support remote help and troubleshooting.

### 5.1 MyLab Data Storage — Locked

- **Desired state:** readable YAML files in `/var/lib/mylab/` (Principles 2 and 4).
- **Runtime data** (users, sessions, audit log, job history): a **SQLite** database in `/var/lib/mylab/`. A single file, no server, easy to back up. The backup prepare step must take a consistent copy (SQLite online backup / `VACUUM INTO`), not a raw copy of the file.

## 6. Container Runtime — Locked (principle)

- Podman, **rootless where possible**; rootful only where a service genuinely requires it (e.g. low ports, host networking, device access), and this is visible to the user.
- **Rationale:** security is a priority; rootless limits the blast radius of a compromised container.

### 6.1 MyLab Process Architecture — Locked

MyLab runs **directly on the host** (not in a container), as two processes shipped in one `.deb` package:

| Component | Runs as | Responsibilities |
|---|---|---|
| `mylab` | Unprivileged `mylab` user, strict systemd sandboxing | Web UI, API, desired state, rootless service management (Quadlets, status, logs) |
| `mylab-agent` | root | A small fixed set of privileged operations only: OS updates, reboots, rootful Quadlets, firewall. Also sets the host time zone. Listens on a local socket reachable only by `mylab`. |

- Nodes in a multi-machine lab run the same package (§10).
- MyLab updates itself through apt, alongside host OS updates (§3.2).
- `mylab` is hardened with systemd sandboxing (`ProtectSystem`, `ProtectHome`, `NoNewPrivileges`, `PrivateTmp`, system call filtering, limited writable paths) to get most of a container's isolation.
- **`mylab` never runs `podman` directly — Locked (2026-09-26).** Rootless Podman needs the setuid `newuidmap`/`newgidmap` helpers, which `NoNewPrivileges` disables. So `mylab` only talks to the `mylab` user's systemd manager (`systemctl --user`, `systemd-run --user`), which runs Podman outside the sandbox. One-off commands (creating secrets, database dumps, `podman unshare` for backups) go through `systemd-run --user`.
- **Rationale:**
  - Keeps root code small and easy to review; privileged operations are explicit, not implied by broad socket access.
  - Podman/Quadlets rely on host systemd for lifecycle, so a containerised manager would need root-equivalent host access anyway.
  - Two components, a standard Debian install and update path, and no "container updating itself" problem.
- **Considered:**
  - *Container + many host mounts (Portainer/Komodo style):* those tools rely on the Docker daemon socket, which is root-equivalent. Under Podman they would also need the Quadlet directory, systemd D-Bus access, and a root agent regardless.
  - *Container web UI + host agent (A′):* a valid design that isolates the web-facing part. Rejected for now: it means either much more root code or three components, and the gain is limited because the agent API must allow arbitrary service deployment. The mylab↔agent API boundary leaves this option open later.
  - *Single root process:* rejected; the web UI would run as root.
- This deviates from PLAN.md's original "mylab orchestration container" wording. Services still run in containers; MyLab itself does not.

### 6.2 Language: Go — Locked

- Both `mylab` and `mylab-agent` are written in Go, in a single codebase.
- **Rationale:** Builds a single static binary, which makes `.deb` packaging trivial. Low memory use suits NUC-class hardware. Podman is written in Go and there are mature systemd/D-Bus libraries. The language is small, readable and widely known, which fits the simplicity principle. The maintainer wants to learn it.
- **Considered:** Rust (steeper learning curve, slower to build with); Python (easiest to write, but harder to package and ship as a host service).

### 6.3 Web UI: Go templates + htmx + Alpine.js — Locked

- Server-rendered HTML, embedded in the `mylab` binary. No JavaScript build step.
- Step-up approach for interactivity. Use the lowest level that works:
  1. **htmx** for server interactions (default)
  2. **Alpine.js** for small in-browser behaviour (toggles, show/hide, simple validation)
  3. **Standalone JS libraries** for heavy widgets (e.g. xterm.js for a terminal, a chart library)
  4. **A separately built component** only when a feature genuinely needs it, decided case by case
- **Architecture rule:** business logic lives in core Go packages. HTTP handlers are a thin layer on top. This keeps the UI replaceable and supports the master/worker API and a possible CLI.
- **Rationale:** MyLab's UI is mostly forms, lists, status and logs, with state held on the server, which is htmx's strength. Validation lives in one place (Go). A small toolchain suits the simplicity principle and a maintainer learning Go.
- **Rejected:** React islands. They bring back a Node build toolchain and a second UI paradigm for limited gain.
- **Known limits:** UIs with a lot of browser-side state (drag-and-drop dashboards, visual editors) would need step 4.

### 6.4 Service Execution: Podman Quadlets — Locked

- MyLab generates **Quadlet** files (`.container`, `.volume`, `.network`, etc.) and systemd runs them. Rootless units go in `~/.config/containers/systemd/`.
- Users never write Quadlets directly. They are generated from MyLab's own service definitions (see §8).
- **Rationale:**
  - Official Podman feature (4.4+), maintained by the Podman project, and an open, well-supported format.
  - systemd provides start-at-boot, restart-on-failure, dependency ordering, and logging (`journalctl`) with nothing extra to build.
  - Designed for rootless operation (§6).
  - The generated files are plain text a homelabber can inspect (Principle 4), and they are declarative (Principle 2).
- **Rejected:** podman-compose. It is community-maintained, supports only part of the Compose spec, and needs extra pieces for boot and restart handling. Also rejected: running docker-compose against the Podman API socket, which brings back a daemon-style model with the same lifecycle gaps.
- Rootless services need `loginctl enable-linger` for the service user so they start at boot without anyone logging in.

### 6.5 Reverse Proxy & TLS — Locked

- **Proxy: Caddy**, run as a rootless container from a MyLab-built image that includes the DNS provider plugins.
- **DNS providers — Locked (2026-09-26):** Cloudflare, Amazon Route 53, Porkbun, Gandi, GoDaddy, Namecheap and Spaceship, defined in one registry (`internal/dns`). Each provider's credentials are stored as Podman secrets and passed to Caddy as environment variables, so they never appear in the Caddyfile. Before saving, the wizard checks credentials by **creating and deleting a temporary TXT record** through the provider's public HTTP API (Namecheap: a read-only check, since its API can only rewrite whole zones). That tests what certificate issuance needs.
- **Dependencies for provider APIs:** prefer plain HTTP calls or small generic libraries; avoid large vendor SDKs (e.g. the AWS SDK). MyLab's checks use the standard library only (including AWS SigV4 signing for Route 53).
- MyLab generates a readable Caddyfile from desired state and reloads Caddy (same pattern as Quadlet generation).
- **Not user-selectable.** One proxy keeps testing and config generation manageable. Users can add custom Caddy snippets, globally and per service.
- **Rootless on 80/443:** `mylab-agent` lowers `net.ipv4.ip_unprivileged_port_start` so the proxy needs no root.
- **Proxy networking (Option C) — Locked (2026-09-26):**
  - Caddy runs in its **own pasta network namespace** (`Network=pasta:--map-host-loopback,169.254.1.3`), publishing 80/443. Pasta keeps each visitor's **real IP**, which the private-route check and CrowdSec rely on.
  - Each routed container stays on its service's private network and is **published on `127.0.0.1:<port>`** only. Caddy reaches it at `169.254.1.3:<port>`, which pasta maps to the host's loopback. The MyLab UI listens on `127.0.0.1:8480` and is reached the same way.
  - Ports come from a central range (20000–29999), are allocated once, and are stored in the instance file (`routes.<name>.port`) so they never change.
  - **Why:** on the Debian 13 VM, rootless bridge networks replaced client IPs with `10.89.0.x` (Podman's `rootlessport`), which Caddy would treat as private, exposing "private" services to the internet. Rootless Podman can't put one container on both pasta/host networking and a bridge network, and the host side can't reach bridge addresses.
  - **Rejected:** Caddy on host networking (works, but shares the host's network namespace); a host-network forwarder using the PROXY protocol in front of a bridge-network Caddy (two proxies, no HTTP/3).
- **TLS: Let's Encrypt via DNS-01.** This is required so that private (LAN/VPN-only) services get valid certificates without being reachable from the internet.
- **Wildcard certificate by default** (e.g. `*.lab.example.com`), with services on subdomains sharing it. Rationale:
  - One issuance and renewal instead of one per service, so fewer rate-limit issues.
  - Subdomain names don't appear in public Certificate Transparency logs, so the list of services you run isn't published.
  - DNS-01 is already required, so there's no extra setup.
  - Trade-off: one certificate covers all services. That's acceptable on a single-owner host.
- **Rationale for Caddy:** best automatic HTTPS, readable config, easy custom snippets. **Considered:** Traefik (many DNS providers built in, but verbose config tied to container labels); Nginx + certbot (familiar, but TLS is a separate tool and config is verbose).

### 6.5.1 DNS Records & Local Access — Locked (2026-09-26)

MyLab manages the DNS records its services need, through the same DNS provider credentials used for certificates.

- **Private services (default):** one **wildcard record** (`*.<domain>`) pointing at the lab's **LAN IP**, in public DNS. Devices at home reach services without local DNS. Anyone can look the record up, but only the home network can reach the address, and service names stay out of public DNS (like the wildcard certificate).
- **Router DNS rebinding protection:** some routers drop DNS answers containing private IPs. MyLab **detects** this by comparing the home network's resolver with a public resolver, and shows instructions (allow the domain in the router, or use local DNS).
- **Local DNS** (e.g. Pi-hole) is optional, for later. Getting every device to use it means changing the router's DHCP DNS setting (router-specific, sometimes impossible). MyLab running DHCP itself was **rejected**: if the lab dies, the home network stops working.
- **Public services (stage 2):** explicit records pointing at the **public IP**; explicit records override the wildcard. Devices at home reach them via the router's NAT loopback (MyLab can test this).
- **Dynamic public IPs (stage 2):** handled by a dynamic DNS container, [qdm12/ddns-updater](https://github.com/qdm12/ddns-updater) (broad provider support), configured by MyLab.
- **Cloudflare proxy (stage 3):** offered per public service, **off by default**, with advantages and disadvantages listed in the UI:
  - For: hides the home IP; DDoS protection; works around some ISP port blocking.
  - Against: Cloudflare decrypts the traffic (at odds with "own your data"); free plan upload limit of 100 MB per request; Cloudflare's terms restrict large media streaming on the free plan.
  - When proxied, Caddy trusts `CF-Connecting-IP` **only from Cloudflare's published IP ranges**, so the private check and CrowdSec still see real visitors.
  - Packages declare proxy warnings, e.g. large uploads (Immich, until its chunked uploads land) or media streaming (Plex, Jellyfin, stricter). The format is to be designed.
- **Ownership:** MyLab only changes records it created (tracked in its state, plus a comment where the provider supports it). An existing record with the same name that MyLab didn't create is never overwritten automatically; the UI explains the conflict. A record that already has the right value is adopted.
- **Taking over a record:** the Owner can explicitly take over a conflicting record from the dashboard. A confirmation page shows the current and new values, warns that anything relying on the current value stops working, and needs an "I understand" tick. The takeover is recorded in the audit log.
- **Switching management on or off** is done from the dashboard's "Home network access" card (Owner only), with the Namecheap risk opt-in where relevant, and applies without a restart. Turning it off leaves records in place.
- **Namecheap:** its API can only replace a domain's whole record set, so record management there is **off unless the Owner ticks an opt-in** with a clear warning, and strongly recommends another provider (e.g. Cloudflare).
- **No usable public IP (CGNAT):** public exposure needs a relay. Future options: Cloudflare Tunnel, or a MyLab Cloud relay (§4).
- **Stages:** 1) wildcard to LAN IP + rebinding check; 2) public records + dynamic DNS (with public exposure, firewall, CrowdSec); 3) Cloudflare proxy.

### 6.6 Domains — Locked

- **v1 requires a user-owned domain** with a supported DNS provider (API token for DNS-01). This is set up once, at install time.
- **Future:** free MyLab-provided subdomains (e.g. `name.mylab.cloud`) with DNS managed for the user, which also links to MyLab Cloud (§4).
- **Rejected for v1:** a local CA (a root certificate on every device is bad UX); plain HTTP (browser warnings, and some clients such as Bitwarden require HTTPS).

### 6.7 Storage Layout — Locked (defaults may change before launch)

Default paths:

```
/var/lib/mylab/                 # MyLab's own state: desired state, secrets, users
/srv/mylab/services/<service>/  # per-service data (bind mounts)
    data/
    db/
/srv/mylab/media/               # large shared media; can be a separate disk
```

- **Paths are configurable**, and all defaults are defined in **one central place** in the code so they can be changed easily. No migration support is needed before launch; the defaults will be locked before release.
- **Bind mounts, not Podman named volumes.** Data sits in a visible, predictable location (Principle 4), and it's easy to back up and move (Principle 1).
- The media location can be configured separately, e.g. a small system SSD plus a large data drive.
- **Open / to prototype early:** rootless user ID mapping. Files owned by UIDs inside containers appear as mapped IDs (e.g. 100000+) on the host. We need to settle how this affects browsing, backups, and media shared between services (e.g. Jellyfin and Immich), and where Podman's `keep-id` option helps.

### 6.8 Databases — Locked

- **One database container per service** (e.g. Immich gets its own Postgres), not a shared instance.
- MyLab **standardises the pattern**: every package uses the same MyLab-managed Postgres/Redis setup, with the same data location (`<service>/db/`), backup method, health checks and upgrade process.
- **Rationale:**
  - Each app runs the exact database version and extensions it's tested with (e.g. Immich needs specific Postgres versions and a vector extension).
  - Upgrades and failures are isolated to one service.
  - Each service is self-contained, so moving it to another host or MyLab Cloud means moving one folder plus its definition (§4 portability).
  - The cost, roughly 30–50 MB RAM per idle instance, is acceptable on NUC-class hardware.
- **Rejected:** a single shared Postgres. It saves RAM, but it couples versions and upgrades, makes it a single point of failure, and makes backing up or moving one service harder.

### 6.9 Backups — Locked

**Tool: restic**, called as a CLI (`--json` output) behind a small internal interface (backup, restore, list snapshots, prune, check), so the tool can be swapped later.

- **Rationale:**
  - A Go single binary; encrypted and deduplicated, with many backends (local, SFTP, S3, B2, rclone, REST server).
  - Users can recover independently with the plain `restic` CLI and the recovery key, even if MyLab is broken. There's a large community and many docs (Principles 1 and 4).
  - MyLab owns scheduling and retention as part of desired state, so there's no competing policy engine.
  - The append-only `rest-server` gives simple ransomware protection and is a natural future MyLab Cloud target.
- **Considered: Kopia.** It's faster, uses less memory (≈4 MB average chunks vs ≈1 MB, plus a memory-mapped index) and has a richer server. It's a realistic fallback if restic's memory or performance becomes a problem with very large libraries; the interface keeps switching cheap.
- **Expected resources** (estimate, ~750 GB / 100k photos and videos): ~150–400 MB RAM for nightly backups, up to ~1 GB for prune/check. Mitigations: `GOGC=20`, infrequent pruning, a systemd memory limit on backup jobs, quiet-hours scheduling.

**Backup process:**
1. **Prepare:** per-package hooks create a consistent state (e.g. `pg_dump` to `<service>/db-dump/`, SQLite's backup command, or a brief stop if there's no safe live method).
2. **Snapshot:** `/var/lib/mylab` plus each service's folder.
3. **Verify:** periodic `restic check` and occasional test restores.
4. **Restore — Locked (2026-09-27):** per service, by the Owner, from the Backups page: choose one of the backups that contain the service (newest first), confirm, and it runs as an uninterruptible coordinator job with a live log. The service stops, its current data is moved aside (`<dir>.before-restore-<time>`), the backup's files and database dump come back, and it starts again. Kept copies are listed on the restore page and deleted only when the Owner chooses. Restoring a whole lab onto a new machine comes with the installer.

**Restore:** per service, or the full lab onto a new machine (install MyLab → point at the backup → enter the recovery key).

**Defaults & policy:**
- Nightly; keep 7 daily, 4 weekly and 6 monthly.
- Packages classify data as **critical** (backed up by default: photos, passwords, repos) or **replaceable** (not backed up by default: media libraries, caches).
- **Destinations:** local disk, remote (S3/B2/SFTP), later MyLab Cloud. **Local-only is allowed, with a persistent warning** recommending off-site.
- **3-2-1 backups — Locked (requirement), Open (design), 2026-09-26:** MyLab should guide users towards 3-2-1 (3 copies of the data, on 2 different kinds of storage, 1 off-site), not just warn. That means:
  - **Suggestions in the UI** explaining what's missing and why, e.g. "Your backups are only on this machine" → suggest a USB disk, then an off-site destination.
  - **Multiple backup destinations**, e.g. a local disk *and* an off-site repository. Likely design: back up to a primary repository, then `restic copy` to the others (efficient, same snapshots everywhere; secondaries initialised with `--copy-chunker-params`). To be designed.
  - The dashboard shows the status of each destination, so a quietly failing off-site copy is noticed.
- **Recovery key:** shown at setup; the user must save or print it, with a strong prompt for the person doing the setup to keep a copy. Optional key escrow via MyLab Cloud later.
- **Large first uploads:** resumable, with progress and an estimated finish time shown in the UI, a configurable bandwidth limit, and possibly a "seed locally, then take off-site" option.

### 6.10 Host Security — Locked

Layered defaults:

1. **Firewall:** default-deny inbound. Allow 80/443 plus ports that specific services need (e.g. WireGuard, Pi-hole DNS on the LAN). `mylab-agent` generates a readable **nftables** ruleset file and reloads it (nftables is Debian 13's default, so no extra dependency).
   - **SSH access — Locked (2026-09-27):** the Owner chooses in the setup wizard (step 2) and in Settings: home network only (recommended, default), anywhere, or nobody. The page lists current SSH connections (read from `/proc/net/tcp`) and a choice that would cut one off must be confirmed. Stored in `lab.yaml` (`firewall.ssh`).
   - MyLab applies the firewall after every apply, so enabled package ports are opened (home network only unless public). Until the SSH question is answered, MyLab leaves the firewall alone.
2. **Admin UI is LAN/VPN-only by default.** Public access requires explicit opt-in **and** mandatory 2FA. Rationale: the UI controls the whole host and is the highest-value target.
3. **SSH hardening:** key-only authentication, no password login, no root login. Applied by the installer with a warning and confirmation to avoid lockout. The user can opt out, but **the UI shows a persistent warning** while hardening is disabled.
4. **Intrusion protection: CrowdSec**, enabled automatically once any service is public. It uses a Caddy module ("bouncer") to block at the proxy, and scenarios for Caddy and SSH logs. Not run on fully private labs (little benefit, saves resources).
   - **fail2ban is not run alongside it.** CrowdSec covers the same local log-parsing and banning, and running both would mean duplicate bans, two configs, and two tools changing the firewall.
   - **Signal sharing: on by default, opt-out.** Opting out is a **prominent choice during install** (and can be changed later in the UI). Only attack data is shared (attacker IPs, attack types, timestamps), nothing about the user's own users or data.
   - **If the user opts out:** local detection and banning keep working fully, like fail2ban but with better parsers and blocking at the proxy. They lose the community blocklist, i.e. pre-emptive blocking of IPs already seen attacking others. Rule and parser updates from the CrowdSec Hub are separate and still work.
5. **Baseline:** automatic security updates (§3.2), Caddy rate limiting on login pages, secrets in files readable only by MyLab (0600), and container images only from trusted registries, pinned to specific versions.

## 7. Updates — Locked

- **Default:** notify when updates are available; user applies them manually.
- **Per-service opt-in:** automatic updates for all versions, or only minor/patch versions.
- MyLab self-update: patch releases automatic; minor/major notify (see §3.2).

## 8. Service Definitions & Catalog — Locked (principle)

- The service definition format is user-facing from early on (custom services is a nice-to-have).
- Breaking changes to the format are acceptable before 1.0.
- **Official packages are curated and opinionated.** Upstream `docker-compose.yml` files are a starting reference, not something we import directly. Each official package includes MyLab-specific configuration so it runs production-ready in the MyLab ecosystem:
  - automatic TLS / reverse proxy wiring
  - backup definitions (what data to back up, how to quiesce it)
  - use of MyLab-managed shared resources (e.g. common databases)
  - standard storage locations
- **Rationale:** Upstream compose files are rarely production-ready as a one-click install. Being opinionated is what makes one-click install and management reliable for less technical users.

### 8.1 Format — Locked (initial; may be revisited)

- **Two file kinds:**
  - **Package** (`catalog/<name>/package.yaml`): authored by MyLab or the community. Describes how to run the app well: containers, settings, storage, databases, routes, health checks.
  - **Instance** (`/var/lib/mylab/services/<name>.yaml`): the user's choices (package version, subdomain, exposure, setting values, update policy). This is part of desired state.
- MyLab combines package + instance and **generates** Quadlets, Caddy site snippets, and backup jobs.
- **Our own YAML schema** (`schema: mylab/v1alpha1`) models what MyLab needs to understand, with a **raw Quadlet passthrough** for unusual needs.
  - Rejected: packages shipping Quadlet templates plus a manifest. Every package would do things differently, and MyLab couldn't reason about them as well.
- **Backups are declared, not scripted:** `databases` entries (`sqlite`, `postgres`, …) and `storage` classes (`critical` / `replaceable`) drive MyLab's standard backup methods.
- **Secrets** are declared (optionally `generate: true`) and delivered as Podman secrets, so they never appear in generated unit files.
- **Images are pinned** by tag + digest.
- **Templating:** Go templates (e.g. `{{ .Routes.web.Host }}`, `{{ .Settings.x }}`).
- **Versioning:** `<upstream version>-mylab.<package revision>`.
- **Additional schema features** (from the Immich stress test):
  - **`when:` conditions** on containers and devices, for optional components (e.g. Immich machine learning), so disabled features don't use RAM.
  - **`devices:`** as abstract requests (e.g. `gpu: intel`) that MyLab maps to device paths and groups (rootless `/dev/dri` needs `keep-groups`).
  - **`depends_on:`**, which becomes systemd ordering (`After=`/`Requires=`).
  - **Route options** at the Caddy level (e.g. `max_body_size`; more expected: timeouts, websockets).
  - **Storage `location:`** (e.g. `media` → the large data disk) and **per-directory storage classes**, so regenerable data (thumbnails, transcodes, caches) is excluded from backups.
  - **Database `image` override** for custom builds (e.g. Immich's Postgres), still following the standard database pattern; **`backup: false`** for transient stores (e.g. Redis job queues).
- **`ports:` — Locked (2026-09-26):** for protocols Caddy can't proxy (SSH, DNS, WireGuard), a package declares a container port with a title, a suggested host port (≥1024) and a protocol. Ports are **off unless the package enables them**; an Admin turns them on per service and can move them. Enabled ports are published on every host address (`PublishPort=<host>:<container>/<proto>`) and are **private** (home network only) via the firewall. Public ports come with the firewall and CrowdSec, like public routes. MyLab rejects clashes with other ports, the UI port and the web-service range (20000–29999). Templates see `{{ .Ports.<name>.Enabled }}` and `.HostPort` (e.g. for clone URLs).
- **`hooks:` — Locked (2026-09-27):** commands run inside a service's container.
  - `on: install` runs once after the first successful start (e.g. creating Forgejo's admin). It is retried while the app starts, so it must be safe to re-run; a failure shows under "Needs attention" with **Try again**.
  - `on: action` is a button on the service page for Admins (e.g. "Reset a password"), with optional `inputs` (string or secret).
  - User-provided values reach hooks **only as environment variables** (`env:` templates, `secrets:`), never templated into the command, so they can't change what a shell script does.
  - Settings can be `required`, have a `min_length`, and be `install_only`: asked at install, used only by install hooks. An `install_only` secret is **deleted once the install hooks succeed**, so MyLab doesn't keep a copy of e.g. the admin password.
  - Only `install` and `action` for now; `upgrade` hooks when a package needs them.
- **Service settings — Locked (2026-09-27):** most of an app's settings stay in the app. Packages list `links:` into its own admin pages (Forgejo's site administration, Vaultwarden's admin panel), shown first under Settings on the service page. MyLab's own settings (not secrets, not `install_only`) and the web address are editable there; saving regenerates and restarts the service. A secret marked `reveal: true` (e.g. Vaultwarden's admin token) can be shown to Admins on request, and each view is audited.
- **Future:** storage shared between services (e.g. Jellyfin reading Immich's library, read-only); package upgrade notes and migration steps for upstream breaking changes.
- **Reference examples:** `docs/examples/vaultwarden.yaml` (simple), `docs/examples/forgejo.yaml` (Postgres, an optional SSH port), `docs/examples/immich.yaml` (complex).

## 9. MVP Scope — Locked

Single node: web UI, reverse proxy with automatic TLS, three catalog services (e.g. Vaultwarden, Forgejo, Immich — increasing complexity), backup and restore. Multiple machines (§10), SSO, DNS, and monitoring follow after the core is solid, but the design should anticipate multiple machines.

---

## 10. Multiple Machines — Locked (2026-09-27)

One lab can span several machines: extra boxes at home, VPSes in the cloud, and later MyLab Cloud. This section is the design to build towards; nothing here is built yet except where marked. It replaces "master/worker" elsewhere in this document.

### 10.1 Roles

- **Node:** any machine running MyLab. Every node runs its own services (Quadlets, reconciler), its own Caddy and certificates, its own backups, OS updates, firewall and agent, exactly as a single-machine lab does today.
- **Controller:** one node also holds the lab's desired state (§1.2: services, placement, settings), users and sessions, and serves the UI. It decides placement; nodes do the work.
- **The controller can move to any node** (chosen, not automatic), e.g. from a home NUC to a VPS:
  - Every node keeps a **replica of the lab's state** (desired state and the MyLab database), pulled with its assignments. The controller encrypts it with the **recovery key** (§6.9) before sending it; nodes store it but can't read it, because the recovery key is never stored on a node other than the controller.
  - **Planned move:** the controller hands over (freezes changes, sends the latest state, the new controller takes the address and certificate authority) and becomes a plain node.
  - **Disaster:** the Owner promotes a node from its replica by entering the recovery key (they have it written down for exactly this). At worst it misses changes since its last pull. Two controllers must never both be active: promotion revokes the old one's authority (a new epoch number that nodes follow).
- The first machine installed is the controller; a lab with one machine is a lab with one node that is also the controller. Single-machine labs stay exactly as simple as today.

### 10.2 Private network: MyLab-managed WireGuard

- MyLab runs a **WireGuard** network between its nodes (WireGuard is in Debian's kernel; `mylab-agent` manages the interface and keys). Every node gets an overlay address (e.g. `100.96.0.x`).
- **No inbound ports at home:** home nodes dial out to reachable nodes (a VPS with a public IP), so a home controller needs no port forwarding. Nodes that can reach each other directly do; others go via a reachable node (hub and spoke first, a mesh later).
- All controller ↔ node traffic runs inside WireGuard **and** uses mutual TLS, so a leaked WireGuard key alone isn't enough.
- **Joining:** the Owner creates a one-time join code in the UI (short-lived, single use) and runs `mylab node join <code>` on the new machine (or picks "Join an existing lab" in its setup wizard). The code carries the controller's address and a CA fingerprint; the node gets its WireGuard peer config and a client certificate from the controller's CA.
- "Private" (home network only, §6.10) widens to **home network or MyLab's WireGuard network**. The same network is the base for remote access for people later (a phone joining over WireGuard).

### 10.3 Controller ↔ node protocol

- **Nodes pull.** Every node periodically (and when nudged) fetches **its assignment**: the instances placed on it (package, version, settings, secrets it needs) plus lab settings it needs (domain, DNS provider, backup destinations). It generates and reconciles locally with the same code as today, then reports back **status**: service health, apply results, activity logs, backups, updates, restart needs.
- **Offline-tolerant:** if the controller is unreachable, a node keeps running its last assignment and queues its reports (§1.3 local-first). Assignments carry a version; nodes apply only newer ones.
- **Actions** (restart a service, run an action hook, restore, update now) are requests the node picks up on its next pull (or immediately when nudged), with results in the report. The same request types the in-process code uses today (e.g. the agent's JSON API) cross the network unchanged.
- **Secrets:** user-provided secrets travel only to the nodes that need them, over mTLS inside WireGuard; generated secrets are made on the node that uses them and never leave it (backups hold them, encrypted).
- **Backups per node:** each node backs up its own services to **its own repository** (or its own folder at the same destination) with **its own key**. The recovery key is added to every repository as a second key (restic supports several), so the Owner can restore anything with it, while a compromised node exposes only its own backups and its key can be revoked.
- **Packages:** the controller's catalog is the source of truth; nodes fetch package files they don't have (same version pinning).

### 10.4 Placement and moving services

- Every instance has a **`host`** (a node ID); default: the controller. The install form asks where to run it when the lab has more than one node, with hints (RAM, disk, GPU, home or cloud).
- **Moving** a service = back up on the old node, restore on the new one, switch its route, remove the old copy (existing per-service backup and restore, §6.9). Portability is the requirement from §4.

### 10.5 Public access through a cloud node ("edge")

For services hosted at home but public (e.g. Immich shared with family), without opening ports 80/443 at home:

- A cloud node can act as an **edge**. It forwards with Caddy's layer-4 plugin (`caddy-l4`, added to MyLab's Caddy image like the DNS plugins; ASSUMPTIONS to verify SNI routing and PROXY protocol). Public DNS records point to the edge's public IP; the edge forwards each connection **over WireGuard** to the home node that runs the service.
- **TLS passthrough by default:** the edge routes on the TLS SNI name and never decrypts; certificates and keys stay on the home node, so the cloud provider can't read the traffic (in line with "own your data", §4; unlike the Cloudflare proxy, §6.5.1). Visitors' IP addresses reach the home node via the PROXY protocol, so private checks, rate limiting and CrowdSec (§6.10) still see real visitors.
- Terminating TLS at the edge (caching, fewer hops) is an option per service later, clearly marked as letting the edge see the traffic.
- The home firewall stays closed to the internet; only WireGuard (outbound) is needed. The same mechanism is how a **MyLab Cloud relay** would work for people behind CGNAT (§6.5.1).
- Home devices keep reaching home services directly (local DNS records, §6.5.1), not via the edge.

### 10.6 Failover (later)

Failover is service-specific, so packages declare what they support:

- **none** (default): if the node is down, the service is down; the UI says so.
- **cold standby:** a second node keeps recent backups of the service; if the primary is unreachable for a set time, the Owner (or, when enabled, MyLab) restores the latest backup there and switches the route. Data since the last backup is lost; suits most home services.
- **replicated:** the service keeps a live copy on another node with its own replication (e.g. Postgres streaming, Vaultwarden via its database), declared per package. Much later.
- Automatic failover needs a way to tell "node down" from "network split" (at least a third node or the edge as a witness), so it comes after manual failover.

### 10.7 What to build now (no networking yet): seams

So single-machine code doesn't paint us into a corner:

1. **`host` on every instance**, defaulting to the controller; loading services filters by host.
2. **Controller/node split in code:** the UI-facing manager (desired state, users, placement) talks to a **`Node` interface** for everything that touches a machine (apply, status, hooks, actions, restores, activity, updates, restarts, firewall). Today: one in-process implementation. Later: a remote implementation speaking the same plain-data requests.
3. **Status, activity and restart state are per node**, and the UI's "This machine" areas are per node (e.g. `/settings/machines/<id>/updates`) once there's more than one.
4. **Node identity:** every install gets a stable node ID and name at setup.

Then a **two-VM prototype**: a second dev VM joins the first over WireGuard and runs one service placed on it.

### 10.8 Decisions (2026-09-27)

- **Network layout:** hub and spoke (machines talk through a reachable node, usually a VPS). Direct links between two networks behind NAT (hole-punching, full mesh) only when a real case needs them.
- **Edge forwarding:** Caddy's `caddy-l4` plugin, not a forwarder of our own.
- **Keys:** the recovery key protects the state replicas and is added to every node's backup repository, but is stored only on the controller; each node has its own backup key (§10.3).
- **MyLab Cloud:** nodes run by MyLab Cloud are trusted like any hosting provider (a machine running your services can see them; there's no real alternative). Roles that don't run services stay **zero-knowledge**: a relay or edge uses TLS passthrough, and a backup target only ever receives restic-encrypted data. Billing is out of scope until MyLab Cloud exists.

### 10.9 Prototype as built (2026-09-27)

What the two-VM prototype does, where it refines the design above:

- **Hub:** the controller is the WireGuard hub (`100.96.0.1`, UDP 51820; network `100.96.0.0/24`). A reachable hub other than the controller comes later.
- **Joining:** the Owner makes a code in Settings → Machines and runs `sudo mylab node join <code>` on the new machine. The code carries a **one-time WireGuard key**, so only the hub's UDP port needs to be reachable (no public TLS endpoint). Over WireGuard, the machine checks the controller's CA against the code's fingerprint, sends its own WireGuard key and a CSR, and gets its certificate; the hub then replaces the one-time key with the machine's own. Codes work once and expire after an hour.
- **Protocol:** every machine serves a lab API on its overlay address (port 8471), mutual TLS with the lab's CA. The controller **sends** assignments and makes requests directly (status, service states, activity, hooks, actions, secrets, backups, restores, updates, restarts, settings); a node also **pulls** its assignment when it starts and every 15 minutes, and keeps its last one (without secrets) to run while the controller is away. Assignments carry a version; older ones are ignored. Typed-in secrets travel once and are never stored on disk.
- **Web routes of a service on another machine:** the machine publishes them on its overlay address (firewall: WireGuard interface only) and the controller's Caddy forwards to it (TLS ends on the controller for now; passthrough is §10.5's edge).
- **Nodes** run no UI, Caddy or DNS. Per-machine pages live under `/settings/machines/<id>/…`.

## Deferred Decisions (post-MVP)

- SSO platform, and whether it is mandatory (linked to unifying MyLab and service accounts, §5)
- Default DNS manager (e.g. Pi-hole) and split-horizon DNS
- Built-in uptime monitoring
- Remote access / remote support for the person who set up the lab
- MyLab Cloud design (§4)
- Shared storage between services (§8.1)
