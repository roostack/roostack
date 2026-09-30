# Roostack Specification

This document records decisions (locked and pending) and the reasoning behind them. See PLAN.md for the project brief and TASKS.md for progress.

Status legend: **Locked** = agreed, **Pending** = under discussion, **Open** = not yet discussed.

**Name (2026-09-28):** the project is **Roostack**, pronounced "Roost Stack" (`roostack` in code, packages, paths and the Linux user). Its domain is **roostack.cloud** (the Go module path is `roostack.cloud/roostack`); **roost.ac** is for subdomains Roostack hosts for people's labs (§6.5). Earlier drafts called it MyLab; there was no release, so nothing migrates from that name.

---

## 1. Guiding Principles — Locked

1. **Recoverable by default.** A user can rebuild their entire lab on new hardware from a single backup. This is the main test for design decisions.
2. **Declarative state.** Roostack stores the *desired* state (services, domains, settings) as files; podman is reconciled to match. This simplifies backups, workers, and debugging.
3. **Local-first.** Everything works without Roostack Cloud or internet access, except public TLS issuance and image pulls.
4. **Transparent output.** Generated artifacts (container definitions, proxy config) are human-readable and inspectable. A comfortable homelabber can always find where things live and what Roostack did.
5. **Simplicity.** Small codebase, minimal dependencies, each chosen collaboratively for maintainability.
6. **Secure by default.** Rootless where possible, nothing public unless explicitly opted in.

## 2. Target Users — Locked

- **Primary:** less technical users who manage a lab after someone else has installed it (e.g. a NUC set up by a family member). Day-to-day management, including installing new services, must be possible entirely from the web UI.
- **Secondary:** comfortable homelabbers, who must be able to inspect and understand everything on disk.
- **Rationale:** Installing Linux and Roostack is an acceptable technical barrier; managing the lab afterwards must not be.

## 3. Deployment Targets — Locked

- **v1:** a dedicated, freshly installed server. Roostack assumes it owns the host.
- **Reference hardware:** x86_64 Intel NUC-style device.
- **Later:** ARM64 (Raspberry Pi, ARM VPS).
- **Rationale:** A dedicated host avoids conflicts with existing software (ports, podman config, firewall) and greatly reduces the support burden.

### 3.1 Supported Distros — Locked

| Tier | Distros | Meaning |
|---|---|---|
| **Fully supported** | Debian 13 | Tested; installer supported; full host OS management (§3.2) |
| **Planned** | Ubuntu 26.04 LTS | Add when practical; avoid Debian-only assumptions that would make it hard |
| **Unsupported** | Anything else with Podman 5+ | May work; no host OS management, no support |

- **Rationale:** Debian 13 ships Podman 5.4, has a ~5-year support cycle, rarely changes, and has built-in unattended security updates, which makes it well suited to servers nobody logs into. Supporting a single distro keeps the installer, testing and docs simple.
- **Rejected:** Fedora Server (a major upgrade every ~13 months doesn't suit unattended servers); Ubuntu 24.04 (Podman 4.9 has no `.pod` Quadlets); Fedora CoreOS (unfamiliar and harder to troubleshoot).
- Distro-specific logic (package management, update tooling) should sit behind a small internal interface so Ubuntu can be added cleanly.

### 3.2 Host OS Management — Locked

- **Look — Locked (2026-09-27):** a design system with built-in colour themes (docs/DESIGN.md). The default is Teal and violet; each user can choose another (Ocean and magenta, or themes from popular open-source palettes: Catppuccin, Gruvbox, Nord, Solarized, Everforest, Rosé Pine, Tokyo Night) and light, dark or follow the device. Every theme must pass WCAG contrast checks in both modes (a test enforces it).
- **Feedback on host operations — Locked (2026-09-27):** anything that acts on the host (OS updates, backups, applying service changes, setup hooks, restores) shows a one-line status that updates live ("OS updates: Installing updates…", then "✓ … finished: Installed 3 updates"), with the full step-by-step log and command output under a collapsed **Details** for technical users. Pages refresh their results when the operation finishes. Logs are kept in memory (latest run of each kind); results that matter later are stored separately.

- On fully supported distros, Roostack manages the host OS: security/package updates, reboot handling, and eventually major version upgrades.
- Users must be told clearly (docs and UI) that major manual changes to the host OS may limit Roostack's ability to manage it.
- **Rationale:** The primary user (§2) cannot maintain a Linux host. An unpatched host undermines everything else.

#### Time zone — Locked (2026-09-26)

- Roostack shows and schedules everything (nightly backups, maintenance windows) in the **host's** time zone.
- The setup wizard's first step confirms it, pre-filled from the browser's zone; the Owner can change it in Settings. `roostack-agent` applies it with `timedatectl set-timezone`, then Roostack restarts, because Go reads the local zone once at startup.
- If the browser's UTC offset differs from the host's, every page shows a prominent banner saying times are the server's, with a link for the Owner to change it.
- **Rationale:** cloud images and VMs default to UTC, which silently moved the "02:00" backup to midday for a user in Melbourne.

#### Update & reboot policy — Locked

| What | Policy |
|---|---|
| Security updates | Automatic, daily (`unattended-upgrades`, managed by `roostack-agent`) |
| Other Debian stable updates | Automatic (stable only receives bug and security fixes) |
| Reboots | "Reboot pending" shown in the UI; automatic reboot in a maintenance window (default 04:00). The Owner can change the window or choose notify-only |
| After reboot | Services return through systemd; Roostack health-checks them and flags failures |
| Major upgrades (e.g. 13 → 14) | Never automatic. Owner-initiated, guided, only once Roostack supports the new release, and only after a fresh successful backup |
| Roostack itself | Patch releases automatic; minor/major releases notify and need approval (same as services, §7) |
| Unknown host changes | Basic detection (extra apt sources, held packages, a changed firewall) triggers a warning; more thorough checks later. As built (2026-09-28): per server, on its Updates page and in "needs attention"; the Owner can mark the current changes as made on purpose, and anything new warns again |

- `unattended-upgrades`' own automatic-reboot option is **disabled**; only Roostack decides when to reboot.
- **How it runs (2026-09-27):** Roostack ships `/etc/apt/apt.conf.d/52roostack-unattended-upgrades` (adds stable-updates, no automatic reboot, removes old kernels). Every night after the 02:00 backup, the coordinator runs an "OS updates" job: `roostack-agent` runs `apt-get update && unattended-upgrade` in a transient unit (outside its own sandbox). The job is queued separately from the backup, so a failing backup doesn't hold back security fixes. Debian's own apt timers stay enabled as a fallback. `/run/reboot-required` (written by the kernel's postinst hook) is checked after each run and every 30 minutes; the coordinator then reboots in the window through the agent.
- The Owner sees the last run, any pending restart and why it waits, and can **check for updates now**, **restart now** (refused while a maintenance job runs), and choose automatic restarts at a chosen hour or notify-only (`lab.yaml`: `maintenance.restart`, `maintenance.restart_hour`).
- **Rationale for automatic reboots:** otherwise the primary user may run an unpatched kernel indefinitely. A brief overnight interruption is the better trade-off.

#### Maintenance coordinator — Locked

A single coordinator in `roostack` sequences every disruptive operation (backups, prunes, restores, service updates, package upgrades, reboots) so that conflicting operations never overlap.

- **Nightly order:** backup → automatic service updates → reboot if needed. This means there's always a fresh backup just before any automatic change.
- **Reboots wait** for running backups, restores and updates, then happen once they finish, within the window.
- **Never interrupted:** restores, major OS upgrades, and a backup's prepare step (services stopped or database dumps being written).
- **Resumable long jobs** (e.g. a multi-day first off-site upload) may be paused for a reboot that has been pending for longer than a deadline (default 24h, configurable), and they resume automatically afterwards. restic's repository stays consistent when interrupted.
- The UI explains why a reboot is waiting (e.g. "waiting for backup to finish, 62%").
- **Rationale:** fixed time windows can't handle jobs of unpredictable length. Delaying kernel security fixes for days is worse than pausing a resumable upload.

## 4. Roostack Cloud — Locked (scope), Open (design)

- A future paid hosting service for: resilience (critical services away from home internet), off-site backup targets, and users without their own hardware who still want to own their data.
- **Out of scope for v1**, but worker communication and backup formats must not preclude it.
- Implication: data portability is essential. A user must be able to move services and backups between home, VPS, and Cloud freely.

## 5. Users & Access — Locked

- **Roostack accounts are separate from service accounts in v1.** Roostack manages who can log in to Roostack; each service (Immich, Vaultwarden, …) manages its own users. Unifying them is part of the deferred SSO decision, and the design should allow Roostack to become the identity source later.
- **Fixed roles (not customisable in v1):**

| Role | Permissions |
|---|---|
| **Owner** | Everything: host settings, OS updates, backups and restore, recovery key, user management. At least one Owner must exist. |
| **Admin** | Install, configure, update and remove services; run backups; view logs. No host-level settings, no user management. |
| **Member** | View the dashboard (service links, status) only. |

  - Rationale: the person who sets up the lab (Owner) keeps control of risky host settings, while the day-to-day user (Admin) can still manage services freely.
- **Authentication:** local accounts, passwords hashed with argon2id. **TOTP 2FA** in v1, required for Owners and for any public admin UI. Passkeys later.
- **Password recovery:** host command line only (`sudo roostack user reset-password <name>`, `reset-2fa <name>` for a lost phone). No email reset (it would need mail setup and adds attack surface), and no recovery codes: the command line is simpler for a home lab.
- **People page (2026-09-27):** the Owner adds people (a temporary password is shown once and must be changed at first login), changes roles, gives someone a new temporary password, turns off someone's two-factor, and removes people. Not on their own account (use Account), and the last Owner can't be demoted or removed. An Owner who forgot their own password uses the command line.
- **Audit log:** records who did what and when, to support remote help and troubleshooting.

### 5.1 Roostack Data Storage — Locked

- **Desired state:** readable YAML files in `/var/lib/roostack/` (Principles 2 and 4).
- **Runtime data** (users, sessions, audit log, job history): a **SQLite** database in `/var/lib/roostack/`. A single file, no server, easy to back up. The backup prepare step must take a consistent copy (SQLite online backup / `VACUUM INTO`), not a raw copy of the file.

## 6. Container Runtime — Locked (principle)

- Podman, **rootless where possible**; rootful only where a service genuinely requires it (e.g. low ports, host networking, device access), and this is visible to the user.
- **Rationale:** security is a priority; rootless limits the blast radius of a compromised container.

### 6.1 Roostack Process Architecture — Locked

Roostack runs **directly on the host** (not in a container), as two processes shipped in one `.deb` package:

| Component | Runs as | Responsibilities |
|---|---|---|
| `roostack` | Unprivileged `roostack` user, strict systemd sandboxing | Web UI, API, desired state, rootless service management (Quadlets, status, logs) |
| `roostack-agent` | root | A small fixed set of privileged operations only: OS updates, reboots, rootful Quadlets, firewall. Also sets the host time zone. Listens on a local socket reachable only by `roostack`. |

- Nodes in a multi-server lab run the same package (§10).
- Roostack updates itself through apt, alongside host OS updates (§3.2).
- `roostack` is hardened with systemd sandboxing (`ProtectSystem`, `ProtectHome`, `NoNewPrivileges`, `PrivateTmp`, system call filtering, limited writable paths) to get most of a container's isolation.
- **`roostack` never runs `podman` directly — Locked (2026-09-26).** Rootless Podman needs the setuid `newuidmap`/`newgidmap` helpers, which `NoNewPrivileges` disables. So `roostack` only talks to the `roostack` user's systemd manager (`systemctl --user`, `systemd-run --user`), which runs Podman outside the sandbox. One-off commands (creating secrets, database dumps, `podman unshare` for backups) go through `systemd-run --user`.
- **Rationale:**
  - Keeps root code small and easy to review; privileged operations are explicit, not implied by broad socket access.
  - Podman/Quadlets rely on host systemd for lifecycle, so a containerised manager would need root-equivalent host access anyway.
  - Two components, a standard Debian install and update path, and no "container updating itself" problem.
- **Considered:**
  - *Container + many host mounts (Portainer/Komodo style):* those tools rely on the Docker daemon socket, which is root-equivalent. Under Podman they would also need the Quadlet directory, systemd D-Bus access, and a root agent regardless.
  - *Container web UI + host agent (A′):* a valid design that isolates the web-facing part. Rejected for now: it means either much more root code or three components, and the gain is limited because the agent API must allow arbitrary service deployment. The roostack↔agent API boundary leaves this option open later.
  - *Single root process:* rejected; the web UI would run as root.
- This deviates from PLAN.md's original "roostack orchestration container" wording. Services still run in containers; Roostack itself does not.

### 6.2 Language: Go — Locked

- Both `roostack` and `roostack-agent` are written in Go, in a single codebase.
- **Rationale:** Builds a single static binary, which makes `.deb` packaging trivial. Low memory use suits NUC-class hardware. Podman is written in Go and there are mature systemd/D-Bus libraries. The language is small, readable and widely known, which fits the simplicity principle. The maintainer wants to learn it.
- **Considered:** Rust (steeper learning curve, slower to build with); Python (easiest to write, but harder to package and ship as a host service).

### 6.3 Web UI: Go templates + htmx + Alpine.js — Locked

- Server-rendered HTML, embedded in the `roostack` binary. No JavaScript build step.
- Step-up approach for interactivity. Use the lowest level that works:
  1. **htmx** for server interactions (default)
  2. **Alpine.js** for small in-browser behaviour (toggles, show/hide, simple validation)
  3. **Standalone JS libraries** for heavy widgets (e.g. xterm.js for a terminal, a chart library)
  4. **A separately built component** only when a feature genuinely needs it, decided case by case
- **Architecture rule:** business logic lives in core Go packages. HTTP handlers are a thin layer on top. This keeps the UI replaceable and supports the master/worker API and a possible CLI.
- **Rationale:** Roostack's UI is mostly forms, lists, status and logs, with state held on the server, which is htmx's strength. Validation lives in one place (Go). A small toolchain suits the simplicity principle and a maintainer learning Go.
- **Rejected:** React islands. They bring back a Node build toolchain and a second UI paradigm for limited gain.
- **Known limits:** UIs with a lot of browser-side state (drag-and-drop dashboards, visual editors) would need step 4.
- **Live updates — as built (2026-09-29):** parts of pages that show changing state (the home page, Services, the Backups status card, the drive list) poll with htmx, and are kept cheap: they poll **only while the tab is visible** (and refresh once when it becomes visible again); each poll sends a fingerprint of what the part shows, and the server answers **204 No Content** when it's unchanged, so htmx leaves the page alone (nothing to download or redraw; open panels, selection and scroll stay put); the controller **shares its reads of other servers** for 10 seconds, so several open tabs cost one round of calls (any change forgets them); and responses are **gzip-compressed** (a page is about a third of its size). Activity logs poll every 2 seconds only while something runs. **Later:** push instead of poll, once Roostack moves to htmx 4, with its WebSocket support (`hx-ws`); see TASKS.
- **Sizes — Locked (2026-09-28):** shown in decimal units everywhere (1 GB = 1,000,000,000 bytes; kB, MB, GB, TB), as drive makers, macOS and most people count them, so an "8 GB" USB drive reads 8.0 GB. One formatter (`internal/units`) serves the UI, logs and the CLI, and sizes people type (e.g. a storage quota in GB) use the same units. Binary units (GiB) were considered: exact, but unfamiliar to most people, and a drive would read smaller than its label.

### 6.3.1 Help site — Locked (2026-09-29)

- **What:** a help site for the person running Roostack, in plain language, with a collapsed **For technical users** section on each page (exact paths, commands, logs), plus a technical reference (files, command line, ports, package format). Source in `help/` (Markdown), separate from `docs/`, which stays developer-facing.
- **Tool: [Zensical](https://zensical.org)** (from the Material for MkDocs team), pinned exactly in `help/pyproject.toml` and `uv.lock` (pre-1.0, so releases may break), run with `uv run zensical`. Chosen by the Owner of the project.
- **Where it's read:** both **inside Roostack** at `/help` (built at `make build` and embedded in the binary, so it works offline and matches the installed version; the UI's "Tell me more" links can point into it) and as a **public site** (address to be decided; `help.roostack.cloud` for now). Pages use relative links, so one build serves both.
- **Look:** Roostack's "Teal and violet" colours (`help/docs/stylesheets/roostack.css`, kept in step with `themes/teal.css`), the Roostack icon, light/dark following the device. No web fonts or anything loaded from other sites.
- **In Roostack — as built (2026-09-29):** `make build` runs `zensical build --strict` (a broken link fails it) and copies the site to `internal/web/helpsite/site`, which is embedded; only a README there is committed, so `go build` and the tests work without Python, and `/help/` then says the help isn't included. Without `uv`, `make build` warns and carries on; `scripts/build-deb.sh` refuses to package a build without the help. `/help/` is served to anyone, logged in or not (it's public anyway, and "Locked out" must be readable before logging in). The top bar has **Help**; the login page links to "Locked out". Considered: committing the built site (every help edit would change ~60 generated files).
- **Page help — as built (2026-09-29):** every page with a matching help page (or section) links to it, top right above the title, on the back link's row: a question-mark icon and the help page's title, opening in a new window like **Help** (the `help` component). A test checks each page and `#section` exists in `help/docs`. Considered: a "Tell me more" link in each page's text (the UI already uses "Tell me more" for details that open in place; mixing the two would surprise people), and a link at the end of each page (easy to miss).
- **Its Content-Security-Policy — Locked (2026-09-29):** `/help/` gets its own policy; the rest of the UI keeps its own. **Scripts:** only the pages' inline scripts, by SHA-256 hash, worked out from the embedded files when Roostack starts (so they always match the build). **Styles:** `'unsafe-inline'`, because Zensical's search box injects a stylesheet held in its minified script, and pages have style attributes; hashing that stylesheet would mean picking a string out of minified code, which would break silently with a new Zensical. Inline styles can't run code, and the help has nothing written by users to inject into. Browsers ignore `'unsafe-inline'` if a hash is also listed, so there are no style hashes. Rejected: `'unsafe-inline'` for scripts.
- **Open:** where the public site is hosted.
- **Considered:** plain MkDocs with Material (Zensical is its successor, by the same team, and reads the same Markdown); writing help pages as Go templates inside the UI (no search, and no public site without Roostack running).

### 6.3.2 Reaching the UI — Locked (2026-09-30)

- **The UI listens on `127.0.0.1:8480` only.** With a domain, it is served at `https://roostack.<domain>` through Caddy, to the home network only (private). Without one, it's reached over an SSH tunnel (`ssh -L 8480:127.0.0.1:8480 …`), which works the same on Windows 10/11 (PowerShell), macOS and Linux, but is a hard first step for people who don't use a terminal. The installer shows the command with the server's address filled in; on a server joining an existing lab, it points to `sudo roostack node join <code>` first, which needs no setup page.
- **Considered (2026-09-30), not built:** serving the setup page to the home network until setup is finished (plain HTTP, only on a server with no public address). A server with a public address would keep the tunnel either way: plain HTTP there crosses the internet. Rejected for now: an option to listen publicly.
- **Open, for later: HTTPS by IP address.** Let's Encrypt issues certificates for bare IP addresses (short-lived ones), which could give a cloud server's setup page `https://<its address>` with a real certificate and no domain. Check whether Caddy can get one and how it renews, once public access has settled.
- **Open: serving the UI through the domain as an explicit choice.** Today a domain automatically serves the UI at `roostack.<domain>` (home network only). Proposed by the project's Owner (2026-09-30): make that something the Owner turns on once a domain is set. Questions: what is the UI reached by if it's off (the tunnel only?); does the wizard offer it at the domain step; should the choice sit with public access (§6.11), which could later make the UI public with 2FA required.

### 6.3.3 Installing, step by step — Locked (2026-09-30)

- **Why:** a first install can take minutes (downloads, and on a brand-new server Caddy's image build), and the UI showed "stopped" with an **Open** link, or nothing at all, so people couldn't tell whether it had hung. Agreed with the project's Owner from a mock-up (2026-09-30).
- **As built (2026-09-30):** a new service (all its containers new) shows **installing** until its last step is done. Its page lists each step (done, in progress with "for …", or to do): *Downloading* and *Starting* each container ("the database (Postgres)", "the cache (Valkey)", or the service), *Making it reachable at its web address* (or *Waiting for Roostack's web proxy to finish building* while Caddy's image builds; left out while Caddy waits for the domain step), and *Finishing setup* if the package has install hooks. The log is under **Details** (the apply's, then the setup's). The Services list and Home show "Step 3 of 5: …" and no **Open** link; the address is shown greyed until it's running. Before its apply begins (e.g. waiting for another change), it shows one step, *Waiting for other changes to finish*. After 5 minutes on one step, the notice says it's taking longer than usual and when its log last changed. When it finishes, the page reloads to show it running.
- **How:** the node tracks installs in memory (`node.InstallProgress`, part of `ServiceDetail`, so other servers' installs show too), fed by the reconciler's progress events. Each new container's image is pulled first as its own step (`podman pull` through `systemd-run --user`, output in the apply log), so the download isn't hidden inside the unit's start (and its 5-minute pull timeout under systemd doesn't apply). New services start before a new Caddy, whose first start waits for its image build, so downloads run while it builds.
- **Not done:** download progress in MB (podman prints a line per layer, not progress, without a terminal); a **Try again** button for a failed download (the next apply retries); the step list after a restart mid-install (the containers' state is shown instead).

### 6.4 Service Execution: Podman Quadlets — Locked

- Roostack generates **Quadlet** files (`.container`, `.volume`, `.network`, etc.) and systemd runs them. Rootless units go in `~/.config/containers/systemd/`.
- Users never write Quadlets directly. They are generated from Roostack's own service definitions (see §8).
- **Rationale:**
  - Official Podman feature (4.4+), maintained by the Podman project, and an open, well-supported format.
  - systemd provides start-at-boot, restart-on-failure, dependency ordering, and logging (`journalctl`) with nothing extra to build.
  - Designed for rootless operation (§6).
  - The generated files are plain text a homelabber can inspect (Principle 4), and they are declarative (Principle 2).
- **Rejected:** podman-compose. It is community-maintained, supports only part of the Compose spec, and needs extra pieces for boot and restart handling. Also rejected: running docker-compose against the Podman API socket, which brings back a daemon-style model with the same lifecycle gaps.
- Rootless services need `loginctl enable-linger` for the service user so they start at boot without anyone logging in.

### 6.5 Reverse Proxy & TLS — Locked

- **Proxy: Caddy**, run as a rootless container from a Roostack-built image that includes the DNS provider plugins.
- **DNS providers — Locked (2026-09-26):** Cloudflare, Amazon Route 53, Porkbun, Gandi, GoDaddy, Namecheap and Spaceship, defined in one registry (`internal/dns`). Each provider's credentials are stored as Podman secrets and passed to Caddy as environment variables, so they never appear in the Caddyfile. Before saving, the wizard checks credentials by **creating and deleting a temporary TXT record** through the provider's public HTTP API (Namecheap: a read-only check, since its API can only rewrite whole zones). That tests what certificate issuance needs.
- **Dependencies for provider APIs:** prefer plain HTTP calls or small generic libraries; avoid large vendor SDKs (e.g. the AWS SDK). Roostack's checks use the standard library only (including AWS SigV4 signing for Route 53).
- Roostack generates a readable Caddyfile from desired state and reloads Caddy (same pattern as Quadlet generation).
- **Not user-selectable.** One proxy keeps testing and config generation manageable. Users can add custom Caddy snippets, globally and per service.
- **Rootless on 80/443:** `roostack-agent` lowers `net.ipv4.ip_unprivileged_port_start` so the proxy needs no root.
- **Proxy networking (Option C) — Locked (2026-09-26):**
  - Caddy runs in its **own pasta network namespace** (`Network=pasta:--map-host-loopback,169.254.1.3`), publishing 80/443. Pasta keeps each visitor's **real IP**, which the private-route check and CrowdSec rely on.
  - Each routed container stays on its service's private network and is **published on `127.0.0.1:<port>`** only. Caddy reaches it at `169.254.1.3:<port>`, which pasta maps to the host's loopback. The Roostack UI listens on `127.0.0.1:8480` and is reached the same way.
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

Roostack manages the DNS records its services need, through the same DNS provider credentials used for certificates.

- **Private services (default):** one **wildcard record** (`*.<domain>`) pointing at the lab's **LAN IP**, in public DNS. Devices at home reach services without local DNS. Anyone can look the record up, but only the home network can reach the address, and service names stay out of public DNS (like the wildcard certificate).
- **Router DNS rebinding protection:** some routers drop DNS answers containing private IPs. Roostack **detects** this by comparing the home network's resolver with a public resolver, and shows instructions (allow the domain in the router, or use local DNS).
- **Local DNS** (e.g. Pi-hole) is optional, for later. Getting every device to use it means changing the router's DHCP DNS setting (router-specific, sometimes impossible). Roostack running DHCP itself was **rejected**: if the lab dies, the home network stops working.
- **Public services (stage 2):** explicit records pointing at the **public IP**; explicit records override the wildcard. Devices at home reach them via the router's NAT loopback (Roostack can test this).
- **Dynamic public IPs (stage 2):** Roostack updates the records itself (§6.11, 2026-09-30; ddns-updater was the earlier plan).
- **Cloudflare proxy (stage 3):** offered per public service, **off by default**, with advantages and disadvantages listed in the UI:
  - For: hides the home IP; DDoS protection; works around some ISP port blocking.
  - Against: Cloudflare decrypts the traffic (at odds with "own your data"); free plan upload limit of 100 MB per request; Cloudflare's terms restrict large media streaming on the free plan.
  - When proxied, Caddy trusts `CF-Connecting-IP` **only from Cloudflare's published IP ranges**, so the private check and CrowdSec still see real visitors.
  - Packages declare proxy warnings, e.g. large uploads (Immich, until its chunked uploads land) or media streaming (Plex, Jellyfin, stricter). The format is to be designed.
- **Ownership:** Roostack only changes records it created (tracked in its state, plus a comment where the provider supports it). An existing record with the same name that Roostack didn't create is never overwritten automatically; the UI explains the conflict. A record that already has the right value is adopted.
- **Taking over a record:** the Owner can explicitly take over a conflicting record from the dashboard. A confirmation page shows the current and new values, warns that anything relying on the current value stops working, and needs an "I understand" tick. The takeover is recorded in the audit log.
- **Switching management on or off** is done from the dashboard's "Home network access" card (Owner only), with the Namecheap risk opt-in where relevant, and applies without a restart. Turning it off leaves records in place.
- **Namecheap:** its API can only replace a domain's whole record set, so record management there is **off unless the Owner ticks an opt-in** with a clear warning, and strongly recommends another provider (e.g. Cloudflare).
- **No usable public IP (CGNAT):** public exposure needs a relay. Future options: Cloudflare Tunnel, or a Roostack Cloud relay (§4).
- **Stages:** 1) wildcard to LAN IP + rebinding check; 2) public records + dynamic DNS (with public exposure, firewall, CrowdSec); 3) Cloudflare proxy.

### 6.6 Domains — Locked

- **v1 requires a user-owned domain** with a supported DNS provider (API token for DNS-01). This is set up once, at install time.
- **Future:** free Roostack-provided subdomains (e.g. `name.roost.ac`) with DNS managed for the user, which also links to Roostack Cloud (§4).
- **Rejected for v1:** a local CA (a root certificate on every device is bad UX); plain HTTP (browser warnings, and some clients such as Bitwarden require HTTPS).

### 6.7 Storage Layout — Locked (defaults may change before launch)

Default paths:

```
/var/lib/roostack/                 # Roostack's own state: desired state, secrets, users
/srv/roostack/services/<service>/  # per-service data (bind mounts)
    data/
    db/
/srv/roostack/media/               # large shared media; can be a separate disk
```

- **Paths are configurable**, and all defaults are defined in **one central place** in the code so they can be changed easily. No migration support is needed before launch; the defaults will be locked before release.
- **Bind mounts, not Podman named volumes.** Data sits in a visible, predictable location (Principle 4), and it's easy to back up and move (Principle 1).
- The media location can be configured separately, e.g. a small system SSD plus a large data drive.
- **Open / to prototype early:** rootless user ID mapping. Files owned by UIDs inside containers appear as mapped IDs (e.g. 100000+) on the host. We need to settle how this affects browsing, backups, and media shared between services (e.g. Jellyfin and Immich), and where Podman's `keep-id` option helps.
- **Disk space — as built (2026-09-28):** each server reports the disk holding its Roostack data (`statfs` of `/srv/roostack`: total, used, free to the roostack user, and what the filesystem keeps back), with what Roostack's parts take: its services' data, the backups it keeps for the lab's servers, and those it keeps for other labs. Free and used space come from `statfs` alone (instant); only the breakdown is counted, with `du -s -B1` (space on the disk, as `df` counts it) at the lowest CPU and disk priority (`nice -n 19 ionice -c3`). `du` reads every file's metadata: quick on an SSD (300,000 files: under a second), but a large photo library on a spinning disk can take a minute or more and fills the kernel's metadata cache. So sizes are counted **once a day, right after the nightly backup**, while restic's reads of the same metadata are still cached, and otherwise only when a size is missing or over a day old (no backups set up, a new service); pages show the last count and when it was made, and never wait for one. Settings → Servers shows it for each server, with a bar, and on the server storing other labs' backups, the space promised to them (their quotas) and how much of it they don't use yet. **Low space** is a warning below 5 GB or 10% free, and a problem below 2 GB or 5%, shown on the home page and on Servers; so is having promised other labs more space than is free. The backup plan shows the free space where each backup is (this server's drives, or another server's disk) and warns when it's nearly full. Only the one disk holding `/srv/roostack` is reported: a separate media disk isn't yet.

### 6.8 Databases — Locked

- **One database container per service** (e.g. Immich gets its own Postgres), not a shared instance.
- Roostack **standardises the pattern**: every package uses the same Roostack-managed Postgres/Redis setup, with the same data location (`<service>/db/`), backup method, health checks and upgrade process.
- **Rationale:**
  - Each app runs the exact database version and extensions it's tested with (e.g. Immich needs specific Postgres versions and a vector extension).
  - Upgrades and failures are isolated to one service.
  - Each service is self-contained, so moving it to another host or Roostack Cloud means moving one folder plus its definition (§4 portability).
  - The cost, roughly 30–50 MB RAM per idle instance, is acceptable on NUC-class hardware.
- **Rejected:** a single shared Postgres. It saves RAM, but it couples versions and upgrades, makes it a single point of failure, and makes backing up or moving one service harder.

### 6.9 Backups — Locked

**Tool: restic**, called as a CLI (`--json` output) behind a small internal interface (backup, restore, list snapshots, prune, check), so the tool can be swapped later.

- **Rationale:**
  - A Go single binary; encrypted and deduplicated, with many backends (local, SFTP, S3, B2, rclone, REST server).
  - Users can recover independently with the plain `restic` CLI and the recovery key, even if Roostack is broken. There's a large community and many docs (Principles 1 and 4).
  - Roostack owns scheduling and retention as part of desired state, so there's no competing policy engine.
  - The append-only `rest-server` gives simple ransomware protection and is a natural future Roostack Cloud target.
- **Considered: Kopia.** It's faster, uses less memory (≈4 MB average chunks vs ≈1 MB, plus a memory-mapped index) and has a richer server. It's a realistic fallback if restic's memory or performance becomes a problem with very large libraries; the interface keeps switching cheap.
- **Expected resources** (estimate, ~750 GB / 100k photos and videos): ~150–400 MB RAM for nightly backups, up to ~1 GB for prune/check. Mitigations: `GOGC=20`, infrequent pruning, a systemd memory limit on backup jobs, quiet-hours scheduling.

**Backup process:**
1. **Prepare:** per-package hooks create a consistent state (e.g. `pg_dump` to `<service>/db-dump/`, SQLite's backup command, or a brief stop if there's no safe live method).
2. **Snapshot:** `/var/lib/roostack` plus each service's folder.
3. **Verify:** periodic `restic check` and occasional test restores.
4. **Restore — Locked (2026-09-27):** per service, by the Owner, from the Backups page: choose one of the backups that contain the service (newest first), confirm, and it runs as an uninterruptible coordinator job with a live log. The service stops, its current data is moved aside (`<dir>.before-restore-<time>`), the backup's files and database dump come back, and it starts again. If getting the files back fails, the current data is put back (a partial restore is kept as `<dir>.failed-restore-<time>`) and the service starts again; if the data can't be put back, the service stays stopped and the log says where the data is, rather than claiming it's back (2026-09-29: a failed put-back used to go unnoticed and the service stayed stopped either way). Kept copies are listed on the restore page and deleted only when the Owner chooses. Restoring a whole lab onto a new server comes with the installer.

**Restore:** per service, or the full lab onto a new server (install Roostack → point at the backup → enter the recovery key).

**Full-lab restore — as built (2026-09-28):** on a fresh install, the first-run page offers "Restore a lab from its backups" next to creating the Owner (both need the setup code). The Owner says where the backups are (an S3-compatible bucket, a disk mounted on the server, or any restic address) and enters the recovery key; Roostack looks for every server's repository there (`roostack/<lab>/<server>`, a few levels deep for a disk), opens each with the key and shows the lab, server and latest backup of those it opens. Only the controller's (or a single server's) backup can be restored: its files go back where they were (Roostack's state, Podman secrets, every service's data, owners included), the recovery key becomes the backup key, and Roostack restarts. At that start the backed-up copy of Roostack's database becomes the live one (same accounts, settings and server identity); once the first apply has set the services up again, each service with a database is stopped, loaded from its dump and started, while "needs attention" says so. Backups carry the servers' WireGuard keys (`wireguard-keys.json`, kept by Roostack like the CA key), and the agent reuses a saved key for an interface that has none, so a restored controller keeps the keys its nodes and friends' labs know; they reconnect if they can reach it the way they reached the old one. Backups then continue at the lab's configured destination. Not yet: restoring from storage at a friend's lab (its link is in the lost state), and restoring a node on its own (restore the controller, then move the node's services).

**Defaults & policy:**
- Nightly; keep 7 daily, 4 weekly and 6 monthly.
- Packages classify data as **critical** (backed up by default: photos, passwords, repos) or **replaceable** (not backed up by default: media libraries, caches).
- **Destinations:** local disk, remote (S3/B2/SFTP), later Roostack Cloud. **Local-only is allowed, with a persistent warning** recommending off-site.
- **3-2-1 backups — Locked (requirement), Open (design), 2026-09-26:** Roostack should guide users towards 3-2-1 (3 copies of the data, on 2 different kinds of storage, 1 off-site), not just warn. That means:
  - **Suggestions in the UI** explaining what's missing and why, e.g. "Your backups are only on this server" → suggest a USB disk, then an off-site destination.
  - **Multiple backup destinations**, e.g. a local disk *and* an off-site repository. **As built (2026-09-28):** each server has a main destination (backup 1) and any number of **extra copies** elsewhere (backups 2, 3, …, in the plan below). The controller creates each copy's repository like the main one (recovery key plus a key of the server's own). Each copy is **its own backup** of the same files, made right after the main one (the database dumps are already fresh), rather than `restic copy`: restic reads both repositories' credentials from the same environment variables, so two S3 accounts or two backup stores couldn't be told apart, and separate backups keep each copy readable on its own. restic's file cache keeps the second run cheap. Copies still run when the main destination fails. Each copy's result is recorded and shown; a failed or stale copy needs attention. Copies are pruned and checked with the main repository. Stopping a copy leaves the copies made so far.
  - **Your backup plan — Locked (2026-09-28, numbering revised the same day):** the Backups page and the wizard's backup step show the same plan, numbered as the 3-2-1 rule counts copies. It opens with a one-line summary of the rule ("Keep 3 copies of your data, on 2 kinds of storage, with 1 away from home…") and a "Tell me more" that explains each number and why. **1 is the data on the server itself**, always there (not a backup, but the first of the three copies). **2 is the main backup** (restores read it first; best at home, where restoring is quick: the plan suggests "Make this the main backup" for an at-home backup when the main one isn't at home). **3 is a second backup.** Any more are **extra backups**, optional and not numbered (a stacked-copies icon instead). Empty places for 2 and 3 are dashed, with a suggestion that adapts to what's there (none yet: "a drive at home, like a USB drive or a NAS"; nothing away from home: "online storage, or space at a friend's lab"; only disks: "not a disk like the one your data is on"); an optional extra-backup place is always offered after them. Backups are added in order: only the next place has "Add". Each card shows its kind (USB drive, second disk, network share, this server's disk, folder, online storage, a friend's lab, another Roostack instance), at home or away, and its last result, and the **space** it takes where known: the size of its repository (restic `stats --mode raw-data`, as stored: deduplicated and compressed), recorded after each successful backup so pages never wait on it; the free space on the drive, for this server's drives; and for a friend's lab, the whole lab's use of its space there. Another server's free space and online storage's aren't shown. A backup on the server's own disk carries a warning: it helps with mistakes, but a failed disk or ransomware on the server loses the data and that backup together. Under "Manage": where it is (the repository), change where the main backup goes (a new backup starts there, the old stay), make another backup the main one (the two swap; both keep their backups and keys), or stop a backup. One **chooser** adds a backup anywhere: a drive (the drive picker below, or a typed folder for technical users), online storage (a saved account or a new one), a friend's lab (their storage, or a new invite), or another Roostack instance's disk (not in the wizard); for another server, "its own disk" (drives plugged into it are listed on its own page for now). Where a server's backups already go isn't offered again; this server's own disk counts once. A backup that fails to set up is forgotten again (the one before it is put back), and the failure stays shown on the plan for an hour. Under the plan, the **checks**: 3 copies (the data and at least two backups), 2 kinds of storage (the data is on a disk, so at least one backup that isn't a disk: disks count as one kind, whether this server's, plugged into it, a folder, or another server's), 1 away from home (online storage, a friend's lab, or another server that's somewhere else), and backed up lately. **Where each server is — Locked (2026-09-28):** Roostack can't tell whether a server is at home or somewhere else (a VPS, a relative's house), so the Owner says, once per server: asked on the plan's card when a backup goes to another server ("Where is vps? At home / Somewhere else"), and changeable on Settings → Servers. It's kept on the controller. A backup on another server is at home when both servers are at home, and away otherwise (different places, or both away from home); until both are known it counts as neither. Detecting it from the lab network was considered and not chosen: it can guess wrong (VPNs, a VPS reached over a home tunnel). The wizard's step needs the main backup to continue (or "Set up backups later"); the others are optional there and can be added later.
  - **Backup drives — Locked (2026-09-28):** nobody types a path. `roostack-agent` lists the server's drives (from `lsblk` and the mounted network shares) and the plan's chooser (wizard and Backups page) shows them as cards, best first: a **USB drive** or **second disk**, then a **network share**, then **this server's own disk** (marked: it protects against mistakes, not a failed disk). The list refreshes by itself every few seconds, so a drive plugged in appears. Choosing a drive mounts it for good by its filesystem's UUID in `/etc/fstab` (`nofail`, `x-systemd.automount`: a missing drive never holds up booting, and one unplugged and plugged back in is mounted again when the backup next uses it), under `/mnt/roostack-<name>`, with a `roostack` folder the roostack user owns (exFAT, FAT and NTFS drives are mounted so Roostack owns their files). A disk with nothing usable on it (new, or an unknown filesystem) can be **erased and prepared** (one GPT partition, ext4, labelled ROOSTACK), after a confirmation naming the disk, model and size; the system's disk, and LVM, RAID and encrypted disks, are never offered. The repository records the drive's UUID, and every backup first checks **that drive is connected and mounted** there, otherwise it fails with "the backup drive … isn't connected" instead of filling the server's own disk. Technical users can instead type a folder under `/mnt`, `/media` or `/srv` (the agent creates it and a `roostack` folder inside it). The roostack user can write in `/srv/roostack` and so could plant a symlink there, so the agent makes and hands over these folders only through an `os.Root` on the allowed place and with `lchown`: nothing is created or changes owner outside it, even if a symlink appears midway (2026-09-29: it used to check the path only after creating the folders, which could have given roostack a folder anywhere, such as a systemd drop-in folder for the agent). The full-lab restore page lists connected drives the same way.
  - **Phase 2 — adding NAS shares from the UI (agreed 2026-09-28, not built yet, see TASKS):** shares already mounted on the server are offered today. Next: add an SMB or NFS share from the wizard (server, share, user and password kept in a root-only credentials file, mounted by the agent like a drive, `nofail` and automounted), with a check that Roostack can write there.
  - **Later — rotating drives (agreed 2026-09-28, design leaves room):** two or more drives swapped in turn (one kept away from home). Each drive would be a destination of its own, recognised by its UUID, with "the other drive is at home, last backed up …" shown; not built yet.
  - The dashboard shows the status of each destination, so a quietly failing off-site copy is noticed.
- **Recovery key:** shown at setup; the user must save or print it, with a strong prompt for the person doing the setup to keep a copy. Optional key escrow via Roostack Cloud later.
  - **Shown when it's made — as built (2026-09-29):** the lab's first backup makes the key, and the Owner is shown it and must confirm saving it, whether the backup was added in the wizard or later on the Backups page (until then, choosing "Set up backups later" and adding one afterwards made a key nobody was shown). The setup page (`/setup/recovery-key`) works only until it's confirmed; confirming restarts Roostack only if a wizard step's settings are waiting for it.
  - **Showing it again — Locked (2026-09-29):** the Owner can see it again from the Backups page ("Show recovery key…"), after re-entering their password and an unused two-factor code, even though logged in: the key opens every backup and keeps working after a password change, so an open or stolen session shouldn't be enough. Each showing and each failed attempt is audited; five wrong attempts end the session (so a stolen session can't be used to guess the password); the page isn't cached. Considered: not showing it again at all (people lose the paper, and a key nobody has makes the backups useless); showing it with no re-check. **Changing it — Locked (2026-09-30), see "Changing the recovery key" below.**
  - **Changing the recovery key — Locked (2026-09-30, simplified the same day):** "Change recovery key…" on the Backups page, after the same password and code check as showing it: a new key is made and shown, the Owner confirms saving it, then Roostack adds it to every repository (main, extras, other servers' repositories where it is the second key, storage at a friend's lab through the controller's admin connection), checks it opens each, and only then removes the old key. Fast, keeps the history, and one key opens everything. A repository that can't be reached (a drive unplugged, a friend's lab offline) stays "waiting" with the old key still valid there, shown on the Backups page, and is finished when it's back. There is never a moment without a key the Owner has confirmed saving. Audited.
    - A restic key only wraps the repository's master key, so a new key doesn't protect copies someone already made of the backups; the page and help say so.
    - Considered and dropped (2026-09-30): asking "could someone also have your backups?" and then starting every backup afresh with a new master key. It made changing the key complicated, and costs a full re-upload and double the space for months, for a rare case. Also considered: new backups use the new key and old ones keep the old key (the Owner would need both keys until the old backups age out). **Later, if needed:** a separate "Start my backups afresh" (new repositories for new backups, with the new key also added to the old ones so one key still opens everything), not part of changing the key.
    - **As built (2026-09-30):** "Change recovery key…" on the Backups page; after the password and code, the new key is shown (and again only after another check) until the Owner ticks "I've saved it", or cancels. Then a maintenance job (one at a time with backups) adds it to every repository the controller reaches for pruning, plus its own main backup, checking the new key opens each; when all have it, the key files swap (the old one kept as `backup-key.old`), and the old key is removed from each (by the ID noted when adding). Unreachable repositories are retried hourly. Progress is in `backup-key.change.json`; all `backup-key.*` files are left out of the lab's backups. The page says plainly that this doesn't protect copies someone already has.
- **Large first uploads:** resumable, with progress and an estimated finish time shown in the UI, a configurable bandwidth limit, and possibly a "seed locally, then take off-site" option.

### 6.10 Host Security — Locked

Layered defaults:

1. **Firewall:** default-deny inbound. Allow 80/443 plus ports that specific services need (e.g. WireGuard, Pi-hole DNS on the LAN). `roostack-agent` generates a readable **nftables** ruleset file and reloads it (nftables is Debian 13's default, so no extra dependency).
   - **SSH access — Locked (2026-09-27):** the Owner chooses in the setup wizard (step 2) and in Settings: home network only (recommended, default), anywhere, or nobody. The page lists current SSH connections (read from `/proc/net/tcp`) and a choice that would cut one off must be confirmed. Stored in `lab.yaml` (`firewall.ssh`).
   - Roostack applies the firewall after every apply, so enabled package ports are opened (home network only unless public). Until the SSH question is answered, Roostack leaves the firewall alone.
2. **Admin UI is LAN/VPN-only by default.** Public access requires explicit opt-in **and** mandatory 2FA. Rationale: the UI controls the whole host and is the highest-value target.
3. **SSH hardening:** key-only authentication, no password login, no root login. Applied by the installer with a warning and confirmation to avoid lockout. The user can opt out, but **the UI shows a persistent warning** while hardening is disabled.
4. **Intrusion protection: CrowdSec**, enabled automatically once any service is public. It uses a Caddy module ("bouncer") to block at the proxy, and scenarios for Caddy and SSH logs. Not run on fully private labs (little benefit, saves resources).
   - **fail2ban is not run alongside it.** CrowdSec covers the same local log-parsing and banning, and running both would mean duplicate bans, two configs, and two tools changing the firewall.
   - **Signal sharing: on by default, opt-out.** Opting out is a **prominent choice when something first goes public** (changed 2026-09-30 from "during install", since CrowdSec only runs from then on; §6.11), and can be changed later in Settings. Only attack data is shared (attacker IPs, attack types, timestamps), nothing about the user's own users or data.
   - **If the user opts out:** local detection and banning keep working fully, like fail2ban but with better parsers and blocking at the proxy. They lose the community blocklist, i.e. pre-emptive blocking of IPs already seen attacking others. Rule and parser updates from the CrowdSec Hub are separate and still work.
5. **Baseline:** automatic security updates (§3.2), Caddy rate limiting on login pages, secrets in files readable only by Roostack (0600), and container images only from trusted registries, pinned to specific versions.
6. **Wrong two-factor codes — as built (2026-09-29):** limited per account, not per login, since someone with the password could otherwise log in again for more tries. Every fifth wrong code in a row locks the account's codes (even the right one) for 15 minutes, doubling each time up to a day; the right code resets the count. That leaves a guesser about five tries a day, while someone mistyping waits 15 minutes. The count is in the database (a restart doesn't clear it), covers the login and the recovery key page (§6.9), and each lock is audited; the password step says so when locked, telling people that wrong codes they didn't enter mean their password is known. Checks run one at a time, so parallel guesses can't slip past. `roostack user reset-2fa` ends a lock. Considered: a limit per login (what we had; it limited nothing), locking the whole account (the lock is only on codes, so it can't be used against an account whose password the attacker doesn't have), and relying on Caddy's rate limiting alone (per address, so it doesn't stop guesses from many addresses).

### 6.11 Public access — Locked (2026-09-30), details Open

Nothing is public unless the Owner makes it so (§1 principle 6). The Owner makes one of a service's addresses public from the service's page; the Roostack UI itself stays private (§6.10 item 2).

- **Ways in ("entries"), all to be supported:**
  1. **Port forwarding at home:** the router forwards TCP 80 and 443 (and UDP 443) to the server; public records point at the home's public address. Built first: it needs nothing beyond the home router.
  2. **Through a cloud server in the lab** (an "edge", §10.5): no router change, and the home's address stays hidden.
  3. **Through Roostack Cloud** (later, §4): the same edge mechanism, run by Roostack Cloud, for people without a cloud server of their own or behind CGNAT.

  A service that runs on a cloud server is reached at that server directly. The entry is a seam in the code from the start (where public records point, and how traffic reaches the service's server), so 2 and 3 slot in without reworking 1.
- **What can be public:** packages mark addresses that aren't safe to make public (admin pages, APIs meant for home), with the reason. The Owner can still make one public, after a warning that gives the package's reason and a tick to confirm; the override is audited. Considered: never allowing it (too rigid for technical Owners who know what they're doing); allowing anything with no mark (non-technical Owners can't judge an admin page's risk).
- **Protection first:** CrowdSec and Caddy's rate limiting (§6.10) run before anything goes public, and the switch waits for them. Dependencies agreed (2026-09-30): the CrowdSec container, and two Caddy plugins, `caddy-crowdsec-bouncer` and `caddy-ratelimit`.
- **Checking it works from outside:** for now, the Owner checks from a phone on mobile data. Roostack Cloud will check automatically later. Considered: port-checking websites (they learn the home's address and domain); checks from the lab's own cloud servers (maybe later).
- **DNS:** each public address gets its own record pointing at the entry's public address, overriding the private wildcard (§6.5.1 stage 2).
- **The home's public address — Locked (2026-09-30):** found with a DNS query (OpenDNS's `myip.opendns.com`, Cloudflare's `whoami.cloudflare` as a fallback), checked every few minutes, and public records updated by Roostack's own record code (§6.5.1), with the same ownership tracking. This replaces ddns-updater (§6.5.1 stage 2): one less container, and one owner for every record. Considered: ddns-updater (more providers, but a second writer to the same zone); HTTP "what's my IP" services (they see every check).
- **CrowdSec signal sharing — Locked (2026-09-30):** asked in the "Protection" step the first time something goes public (CrowdSec only runs from then on), not during install (§6.10 item 4 updated), and changeable later in Settings.
- **CrowdSec and SSH — Deferred (2026-09-30):** CrowdSec reads Caddy's logs only for now. The rootless container can't read the host's journal, and SSH is home-network-only by default. To revisit: the agent passing SSH log lines on, or blocking with nftables (TASKS.md 1.18).
- **Rate limits — Locked (2026-09-30):** every public address gets a generous limit per visitor address, and the login pages a package names get a strict one.
- **As built (2026-09-30):**
  - Packages: `routes.<r>.public` with `unsafe` (a reason), `private_paths` (each with a reason) and `login_paths`; the Owner's opened paths are the instance's `public_paths`.
  - Caddy, for a public address, in one `route` block: private paths refused to outside visitors, CrowdSec's bouncer, 1000 requests per 10 s per visitor and 10 a minute on login paths, then the service. The only path that reaches Roostack is the check link (a 32-digit token).
  - While anything is public, the lab runs CrowdSec (pinned image) reading Caddy's JSON access log; the bouncer's key is a generated Podman secret. The sharing opt-out travels in the assignment.
  - The entry is `internal/public.Entry`; port forwarding finds the home's address over DNS. `dnsrecords` sets each public address's record once CrowdSec's health check answers, re-syncing every 5 minutes (30 s while waiting), and points it home again when the address goes private: the providers' record APIs can't delete yet.
  - UI: "Reachable from" on the service page; a page per address (warning, reasons, private paths, the sharing question, then the checklist: protection, DNS record, the router, the phone check); Settings → Public access. Owner only, audited.

## 7. Updates — Locked

- **Default:** notify when updates are available; user applies them manually.
- **Per-service opt-in:** automatic updates for all versions, or only minor/patch versions.
- Roostack self-update: patch releases automatic; minor/major notify (see §3.2).

## 8. Service Definitions & Catalog — Locked (principle)

- The service definition format is user-facing from early on (custom services is a nice-to-have).
- Breaking changes to the format are acceptable before 1.0.
- **Official packages are curated and opinionated.** Upstream `docker-compose.yml` files are a starting reference, not something we import directly. Each official package includes Roostack-specific configuration so it runs production-ready in the Roostack ecosystem:
  - automatic TLS / reverse proxy wiring
  - backup definitions (what data to back up, how to quiesce it)
  - use of Roostack-managed shared resources (e.g. common databases)
  - standard storage locations
- **Rationale:** Upstream compose files are rarely production-ready as a one-click install. Being opinionated is what makes one-click install and management reliable for less technical users.

### 8.1 Format — Locked (initial; may be revisited)

- **Two file kinds:**
  - **Package** (`catalog/<name>/package.yaml`): authored by Roostack or the community. Describes how to run the app well: containers, settings, storage, databases, routes, health checks.
  - **Instance** (`/var/lib/roostack/services/<name>.yaml`): the user's choices (package version, subdomain, exposure, setting values, update policy). This is part of desired state.
- Roostack combines package + instance and **generates** Quadlets, Caddy site snippets, and backup jobs.
- **Our own YAML schema** (`schema: roostack/v1alpha1`) models what Roostack needs to understand, with a **raw Quadlet passthrough** for unusual needs.
  - Rejected: packages shipping Quadlet templates plus a manifest. Every package would do things differently, and Roostack couldn't reason about them as well.
- **Backups are declared, not scripted:** `databases` entries (`sqlite`, `postgres`, …) and `storage` classes (`critical` / `replaceable`) drive Roostack's standard backup methods.
- **Secrets** are declared (optionally `generate: true`) and delivered as Podman secrets, so they never appear in generated unit files.
- **Images are pinned** by tag + digest. Roostack's own images too (2026-09-29): rest-server by digest, and the Caddy image, built on each server, from base images pinned by digest and DNS plugins pinned by version (`build/caddy/Containerfile`). Digests are the multi-arch index, so the same pin works on ARM.
- **Templating:** Go templates (e.g. `{{ .Routes.web.Host }}`, `{{ .Settings.x }}`).
- **Versioning:** `<upstream version>-roostack.<package revision>`.
- **Additional schema features** (from the Immich stress test):
  - **`when:` conditions** on containers and devices, for optional components (e.g. Immich machine learning), so disabled features don't use RAM.
  - **`devices:`** as abstract requests (e.g. `gpu: intel`) that Roostack maps to device paths and groups (rootless `/dev/dri` needs `keep-groups`).
  - **`depends_on:`**, which becomes systemd ordering (`After=`/`Requires=`).
  - **Route options** at the Caddy level (e.g. `max_body_size`; more expected: timeouts, websockets).
  - **Storage `location:`** (e.g. `media` → the large data disk) and **per-directory storage classes**, so regenerable data (thumbnails, transcodes, caches) is excluded from backups.
  - **Database `image` override** for custom builds (e.g. Immich's Postgres), still following the standard database pattern; **`backup: false`** for transient stores (e.g. Redis job queues).
- **`ports:` — Locked (2026-09-26):** for protocols Caddy can't proxy (SSH, DNS, WireGuard), a package declares a container port with a title, a suggested host port (≥1024) and a protocol. Ports are **off unless the package enables them**; an Admin turns them on per service and can move them. Enabled ports are published on every host address (`PublishPort=<host>:<container>/<proto>`) and are **private** (home network only) via the firewall. Public ports come with the firewall and CrowdSec, like public routes. Roostack rejects clashes with other ports, the UI port and the web-service range (20000–29999). Templates see `{{ .Ports.<name>.Enabled }}` and `.HostPort` (e.g. for clone URLs).
- **`hooks:` — Locked (2026-09-27):** commands run inside a service's container.
  - `on: install` runs once after the first successful start (e.g. creating Forgejo's admin). It is retried while the app starts, so it must be safe to re-run; a failure shows under "Needs attention" with **Try again**.
  - `on: action` is a button on the service page for Admins (e.g. "Reset a password"), with optional `inputs` (string or secret).
  - User-provided values reach hooks **only as environment variables** (`env:` templates, `secrets:`), never templated into the command, so they can't change what a shell script does.
  - Settings can be `required`, have a `min_length`, and be `install_only`: asked at install, used only by install hooks. An `install_only` secret is **deleted once the install hooks succeed**, so Roostack doesn't keep a copy of e.g. the admin password.
  - Only `install` and `action` for now; `upgrade` hooks when a package needs them.
- **Service settings — Locked (2026-09-27):** most of an app's settings stay in the app. Packages list `links:` into its own admin pages (Forgejo's site administration, Vaultwarden's admin panel), shown first under Settings on the service page. Roostack's own settings (not secrets, not `install_only`) and the web address are editable there; saving regenerates and restarts the service. A secret marked `reveal: true` (e.g. Vaultwarden's admin token) can be shown to Admins on request, and each view is audited.
- **Future:** storage shared between services (e.g. Jellyfin reading Immich's library, read-only); package upgrade notes and migration steps for upstream breaking changes.
- **Reference examples:** `docs/examples/vaultwarden.yaml` (simple), `docs/examples/forgejo.yaml` (Postgres, an optional SSH port), `docs/examples/immich.yaml` (complex).

## 9. MVP Scope — Locked

Single node: web UI, reverse proxy with automatic TLS, three catalog services (e.g. Vaultwarden, Forgejo, Immich — increasing complexity), backup and restore. Multiple servers (§10), SSO, DNS, and monitoring follow after the core is solid, but the design should anticipate multiple servers.

---

## 10. Multiple Servers — Locked (2026-09-27)

One lab can span several servers: extra boxes at home, VPSes in the cloud, and later Roostack Cloud. This section is the design to build towards; nothing here is built yet except where marked. It replaces "master/worker" elsewhere in this document.

### 10.1 Roles

**Naming (2026-09-28):** the computers in a lab are **servers** everywhere: the UI ("My Servers", Settings → Servers, "Move to another server"), this document and the code (earlier drafts said "machine"; the database was migrated). A **node** is a server's role when it isn't the controller, and `node.Node` is the code's handle on any server.

**Servers in the UI (2026-09-28):** Home shows **My Servers** beside My Services (status, backups, updates, services, a link to each server's settings). Settings that differ per server (updates and restarts, time zone, remote access, backups) are grouped under "Each server": the overview summarises every server, and each area's page opens on a summary row per server that also switches between them. Each server has its own page (`/settings/servers/<id>`) with its card and its settings. Service cards say which server a service runs on and when it was last backed up, with details (containers and images, web routes, ports, data folders and size); Roostack's own services (Roostack and its agent, Caddy, backup stores, network links) are shown per server behind a toggle, so nothing is hidden from a technical user.

- **Node:** any server running Roostack. Every node runs its own services (Quadlets, reconciler), its own Caddy and certificates, its own backups, OS updates, firewall and agent, exactly as a single-server lab does today.
- **Controller:** one node also holds the lab's desired state (§1.2: services, placement, settings), users and sessions, and serves the UI. It decides placement; nodes do the work.
- **The controller can move to any node** (chosen, not automatic), e.g. from a home NUC to a VPS:
  - Every node keeps a **replica of the lab's state** (desired state and the Roostack database), pulled with its assignments. The controller encrypts it with the **recovery key** (§6.9) before sending it; nodes store it but can't read it, because the recovery key is never stored on a node other than the controller.
  - **Planned move:** the controller hands over (freezes changes, sends the latest state, the new controller takes the address and certificate authority) and becomes a plain node.
  - **Disaster:** the Owner promotes a node from its replica by entering the recovery key (they have it written down for exactly this). At worst it misses changes since its last pull. Two controllers must never both be active: promotion revokes the old one's authority (a new epoch number that nodes follow).
- The first server installed is the controller; a lab with one server is a lab with one node that is also the controller. Single-server labs stay exactly as simple as today.

### 10.2 Private network: Roostack-managed WireGuard

- Roostack runs a **WireGuard** network between its nodes (WireGuard is in Debian's kernel; `roostack-agent` manages the interface and keys). Every node gets an overlay address (e.g. `100.96.0.x`).
- **No inbound ports at home:** home nodes dial out to reachable nodes (a VPS with a public IP), so a home controller needs no port forwarding. Nodes that can reach each other directly do; others go via a reachable node (hub and spoke first, a mesh later).
- All controller ↔ node traffic runs inside WireGuard **and** uses mutual TLS, so a leaked WireGuard key alone isn't enough.
- **Joining:** the Owner creates a one-time join code in the UI (short-lived, single use) and runs `roostack node join <code>` on the new server (or picks "Join an existing lab" in its setup wizard). The code carries the controller's address and a CA fingerprint; the node gets its WireGuard peer config and a client certificate from the controller's CA.
- "Private" (home network only, §6.10) widens to **home network or Roostack's WireGuard network**. The same network is the base for remote access for people later (a phone joining over WireGuard).

### 10.3 Controller ↔ node protocol

- **Nodes pull.** Every node periodically (and when nudged) fetches **its assignment**: the instances placed on it (package, version, settings, secrets it needs) plus lab settings it needs (domain, DNS provider, backup destinations). It generates and reconciles locally with the same code as today, then reports back **status**: service health, apply results, activity logs, backups, updates, restart needs.
- **Offline-tolerant:** if the controller is unreachable, a node keeps running its last assignment and queues its reports (§1.3 local-first). Assignments carry a version; nodes apply only newer ones.
- **Actions** (restart a service, run an action hook, restore, update now) are requests the node picks up on its next pull (or immediately when nudged), with results in the report. The same request types the in-process code uses today (e.g. the agent's JSON API) cross the network unchanged.
- **Secrets:** user-provided secrets travel only to the nodes that need them, over mTLS inside WireGuard; generated secrets are made on the node that uses them and never leave it (backups hold them, encrypted).
- **Backups per node:** each node backs up its own services to **its own repository** (or its own folder at the same destination) with **its own key**. The recovery key is added to every repository as a second key (restic supports several), so the Owner can restore anything with it, while a compromised node exposes only its own backups and its key can be revoked.
- **Packages:** the controller's catalog is the source of truth; nodes fetch package files they don't have (same version pinning).
- **Certificates (2026-09-28):** each server's certificate (from the controller's CA) lasts a year and renews automatically when 120 days are left: nodes ask the controller over the lab API with their current certificate; the controller signs its own. TLS picks up a renewed certificate without a restart. The controller records the certificate each server may use (its serial) and its lab API accepts only that one, plus the one it replaced until the new one is first used (in case the reply was lost). Removing a server therefore revokes its certificate as well as its WireGuard peer; a server rejoining gets a new one. Nodes accept requests only from the controller. Settings → Servers shows each certificate's expiry; one within 30 days of expiring (renewal failing) needs attention. The CA itself (20 years) isn't renewed yet.
- **Before the domain is set up**, Caddy waits for its DNS credentials and the rest of the lab (services, backup stores, storage for others) is applied without it.

### 10.4 Placement and moving services

- Every instance has a **`host`** (a node ID); default: the controller. The install form asks where to run it when the lab has more than one node, with hints (RAM, disk, GPU, home or cloud).
- **Moving** a service = back up on the old node, restore on the new one, switch its route, remove the old copy (existing per-service backup and restore, §6.9). Portability is the requirement from §4.

### 10.5 Public access through a cloud node ("edge")

For services hosted at home but public (e.g. Immich shared with family), without opening ports 80/443 at home:

- A cloud node can act as an **edge**. It forwards with Caddy's layer-4 plugin (`caddy-l4`, added to Roostack's Caddy image like the DNS plugins; ASSUMPTIONS to verify SNI routing and PROXY protocol). Public DNS records point to the edge's public IP; the edge forwards each connection **over WireGuard** to the home node that runs the service.
- **TLS passthrough by default:** the edge routes on the TLS SNI name and never decrypts; certificates and keys stay on the home node, so the cloud provider can't read the traffic (in line with "own your data", §4; unlike the Cloudflare proxy, §6.5.1). Visitors' IP addresses reach the home node via the PROXY protocol, so private checks, rate limiting and CrowdSec (§6.10) still see real visitors.
- Terminating TLS at the edge (caching, fewer hops) is an option per service later, clearly marked as letting the edge see the traffic.
- The home firewall stays closed to the internet; only WireGuard (outbound) is needed. The same mechanism is how a **Roostack Cloud relay** would work for people behind CGNAT (§6.5.1).
- Home devices keep reaching home services directly (local DNS records, §6.5.1), not via the edge.

### 10.6 Failover (later)

Failover is service-specific, so packages declare what they support:

- **none** (default): if the node is down, the service is down; the UI says so.
- **cold standby:** a second node keeps recent backups of the service; if the primary is unreachable for a set time, the Owner (or, when enabled, Roostack) restores the latest backup there and switches the route. Data since the last backup is lost; suits most home services.
- **replicated:** the service keeps a live copy on another node with its own replication (e.g. Postgres streaming, Vaultwarden via its database), declared per package. Much later.
- Automatic failover needs a way to tell "node down" from "network split" (at least a third node or the edge as a witness), so it comes after manual failover.

### 10.7 What to build now (no networking yet): seams

So single-server code doesn't paint us into a corner:

1. **`host` on every instance**, defaulting to the controller; loading services filters by host.
2. **Controller/node split in code:** the UI-facing manager (desired state, users, placement) talks to a **`Node` interface** for everything that touches a server (apply, status, hooks, actions, restores, activity, updates, restarts, firewall). Today: one in-process implementation. Later: a remote implementation speaking the same plain-data requests.
3. **Status, activity and restart state are per node**, and the UI's "This server" areas are per node (e.g. `/settings/servers/<id>/updates`) once there's more than one.
4. **Node identity:** every install gets a stable node ID and name at setup.

Then a **two-VM prototype**: a second dev VM joins the first over WireGuard and runs one service placed on it.

### 10.8 Decisions (2026-09-27)

- **Network layout:** hub and spoke (servers talk through a reachable node, usually a VPS). Direct links between two networks behind NAT (hole-punching, full mesh) only when a real case needs them.
- **Edge forwarding:** Caddy's `caddy-l4` plugin, not a forwarder of our own.
- **Keys:** the recovery key protects the state replicas and is added to every node's backup repository, but is stored only on the controller; each node has its own backup key (§10.3).
- **Roostack Cloud:** nodes run by Roostack Cloud are trusted like any hosting provider (a server running your services can see them; there's no real alternative). Roles that don't run services stay **zero-knowledge**: a relay or edge uses TLS passthrough, and a backup target only ever receives restic-encrypted data. Billing is out of scope until Roostack Cloud exists.

### 10.9 Prototype as built (2026-09-27)

What the two-VM prototype does, where it refines the design above:

- **Hub:** the controller is the WireGuard hub (`100.96.0.1`, UDP 51820; network `100.96.0.0/24`). A reachable hub other than the controller comes later.
- **Joining:** the Owner makes a code in Settings → Servers and runs `sudo roostack node join <code>` on the new server. The code carries a **one-time WireGuard key**, so only the hub's UDP port needs to be reachable (no public TLS endpoint). Over WireGuard, the server checks the controller's CA against the code's fingerprint, sends its own WireGuard key and a CSR, and gets its certificate; the hub then replaces the one-time key with the server's own. Codes work once and expire after an hour.
- **Protocol:** every server serves a lab API on its overlay address (port 8471), mutual TLS with the lab's CA. The controller **sends** assignments and makes requests directly (status, service states, activity, hooks, actions, secrets, backups, restores, updates, restarts, settings); a node also **pulls** its assignment when it starts and every 15 minutes, and keeps its last one (without secrets) to run while the controller is away. Assignments carry a version; older ones are ignored. The controller works out every server's assignment on every change, but **sends only what changed**: a server that already has the same assignment, and applied it without error, isn't sent it again (it would only look busy, e.g. "Applying changes on vps" while a service installs elsewhere); one whose last apply failed is sent it again, as its retry. Typed-in secrets travel once and are never stored on disk.
- **Web routes of a service on another server:** the server publishes them on its overlay address (firewall: WireGuard interface only) and the controller's Caddy forwards to it (TLS ends on the controller for now; passthrough is §10.5's edge).
- **Nodes** run no UI, Caddy or DNS. Per-server pages live under `/settings/servers/<id>/…`.

### 10.10 Backups across servers, and moving services — Locked (2026-09-27)

Builds on §6.9 (restic, recovery key, 3-2-1) and §10.3 (each server backs up its own services to its own repository with its own key).

**Destinations are chosen per server, from providers the lab knows.**
- A **provider** is a place backups can go: a cloud account (B2, S3, …), an SFTP server, **a server's own disk**, or **another server offering storage** (below). A provider is set up once (its credentials) and any server can use it; the Backups page of each server picks one (and later extra copies, for 3-2-1).
- Every server's repository goes in **its own folder**, always, even if only one server uses the provider: `<provider path>/roostack/<lab id>/<server id>/`. Moving a lab or adding servers never reshuffles folders. On a rest-server store the server's own area comes first instead (`/<server id>/<lab id>`), because private repositories are named after the user; at another lab's store it is `/<guest user>/<server id>` (the user is the lab's own, and rest-server allows at most two levels).
- **The controller creates each repository** (decided 2026-09-27): it runs `restic init` at the destination with the recovery key, adds a new key for the server, and gives the server only that key. The recovery key never leaves the controller. So the controller must reach every destination, which the next point guarantees.

**Disks are offered over Roostack's network (restic's rest-server).**
- A server's own disk is used through a small **rest-server** on that server, listening only on Roostack's network. The controller creates repositories through it like any other destination, and the server backs up to itself through it too. One mechanism covers "local disk", "the NAS at home" and "a friend's server".
- rest-server runs **append-only** for backup clients, so a compromised server can add backups but not delete old ones (ransomware protection, §6.9). Pruning is done by a key that can delete, held by the controller (see open question 2).

**A server can be a backup target for other labs (friends and family).**
- The Owner of the storage server creates a **storage invite** (like a join code) with a **quota**; the other lab's Owner adds it as a provider. Each invited lab gets its **own rest-server repository area with its own size limit**, reached over a WireGuard link that allows **only** that port (it isn't a server in the host's lab and can reach nothing else).
- The host only ever sees encrypted data and sizes (zero-knowledge, like §10.8's backup targets).
- **Storage management:** the host sets and can change each guest's quota; Roostack keeps free-space headroom on the disk and refuses quotas that don't fit; both sides see usage (the guest: "8.2 of 50 GB used at Mum's"); warnings at 80% and when a backup fails for lack of space; the host can remove a guest (their data is deleted after a grace period, with notice).

**Moving a service between servers: two ways, the UI suggests one.**
- **Copy directly** (quick, for smaller data or servers on the same network): copy the data to the new server over Roostack's network *while the service keeps running*, then stop it, copy what changed, start it on the new server and switch its web address. Downtime is only the final pass. The old copy is kept until the Owner confirms.
- **Through a backup** (large data, slow or unreliable links, or servers far apart): back up on the old server, then restore on the new one from the old server's repository. The controller adds a temporary key to that repository for the new server and removes it afterwards. Resumable, and reuses restore (§6.9).
- The move page shows the data size and suggests a way (through a backup above 10 GB, when the old server's backups are set up through Roostack).
- As built (2026-09-28): data travels **through the controller** in both ways (it is the hub every server already connects through); in the backup way the controller reads the old server's repository itself with the recovery key (`restic dump`), so no repository keys or store passwords are shared between servers. The service's Podman secrets are copied from the old server. Data already on the new server is kept aside (`<dir>.before-move-<time>`), and the old server's copy is renamed (`<dir>.moved-to-<server>-<time>`), both kept until the Owner deletes them.

**Decisions (2026-09-27)**
1. ~~Provider credentials~~ **Decided (2026-09-27): scoped where cheap and stable, shared elsewhere.** rest-server gets a user per server (`--private-repos`); B2 gets an application key per server limited to its folder (plain HTTP API, deleted when the server is removed). Other providers use the shared credentials, and the server's Backups page says so ("these credentials can reach the whole bucket"). More providers get scoped keys one at a time when there's a need.
2. ~~Pruning~~ **Decided (2026-09-27):** the controller runs `restic forget/prune` weekly with the recovery key, over a separate non-append-only credential. For friends' storage, the guest lab's controller does it.
3. ~~rest-server as a dependency~~ **Decided (2026-09-27):** run it as a pinned container (`docker.io/restic/rest-server`), like Caddy: containers where possible make other distributions easier to support later.
4. ~~Guest link~~ **Decided (2026-09-27): a separate WireGuard interface for guests** (`roostack-guests`, its own address range, e.g. `100.97.0.0/24`, and its own UDP port, default 51821). The firewall drops everything arriving on it except the rest-server port; the lab API and web routes are never reachable from it. Turning "storage for others" off takes the interface down.
   - **UDP and port forwarding:** a guest must be able to reach the host's port from the internet. Roostack opens the UDP port in its own firewall, then **explains port forwarding** on the storage page: which port (UDP, not TCP), to which server (its home-network address), a short how-to with common router names, and a **reachability check** (the guest's first connection, or a check from Roostack Cloud later) so the Owner knows it worked. The same applies to the servers port (51820) when a server outside the home joins; the join page gets the same explanation. A Roostack Cloud relay can later remove the need for port forwarding (§10.5).


## Deferred Decisions (post-MVP)

- SSO platform, and whether it is mandatory (linked to unifying Roostack and service accounts, §5)
- Default DNS manager (e.g. Pi-hole) and split-horizon DNS
- Built-in uptime monitoring
- Remote access / remote support for the person who set up the lab
- Roostack Cloud design (§4)
- Shared storage between services (§8.1)

- **Storage for others on any server (2026-09-28):** the Owner chooses which server offers it (e.g. one with a big disk; the controller by default), only while no other lab stores anything. That server runs the guests' WireGuard link (`roostack-guests`, UDP 51821), their stores, and holds their data; the controller keeps the records and answers guests' requests (joining, usage), which that server forwards to it over Roostack's network (only from the server offering storage). Usage, free space and deleting a guest's data are asked of that server. A removed guest lab keeps its link for 30 days, hears about the removal (its Backups page and "needs attention" show when its backups there are deleted), and its data is then deleted (hourly check), or sooner if the host chooses.
