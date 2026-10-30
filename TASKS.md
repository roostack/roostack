# Roostack Tasks

What's still to do, by phase. Finished items are in [TASKS-DONE.md](TASKS-DONE.md) under the same headings, moved there on 2026-10-22; move them across as you finish them. A finished item stays here while something under it is still open, so the open work keeps its context.

## Phase 0: Requirements & Decisions ✅

- Deferred (post-MVP): see SPEC.md "Deferred Decisions"

## Phase 1: MVP (single node)

Order changed on 2026-09-25: coding started before the Debian 13 prototype, based on the assumptions in `docs/ASSUMPTIONS.md`. Everything marked 🟡 is written and unit-tested, but **not yet run on Debian 13**.

- [ ] **1.2 Verify assumptions on Debian 13** (see `docs/ASSUMPTIONS.md`; ⚠️ items first)
  - [ ] A7: Intel GPU (`/dev/dri`) passthrough with `keep-groups`
- [ ] 🟡 **1.6 Reconciler:** plan/apply, `roostack apply [--dry-run]` (`internal/reconcile`, `internal/state`)
- [ ] 🟡 **1.7 Proxy:** Caddyfile generation ✅ (checked with real Caddy); Caddy image Containerfile written, not built
- [ ] 🟡 **1.8 roostack-agent:** socket API with peer-credential checks; port sysctl; nftables firewall (ruleset checked with `nft -c`)
- [ ] 🟡 **1.9 Web UI skeleton**
  - [ ] Mark untested catalog packages (Immich) before release (not needed while in development). 2026-10-18: Forgejo and Immich work end to end on arm64 (ASSUMPTIONS R7), so neither needs the mark there; Immich hasn't run on amd64 yet
  - [ ] Public exposure option (after firewall + CrowdSec)
  - [ ] Wizard: SFTP destination; changing settings later from a Settings page
  - [ ] Test each DNS provider with a real account (Cloudflare done; the other six only against fake APIs)
  - [ ] **DNS records (SPEC §6.5.1)**
    - [ ] Stage 2: public records → public IP; dynamic DNS via qdm12/ddns-updater; NAT loopback check (with public exposure)
    - [ ] Stage 3: Cloudflare proxy option (CF-Connecting-IP from Cloudflare ranges only; package proxy warnings)
    - [ ] Later: CGNAT detection → Cloudflare Tunnel / Roostack Cloud relay; optional local DNS (Pi-hole)
  - [ ] Maybe later: deSEC, DigitalOcean, Hetzner, OVH, DuckDNS
- [ ] 🟡 **1.10 Backups**
  - [ ] Full-lab restore from storage at a friend's lab
  - [ ] The drive picker for another server's drives (today only its own disk is offered from the controller; its USB drives only on its own page)
  - [ ] **Later (agreed): push instead of poll.** Move to htmx 4 (if not already) and use its WebSocket support (`hx-ws`): the server sends a part when it changes. Needs change notifications from every source (systemd unit states, other servers' apply and backup status, updates, disk space)
  - [ ] **Phase 2 (agreed, don't lose): add NAS shares (SMB/NFS) from the UI**: server, share and credentials in a root-only file, mounted by the agent like a drive (nofail, automount), write check. Already-mounted shares are offered today. Media libraries' shares (2026-10-19, `internal/agent/shares.go`) already check, save the login and mount read-only: a backup share needs the same read-write, with a write check
  - [ ] **Later (agreed): rotating backup drives**: two or more drives swapped in turn, each its own destination by UUID, with the absent one's last backup shown
  - [ ] Measure restic RAM/time on a realistic photo/video dataset
- [ ] 🟡 **1.11 Maintenance coordinator + host OS updates**
- [ ] **1.14 Design system** (agreed 2026-09-27)
  - [ ] Look at the HTML emails (SPEC §6.13, "Email in HTML", 2026-10-22) in Gmail (web and phone), Apple Mail (light and dark), Outlook, and Fastmail: a notification and the test email, sent from a VM; fix what a client shows wrongly
  - [ ] The `roostack` icon in `icons.go` (a house, for Roostack's own services) could become a line drawing of the hen
- [ ] **1.17 Help site (SPEC §6.3.1)**
  - [ ] Publish it, with the website, when the Owner says
  - [ ] Maybe: let a long upload (a first backup of a big library to online storage can take days) give way to other maintenance (OS updates, restarts) and carry on afterwards; the upload is already interruptible and resumes
  - [ ] Maybe later (agreed 2026-09-30, only if needed): "Start my backups afresh", separate from changing the key: new repositories for new backups (a new master key), the new key also added to the old ones so one key opens everything, old ones deleted as they age out. Costs a full upload and double the space meanwhile
- [ ] **1.16 Code review (2026-09-29)**
  - [ ] Maybe: separate `web.Server` fields per manager area, so each test fake implements only what its pages use
- [ ] **1.18 Public access (SPEC §6.11)**
  - [ ] **Come back to (agreed 2026-09-30): CrowdSec and SSH logs** (the rootless container can't read the host's journal; the agent could pass lines on, or bans could go into nftables)
  - [ ] CrowdSec end to end on a VM through Roostack itself (needs the public switch)
  - [ ] IPv6 (AAAA) public records, for homes with IPv6
  - [ ] When a server the hub dials gets a new address, follow it (today: re-join it)
  - [ ] Each public address choosing its own way in (SPEC §10.5: a route's `entry`), after the whole-lab version
  - [ ] HTTP/3 through an edge, if Caddy gains a way to learn a QUIC visitor's address (ASSUMPTIONS Q7)
  - [ ] Try the report, names on hold and **Turn the relay on** with a lab against Cloud's test environment, once both sides are deployed there
  - [ ] A full restore from Roostack Cloud onto a fresh VM, of a lab made for it (no real DNS records), including a big service's data taking longer than an hour
  - [ ] Each other provider's CNAME, TXT and delete against a real account (ASSUMPTIONS M7; Cloudflare's CNAME and TXT seen)
  - [ ] Countries per service (Cloud's DESIGN §5 says the Owner's settings are per service), once the lab-wide setting has been used
  - [ ] Countries for the other ways in (port forwarding, the lab's own edge), in the lab's own Caddy, which could show a page saying why
  - [x] **WAF, step 1 (agreed 2026-10-06, built 2026-10-19, SPEC §6.10 item 4):** CrowdSec AppSec virtual patching, on for every public service (and the UI when public), for outside visitors. The pinned bouncer (v0.14.1) already had AppSec: its `appsec` package is built into Caddy now, sending the first 1 MiB of a body; fails open (SPEC says why), and the protection check counts AppSec. CrowdSec runs the Hub's virtual patches and generic rules, the Hub pinned to one commit (the Caddy collection with it); a changed CrowdSec file restarts it. The Owner can turn it off per service on its public access page (a red confirm, audited, `no_waf`); a package can't. The service page's **Known exploits** card and the weekly summary say what it refused; help: Make a service public → Blocking known exploits, and false positives. Tested on VM1: docs/ASSUMPTIONS.md P10, P11
    - [ ] Move `generate.CrowdSecHub` to the Hub's latest commit for each release (a step in the release checklist), checking on a VM that CrowdSec starts and blocks
  - [ ] **Encryption anyone can check (agreed 2026-10-06):** backups are encrypted on the lab with keys that never leave it; make that verifiable rather than a promise: signed, reproducible releases (anyone can rebuild the published code and get the same binary), and a help page showing where the keys live, what Roostack sends off the server (e.g. to Roostack Cloud), and how to watch it
    - [ ] The source public, so "anyone" can rebuild (the repository is private)
    - [ ] Not in Cloud's request log: what Caddy writes to Cloud's challenge server at each renewal (Caddy calls it itself; its journal is out of the daemon's reach, ASSUMPTIONS A10). To choose: Caddy's `tls` loggers to a file the daemon reads, or `server_url` through a recording proxy in the daemon (SPEC §6.6)
  - [ ] **WAF, step 2 (later):** the OWASP Core Rule Set per service, "detect only" first; package format for WAF exclusions (e.g. upload paths); choose the engine (CrowdSec AppSec loading the rules, or Coraza)
- [ ] **1.19 Labs on the internet (SPEC §6.12)**
  - [ ] Verify on a real cloud server (the Linode): the wizard, public UI and services, a private service through a tunnel, CrowdSec
  - [ ] Package ports ("extra connections") on a lab on the internet: "home network" there means the provider's network
  - [ ] Later: a warning when home DNS answers a public address with something else (SPEC §6.12)
  - [ ] **Checking from outside through Roostack Cloud (agreed 2026-10-06, SPEC §6.12):** "Check from outside now" on public-access pages (and offered after a service's public setting changes); results per service ("Reachable from outside: checked …"); a private service found reachable, or a risky port open, as a problem in the attention list and by email; the manual phone check stays without Cloud
- [ ] **1.12 Packaging:** .deb, apt repo, installer flow
  - [ ] Download progress in MB: not without a terminal (Podman 5.4.2 prints a line per layer, and has no option for progress, checked on VM1 2026-10-18); only through Podman's API socket, which Roostack doesn't run (SPEC §6.3.3)
  - [ ] **Domain optional (agreed 2026-10-06, SPEC §6.6):** a lab uses its own domain *or* a free `roost.ac` name from Roostack Cloud. Untangle the requirement:
    - [ ] The own-domain path on a fresh install with a real DNS provider (unchanged code, tested only in unit tests since the change)
    - [ ] Addresses as a primary plus (later) extra ones, in the records from the start
    - [ ] Switching to or from a roost.ac name keeps the old one too (once roost.ac addresses work); Roostack's own address there is the name itself, not `roost.`
    - [ ] To discuss: changing one service's web address (its subdomain) could keep the old name for 30 days too; today "old links stop working" at once
    - [ ] A lab whose address goes away within an old address's 30 days (a roost.ac name dropped with the link) deletes the old address's records when it ends, but doesn't keep them updated meanwhile: the records manager only syncs while the lab has an address
    - [ ] **Benched (user, 2026-10-10):** own domain, unsupported DNS provider (SPEC §6.6): challenge delegation through Cloud when linked (two CNAMEs added once: `_acme-challenge` and `*`), and port 443 as a first-class choice (TLS-ALPN-01, a certificate per name), built together. Meanwhile the help says to move the domain's DNS to a supported provider
  - [ ] **Owner, before the first release:** apply roostack-cloud's `ops/tofu/releases` and put its outputs in the GitHub `release` environment (roostack-cloud `docs/RUNBOOK-RELEASES.md`) (the names `dl.`/`apt.roostack.cloud` confirmed 2026-10-18; the bucket's still to confirm)
  - [ ] One acceptance of a new series for the whole lab (today each server's Updates page), and the order a controller and its nodes move in, once it's known what a lab API change between series needs
  - [ ] Pre-releases, if wanted: a suite of their own (e.g. `0.6-beta`) a server opts into; `build-apt-repo.sh` refuses them today
  - [ ] At the first release: check that `https://dl.roostack.cloud/install.sh` and `https://apt.roostack.cloud/roostack-archive-keyring.gpg` are there (apt-publish publishes both since 2026-10-28), and install with them on a fresh Debian 13
  - [ ] Roostack Cloud's pages could use the cloud scenes in `assets/errors` (Cloud's repository)
  - [ ] Later: HTTPS by IP address (Let's Encrypt IP certificates) for a cloud server's setup page (SPEC §6.3.2)
  - [ ] To discuss: serving the UI at `roost.<domain>` as an explicit choice instead of automatically, at home (SPEC §6.3.2 has the questions; on the internet it's decided)
  - [ ] **The release key's ceremony** (the Owner): held 2026-10-27 (docs/RELEASING.md, "Record"): the key, its subkey, the public key in `packaging/keys/roostack-release.asc`, the fingerprint in the help and on Cloud's get-started page. Left: the `release` environment (a required reviewer, tags `v*` only) with the secret `ROOSTACK_RELEASE_SIGNING_KEY`, once roostack/roostack is public
  - [ ] `registry.Status.NoRootKey` and the catalog's "no root key" message can't happen in a build from this repository now that `rootKeys` has a key; keep them (a fork, or a future empty list) or remove them
  - [ ] Pre-releases (`v1.2.0-rc.1`): the release workflow takes only `vMAJOR.MINOR.PATCH`; a Debian version would need `~rc.1` to sort before the release

## Phase 2: Multiple machines (SPEC §10, draft)

- [ ] Seams
- [ ] Multi-machine follow-ups
  - [ ] Backups across machines (SPEC.md §10.10)
    - [ ] Try the S3 bucket check and rollback on a real AWS, Wasabi or MinIO bucket with a key that can't delete old versions (ASSUMPTIONS N8)
    - [ ] Scoped keys per server where an S3 provider can make them (AWS, MinIO, Wasabi session policies, as Cloud does), so a server can't reach the others' folders
  - [ ] Move other labs' backups when the offering server changes
  - [ ] Move a service between machines (SPEC.md §10.10)
  - [ ] Renew the lab CA (20 years); nodes behind a hub that isn't the controller
- [ ] Controller move (planned hand-over) and promotion from a node's replica
- [ ] Edge: public service at home routed through a cloud node (TLS passthrough over WireGuard, PROXY protocol)
- [ ] Failover: cold standby first
- [ ] Who counts as home on the network (security review of 2026-10-16: H3, H4, M23; SPEC §6.10 item 1)
  - [ ] People's devices on Roostack's network (remote access for people, SPEC §10.2): `100.96.128.0/17` is kept and counts as home; the hub would need to forward their traffic to other servers' home ports (today it forwards only the store port, from servers)

## Service registry (SPEC §8.3, mostly decided 2026-10-13)

- [ ] **The key ceremony** (the Owner): held 2026-10-27 (docs/REGISTRY-KEYS.md, "Record"): root and signing keys, `keys/` committed to the registry, the root public key in `internal/registry/rootkeys.go`, the `publish` environment's secret. Left: GitHub Pages, the DNS record and the workflow's variables (after the first release), then `roostack registry check`. Until then labs can't fetch the real registry
- [ ] Publish release binaries (the Owner, 2026-10-14: soon): the registry's Publish workflow downloads a release's linux/amd64 `roostack`, pinned by SHA-256 (`ROOSTACK_BINARY_URL`, `ROOSTACK_BINARY_SHA256`). The release workflow makes it (2026-10-18): `https://dl.roostack.cloud/releases/<ver>/roostack-<ver>-linux-amd64`, its SHA-256 in that release's `SHA256SUMS` (docs/RELEASING.md); waits for the first release
- [ ] Offer versions other than the newest (an older one, to match another server or for a downgrade), fetching them on demand
- [ ] Code owners for community packages, when they open: each package's folder gets its maintainers from its `registry.yaml` (`registry.yaml` itself stays the registry maintainers'); a check in `registry index -check` that CODEOWNERS matches
- [ ] Website (roostack-cloud): `services.ts` licences use deprecated SPDX ids (`AGPL-3.0`); Immich's is `AGPL-3.0-only` (its notice names version 3 without "or later"). Moot once the facts come from the index
- [ ] Community packages: who maintains it and when it was last reviewed, and the tier text ("Roostack checked how this is set up; the app itself is made by …"), on the install and service pages (SPEC §8.3.4)
- [ ] Packages in the registry could add `about.releases` (roostack-services; needs `requires.roostack` of the release that knows the field)
- [ ] Updates: an update to a new image (the download first): still tried only with new versions of the same image
- [ ] Updates: offer a fix on the installed line when the newest version is a major the policy doesn't take (the fetcher keeps only the newest version)
- [ ] Updates: ask for a setting a new version requires (today such an update is blocked, with a sentence); `upgrade` hooks and migration steps (SPEC §8.1 Future)
- [ ] **Update bot in the registry** (SPEC §8.3.8, docs/REGISTRY-UPDATES.md): hourly upstream checks, pull requests, CI that installs and health-checks (and backs up and restores) each update, auto-merge for official patch releases and rebuilds (the Owner, 2026-10-28)
  - [x] Finding and proposing (2026-10-28): `roostack registry updates` (`internal/registry/updates`; `imagecheck` lists tags and resolves a tag's digest) finds a new app version or a rebuilt image for each package's newest version and writes the revision; the registry's Updates workflow (`updates-workflow` branch in roostack/services) opens a pull request for each, and `updates.yaml` holds each package's rules (Forgejo on its 15 line, Plex's build-hash tags). Rebuilt images, found at the first Publish (HedgeDoc's), are its rebuild half. Tried against the real registry: 16 updates; two written passed the full check
  - [ ] Release 0.1.1 with `registry updates`, point the registry's binary variables at it, then merge `updates-workflow` (after `registry-and-watcher`)
  - [ ] The update test: a Debian 13 VM on GitHub's runner installs the released Roostack, the package's previous version, updates it to the new one, and backs up and restores (docs/REGISTRY-UPDATES.md, "Next"); first try on a dev VM whether `roostack apply` alone sets up a fresh install from the command line
  - [ ] Auto-merge of official patches and rebuilds once Publish and the update test pass; the Owner allows auto-merge and requires both checks on main
- [ ] "Follow upstream" per service at patch or minor (SPEC §8.3.8): package `updates:` tag pattern, the resolved digest kept in the lab's state, health check and roll back to the previous image, the explanation where it's chosen; withdrawals by image digest
- [ ] Registry settings (the Owner): create the team `@roostack/registry-maintainers`, and protect `main` (pull requests, the Publish check, reviews from code owners)
- [ ] Package editor: deploy roostack.cloud's `/package-editor` (the roostack-cloud branch above, then `ops/site-deploy.sh`; the Owner)
- [ ] Package editor: render templates in the browser too (Go's validator as WebAssembly, the Owner to decide), and a preview of the generated files
- [ ] Package editor: look up an image's digest and processors from Roostack (`imagecheck`), since the browser can't
- [ ] Package editor: open a package already added (Your own) to change it; Compose `profiles` as `when:` and a setting; `extends` and several Compose files
- [ ] Package editor: its table of the registry's apps (sign-in paths, licences) is a copy (suggest.js `known`, 2026-10-20); generate it from the registry's index when the editor runs in Roostack
- [ ] The guide's "Update it" step, once service updates (SPEC §7) can move a service to a new package version; `roostack package new <name>` (SPEC §8.3.7), if copying a registry package proves awkward
- [ ] Website (roostack-cloud repository): every package under `/services` (heroes first, a page each at `/services/<name>`) built from the index; `services.ts` facts move into packages' `about:`

## More services (the Owner, 2026-10-18; SPEC §8.1 "More services")

Packages go to the registry (`roostack/services`), each checked with `roostack validate` and tried on a lab (install, a backup, a restore, removal) before it's committed.

- [ ] Needs Roostack support, a session each: Frigate (and Viseron), Pi-hole, Home Assistant
- [ ] Cal.diy: heavy, and its README says personal, non-production use; decide with the Owner
- [ ] **Before go-live: every placeholder logo replaced** with the project's own, once its terms allow it or the project agrees (each package's file says why it has a placeholder)
  - [ ] 18 replaced on 2026-10-28 (roostack/services, each registry.yaml says where from); Plex's guidelines allow its logo with a trademark notice on the service's page, PhotoPrism's as fair use without implying affiliation or endorsement (the registry page's footer says so). Left: Ghost and HedgeDoc, whose terms ask for permission: ask each project, or keep the placeholder
  - [ ] A project's notice in Roostack itself (Plex asks for "Plex, the Plex Play logo and Plex Media Server are trademarks of Plex and used under a license" on the service's page): the registry's page shows it from its `notices.json`; Roostack's install and service pages could show it too, from a package field (`about.notice`: pkgspec, the editor's check.js, the help's package format) and a new Plex revision once a release knows the field
- [ ] **Media libraries** (design locked 2026-10-19 with a mock-up, SPEC §8.1 "Media libraries"): libraries per server (drive folder, share, or a folder on the server), chosen per service, read-only and not backed up, `ro,rslave`; "read as owner" through idmapped mounts
  - [ ] The move page naming libraries the service reads that its new server lacks
- [ ] **Check ASSUMPTIONS I3 at the first MySQL or MariaDB pin change:** a service with real data, updated across the major version
- [ ] **A setting's `warning:`** (SPEC §8.1 "Configuring a service"): in pkgspec and validate, shown beside the option at install and in the service's settings, in the warning colour; then move HedgeDoc's Libravatar warning from its description into it (a new package revision)
- [ ] **Kan** waits on upstream and Roostack: its web image has no version tags since 0.2.1 (only `latest`/`main`), and its migrations run in a separate one-shot container (`kan-migrate`), which packages can't declare. Revisit when Kan tags its images; a one-shot container type ("init" containers run before the service starts) would help other apps too
- [ ] **Generated secrets in other formats**, if more Laravel apps come (Lychee's `APP_KEY` wants "base64:" and standard base64; its package converts Roostack's URL-safe value in a shell wrapper)
- [ ] **Publishing the new packages:** Papra, Linkwarden, Paperless-ngx, Mealie, Lychee, Ghost, PhotoPrism, Jellyfin, Navidrome, Plex and Vaultwarden 1.37.4-roostack.2 use `install_value`, `databases.<name>.url`, `.Lab.TimeZone`, colons in paths, `client_ip_header`, `mysql`, `mariadb` or media libraries, so they wait for a Roostack release with them (the registry's Publish workflow checks with a pinned release)
- [ ] **Seafile's package** uses scheduled tasks (`on: schedule`, SPEC §8.1) for its garbage collection, so it waits for a Roostack release with them too, named in its `requires.roostack`
- [ ] **Forgejo 19 (the next LTS, April 2027):** the package stays on the 15 LTS line (v17.0.0 had an upstream "don't upgrade" warning on 2026-10-18, and 16 ends 2026-10-29)
- [ ] **Extra settings per service** (Settings → Advanced, Owner only): extra environment variables, plain or secret; the package's own variables refused; kept in the instance, through updates; shown in the generated files and Details
- [ ] **Config files a package declares** (`config_files:`), with Frigate and Home Assistant: an editor in the browser (syntax check, the app's own check, the previous version kept, restart on save); a code editor library only if chosen with the Owner
- [ ] **Frigate** (a session): devices beyond Intel graphics (Coral and other USB accelerators), shared memory, cameras over RTSP
- [ ] **Pi-hole** (a session): DNS on port 53, beside any resolver the server runs
- [ ] **Home Assistant** (a session): finding devices on the home network (host networking, or what's needed instead), within the package policy
- [ ] **Monitoring** (design locked 2026-10-19, built the same day, SPEC §6.15): Roostack's own checks every minute from the controller, history in SQLite, alerts through "needs attention" and the notifier, a public status page Roostack writes. Gatus was tried on VM3 and set aside (SPEC)
  - [ ] Wording: the Owner doesn't like it yet (the status page's subtitle, the uptime card); revisit with them before or while building the pages
  - [ ] Check on a lab with a real address (ASSUMPTIONS U4): a real certificate, the status page through an edge and Roostack Cloud's relay, its DNS record
  - [ ] When a service is removed, forget its checks' history (`store.ForgetMonitorCheck`; own checks already do); until then it goes with History's limit

## ARM64, then Raspberry Pi (ARM64-PLAN.md)

- [ ] **Phase 1: general ARM64** (Debian 13 arm64 on UEFI machines, SPEC §3.1; decided 2026-10-13)
  - [ ] End to end on the arm64 VM: see ARM64-PLAN.md 1.6 for what's done and left
- [ ] Phase 2: Raspberry Pi OS (needs a Pi 4 and a Pi 5)
- [ ] Phase 3: hardware checks (SD card, memory, heat, power, clock)
- [ ] Phase 4: labs mixing PCs and Pis
  - [ ] Try it on a real mixed lab: an amd64 VM and an arm64 VM joined, a package built for one only (a test-signed registry), installed, refused on the other, moved
- [ ] Older Pis (3, Zero 2 W): measure Roostack's own memory use in a 1 GB arm64 VM, then decide (ARM64-PLAN.md Decision 6)

## Security review of 2026-10-16: restore integrity (restore-integrity, 2026-10-17)

Changes to the lab ↔ Cloud contract (`cloudapi`) made with Roostack Cloud's side.

- [ ] **Not yet taken up from the review** (2026-10-16; the review is docs/REVIEW-2026-10-16.md in roostack-cloud):

## Browser check of the review's pages (browser-check, 2026-10-17)

Every page the two review rounds changed, in Roostack and Roostack Cloud, at 1280 px and 375 px, light and dark (the list is in the session's report). Fixed: phone-width overflow (Needs attention, the link code under "Setting up …", the disk key, long buttons, the page header, copy boxes), wording (the junk items and page, the restore page, the rollback check), and Cloud's login wording and console layout. Not fixed here:

- [ ] Checked only through the web tests' fakes, not on a real lab (they need Backblaze, a versioned S3 bucket or a second server): the plan's delete notices, the rollback check and confirmation, Change the key, the lost main backup after a server left, the Servers page after removing a server, the junk page with reports, the restore page with several labs in Roostack Cloud, the two-factor prompt for Admins on a public UI, the install page on a lab on the internet, the SSH passwords warning (VM2's sshd already refused passwords)

## Forms (the Owner, 2026-10-19: "a lot of forms within Roostack are long lists of inputs")

- [x] **Add a media library** as one form (SPEC §8.1): a segmented control for where it is (drive, folder on the server, network share), folders chosen in a dialog (the folder picker), name, services and the readability check on the same form. Mock-ups: [docs/mockups/media-add/](docs/mockups/media-add/README.md) (2026-10-19, direction A; new components in the gallery's Choosing section; the agent now counts a folder's own files and leaves Roostack's own folders out of a server folder's list. Tried on VM3 in the browser, desktop and phone, light and dark: a drive's folder chosen in the dialog, a folder on the server, SMB and NFS, a name already taken, a library added and removed)
  - [ ] VM3's second server (roostack-test, a Linode) runs a Roostack without the media library API, so its add page fails (it did before too); update it when convenient
- [ ] The rest, from a look at each in the browser on VM3 (2026-10-19). New controls this would use: the segmented control and folder picker (above), a **toggle** for on/off settings that save with their form (a styled checkbox: still a checkbox without scripts), **fields side by side** (two to a row, one under another on phones), a **short input** for numbers and ports, a fixed **unit** after a number (`.with-suffix`, as the domain), and the existing `<details>` for rarely used fields.
  - Fine as they are: Updates and restarts (radios with notes, the time as `select.inline`), setup's SSH step (choices with consequences each need their note), the address choice, the backup plan's chooser tiles

## Bringing your own data, and moving from Docker Compose (SPEC §8.4, decided 2026-10-20)

- [ ] Measure a real move's timings on real hardware (W8), check a share bound at its own path through a NAS restart, and check the reconnect restart doesn't fire at boot
- [ ] Warn when Docker runs on a Roostack server (its published ports get past Roostack's firewall, W7)
- [ ] Bringing data in, still to try (ASSUMPTIONS W12): a share as the source, a MariaDB dump, an import onto a node, a drive read as its owner; the reconnect watcher when Roostack restarts between the unplug and the replug (it notes what's there at start, so the service stays blind until restarted: compare the container's start time with the mount's?)
- [ ] Bringing data in, small things seen on the way: the restart after a library's drive comes back isn't in History or on the service's page; "On the old machine" box's `pg_dump` command breaks inside `--format` on a phone; the old setup's choice (docker inspect, Compose file, Neither) wraps Neither onto a line of its own on a phone; `Made by Plex, Inc..` (a doubled full stop on the service page, where the maker ends in one)
- [ ] Bringing data in, later: leaving subfolders out of a copy ("Leave out…" on a row); a dump uploaded through the browser; plain SQL and `pg_dumpall` (Immich's nightly backups); a check that warns about Docker found on the server (ASSUMPTIONS W7); the import's check and copy logs from another server on the page (only its progress shows there now)
- [ ] Later: a dump uploaded in the browser, plain SQL and `pg_dumpall`, another machine over SSH, a storage hint in the package format, help pages for Jellyfin, Vaultwarden, Navidrome

## Bringing photos into Immich from other platforms (SPEC §8.5, decided 2026-10-21)

- [ ] Photo migration, still to try (ASSUMPTIONS X10): a share as the source, real videos and HEICs and a Live Photo pair (make them with Immich's ffmpeg for `maketakeout -videos-from`), a real Takeout's localised names, a library over 50,000 photos, the time zone shown for a sidecar-only date, the emails with a mail server, amd64
- [ ] Photo migration into an Immich on another server: the node's photo calls over the lab API (as `importwire.go` does for §8.4); refused with a sentence until then
- [ ] **Open, the Owner's call:** immich-go's catalogue patch (frees each folder's once handled: live data at the end −25%, peak unchanged): carry it, offer it upstream (PR prepared, not sent), or neither
- [ ] Upstream ideas for immich-go's memory, if offered: a slimmer record of the account's assets (ID, checksum, name, date, size), finished tracker records dropped, a resume record so a carry-on doesn't re-read what's done
- [ ] Photo migration where the export is (decided 2026-10-21): immich-go on the server the drive is in, or Immich's for a share or folder, reaching Immich over the lab network, with "Immich on another server" below
- [ ] `maketakeout`: a fake `.psd` is expected to be left out, but Immich takes `.psd` files: list it as an asset, or make it something else
- [ ] Separately (SPEC §5.2, later): "Sign in with single sign-on" on Roostack's own login page, so the sign-in page's link needs no second sign-in
- [ ] The first version: Google Takeout and a plain folder, from a drive, share or server folder; tested on VM 1 with a synthetic Takeout (§13)
- [ ] Straight after: Apple's privacy export (and a Mac's export as a plain folder)
- [ ] Before shipping: immich-go's next release (this week's Live Photo and resume fixes)
- [ ] Later: an upload share (Samba), the Locked Folder and trash, Flickr and Facebook, cloud drives, PhotoPrism, the pages on the sign-in site too, a queue of imports

## Code quality review (2026-10-21)

The review of both repositories is [docs/QUALITY-REVIEW-2026-10-21.md](docs/QUALITY-REVIEW-2026-10-21.md); IDs refer to it.

- [x] The Owner decides the rest of the review's §12 questions and agrees the plan (§11). All decided on 2026-10-22 (§12, "Decided")
- [x] CLAUDE.md's writing rules: the Owner's tense rule (2026-10-22), in batch 1e
- [x] Wave 0: the behaviour fixes found in passing (§2), all approved by the Owner on 2026-10-22 (P3 with its agreed wording, P9 in UTC); each finding's status, with its commit, is in the review
  - [x] Roostack's: P3–P7, P12, P13, P15, P16 (2026-10-22)
  - [x] Cloud's: P1, P2, P8–P11, P14 (2026-10-22, roostack-cloud branch `claude/wave0-fixes-177bbd`, merged)
  - [ ] P3's follow-up: check a failed move through a backup on two VMs (only fakes so far)
- [x] Wave 1: the parallel batches 1a–1j (§11), all merged by 2026-10-22; the review's §11 "Wave 1 status" says what each did
  - [x] 1a–1e here and 1h–1j in Cloud, merged into both `main`s on 2026-10-22
  - [x] 1f (CI, staticcheck, generated files, flaky tests) and 1g (the editor), merged on 2026-10-22
  - [x] S10, one capability convention on `Node` (batch 1c's follow-up), merged on 2026-10-22
  - [x] Cloud's reconciliation after 1h's move to `internal/ui`: the sync scripts' and mock-ups' paths, the design copy re-synced and checked, the docs (batch 1i's follow-up), merged into Cloud's `main` on 2026-10-22; Cloud's editor copy reads 1g's `scripts.json` (merged on 2026-10-22, pinned at Roostack `f27115b`)
  - [x] Moving a service back to the controller with "copy directly" hung (found by 1c; on `main` before wave 1): the import now reads each tar stream to its end (2026-10-22, SPEC §10.10). Checked on VMs 5 and 6: Vaultwarden there and back, with its data
  - [ ] T1's later step: split `agent` into portable wire types and client, and the Linux server
  - [ ] Turn on GitHub's private vulnerability reporting in the repository's settings when the repository goes public: GitHub offers it only for public repositories (the maintainer, 2026-10-23). SECURITY.md names it beside `security@roostack.cloud`, which works until then
  - [ ] `web.NewServer` could take `Node` as an argument too (always set in production; not in S1's decision)
  - [ ] `li.card.service` in app.css is unused (not on §9's list)
  - [ ] The media library form doesn't trim a share's server and path, as the photo and import steps do
  - [ ] `importBrowsePage`'s "Use …" label trims a leading "." from folder names (`TrimPrefix(rel, ".")`)
  - [ ] Right after saving a server's restart policy, its page shows the old hour until a reload a few seconds later (on `main` before wave 1)
  - [x] D3 (wave 2): `store.FormError` was a fifth "error a person can fix" type; all five are now `usererr.Error` (2026-10-22)
  - [ ] `AdoptBackupRuns` and `backup_runs.destination` exist only for runs from before repositories were recorded: pre-release aids that could go (a behaviour change, the Owner's call)
  - [ ] Wave 4g: "the Owner" still means the maintainer in many older SPEC, TASKS and DESIGN entries ("the Owner's choice"); SPEC's top note explains it
- [x] Wave 2: the shared helpers (§11), merged by 2026-10-22
  - [x] P5's follow-up: one parser for the `# Added by Roostack:` blocks, with typed tag constructors (the edits share `editFstab` since wave 0, and `/etc/fstab` is replaced in one step since 2026-10-22, SPEC §6.1 "Editing `/etc/fstab`"); done on 2026-10-22: `fstabBlocks` and the `tag` type in `internal/agent/fstab.go`
  - [x] D1: the atomic writes of files that aren't private (0644: generated files, the status page, the registry's, `labnet`'s certificates, the agent's sysctl, firewall and SSH files) and the key hints use `fsutil.WriteFile`, and the streamed copies (`labrestore`'s database, `node/importcopy.go`'s dumps) `fsutil.CopyFile` (2026-10-22). Wave 0 (P7) moved every private (0600) and 0640 file to `fsutil`. Left as they are: the files SQLite's `VACUUM INTO` and a dump command write, which go through their own temporary file and rename
  - [x] D4: one tested `fsutil.Within` in place of both `within` helpers and the inline checks; `manager` and `node` check service IDs with `instance.ValidName` and package names with `pkgspec.ValidName`; the agent's looser `serviceName` says why, and a test keeps it and `whoName` in step with their owners (2026-10-22)
  - [x] D5: one copy each of `oldContainer` (`importer.Setup.Container`), the random password (`labnet.NewPassword`), `FstabUnescape`, `HostOnly`, the remote headers and the folder roots; `authkit.HashToken`, `cmp.Or`, `slices.Contains` and `trust.CGNATRange` called rather than copied; certificate serials left to `x509`; no `rand.Read` error checks (2026-10-22). The two `printable`s stay: one shortens text for an error and marks control characters, the other drops them from a drive's label
  - [x] D6: `Paths.ServiceDataDirs`, `DatabaseFile` and `SetupTokenFile` (the setup pages name the file from it); `internal/destination` parses and writes `kind:id` references for `manager` and `web`; `Instance.HostOr`; the agent's `SystemBackupDir` says why it's fixed, and a test keeps it equal to config's default (2026-10-22)
  - [x] D7: `store.GetJSON[T]` and `PutJSON`, which return errors, in place of `node.loadSetting`/`saveSetting` and the hand-written copies in `node`, `manager`, `osupdate` and the status page's choices (2026-10-22). Left: `osupdate.Resume` (it tells a value it can't read from one it can't parse) and the backup fence (its own parser)
  - [x] X4: `authkit`'s `SealFor`, `OpenFor` and `IsSealed`, binding a secret to its owner as GCM's additional data; Cloud's `totpseal` uses them and still opens its two older forms (2026-10-22)
  - [x] X7: `cloudapi.Routes`, each lab route's method, path and rules; Cloud's `TestRoutes` checks its routes against it both ways, and Roostack's fake Cloud follows it, with the backups choice while suspended and the per-lab write limit (2026-10-22)
  - [x] X1: `internal/units` is now the public `units`, with `AsSold` ("128 GB", "under 1 MB", "nothing") beside `Bytes`, so Cloud's four size formatters and its `thousands` are gone: its portal and storage emails use `AsSold`, its admin console and command line `Bytes` (2026-10-22). Roostack's output is unchanged
  - [ ] The maintainer's call (X4): reseal Cloud's `bound1:` two-factor secrets into `SealFor`'s form when next used, as unbound ones are, so the old form can later be refused like `refuse_unbound`
  - [ ] When Cloud answers a certificate renewal with 429 (too many changes), the lab records it as "couldn't reach Cloud", so its page would wrongly say Cloud can't be reached (found by X7; only a lab stuck in a loop would hit it)
  - [ ] `TestRequestLogLeavesOutSecrets` sometimes fails under load: a background name claim writes state after the test has removed its temporary folder
  - [x] D8: `internal/sleep` (`For` and `Every`) for the "every d until ctx ends" loops and the waits that give up when ctx ends; transient units named after what they run (`roostack-restic-…`, `roostack-podman-pull-…`), not all `roostack-backup-…` (2026-10-22). Left as they are: loops that also wait on a channel of their own (a wake-up, a stop, a release) and the timeouts
  - [x] D2: `internal/words` (`Capitalise`, `And`, `Or`, `Count`, `Plural`, `FirstLine`, `RoughDuration`, `ExactDuration`) in place of about 30 copies in `web`, `manager`, `node` and `setup`; the template functions call it, and photos' template function `durationText` is now `roughDuration`; `size`, `sizeText` and `gbText` are `units.Bytes` (2026-10-22). Every page, activity line, audit line, email and user error the whole suite produces was the same before and after. One change by design: a first letter outside ASCII (an Owner called "émilie") is now capitalised, where the byte-wise copies wrote a broken character
  - [ ] Wave 4, from D2: the lengths of time that fit neither of `words`' styles without new wording: monitoring's `durationText` (rounds down, counts days), the update check's `minutes` ("40 seconds"), maintenance's `roundHours`, the history page's Go `Duration.String` ("1m30s"), the idle choice's `idleText` and the S3 check's `days`; counts without thousands separators (`folderCounts`, a server's built-ins), which `words.Count` would give; and `manager` and `node` take an error's first line after trimming it, `web` and `setup` before
- [x] Wave 3: the structural changes the Owner chose (§11), merged by 2026-10-23
  - [x] D9: each provider's credentials check is the records code's own (`recordsAPI`: the TXT record set, then deleted), keeping Cloudflare's hint; Namecheap keeps its read-only check. `dns.FQDN` in place of `dnsrecords.fqdnOf`, a typed "not found", Roostack Cloud's provider in the list, a `conn` for one zone at a provider in place of `(hc, v, zone)`. `dnstest` has the one zone (the check's search for it is tested), logs every request, and refuses a CNAME beside another record; `dnsrecords`' own Gandi fake and `dns`' probe fakes are gone (2026-10-23). The check now sends what records do, so it differs from before: Porkbun and Spaceship replace a left-over test record rather than add one, Cloudflare's carries "Managed by Roostack", TTLs are 300 (Cloudflare's was 120, Route 53's 60), Gandi's TXT value is quoted, and Route 53's is XML-escaped
  - [ ] A second backup drive with the same label as one Roostack already knows (e.g. a new drive erased as ROOSTACK while the first is unplugged) gets the same mount point: `mountForGood` (`internal/agent/drives.go`) only checks that `/mnt/roostack-<label>` is empty, not that another fstab line uses it, so the new drive's line clashes and mounting it fails ("A dependency job for mnt-roostack\x2droostack.mount failed"). Found on VM 4 while checking S13 (2026-10-23); on `main` before wave 3
  - [x] S12: `serve` wires the controller a step at a time, each a method of a `controller` struct (`wireSetupPages`, `wireManager`, `wireWizard`, `wireWeb`, `wireCloud`, `wirePublic`, `wireNetwork`, `wireBackups`, `serveLabAPI`, `wireCatalog`, `startJobs`, `wireSetup`, `startCloud`, `wireNotifier`, `wireMonitoring`, `wireDNS`, `listen`), in an order `run` shows and explains; restarting is a `restarter`; `startMaintenance` is `serverJobs` (`cmd/roostack/jobs.go`); the package-level `cloudBackupSource` is a `cloudBackupKeys` that `serve` passes down; `serviceViews` and `setupToken` are `web.ServiceViews` and `web.SetupToken`, and `pullImage` is `podman.Pull`, each with a test (2026-10-23)
  - [x] `podman.Pull` can lose podman's reason: it reads stdout and stderr separately, so when the error line arrives before the progress, the error is only "exit status 125". `TestPull` fails this way on every run in VM 4, and once in a while on a Mac. Found while checking S2 (2026-10-23); `internal/podman` is as S12 left it Fixed on 2026-10-23: podman's output and errors share one writer, so exec gives them one pipe and they arrive in order, and the reason is the last "Error:" line wherever it is; `TestPull` passes in VM 5
  - [x] S13: the agent's `Handler` is a route table of one line or so per call (`route`, `action`, `do`, `read`), with named request and answer types shared by the client (`internal/agent/wire.go`); names are checked there, so the API tests show a bad one never reaches the host; a host's refusal is a 400 and its own failure a 500; every call that changes something is logged; `LinuxHost`'s firewall, time zone, update, reboot and WireGuard methods are in their files; one line splitter for the update stream and the dpkg filter (2026-10-23). The agent's API tests now run on a Mac too
  - [x] S2 steps 2 and 4: `manager.PlanChecks` (the 3-2-1 checks, the main backup's hint and what a plan lacks), `ServerLocation`, `SetServerLocation` and `ServerPlace`, `NewServiceNames` and `InstallServer`; "Needs attention" and the notifier are `internal/attention` (`Lab.For`, `Notifier`), reading the lab through small interfaces that `web.Server.AttentionLab` fills, with the low-space thresholds, Cloud backup's space and the host-changes cache beside them; `words.When`, monitoring's sentences on `monitor.Status`, `manager.AppVersion` and `UpdateRecord.Shown` in place of web's copies (2026-10-23). Every attention item, email and page the web tests produce is the same as before; on VM 4, Home's Needs attention, the backup plan and a notifier email through a local SMTP catcher
  - [ ] S2's other background jobs in web, left where they are because none fits cleanly yet: the monitor's checks (`MonitorChecks`, `MonitorOn`) read the DNS records' status, which `serve` sets only after monitoring starts, and the Monitoring page owns the on/off setting; the history cull (`RunHistoryCull`) needs the History page's list of servers, and its days are a setting monitoring reads too
  - [x] X6's static sites (round 1, 2026-10-23): the public help's headers add the website's `Permissions-Policy` and `Cross-Origin-Opener-Policy`, and its password gate has the website's test (`help/worker/gate.test.js`, `make gate-test`, in CI's test job). The two header builders stay apart (Python here, Node in Cloud's `site/tools/headers.mjs`), each saying to keep them alike
  - [x] D12: one kept-copy system, `internal/kept`: a copy is the folders set aside for one service at one moment, with why (`kept.Why`, its label in the Owner's wording); each server lists, puts back and deletes its copies (`node.Mover.KeptCopies`, `PutBackKept`, `RemoveKept`, `GET /v1/kept-copies`), and the restore page and the Servers page show one "Kept copies" list with the same labels and buttons, as in the approved mock-ups (`docs/mockups/kept-copies`). A restore from another server now keeps what was there as `before-restore`, and a put back after a restore in place as `before-put-back` (2026-10-23)
  - [ ] Remove the kept-copy routes from before 2026-10-23 (`/v1/kept`, `/v1/kept/put-back`, `/v1/kept/remove`, `GET /v1/restores/{service}/kept`, `POST /v1/restores/{service}/kept/remove`), the controller's fallback to them (`node/keptwire.go`) and `WireStatus.Kept`'s check, once no server in a lab can be from before then (the release after the first one has them)
  - [x] D11: a move (directly or through a backup), a restore and a gone server's restore are one procedure, `manager.transferJob` (`transfer.go`), in place of the 8- and 9-parameter functions; each keeps its own ending when the databases don't load (P3). One step now matches across them: a move through a backup clears what an earlier attempt left in the staging folders first, as a restore did (2026-10-23)
  - [x] T2, T8 and T9, Roostack's tests: `backup`'s restic tests run in parallel, each on a copy of one repository made once (`restictest.Init`, since `restic init` takes about 3 s), and so do `manager`'s and `labrestore`'s slow tests; the timings tests shortened by changing package variables are fields that each test sets on its own value (`Manager.waits`, `JobDeps.ReachTimeout`, `Repo.JunkRecheck`, `Local.buildPoll`), and the reconciler's settle is 100 ms in its test. `restictest.Bin` finds restic (and fails, rather than skips, when `ROOSTACK_RESTIC_BIN` won't run), `storetest.Open` opens a test's store, `b2test` is the one fake Backblaze, `nodetest.Base` the base of other packages' fake servers, and the hand-written wait loops use `testwait.For`. On a Mac under load, `go test ./...` went from 256 s to 109 s (`backup` 224 s to 61 s, `manager` 246 s to 65 s), and the full suite in VM 6 from 9 min 33 s to 4 min 32 s (2026-10-23)
- [x] Wave 4 (the maintainer, 2026-10-22): wording across both repositories, merged by 2026-10-23. The sample (`docs/wording-examples.md`), then 4a (templates), 4b (Go-built text, logs, errors, the CLI), 4c (help), 4d (editor and `pkgspec`), 4e (Cloud's website, console and emails), 4f (Cloud's site and status page) and 4g (the project documents in both repositories); each batch's before-and-after list is in the shared notes (`w4a-changes.md` to `w4g-changes.md`)
  - [x] Out-of-date facts 4g found (fixed after it, 2026-10-23, in SPEC, ASSUMPTIONS, IMMICH-IMPORT, MIGRATION-PLAN and Cloud's ARCHITECTURE, DESIGN and RUNBOOK-DEPLOY; photoimport's unused dry run removed too): SPEC §6.9 says a whole-lab restore "comes with the installer" (it's on the first-run page); MIGRATION-PLAN's header says "Decided, not built" (SPEC §8.4 records it built on 2026-10-21); IMMICH-IMPORT §10 and §13 still list the dry run, dropped while building; ASSUMPTIONS M4 and D2 contradict what's built; Cloud's RUNBOOK-DEPLOY says nothing has run on the server (it has, on test, 2026-10-17); three plans in Cloud's DESIGN have since happened (the DNS move to Cloudflare; passkeys, twice)
  - [ ] Documents 4g didn't cover: TASKS.md, ARM64-PLAN.md, SECURITY.md, the READMEs in subfolders, and Cloud's `docs/BACKUP-STORAGE.md`
  - [x] S8 step 2: `startApplyLocked(reason)` in place of the `applyReason` field; one runtime copy of `lab.yaml` (`Manager.labNow`, swapped by `saveLab` after `UpdateLab` saves) in place of `public`, `noStops` and `statusPage`; `hiddenCache`, `switching`, `rollbackChecks` and `remoteConfigMu` are fields, not package-level; one `checkSecret` for install and retry; the server-naming helpers in `servers.go`; `Manager`'s fields grouped by feature (2026-10-23). Every activity line, user error, audit line, attention item, email and page the suite produces is the same as before
  - [x] S8 step 4, photos: `manager.Photos` (`Manager.Photos()`), reaching the rest of the controller through a small interface (`core`, `core.go`: the lab, the log, `node`, `nameOf`, `installedService`, `Apply`, and for photos the settings and media libraries); the emails when a migration starts and ends moved from web with it (`MigrationStarted`, `MigrationEnded`) and now have a test. All 166 emails a temporary test made in every case were the same before and after (2026-10-23)
  - [x] S8 step 4, storage for other labs: `manager.Guests` (`Manager.Guests()`, `guests.go`, `guesthost.go`) through the same `core`; this lab's storage at other labs is a backup destination, so it stays on `Manager`, in `remotestorage.go`. In web, `StorageSharing` is `Server.Guests`, and `StorageAtOtherLabs` stays in `ServiceManager`; `attention.Lab` has `Guests` (2026-10-23)
  - [x] S8 step 4, recovery key changes: `manager.KeyChanges` (`Manager.KeyChanges()`, `keychange.go`) through the same `core`, with the activity logs, maintenance jobs, Roostack Cloud backup and `keyRepos`, which stays on `Manager` in `backups.go` because restores use it too. In web, `RecoveryKeys` is `Server.Keys`. `Manager` now has 140 exported methods, from 168 before step 4 (2026-10-23)
  - [x] S11: `node.Local`'s photo migration and imports are types of their own, `localPhotos` and `localImports` (`Local.Photos()` and `Imports()` return them, no longer the Local), as `Remote`'s capabilities are; each is a view of the Local, whose `pho` and `imp` keep what they share while it runs. `Local` has 138 methods, from 210 (2026-10-23)

## Load review of the repeating jobs (2026-10-24)

What every repeating job in Roostack, Cloud, and the registry asks of shared services, reviewed with the maintainer; the fixes they chose:

- [x] The watcher's hourly commit would have started Publish every hour once `WATCH_TOKEN` is set (a new index each hour, every lab nudged): Publish skips pushes that change only `watch/` or `boms/`, and the watcher commits its state only when more than the time of the run changed, or once a day (registry branch `watch-workflow`, not pushed)
- [x] Every server's nightly work started in the same minute: the backup, the weekly maintenance, and the restart window now start at the server's own minute in the half hour after (`maintenance.Offset`, SPEC §6.9 "Each server at its own minute"); Settings → Updates shows the exact time
- [x] The registry's key statement was downloaded every hour: it's asked for with `If-None-Match`, as the index is
- [x] Cloud's name suffix was asked for every few seconds while Cloud was out of reach and a page was open: not again for a minute after a failure
- [x] In Cloud (branch `red-button`): edges' config pulls rewrite only the rows that changed; Cloudflare is asked only about the names a small change touches, and its Retry-After is honoured; so is Cert Spotter's
- [x] Roostack Cloud backup's 12-hour keys could run out during a long upload, and a 403 then counted as a failure that needs someone: keys are renewed once they're an hour old, and a command that fails once its keys have run out runs again at once with new ones (SPEC §6.9, docs/ASSUMPTIONS.md N10)
- [ ] Left: the watcher's feeds have no conditional requests (CISA's list is over 1 MB an hour), which needs somewhere to keep their ETags; logging in to Docker Hub in the registry's workflows if they meet its anonymous limit; the edges' config could carry a version so that an unchanged pull costs nothing

## To discuss with the user

Nothing at the moment.

## The big red button (SPEC §6.16, decided 2026-10-22)

Waited for the code-quality review to finish (the maintainer, 2026-10-22); the maintainer started the build on 2026-10-23. Everything is decided, `built_with:`'s shape included (docs/RED-BUTTON-PLAN.md §4.1).

- [ ] Phase 1, the button (built 2026-10-23, SPEC §6.16 "As built"): `public.pause` and `public_pause`; Caddy's page for outside visitors (first in the route block), the error page, public ports home-only, the sign-in page following its services, Paused on the status page; `built_with:` in pkgspec, the editor and the format reference; Home's **Pause public access…** page, the service page, Settings → Public access, lifting; roles, audit, Security emails, the weekly summary; `roostack public pause | resume | status`. Checked on VM7 (docs/ASSUMPTIONS.md P13). The status page and the emails checked too (P15). Left: a check through the lab's own edge and Roostack Cloud's relay, which needs a second VM as a cloud server and a development Cloud
  - [ ] Require `built_with:` in the package policy once the registry's packages have it (installs refuse a registry package with any policy problem, so not before): once the `built-with` branch is merged and published
  - [x] Pages show `built_with:` names as written (`php`): they show the registry's titles (PHP) from its list in the index (2026-10-23)
  - [x] A pause waited behind the rest of an apply, and a failed apply didn't reload Caddy: pauses now reach Caddy at once, a failed apply still reloads it, and the changes waiting are listed (SPEC §6.16 "Pausing doesn't wait", 2026-10-23)
  - [x] `roostack public` restarted Roostack to apply a change, which stopped a running backup: it now sends SIGHUP, and Roostack reads `lab.yaml`'s pauses again (2026-10-23)
  - [ ] `words.And` has no Oxford comma ("a, b and c"), which the writing rules now ask for (2026-10-23); it's shared, so for wave 4
- [ ] Phase 1, in the registry: `built_with:` for every package, and the list of names (title, a line, and the purls and CPEs the watcher will map from) in the index. Written on the branch `built-with` in the maintainer's checkout (2026-10-23, not pushed): new revisions of all 23 packages and `built-with.yaml`, passing `roostack registry index -check`. The names were checked against Syft's bills of materials for every newest image (2026-10-23): what's left is the maintainer's call (FFmpeg in Mealie and Paperless-ngx, Git in Radicale)
- [x] Phase 2, advisories (built 2026-10-23, SPEC §6.16 "As built, phase 2"): `advisories/` in the registry and the index's field, with the build's checks; matching on the controller; the automation setting; worked out each time and lifting by themselves; **Keep it public anyway**; updating at once; hourly fetching; emails. Checked on VM7 (docs/ASSUMPTIONS.md P16)
  - [x] A line in the lab's report to Cloud: how many public services are paused and since when (2026-10-23; Cloud's repository shows it)
  - [x] `roostack public status` lists advisory pauses (2026-10-23)
  - [ ] The registry repository: an `advisories/` folder, its CI running the build's checks, and a short guide for writing one
- [ ] Phase 3: Cloud's nudge; pausing at the relay's edges from Cloud's website (Cloud's repository); the "closed" level (firewall, edge, relay) for problems in Caddy or Roostack
  - [x] The lab's side of the nudge (`Relay.Registry`, `GET /v1/lab/news`) and of pausing at the relay's edges (`Relay.Paused`, shown, emailed, and lifted with `RelayRequest.Resume`), tested against the fake Cloud (2026-10-23, SPEC §6.16)
  - [x] Cloud's side of both, and showing the report's `paused` line on the lab's page (Cloud's repository: built on its branch `red-button`, 2026-10-24, not pushed; checked with VM7, docs/ASSUMPTIONS.md P19)
  - [x] The closed level (2026-10-23, SPEC §6.16 "As built, the closed level"): every server's firewall, the relay asked with `RelayRequest.Pause`, advisories naming Roostack releases asking on Home and lifting by themselves, `/pause/close`, `roostack public pause|resume -closed`. Checked on VM7 (docs/ASSUMPTIONS.md P18)
  - [x] Cloud's side of `RelayRequest.Pause`: close the edges for the lab's names while its requests say so (Cloud's branch `red-button`, 2026-10-24, P19)
- [ ] Phase 4, the watcher (docs/RED-BUTTON-PLAN.md §10.1; can start alongside phase 2; first part built 2026-10-23, SPEC §6.16, docs/REGISTRY-WATCH.md): `roostack registry watch` in the registry's CI reading OSV, GitHub's advisories, both KEV lists, EPSS, oss-security, projects' notices and new CrowdSec patch rules; matching on `built_with:`; draft advisory pull requests, a weekly digest, ntfy notifications for critical exploited matches; Syft bills of materials for exact matches, and its findings compared with `built_with:` in the registry's pull-request checks; then an AI first pass once the watcher works without it. Publishing with nobody around: not now, revisit after a few months of accurate drafts
  - [x] `roostack registry watch`: GitHub's advisories (packages' sources and Roostack's parts), CISA's list, CrowdSec's rules, EPSS, OSV for bills of materials; drafts, the digest, the state; a dry run with the real feeds (2026-10-23)
  - [ ] The workflow in roostack/services: written on the branch `watch-workflow` in the maintainer's checkout (2026-10-23, not pushed); then the variables, the `NTFY_URL` and `WATCH_TOKEN` secrets, `main` accepting the state's commit, and a Roostack release with `registry watch`
  - [x] Syft in the registry's CI and the comparison with `built_with:` (2026-10-23): `roostack registry images` and `bom-check`; the registry's scripts, the Bills of materials workflow, and Publish's pull-request step, on the local branch `boms` (on top of `built-with`), not pushed
  - [x] The registry's HedgeDoc couldn't be pulled: quay.io deleted the image its package pinned when the 1.12.0 tag was rebuilt for Node.js 24.22.1 (21 October). HedgeDoc 1.12.0-roostack.3 pins the tag's image now (checked: HedgeDoc 1.12.0 inside, pulled and run on a VM), on the local branch `hedgedoc-image` (2026-10-23, not pushed); `built-with` and `boms` are stacked on it, so HedgeDoc's built_with revision is .4
  - [x] A version whose image has gone upstream failed the registry's whole build: now, on an older version, it's a warning, and the version is marked `image_gone` and never offered (2026-10-23, SPEC §8.3.3); the newest's still fails, so HedgeDoc's fix (`hedgedoc-image`) is still needed
  - [ ] More sources: VulnCheck's list (needs an account), oss-security (check its feed is official), Codeberg's advisories for Forgejo, projects' own feeds
  - [ ] Later: the AI first pass, once the watcher works without it

## Single sign-on: what's next (SPEC §5.2)

- [ ] Run the conformance suite again before each release that changes `internal/sso` or the sign-in page (docs/SSO-CONFORMANCE.md)
- [ ] Add the `sso` part to the registry's Mealie and Stirling-PDF packages (as in `internal/testdata/packages`), once a Roostack release has it (`requires.roostack`)
- [ ] The registry's single sign-on revisions say `requires.roostack: "0.1"`, which is right only if Roostack's single sign-on branches are merged before v0.1.0 is tagged (the first release, docs/RELEASING.md; development builds run any package). If 0.1.0 is tagged without them, raise each to the first release that has them
- [ ] If a network's options change after release: the reconciler removes and recreates a network whose `.network` file changed (stopping its service's containers first), since Podman keeps an existing network as it is (ASSUMPTIONS C8; SPEC §6.4)
- [ ] Later (the Owner, 2026-10-21): Uptime Kuma (its sign-in can be turned off only over its live connection with its password), PhotoPrism (free edition: people who sign in are guests, its admin isn't linked), Lychee (named providers only, none at our addresses), Ghost (a custom SSO adapter)
- [ ] Packages with env only: Vaultwarden, Miniflux, Linkwarden, PhotoPrism, Actual Budget, HedgeDoc; check each app's first admin is the installing Owner (Miniflux and Actual need it)
- [ ] Services on another server: the controller makes the client secret and sends it in the assignment; `AddHost` to the controller over WireGuard; Caddy lets the lab's servers reach the sign-in page
- [ ] Removing a service: delete its `service_access` rows (`internal/web/services.go`, left alone while the package editor changes it)
- [ ] Check on a lab with a real address: a real certificate, the sign-in page public (CrowdSec, AppSec, the login limit, its DNS record), and a backup and restore keeping the signing key (docs/ASSUMPTIONS.md V6)
- [ ] Later (SPEC §5.2): passkeys (`go-webauthn`, to agree), single sign-out (back-channel logout), signing-key rotation, choosing an app's admins per service

- K5: setup-code approach for first-run setup (add to SPEC?)
- roost.ac must go on the Public Suffix List before people get subdomains there (so browsers keep one lab's cookies away from another's); the request takes weeks, so file it early (SPEC §6.6)
