# Raspberry Pi support: implementation plan

Written 2026-09-30 for whoever picks this up with a Raspberry Pi to hand. It
turns SPEC.md §3 "Later: ARM64 (Raspberry Pi, ARM VPS)" into work. Read
[CLAUDE.md](CLAUDE.md) first: its rules (docs in the same commit, verify on
real hardware, never log secrets, ask before adding dependencies) apply here
too.

When this plan is done, its decisions move into SPEC.md (§3, §3.1, §3.2), its
work into TASKS.md as a phase, and what was verified into
docs/ASSUMPTIONS.md. Then delete this file.

## Where things stand

Checked on 2026-09-30:

- **The code is portable.** Roostack is pure Go with `CGO_ENABLED=0`; the
  SQLite driver (`modernc.org/sqlite`) is pure Go too. `GOARCH=arm64 make
  build` should just work. The one byte-order assumption
  ([firewall.go:135](internal/firewall/firewall.go)) holds on arm64.
- **Every pinned image has an arm64 build.** Each image is pinned by its
  multi-arch index digest (SPEC §8.1), so the same pin pulls the right build
  on ARM. Checked against the registries:

  | Image | Where it's pinned | arm64 |
  |---|---|---|
  | caddy builder and runtime | build/caddy/Containerfile | ✅ |
  | crowdsec | internal/generate/system.go | ✅ |
  | rest-server | internal/generate/storage.go | ✅ |
  | postgres, valkey | internal/generate/defaults.go | ✅ |
  | vaultwarden, forgejo | docs/examples/*.yaml | ✅ |
  | immich server, machine learning, postgres | docs/examples/immich.yaml | ✅ |

- **What assumes x86:** the package (`scripts/build-deb.sh`,
  `packaging/deb/control`: `Architecture: amd64`), the dev VM script
  (`scripts/dev-vm.sh`: amd64 cloud image, `qemu-system-x86_64`,
  `linux-image-amd64`), the install help (`help/docs/start/install.md`), and
  the README's requirements.
- **What assumes Debian's own images on a PC:** automatic updates only take
  Debian's origins (`packaging/apt/52roostack-unattended-upgrades`); the host
  check flags any package source that isn't `*.debian.org`
  ([hostcheck.go](internal/agent/hostcheck.go), `debianHost`); drive
  detection knows `/boot/efi` but not `/boot/firmware`
  ([drives.go:65](internal/agent/drives.go), `systemMounts`).

## Decisions to confirm with Tyris before starting

Each has a recommendation; the rest of the plan assumes it.

1. **Operating system: Raspberry Pi OS Lite (64-bit), the Debian 13
   "trixie" release.** It's what Pi owners have, it supports the Pi 5, and
   Raspberry Pi Imager sets up the user, SSH and Wi-Fi before first boot.
   Rejected: Debian's own Raspberry Pi images (raspi.debian.net), which
   support the Pi 5 poorly and need more steps. Raspberry Pi OS becomes a
   second "Fully supported" row in SPEC §3.1, with its differences behind
   the distro-specific seam that section already asks for.
2. **Supported Pis: Pi 4 and Pi 5, 4 GB or more, running from an SSD** (USB,
   or NVMe on the Pi 5). An SD card works but gets a warning. 8 GB
   recommended for Immich. Pi 3 and Zero 2 W: unsupported (1 GB or less).
3. **The Pi 5's 16 KB memory pages** (see Phase 2.4): recommend Roostack
   *offers* the fix with a button (the agent edits `config.txt`, then the
   Pi restarts at a time the Owner picks), rather than only explaining it.
   Same for turning on memory accounting (Phase 2.3) if it's needed.
4. **32-bit Raspberry Pi OS: not supported.** Setup refuses with a clear
   message. Several images have no 32-bit ARM build (Immich), and Podman on
   32-bit is little tested.

## How to test

- **A real Pi is required** for Phases 2 and 3: nothing Pi-specific shows up
  in emulation. Ideally a Pi 5 (16 KB pages, NVMe, RTC) and a Pi 4
  (the older kernel defaults), each on an SSD, plus one spare SD card
  install to test the warning.
- **For Phase 1 an ARM64 Debian 13 VM is enough**, either:
  - emulated locally: `qemu-system-aarch64` with `-M virt` and UEFI firmware
    (Arch: `qemu-system-aarch64`, `edk2-aarch64`; Debian:
    `qemu-system-arm`, `qemu-efi-aarch64`). These are new system packages
    for the dev machine, so check with Tyris first. Slow (no KVM on x86) but
    fine for installing and running the suite; or
  - an ARM cloud server (e.g. Hetzner CAX11), which also covers the "ARM
    VPS" target. Costs money: ask Tyris.
- Treat the Pi like a dev VM: deploy with `scp` and `install` as in
  CLAUDE.md, and record every check in docs/ASSUMPTIONS.md with ✅ and the
  date, on which model and OS image.
- Run the full test suite with restic on arm64 too (copy the Pi's
  `/usr/bin/restic`, as CLAUDE.md describes), at least once per phase.

## Phase 1: ARM64 builds and packages (no Pi needed)

Goal: `apt install ./roostack_<version>_arm64.deb` on an ARM64 Debian 13
server gives a working lab, the same as on x86.

1.1 **Build for either architecture.**
- Makefile: take `GOARCH` from the environment (`GOARCH=arm64 make build`)
  and write to `bin/` as now, or to `bin/linux_arm64/` so both can sit side
  by side. Pick one; the second is kinder to anyone building both.
- `make build` also builds the help site, which is architecture-independent:
  no change.

1.2 **Package for either architecture.**
- `scripts/build-deb.sh`: take the architecture (from `GOARCH`, default
  `amd64`), check the binaries match it (`file bin/roostack` or read the ELF
  header with `go version -m`), and name the output
  `dist/roostack_<version>_<arch>.deb`. Update the header comment.
- `packaging/deb/control`: `Architecture: @ARCH@`, filled in by the script
  like `@VERSION@`.
- `scripts/dev-install.sh`: pick the .deb for `dpkg --print-architecture`.
- Check every `Depends:` package exists for arm64 in Debian 13 (they should:
  podman, passt, nftables, uidmap, crun, catatonit, dbus-user-session,
  restic, unattended-upgrades, wireguard-tools).

1.3 **Guard the images.**
- Add a test (e.g. `internal/generate/images_test.go`, skipped without
  network like the restic tests, or a script `scripts/check-images.sh` run
  before release) that reads every pinned image (the Go constants, the
  Containerfile's `FROM` lines and the catalog's `image:` lines) and fails if
  its index lacks `linux/amd64` or `linux/arm64`. The check used for this
  plan queried each registry's `/v2/<repo>/manifests/<digest>` with the
  index media types; no new dependency needed (net/http and a token
  request per registry: Docker Hub, ghcr.io, codeberg.org).
- A package whose image has no build for a server's architecture must fail
  with a sentence the Owner understands ("Immich has no version for this
  server's kind of processor (arm64)"), not Podman's "no image found in
  manifest list". Find where image pulls report errors (reconcile and the
  node's apply log) and translate that one error.

1.4 **The Caddy image builds on ARM.** It's built on each server from the
Containerfile, so this just needs checking: time the build on the ARM VM
(and later on each Pi), and note it in ASSUMPTIONS. If it takes more than a
few minutes or runs out of memory on a 4 GB Pi, raise it with Tyris: the
alternative is publishing a prebuilt multi-arch Roostack Caddy image, which
is a bigger decision (a registry to run, signing).

1.5 **Dev VM on ARM.** Optional, but it makes Phase 1 repeatable:
`ROOSTACK_VM_ARCH=arm64 scripts/dev-vm.sh up` uses
`debian-13-genericcloud-arm64.qcow2`, `qemu-system-aarch64 -M virt -cpu max`
with the UEFI firmware, and `linux-image-arm64` in cloud-init. Only if Tyris
agrees to the new system packages.

1.6 **Verify end to end on ARM64:** install the .deb, run the wizard
(domain, backups to a drive and to B2, recovery key), install Vaultwarden,
Forgejo and Immich, make one public (SPEC §6.11) and check CrowdSec, run
OS updates, back up and restore a service. Record the results.

1.7 **Docs:** SPEC §3 (ARM64 moves from "Later" to supported, dated), the
README requirements, `help/docs/start/install.md` (which package to
download), ARCHITECTURE (the build), TASKS.

## Phase 2: Raspberry Pi OS

Goal: a Pi 4 or 5 running Raspberry Pi OS Lite 64-bit behaves like the
reference NUC. Each item starts with checking on the Pi, because several of
these are known from older Raspberry Pi OS releases and may have changed.

2.1 **Recognise the OS.**
- Find how Roostack identifies its distro today (grep `os-release`) and
  add Raspberry Pi OS (`/etc/os-release` `ID=debian` with
  `/etc/rpi-issue` present, or the `raspberrypi-sys-mods` package). Put it
  behind the distro seam SPEC §3.1 describes, so package sources and update
  origins come from one place.
- Record the model (`/proc/device-tree/model`, e.g. "Raspberry Pi 5 Model B
  Rev 1.0") for the server page.

2.2 **Updates** (SPEC §3.2).
- Raspberry Pi OS's kernel and firmware come from `archive.raspberrypi.com`.
  Add its origin to the unattended-upgrades file Roostack installs: check
  the exact origin and label with `apt-cache policy` on the Pi (probably
  `origin=Raspberry Pi Foundation,codename=${distro_codename},label=Raspberry Pi Foundation`).
  Install that line only on Raspberry Pi OS (postinst writes it, or a second
  file), since on plain Debian it would be meaningless.
- Check that a kernel update creates `/run/reboot-required` (and `.pkgs`)
  as Debian's kernel packages do; `osupdate` relies on it to schedule the
  reboot. If it doesn't, find what does signal it (the new kernel in
  `/boot/firmware` vs `uname -r`) and add that for Raspberry Pi OS.
- `Remove-Unused-Kernel-Packages` and `/boot/firmware` space: check old
  kernels don't fill the (small, FAT) boot partition.
- Host check: `archive.raspberrypi.com` must not be reported as "software
  from outside Debian" on Raspberry Pi OS. Extend `debianHost` (or its
  replacement behind the distro seam) with the distro's own sources, with a
  test.
- Do a real update and reboot through Roostack's maintenance window on the
  Pi.

2.3 **Memory accounting (cgroups).**
- Older Raspberry Pi kernels turned off the memory controller by default
  (users added `cgroup_enable=memory` to `cmdline.txt`). Check on trixie:
  `cat /sys/fs/cgroup/cgroup.controllers` and, for the roostack user's
  delegated tree,
  `/sys/fs/cgroup/user.slice/user-999.slice/user@999.service/cgroup.controllers`
  (uid from `id -u roostack`).
- Roostack doesn't set memory limits today, but Podman uses the controller
  for `podman stats` and OOM handling, and packages may set limits later.
  If it's missing: the agent offers the fix (Decision 3). `cmdline.txt` is a
  single line: append the words, keep one line, keep a backup
  (`cmdline.txt.roostack-backup`), then restart at a time the Owner picks.

2.4 **The Pi 5's 16 KB memory pages.**
- Raspberry Pi OS boots the Pi 5 with a 16 KB page kernel (`kernel_2712.img`).
  Software built with jemalloc for 4 KB pages refuses to start ("Unsupported
  system page size"); Valkey (Immich's Redis) is the likely victim, and
  maybe Immich's machine learning. Check each catalog package and built-in
  image on a Pi 5 as shipped, and record which fail.
- If any fail: Roostack checks `os.Getpagesize()` (Go standard library;
  the daemon can call it itself) and offers the fix: `kernel=kernel8.img`
  in `/boot/firmware/config.txt` under `[pi5]`, done by the agent with a
  backup of the file, then a restart. Say what it changes and why
  (a slightly slower kernel so all services can run).
- Setup and the catalog both show the problem until it's fixed, so nobody
  installs a service that can't start.

2.5 **Drives and the boot partition.**
- Add `/boot/firmware` to `systemMounts` in
  [drives.go](internal/agent/drives.go), with a test fixture from a Pi's
  `lsblk --json --bytes -o …` (the exact columns the agent asks for).
- Skip `mmcblk*boot0` and `mmcblk*boot1` (eMMC boot areas on Compute
  Modules) and read-only devices (`ro: true`).
- A Pi on an SSD with its SD card still inserted: the SD card isn't the
  system disk, so it would be offered for backups (and for erasing).
  Check what happens and decide with Tyris whether an SD card slot
  (`mmcblk`) is ever offered. Recommend: offered, named "SD card", never
  pre-selected.
- zram swap (`rpi-swap` in trixie) is already skipped.

2.6 **The clock.**
- Pis have no battery-backed clock (the Pi 5 has one, but only with a
  battery fitted); after power loss the time is wrong until the network
  time arrives (Raspberry Pi OS restores the last saved time with
  `fake-hwclock`, so it's old, not 1970). 2FA codes, certificate checks,
  backup times, snapshot pruning and the maintenance window all depend on
  it.
- Check what Raspberry Pi OS uses (`systemd-timesyncd`?) and how to read
  "synchronised" from inside the daemon's sandbox (the file
  `/run/systemd/timesync/synchronized` exists once timesyncd has synced; or
  ask the agent to run `timedatectl show -p NTPSynchronized`).
- Then: the maintenance coordinator doesn't start scheduled jobs (backups,
  updates, pruning, certificate renewal is Caddy's own) until the clock is
  set; the login page and Home show a notice while it isn't ("This server
  hasn't set its clock yet. Codes from your authenticator app may be
  refused until it has."). Applies to every server, not only Pis. Check
  that Caddy and restic behave when started early.

2.7 **Networking.**
- Raspberry Pi OS uses NetworkManager and is often on Wi-Fi. Check the home
  network address Roostack picks (for DNS records, the SSH "home network"
  choice, public access) is right on Wi-Fi only, Ethernet only, and both.
- Check pasta, the firewall (nftables) and WireGuard (a lab with the Pi as a
  node of the NUC, and the other way round).
- SSH: Imager sets up password login by default. Check the wizard's
  remote-access step (`setup-ssh.html`) still makes sense there, and that
  "Only from my home network" doesn't lock out someone on Wi-Fi.

2.8 **Install guide:** a new page `help/docs/start/install-pi.md` (added to
`help/zensical.toml`): what to buy (Pi 4/5, 4 GB+, SSD, the official power
supply), Raspberry Pi Imager with Raspberry Pi OS Lite 64-bit and its
settings (hostname, user, SSH, Wi-Fi only if needed), booting from the SSD,
copying and installing the .deb, then the wizard. Link it from
`install.md`. British English, short sentences, a "Tell me more" level of
detail (CLAUDE.md "Writing").

## Phase 3: Hardware checks

Goal: the problems that make Pis unreliable are shown before they cause a
mysterious failure. Show them in the wizard (as warnings, not blockers,
except where noted) and on the server's page.

3.1 **A host-info call.** The server page has nowhere to show hardware
today. Add `HostInfo` (architecture, model, memory, page size, system disk
kind, clock synchronised, temperature, power warnings) read by the node:
the daemon reads what it can itself (`/proc/meminfo`, `/proc/device-tree/model`,
`runtime.GOARCH`, page size); anything needing root goes through a small
agent call that validates nothing it isn't given. Remote nodes return it
over the lab API (`node.Node` gets a method; `Remote` and `Local` both
implement it; fakes updated). Check each file is readable inside the unit's
full sandbox (`ProtectKernelTunables=yes` makes /sys read-only, which is
enough).

3.2 **The warnings:**

| Check | How | Message (draft) |
|---|---|---|
| System on an SD card | system disk is `mmcblk*` and not eMMC | "This server runs from an SD card. SD cards are slow and wear out; an SSD is much more reliable." Link to the guide. |
| Low memory | `MemTotal` < 4 GB | "This server has 2 GB of memory. Some services may not fit." |
| 16 KB pages | Phase 2.4 | with the fix button |
| Memory accounting off | Phase 2.3 | with the fix button |
| Low power now or since boot | `/sys/devices/platform/soc/soc:firmware/raspberrypi-hwmon/hwmon*/in0_lcrit_alarm`, or `vcgencmd get_throttled` through the agent; check which exists on trixie | "The power supply isn't giving this Pi enough power. Use the official power supply; USB drives may need a powered hub." |
| Too hot | `/sys/class/thermal/thermal_zone0/temp` above ~80 °C, and the throttling bits | "This Pi is slowing down because it's too hot. It needs a case with a fan or a heatsink." |
| Clock not set | Phase 2.6 | |
| 32-bit OS | `dpkg --print-architecture` is `armhf` | Blocks the install (postinst refuses) with the reason. |

- These go on the server's page in Settings → Servers, and the serious ones
  (low power, too hot, clock) on Home as a notice. Use existing components
  (`notice`, `status`); a new one goes in the gallery.

3.3 **Memory needs per package.** Add an optional `resources.memory`
(e.g. `2GB`) to `pkgspec` (validated, documented in
`help/docs/reference/packages.md`), set it for Immich, and have the catalog
warn when a server has less memory than a package needs plus what's
already installed (a rough estimate, and say so). Warn, don't block.

## Phase 4: Labs mixing PCs and Pis

Goal: a lab with a NUC and a Pi (as in SPEC §10) places and moves services
safely.

4.1 **Placement.** The install form's server choice and moving a service
only offer servers whose architecture every image of the package has. The
node reports its architecture (Phase 3.1). Where the architectures come
from: either the catalog records them per image (written by the Phase 1.3
check), or Roostack reads the index at install time. Recommend the
catalog: it works offline.

4.2 **Moving services and restoring across architectures.** Services move
as raw files (SPEC §10.10), including the Postgres data folder. Postgres
doesn't promise its data files work on another architecture, though amd64
and arm64 are both 64-bit little-endian and usually agree. Until it's
tested:
- test a move of Forgejo and Immich (both with Postgres) NUC → Pi and back,
  and a full lab restore (internal/labrestore) onto the other architecture;
- if they work, record it in ASSUMPTIONS and allow it;
- if not, a move between architectures dumps and reloads the database
  (`pg_dump`/`pg_restore` in the database container) instead of copying its
  folder, which is a bigger change to take to Tyris first. Until then,
  block such moves with a clear message.

4.3 **Help:** a short section in the servers help on mixing PCs and Pis.

## Out of scope

- Other ARM boards and ARM cloud servers beyond checking Phase 1 runs on a
  generic ARM64 VM (they'd get "Unsupported, may work" per SPEC §3.1).
- Pi-specific hardware beyond storage, power and heat (cameras, GPIO, the
  Hailo AI kit for Immich).
- A Roostack image for Raspberry Pi Imager (a pre-built SD/SSD image with
  Roostack installed). Worth discussing later: it would remove the whole
  install step for Pi owners.
- Ubuntu on the Pi.

## Suggested order and size

1. Phase 1 (a few days with an ARM VM), reviewed and merged on its own.
2. Phase 2.1–2.5 on a Pi 5, then a Pi 4 (a week).
3. Phase 2.6–2.8 and Phase 3 (a week), with a mock-up of the server page's
   hardware section shown to Tyris before building it (CLAUDE.md: mock-ups
   before a big UI change).
4. Phase 4 once there's a lab with both (a few days, more if databases need
   dump and reload).

Commit each finished, tested piece with its docs, as CLAUDE.md describes.
