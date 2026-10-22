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
  - [ ] **Owner, before the first release:** the release key ceremony and its public key in `packaging/keys/roostack-release.asc` (R1); apply roostack-cloud's `ops/tofu/releases` and put its outputs in the GitHub `release` environment (roostack-cloud `docs/RUNBOOK-RELEASES.md`) (the names `dl.`/`apt.roostack.cloud` confirmed 2026-10-18; the bucket's still to confirm)
  - [ ] One acceptance of a new series for the whole lab (today each server's Updates page), and the order a controller and its nodes move in, once it's known what a lab API change between series needs
  - [ ] Pre-releases, if wanted: a suite of their own (e.g. `0.6-beta`) a server opts into; `build-apt-repo.sh` refuses them today
  - [ ] At the first release: fill in `scripts/install.sh`'s `@SUITE@` and `@KEY_SHA256@`, the help's `RELEASE-KEY-FINGERPRINT` and `SUITE` (help/docs/start/install.md), publish `install.sh` at `https://dl.roostack.cloud/install.sh` (R2's apt-publish publishes the keyring at `https://apt.roostack.cloud/roostack-archive-keyring.gpg`)
  - [ ] Roostack Cloud's pages could use the cloud scenes in `assets/errors` (Cloud's repository)
  - [ ] Later: HTTPS by IP address (Let's Encrypt IP certificates) for a cloud server's setup page (SPEC §6.3.2)
  - [ ] To discuss: serving the UI at `roost.<domain>` as an explicit choice instead of automatically, at home (SPEC §6.3.2 has the questions; on the internet it's decided)
  - [ ] **The release key's ceremony** (the Owner): [docs/RELEASING.md](docs/RELEASING.md), "The ceremony": the OpenPGP key and its signing subkey, the `release` environment (a required reviewer, tags `v*` only) with the secret `ROOSTACK_RELEASE_SIGNING_KEY`, the public key into `packaging/keys/roostack-release.asc`, the fingerprint published. Until then the release workflow refuses to run; it also needs the registry's ceremony (REGISTRY-KEYS.md)
  - [ ] Confirm the release names (placeholders): the file names, `dl.roostack.cloud/releases/<ver>/`, the key's user ID `Roostack releases <releases@roostack.cloud>`
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

- [ ] **The key ceremony** (the Owner, when ready): [docs/REGISTRY-KEYS.md](docs/REGISTRY-KEYS.md): root and signing keys, `keys/` committed to the registry, the root public key into `internal/registry/rootkeys.go`, GitHub Pages, the DNS record and the workflow's variables. Until then labs can't fetch the real registry (release builds refuse to be made)
- [ ] Publish release binaries (the Owner, 2026-10-14: soon): the registry's Publish workflow downloads a release's linux/amd64 `roostack`, pinned by SHA-256 (`ROOSTACK_BINARY_URL`, `ROOSTACK_BINARY_SHA256`). The release workflow makes it (2026-10-18): `https://dl.roostack.cloud/releases/<ver>/roostack-<ver>-linux-amd64`, its SHA-256 in that release's `SHA256SUMS` (docs/RELEASING.md); waits for the first release
- [ ] Offer versions other than the newest (an older one, to match another server or for a downgrade), fetching them on demand
- [ ] Code owners for community packages, when they open: each package's folder gets its maintainers from its `registry.yaml` (`registry.yaml` itself stays the registry maintainers'); a check in `registry index -check` that CODEOWNERS matches
- [ ] Website (roostack-cloud): `services.ts` licences use deprecated SPDX ids (`AGPL-3.0`); Immich's is `AGPL-3.0-only` (its notice names version 3 without "or later"). Moot once the facts come from the index
- [ ] Community packages: who maintains it and when it was last reviewed, and the tier text ("Roostack checked how this is set up; the app itself is made by …"), on the install and service pages (SPEC §8.3.4)
- [ ] Packages in the registry could add `about.releases` (roostack-services; needs `requires.roostack` of the release that knows the field)
- [ ] Updates: an update to a new image (the download first): still tried only with new versions of the same image
- [ ] Updates: offer a fix on the installed line when the newest version is a major the policy doesn't take (the fetcher keeps only the newest version)
- [ ] Updates: ask for a setting a new version requires (today such an update is blocked, with a sentence); `upgrade` hooks and migration steps (SPEC §8.1 Future)
- [ ] Update bot in the registry: hourly upstream checks, pull requests, CI that installs and health-checks (and backs up and restores) each update, auto-merge for official patch releases (SPEC §8.3.8)
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
- [ ] **Media libraries** (design locked 2026-10-19 with a mock-up, SPEC §8.1 "Media libraries"): libraries per server (drive folder, share, or a folder on the server), chosen per service, read-only and not backed up, `ro,rslave`; "read as owner" through idmapped mounts
  - [ ] The move page naming libraries the service reads that its new server lacks
- [ ] **Check ASSUMPTIONS I3 at the first MySQL or MariaDB pin change:** a service with real data, updated across the major version
- [ ] **A setting's `warning:`** (SPEC §8.1 "Configuring a service"): in pkgspec and validate, shown beside the option at install and in the service's settings, in the warning colour; then move HedgeDoc's Libravatar warning from its description into it (a new package revision)
- [ ] **Kan** waits on upstream and Roostack: its web image has no version tags since 0.2.1 (only `latest`/`main`), and its migrations run in a separate one-shot container (`kan-migrate`), which packages can't declare. Revisit when Kan tags its images; a one-shot container type ("init" containers run before the service starts) would help other apps too
- [ ] **Generated secrets in other formats**, if more Laravel apps come (Lychee's `APP_KEY` wants "base64:" and standard base64; its package converts Roostack's URL-safe value in a shell wrapper)
- [ ] **Publishing the new packages:** Papra, Linkwarden, Paperless-ngx, Mealie, Lychee, Ghost, PhotoPrism, Jellyfin, Navidrome, Plex and Vaultwarden 1.37.4-roostack.2 use `install_value`, `databases.<name>.url`, `.Lab.TimeZone`, colons in paths, `client_ip_header`, `mysql`, `mariadb` or media libraries, so they wait for a Roostack release with them (the registry's Publish workflow checks with a pinned release)
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

- [ ] The Owner decides the rest of the review's §12 questions and agrees the plan (§11). All decided on 2026-10-22 (§12, "Decided")
- [ ] CLAUDE.md's writing rules: the Owner's tense rule (2026-10-22), in batch 1e
- [x] Wave 0: the behaviour fixes found in passing (§2), all approved by the Owner on 2026-10-22 (P3 with its agreed wording, P9 in UTC); each finding's status, with its commit, is in the review
  - [x] Roostack's: P3–P7, P12, P13, P15, P16 (2026-10-22)
  - [x] Cloud's: P1, P2, P8–P11, P14 (2026-10-22, roostack-cloud branch `claude/wave0-fixes-177bbd`, not merged yet)
  - [ ] P3's follow-up: check a failed move through a backup on two VMs (only fakes so far)
- [ ] Wave 1: the parallel batches 1a–1j (§11)
- [ ] Wave 2: the shared helpers, one at a time (§11)
  - [ ] P5's follow-up: one parser for the `# Added by Roostack:` blocks, with typed tag constructors (the edits share `editFstab` since wave 0, and `/etc/fstab` is replaced in one step since 2026-10-22, SPEC §6.1 "Editing `/etc/fstab`")
  - [ ] D1: the atomic writes of files that aren't private (0644: generated files, Caddy's, the registry's, `labnet`'s certificates) still have their own copies; switch them to `fsutil.WriteFile`, and add `fsutil.CopyFile` for the streamed copies (`labrestore.copyFile`, `node/importcopy.go`). Wave 0 (P7) moved every private (0600) and 0640 file to `fsutil` (2026-10-22)
- [ ] Wave 3: the structural changes the Owner chooses (§11)
- [ ] Wave 4 (the Owner, 2026-10-22): wording across both repositories. Rework the present simple for actions and short, clipped statements into natural sentences (the future for what will happen, the present continuous for what's happening). A sample of rewrites for the Owner first, then batches by area (§11)

## To discuss with the user

Nothing at the moment.

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
