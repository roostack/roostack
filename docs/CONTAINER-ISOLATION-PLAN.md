# Keeping services away from Roostack's own user (plan, 2026-11-19)

A design for the maintainer to decide on. Item 2 of the list on 2026-11-19 ("the next big ticket items").

**Prototype, then test, then decide** (the maintainer, 2026-11-19: high risk, so nothing is built until a prototype has been tried). A throwaway prototype ran on two VMs on 2026-11-21: [what it showed](#prototype-results-2026-11-21) is below, with [how many user IDs are needed and why](#how-many-user-ids-and-why). Nothing is built; the prototype is on the branch `proto/container-slots`, which won't be merged.

## The problem

Every service runs in rootless Podman under the `roostack` user, the same user as the daemon:

- **A container's root is `roostack` on the host** (docs/ASSUMPTIONS.md B1; SPEC.md §6.7, "Written safely"). Podman's rootless mapping puts container uid 0 on the user's own uid (999 on the dev VMs), and container uid *n* on `subuid_start + n − 1`.
- **Every service shares that one mapping.** Service A's Postgres (uid 70) and service B's Postgres are the same host uid (`231141` on VM3, B1), and both services' root is 999.

So a process that gets out of its container, whether through a kernel bug, a runtime CVE (such as runc's `/proc/self/exe` in 2019), or a leaked file descriptor, would be uid 999 in the host's namespaces. That's the user that holds:

- `/var/lib/roostack/roostack.db`: every person's password hash and two-factor secret, the single sign-on signing key, the lab's keys, backup credentials, and Cloud's token;
- every service's data under `/srv/roostack/services`, and every service's Podman secrets (Podman's file driver, in the user's store);
- the agent's socket (mode 0660, group `roostack`). Since S6, the agent refuses callers from another user namespace, but a process that has *left* its namespace is in the host's and would pass;
- the user's systemd manager, so it could start containers with any mount the user can reach.

Even without an escape, the uid that writes a service's files is the daemon's. That's why config files go through `os.Root` (SPEC.md §6.7): an app could otherwise plant a symlink and have Roostack overwrite a Quadlet file.

What we want: **a service's processes, inside or out of their container, are never the daemon's user, and never another service's.**

## Options

### Option A: a fixed range of IDs for each service (recommended)

Each service gets its own **slot**: a block of 65,536 IDs, the same on every server in the lab. Its containers run with `UIDMap=0:<base>:65536` and `GIDMap=0:<base>:65536` (Quadlet's own keys; in rootless Podman these are *intermediate* IDs, numbered along the user's subuid ranges). `base` is `1 + slot × 65536`. Intermediate ID 0, the `roostack` user, is never mapped into a container.

- Container root becomes a subuid that owns only that service's files. An escaped process can't open the database (0600, `roostack`), can't connect to the agent (not in the group), and can't read another service's files where their modes keep others out.
- Each service's Podman secrets are already only readable by `roostack`, so an escaped process can't read them.
- Roostack itself still reaches everything as it does now, through `podman unshare`, where it's root over the whole range (backups, moves, imports, deleting data, sizes).

**Why not `UserNS=auto`?** Podman picks the range at each container's creation, and Quadlet recreates containers at every start. The documentation shows two runs getting different ranges and promises no stability, so the files' owners would stop matching after a restart, unless every start chowned every file. A fixed map is predictable, and a technical user can be told which numbers belong to which service. Podman's `idmap` mount option would have fixed that, but it's rootful only: "The Linux kernel does not allow the use of idmapped file systems for unprivileged users" (podman-run(1)).

**Why the same slot on every server?** Moves (tar inside `podman unshare`) and backups (restic inside `podman unshare`) keep the intermediate IDs. With a lab-wide slot, a service's files mean the same on any server, with no shifting. The controller hands out slots (lowest free), keeps them in the service's desired state, and doesn't reuse one while the removed service's data or kept copies still exist.

**What it doesn't protect against:**

- A kernel bug that ends in real root.
- Roostack's own daemon being taken over (a web bug), which already holds everything.
- A container's network reach: what it can connect to on the host or the lab's network. That's a separate question for the security review; it's worth checking whether pasta lets a container reach Roostack's listeners on the host.
- A package of the Owner's own that mounts something the policy allows.
- Files a service has made world-readable, which another service's escaped process could read if it can get to the folder (folder modes, I4).

### Option B: services under a second host user

The daemon stays `roostack`, and the containers move to a new user, `roostack-services`, with its own systemd manager and Podman store.

- **Protects:** the database, the lab's keys and the agent's socket, even from container root without an escape.
- **Doesn't protect:** services from each other. They still share one mapping, and every service's Podman secrets are in that user's store.
- **The cost is the architecture.** The daemon runs podman, `systemctl --user`, `journalctl --user`, `podman exec` hooks, health checks, unit status, and `podman unshare` (backups, moves, imports, sizes) through `userLaunch`, 11 call sites. Another user's manager and store can't be driven without root. So either:
  - **the agent does it**, which goes against "keep it small": every container operation would cross the root boundary;
  - **or a new third process does it**, running as `roostack-services` with a narrow socket API. But an escaped container *is* that process's user. It would take over the runner, see whatever passes through it (restic's repository password, generated secrets, the backup stream), and could lie in its answers to the daemon. The daemon would have to treat every answer as untrusted.
- **Backups get awkward.** restic needs the service's files (the services' user) and the repository key (the daemon's). Running restic on the services' side hands the key to the user an escaped container would be. `/proc/<pid>/environ` is readable by the same uid, even with Yama's ptrace scope 1, so the key could only travel on stdin.

### Option C: both

This is the most protection: the daemon's user isn't the services' user, and each service has its own slot. It costs B's architecture plus all of A's work. It only adds over A what A already gets from the folder modes and the socket group, plus protection against a container's own root without an escape, which A gives too, since container root isn't `roostack`. **C adds little over A for twice the work.**

### Option 0: harden what's there

`NoNewPrivileges`, fewer capabilities, a read-only root filesystem where an app allows it, and Podman's default seccomp (on already). Each of these makes an escape less likely, but none changes who an escaped process is. It's worth doing as well (the security review can list which), not instead.

## Option A, piece by piece

| Area | What changes | Risk |
|---|---|---|
| **Generator** | Every container gets `UIDMap=`/`GIDMap=` for its service's slot. `keep-id` goes away, along with B3c's `keepIDUser` translation. A package's `UserNS=keep-id:uid=U,gid=G` becomes `User=U:G` inside its slot. The policy (`internal/pkgspec/policy.go`) keeps refusing `UIDMap`/`UserNS` in packages; only Roostack writes them. | Low. |
| **The user's subuid range** | `useradd --add-subids-for-system` gives 65,536 IDs (A9), enough for one slot. The installer needs a range for, say, 1,024 slots (67 million IDs, from a 4-billion space). Fresh install: `useradd -K SUB_UID_COUNT=… -K SUB_GID_COUNT=…` (I1). Existing server: `usermod --add-subuids`/`--add-subgids` with a free range after the current one, then `podman system migrate` (I2). | Medium: overlapping someone's own range must be impossible. Shadow picks free ranges for `--add-subids-for-system`, but `usermod` takes what it's given, so Roostack must find a free one. |
| **Making a storage folder** | Today Roostack makes it as `roostack` and `:U` hands it to the container's user at every start. Under A, Roostack makes it inside `podman unshare` and chowns it to the slot's root (or the package's `User=`). `:U` can stay at first, with its known cost (B3c), then become a one-off chown at creation, restore, move, and import, which would remove the 69–81 s scan on a slow disk. | Low; the speed-up is a later step. |
| **Config files** (`internal/reconcile/config.go`) | They're written and read as `roostack` (intermediate 0). Under A, that user isn't in the slot, so a folder owned by the slot's root (0755) can't be written. The writer moves into a helper under `podman unshare` (as `backup-helper sqlite-copy` does), still through `os.Root`, with an `fchownat(…, AT_SYMLINK_NOFOLLOW)` to the slot's ID. The daemon reads them back through the same helper. | **Medium.** The helper is root over *every* service's files, so its symlink defence must be airtight: one `os.Root` per call, no path from the app followed, and tests with planted links. Today a mistake there reaches `roostack`'s files; under A, it reaches every service's. |
| **Backups and restores** | Already inside `podman unshare` (restic, `sqlite-copy`, `sqlite-restore`), so IDs are kept as intermediate IDs and a backup restores into the same slot unchanged. **A backup from before A** has container root as 0 and container uid *n* as *n*. Restoring it into a slot shifts every owner by `base`, during the restore, inside `podman unshare`. | Medium: one shift function, used everywhere (below), and a large library takes a while (one `fchownat` per file: roughly as long as `:U`'s first start, B3c). |
| **Whole-lab restore** | Slots are in the lab's desired state, which the server's own backup holds, so a restored lab gets them back. Older lab backups (no slots) get slots handed out and their services shifted. | Medium: this is the path the maintainer's planned Linode restore would take (item 4). |
| **Moves between servers** | The tar runs inside `podman unshare` on both ends (`internal/node/move.go`), so a lab-wide slot needs no shift. During an upgrade, when one end has A and the other doesn't, the receiving end shifts. | Low with the shift function. |
| **Imports** (`internal/node/import.go`, `importcopy.go`) | Data owned by another user is copied in and handed over. Under A, the handover chowns into the slot instead of to 0 or the package's user. | Low. |
| **Databases** | Each is a container in its service, so it's in the slot. Dumps and loads run through `podman exec`, unchanged. | Low. |
| **Devices** (Intel GPU: `AddDevice=/dev/dri`, `GroupAdd=keep-groups`) | `/dev/dri/renderD128` is group `render`. The docs' own example maps a host group into the container with `GIDMap=+g<id>:@<host gid>` alongside `keep-groups`. Under A, a GPU container would get `render` that way, with no `roostack` uid (I5). | **Medium:** never tried; `keep-groups` is crun only (Debian's default). |
| **Media libraries** | Today, libraries are read as `roostack` (keep-id), and "read as owner" is an agent-made idmapped mount showing the owner's files as `roostack`'s (SPEC.md §8.1). Under A, each service reads as its own subuid. World-readable files still work. "Read as owner" would become an idmapped mount **per service view** (`config.LibraryView(svc)` already exists): the agent, which is root and can make idmapped mounts, maps the owner to that service's app user in its slot. | **High:** this is the change most likely to stop someone's media from playing. Per-service idmapped mounts under `rslave` propagation need a VM check (I6). |
| **A service's folder as a library** (`shared: true`, Deluge's downloads for Jellyfin) | The reader sees the writer's files under the writer's slot: readable when they're world-readable (Deluge's default umask 022 is), unreadable otherwise. A fix, if needed: a host group, `roostack-shared`, mapped into both with `@gid`, and setgid folders. | Medium: depends on each app's umask. |
| **The agent's socket** | No change. A subuid isn't in the `roostack` group, so it can't connect, and the uid_map check (S6) stays for `podman unshare` and containers. Check that the agent also refuses a peer uid that isn't root or `roostack`, so that a socket handed over in a mount doesn't reopen this. | Low. |
| **Seeing it** | `ls -l` already shows big numbers for a non-root container user; under A, container root shows as one too. Each service's technical side says "Its files belong to IDs 1,179,648 to 1,245,183 on this server (its slot, 18)", so nothing is magical. | Low. |
| **Leftover data and kept copies** | Already deleted inside `podman unshare`. A slot stays taken until its data is gone. | Low. |
| **Packages whose containers share storage as different users** | Under keep-id, both were `roostack`, so their permissions never mattered. Under A, they're two real IDs, as under Docker. Most images expect that, but each such package needs installing, updating, backing up, and restoring once. | Medium: found per package. All 23 registry packages need one run through, on amd64 too (UNTESTED-REVIEW). |

### One shift function

Restores of older backups, moves from an older server, imports, and the in-place upgrade (below) all do the same thing: walk a folder inside `podman unshare` and add `base` to every owner, without following links. That should be one function with one test suite, and a record in the service's state of which layout its files are in, so it can never run twice. Running it twice would put every file in someone else's slot.

### The maintainer's lab on Linode

Only fresh installs are supported, but that lab holds real data (Forgejo, Gokapi, Jotty, Memos, Outline, Plik, Seafile). Two ways:

1. **Restore into A.** Wipe the Linode, install a Roostack with A, and restore the whole lab from Roostack Cloud. The restore hands out slots and shifts each service's files. This is the restore planned as item 4, and it would test the shift on real data. Recommended if the timing lines up: do item 4 after A ships, or do it twice.
2. **Upgrade in place.** A one-time step when the new Roostack starts, as B3c's keep-id change ran by itself:
   - add a subuid range;
   - stop each service and shift its files;
   - regenerate its Quadlet files and start it.

   It needs a fresh backup first, and it would take minutes for Seafile's library. It's more code (an upgrade path the project otherwise doesn't promise), but it's the same shift function.

Either way, backups taken before the change still restore, through the shift.

## How many user IDs, and why

The plan reserves 1,024 slots of 65,536 IDs, about 67 million. That sounds like a lot, so here is what the numbers mean and where fewer would do.

**IDs cost nothing.** A subordinate ID range is one line in `/etc/subuid` and `/etc/subgid`: the kernel keeps no memory, disk, or table per ID. Linux has 4,294,967,296 user IDs, so 67 million is 1.6% of them. The only real rule is that ranges mustn't overlap another user's (a login user's own rootless Podman, for example), and `useradd` picks a free range by itself (I1).

**Why 65,536 for each service.** A container image is built for a whole Linux system, which has users 0 to 65,535. Most images only use a few low ones (root, an app user such as 1000, Postgres's 70), but some use high ones: `nobody` is 65,534 in almost every image, distroless images' user is 65,532, and others pick 10001 or 568. Rootless Podman gives a container 65,536 IDs by default for that reason. With fewer, the prototype showed what breaks (2026-11-21, VM 7):

- Every image tried still started with only 1,024 IDs (Alpine, Forgejo, Navidrome, Postgres, Gokapi, and five of AliasVault's).
- A process switching to `nobody` failed with 4,096 IDs ("su: can't set groups: Invalid argument") and worked with 65,536.
- A file an image gives to user 65,532 appeared as owned by nobody with 4,096 IDs, so the app couldn't write it.

So smaller blocks fail later, inside the app, and only for some images and only when the app reaches that code. Every package would need its IDs checked by the registry's CI, and an update that starts using a higher ID would need a bigger block, which means a new slot and moving every file. Podman's own `UserNS=auto` sizes blocks this way, from what it finds in the image (1,024 at least), which is part of why its ranges change.

**Why 1,024 slots.** That's a ceiling, not what's used: slots are handed out one per installed service, lowest free first, so a lab with seven services uses seven slots (about 460,000 IDs). The range is reserved up front because growing it later takes `podman system migrate`, which stops every running container on that server (I2), and because slots are the same on every server, so each server's range must reach the highest slot used anywhere in the lab.

**Fewer would do, at a price:**

| Choice | IDs | What it costs |
|---|---|---|
| 1,024 slots of 65,536 (the plan) | 67 million | Nothing beyond the number looking alarming in `/etc/subuid`. |
| 256 slots of 65,536 | 16.8 million | Nothing until a lab installs its 256th service (counting removed ones whose data is kept), then a migrate on every server. |
| 64 slots of 65,536 | 4.2 million | The same, at the 64th service, which a busy lab could reach. |
| Smaller blocks (4,096 plus 65,534 mapped separately) | under 0.3 million for 64 services | The failures above, found per image and per update. |

I'd keep 65,536 per slot, and reserve 256 slots rather than 1,024 if the bigger number worries anyone: it's still far more services than a lab will run, and it still costs nothing.

## Prototype results (2026-11-21)

**How it was done.** A throwaway change on the branch `proto/container-slots` (`internal/generate/protoslots.go`): for each service named in `ROOSTACK_PROTO_SLOTS` (an environment variable in a drop-in for `roostack.service`), every container gets `UIDMap=0:<base>:65536` and `GIDMap=0:<base>:65536`, `keep-id` becomes `User=` inside the slot, and `:U` goes on every folder of the service's own. Nothing else in Roostack changed: storage folders, the config-file writer, backups, restores, moves, and removals ran their usual code. Two VMs (Debian 13, Podman 5.4.2, crun 1.21, kernel 6.12, amd64), VM 7 as the lab and VM 8 joined to it for a move, were destroyed afterwards. Four packages, installed as packages of the Owner's own under other names so that they didn't clash with the registry's:

- **Navidrome** (`navi`, slot 1): SQLite and a media library. Installed in today's mapping first, then put into its slot with its data.
- **Forgejo** (`forge`, slot 2): Postgres, and `keep-id:uid=1000` in its package.
- **Gokapi** (`goka`, slot 3): a config file Roostack writes, a hook through `podman exec`, and storage under `/srv/roostack/media`.
- **AliasVault** (`alias`, slot 4): seven containers, Postgres, and a folder shared by two containers.

Podman specifics, briefly: in rootless Podman, `podman unshare` runs a command inside the roostack user's own user namespace, where it is root over every ID it has been given, numbered 0 (the roostack user itself), then 1 onwards along its ranges in `/etc/subuid`. These are the *intermediate* IDs that `UIDMap=` names. `:U` on a volume tells Podman to give the folder to the container's user at each start.

### What worked

All four services installed, said Running, and answered on their ports, each container running as its own slot's IDs and none as `roostack`: Navidrome as host uid 296,608, Forgejo's Postgres as 362,214 (slot 2's base plus Postgres's 70), Forgejo as 363,144, and Gokapi as 427,680. Backups (restic and the database dumps, inside `podman unshare`), restores of Forgejo (Postgres) and Navidrome (SQLite), a move of Forgejo to server-8, sizes, and removing AliasVault with its data all worked through Roostack's own code, unchanged.

### Checks

| ID | Assumption | Result |
|---|---|---|
| I1 ✅ | `useradd --system --add-subids-for-system -K SUB_UID_COUNT=67108864 -K SUB_GID_COUNT=67108864` gives a free range that large on Debian 13 (shadow 4.17.4). | **Verified** on VM 7, with two login users and the roostack user's two ranges already there: two new system users got `67405472:67108864` and `134514336:67108864`, after everything taken, with `/etc/subgid` the same. |
| I2 ✅ | Adding a second subuid range to a user with running services, then `podman system migrate`, keeps the existing intermediate IDs. | **Verified** on VM 7 with Navidrome running: after `usermod --add-subuids 296608-67405471 --add-subgids …`, `podman unshare cat /proc/self/uid_map` kept `1 231072 65536` and added `65537 296608 67108864`, and Navidrome's files and database were intact. **Two catches:** the migrate stopped the running container (Roostack must start everything again afterwards), and until the migrate runs, `podman unshare` still has only the old range: on VM 8, which had the range but no migrate, the first move failed with "tar: Cannot change ownership to uid 131143: Invalid argument". Every server (the installer, a joining node, an upgrade) must add the range and migrate before any slotted service arrives. |
| I3 ✅ | `UIDMap=`/`GIDMap=` with intermediate IDs beyond the first 65,536 work rootless, and services in different slots run side by side. | **Verified:** four services in slots 1 to 4, at the same time, owners as expected on the host and inside. |
| I4 ✅ | A bind mount still works when the service folder's parent is `roostack`'s 0700 and the container's IDs don't include `roostack`. | **Verified:** with `/srv/roostack/services` and `/srv/roostack/media` at 0700, Forgejo and Gokapi restarted and answered. Podman sets up the mounts before the container's user takes over, so the parents can stay closed. |
| I5 ⚠️ | An Intel GPU works under a slot. | **Not tried with a GPU** (the VM has none). With a stand-in file owned by group `render`, mode 0660: `GroupAdd=keep-groups` let a slotted container read it, as root and as uid 1000, and `GIDMap=+g991:@991` (the docs' example) was refused rootless: "parent ID GID 991 is not mapped/delegated". **But `keep-groups` keeps every group of the roostack user, `roostack` (989) included:** `/proc/<pid>/status` showed `Groups: 44 989 991` for a slotted container. An escaped process from a GPU container could then read `/var/lib/roostack/lab.yaml` and `node.json` (0640, group `roostack`), which `setpriv` confirmed. The agent still refused it (403: it checks the caller's uid). See finding 2. |
| I6 ✅ | The agent can make an idmapped mount of a library in each service's view folder, mapping the owner to that service's user, and `rslave` passes it into a running container. | **Verified by hand** (as root, `mount --bind -o ro,X-mount.idmap=…`, as the agent's fstab lines do): an owner-only folder (uid 1000, 0700, a 0600 file) mapped to slot 1's root appeared in the already running Navidrome, which read it; a second view mapped to slot 2's uid 1000 was read by a container in slot 2; and slot 2 was refused slot 1's view. The agent's code isn't changed yet. |
| I7 ✅ | `podman unshare` as `roostack` still reads and writes every slot's files with the larger range. | **Verified:** a backup of all four services in 8 s; Forgejo restored (a deleted repository came back, its files kept slot 2's owners); Navidrome restored; `du` for sizes; AliasVault removed with its 49 MB of data. **One catch:** Roostack's SQLite restore wrote `navidrome.db` as intermediate 0, and it only worked because `:U` gave it back to the slot at the next start. See finding 4. |
| I8 ✅ | An escaped process ends up as the slot's root and can't open the database or the agent's socket. | **Verified, simulated** with `setpriv --reuid=296608 --regid=296608 --clear-groups` as root (the credentials an escaped process would have in the host's namespaces): `roostack.db`, the agent's socket, another service's folder, Podman's secrets, and the roostack user's systemd manager were all refused. As uid 999 (`roostack`, as today) all five were reachable. A real escape wasn't attempted. |
| I9 ✅ | (New) Moving a service between servers keeps its slot's owners with no shifting. | **Verified:** Forgejo moved from VM 7 to VM 8 by copying, ran there with the same host IDs, and its repository was there. Both VMs' ranges happened to start at the same host ID, but the move only depends on the intermediate IDs. |

### Findings the plan didn't have

1. **Each slot gets its own copy of every image it uses** (B3b's cost, once per service). Podman can't share an image's layers between ID maps rootless, so the first start in a slot makes a re-owned copy of the whole image (`storage-chown-by-maps`): Navidrome's 243 MB image got a second 243 MB layer marked `uidmap 0→65537`, in about 4 s on VM 7's disk. B3b measured Docmost's 893 MB copy at 12 min 19 s on VM 3's slower disk, against `TimeoutStartSec`'s 15 minutes. So every install and every update pays a copy, two services using the same Postgres image hold two copies, and disk use for images roughly doubles (the default copy stays for pulls). Ways out: accept it, with a timeout and a "preparing the image" step people can see; or have Podman use `fuse-overlayfs`, which maps IDs as files are read rather than copying (a system package, slower file access, and a reset of Podman's storage; not tried).
2. **GPU containers keep the `roostack` group** (I5). Either nothing the daemon owns is group-readable (`lab.yaml` and `node.json` become 0600, and `/var/lib/roostack` and `/srv/roostack` 0700, after checking what reads them through the group today), or the roostack user's primary group moves to one that holds nothing. Small, but it must come with the devices step.
3. **The config-file writer breaks, as expected.** Once `:U` gave Gokapi's settings folder to its slot, Roostack's next apply logged "config file left as it is … open /srv/roostack/services/goka/settings: permission denied" and changed nothing. It fails safe, but a setting changed in the UI wouldn't reach the app. The helper inside `podman unshare` the plan describes is needed before any package with config files can go into a slot.
4. **Every place that writes a service's files must give them to the slot.** The SQLite restore only worked thanks to `:U` (I7), and the same goes for imports and the bring-your-data step. While `:U` stays, it covers these; if it's replaced by one chown at creation (question 3 below), each of those must set the owner itself, through the one shift function.
5. **Media storage lives outside the service's folder** (`/srv/roostack/media/<service>`). The prototype's first version missed it, and Gokapi's root couldn't write its files folder. That's the isolation working, and the generator must treat both places as the service's own.
6. **`:U` already acts as the shift for simple services.** Navidrome, installed in today's mapping, moved into slot 1 with its data on its next start, with no other step. That won't hold for services whose containers use several users (Postgres's data is 70, not root), which still need the shift function.

## Effort (revised after the prototype)

- **A:** about 6 to 7 sessions, up from 5:
  1. Slots, the generator (both storage places), the subuid range and the migrate on install and join.
  2. The config-file helper, owners on restore, import, and bringing data, and the shift function.
  3. Backups, restores, moves, imports, and restoring backups made before slots.
  4. Devices (`keep-groups`, and closing the `roostack` group's reach), libraries, and "read as owner" in the agent.
  5. The image copy: try a big image (Immich or Docmost) on a slow disk under a slot, then decide between accepting it with a visible step and `fuse-overlayfs`. Half a session to a session.
  6. A VM run of all packages, on arm64 and amd64, and an Intel GPU (amd64 hardware, which the VMs can't give).
- **B:** about 8 to 10, unchanged.

## Recommendation

**Go ahead with A**, in the order above, with one more check before the first session's code lands: the image copy on a big image and a slow disk (step 5's first half), since that's the one cost that could make installs and updates noticeably slower for everyone. The core held up: four quite different packages started, backed up, restored, moved, and were removed under slots with Roostack's own code untouched, and an escaped process (simulated) couldn't reach anything of Roostack's. What broke was what the plan predicted (the config-file writer) and two things it didn't (GPU containers keeping the `roostack` group, and every server needing the migrate), each with a clear fix.

## Questions for the maintainer

1. **Option A (a fixed range per service), B (a second user), or C (both)?** I recommend A: it's the only one that also keeps services from each other, it fits how backups and moves already work, and it doesn't grow the agent or add a process.
2. **Slots lab-wide (the same on every server) or per server (shifting on every move)?** I recommend lab-wide.
3. **`:U` at every start: keep it for now, or replace it with one chown when the folder is made, restored, moved, or imported?** I recommend keeping it for the first version and replacing it after, so one change at a time.
4. **"Read as owner" for media libraries:** per-service idmapped mounts (more agent work, invisible to the Owner), or stop offering it and ask people to make their media world-readable (simpler, and a step back from today)? I recommend per-service mounts, if I6 passes.
5. **The Linode lab:** wait and do the whole-lab restore (item 4) onto a Roostack with A, or write the in-place upgrade? I recommend the restore, which tests the shift on real data and keeps the code to what fresh installs and old backups need.
6. **When:** before the first public release (no labs but yours to move), or after? I recommend before. After it, the in-place upgrade becomes something every lab needs.
7. **Option 0's hardening** (`NoNewPrivileges`, fewer capabilities, read-only root filesystems): fold it into this work, or leave it to the security review's findings? I recommend the review, so it's judged with the rest.
8. **The range's size:** 1,024 slots (67 million IDs) or 256 (16.8 million)? Both cost nothing; I'd pick 256 if the bigger number reads as alarming, and keep 65,536 IDs per slot either way ([why](#how-many-user-ids-and-why)).
9. **The image copy for each slot:** accept it, with a step people can see and a longer timeout, or try `fuse-overlayfs` (a new system package, so chosen together)? I recommend measuring a big image on a slow disk first, then deciding.
