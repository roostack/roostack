# What hasn't been tried for real (review, 2026-10-27)

A list for the maintainer to review before Roostack marks options "untested" in its pages. Nothing has been labelled or changed yet.

"Tried for real" means used against the real thing on a dev VM or a real account, as recorded in `docs/ASSUMPTIONS.md` (cited by its IDs, such as N8), `TASKS-DONE.md`, `SPEC.md`, or a package's commit. No Go test runs against a live service: every provider, S3, and Cloud test uses a fake (`dnstest`, `b2test`, `s3fake`, `cloudtest`), so a passing test suite says nothing about these.

Since 2026-10-13 the dev VMs have run on an Apple Silicon Mac, so almost everything newer has only run on **arm64**. There's no physical hardware in the testing at all (W8).

## 1. Offered to people now, never tried for real

These are candidates for the label.

### Certificates and DNS
- **Six of the seven DNS providers:** Amazon Route 53, Porkbun, Gandi, GoDaddy, Namecheap, and Spaceship. Only Cloudflare has issued a real certificate (D1) and managed real records (L14, P8). The others have only been tested against fakes, and Caddy's plugins for them only had their versions checked (D2). If one fails, the lab gets no HTTPS at all.
  - **Namecheap is the riskiest.** Its API rewrites the whole zone to change one record, so a mistake would wipe the person's DNS. It's behind an opt-in (`allow_risky_records`, M7).
- **Cloudflare's credentials check** was rewritten on 2026-10-23 (D9) and hasn't been tried since.
- **Deleting records** (any provider, Cloudflare included): fakes only.
- **The relay with your own domain** at any provider but Cloudflare: fakes only.

### Backups
- **S3 providers people add themselves:** Wasabi, AWS, MinIO, and Cloudflare R2 (including R2's `auto` region). The check that a bucket is safe from ransomware, and rolling back on a versioned bucket, have only been tested against a fake (N8). Backblaze B2 is the only one tried for real (N2–N9), and on amd64 only.
- **Drives that aren't ext4:** exFAT, FAT, NTFS, XFS, Btrfs, and F2FS are all offered when a drive is used as it is, but none has been tried (M2).
- **A network share** (an already-mounted NAS) as the place for backups: never run (S7).
- **A second internal disk:** the same code as a USB drive, but there's no record of one being tried.

### Restoring a whole lab
- **From Roostack Cloud:** it found the backups, then stopped before restoring (TASKS-DONE, 2026-10-13).
- **From an S3 or B2 bucket**, or **from a restic address:** never run to the end. A whole-lab restore has only been done from a folder and from a USB drive (O13).

### Making a service public
- **Port forwarding through a real router**, which is the default way in: only tried with a simulated outside visitor (P8).
- **Google as the fallback for finding the home's public address:** never used. OpenDNS, the first choice, works (P8).
- **CrowdSec with signal sharing on**, the default once a service is public: every test ran with it off (P5, P8).
- **The edge's own listener** (Q12): network namespaces only, not a real edge.

### Email
- **SMTP providers other than Fastmail**, such as Gmail and Outlook. It isn't recorded whether Fastmail was tried on port 587 (STARTTLS) or 465 (TLS), so one of the two modes has never been tried with a real provider.
- **HTML emails in real mail clients:** never looked at.

### Hardware and servers
- **amd64 for anything built since 2026-10-13:** Immich, photo migration, and `verify-release`. No current package version has run on amd64.
- **Intel GPU passthrough** (`devices: gpu: intel`) in Immich, Jellyfin, and Plex, with the setting on `auto` by default: never tried (A7, G1).
- **Moving or restoring PostgreSQL data between amd64 and arm64:** never tried, and **not blocked**. `checkArch` only checks that an image exists for the processor.
- **Cloud servers other than Linode** (Hetzner, AWS, DigitalOcean, Oracle, and OVH are named in the help), and a provider whose network looks like home (AWS, Oracle). The full check of a lab on a Linode is still open (TASKS: "Verify on a real cloud server").

### Moving and importing
- **Moving a service through a backup** rather than directly: unit tests with real restic only, with no VM run.
- **Imports:** from an SMB or NFS share, from MariaDB or MySQL dumps, and onto a server that isn't the controller (W12).
- **Photo migration:** from "A folder of photos", which has unit tests only; with real photos, since every Takeout was synthetic (X1–X15); and with real videos, HEIC, and Live Photos (X10).

### Labs of several servers
- **Join-code addresses** at `link.<domain>` or the public address: built, but no VM run.
- **"Notify only" for restarts after updates:** no specific run found.

## 2. Services: what's been tried for each

Every one of the registry's 23 packages has been installed on a dev VM, with its data backed up, deleted, and restored. Stirling-PDF is the exception: only its settings file was restored. The gaps cut across them:

- **Updates across real versions:** never. The update machinery has only moved Vaultwarden between test versions of the same image (1.34.3 → 1.34.5), plus an ntfy test revision and a rollback (F2, F3).
- **Single sign-on with a real certificate or a public sign-in page:** never. Every SSO test trusted Caddy's own CA by hand (V6).
  - The Owner's own login was refused, or asked to finish signing up, in Papra, Linkwarden, Miniflux, and Paperless-ngx (V10, V14, V16). Worth deciding whether those count as working.
  - Ghost, PhotoPrism, Lychee, and Uptime Kuma have no single sign-on yet.
- **Made public:** only Vaultwarden (through every way in) and ntfy. Immich, HedgeDoc, Wiki.js, Papra, and Linkwarden say plainly "not tried: public access", and the rest have no record.
- **Whole-lab restore with a named service in it:** only Forgejo.
- **MySQL and MariaDB major-version upgrades** (Ghost, PhotoPrism): never tried (I3).
- **HedgeDoc can't be installed at the moment:** both published revisions point at an image quay.io deleted on 2026-10-21. The fix (1.12.0-roostack.3) is on the unpushed `hedgedoc-image` branch in roostack-services.
- **Revisions never installed as published:** Vaultwarden 1.37.4-roostack.2 and .3, and the storage-title revisions of Forgejo 15.0.9, Immich 2.1.0, and Vaultwarden 1.34.3.
- **Plex:** claiming a server (it needs a Plex account) was never tried.
- **Smart search on Immich 3.3.3:** never tried.
- **Navidrome:** never tried with real audio.

## 3. Not built yet, so no label needed

For completeness, since the help or the plans mention them:
- SFTP backups;
- backups and restores to and from a friend's lab, beyond storing for one;
- UPnP and NAT-PMP;
- detecting CGNAT, and Cloudflare Tunnel;
- checking from outside through Cloud;
- notification channels other than email (ntfy, webhooks);
- adding a network share from Roostack's pages;
- Raspberry Pi and Raspberry Pi OS;
- servers behind a hub that isn't the controller;
- single sign-on for a service on another server;
- the Debian 13 → 14 upgrade;
- Ubuntu;
- photo imports from Apple, Flickr, and others;
- DNS providers deSEC, DigitalOcean, Hetzner, OVH, and DuckDNS.

## 4. Questions for the maintainer

1. **What counts as tested?** Is a run on an arm64 VM enough, or does an option need amd64 (the reference NUC) too before it loses the label?
2. **Which level gets the label?** For example, a whole DNS provider, or only its record management (Namecheap could be "untested" for records, but every provider but Cloudflare is untested even for certificates).
3. **Does a single sign-on with a refused Owner login count as working** (Papra, Linkwarden, Miniflux, Paperless-ngx)?
4. **Should the riskiest options be hidden rather than labelled until tried?** The candidates: Namecheap's record management, moving PostgreSQL data between processors, and the GPU setting's `auto` default.

## Doc mismatches found on the way

- SPEC.md says the public-address fallback is Cloudflare's `whoami`, but the code uses Google (`internal/public/public.go`).
- TASKS.md says the Caddy image is "not built", but it's built on every VM (R4, A10).
