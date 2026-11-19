# Keeping services away from Roostack's own user (plan, 2026-11-19)

A design for the maintainer to decide on. Nothing is built, and nothing on a VM has been tried for it. Item 2 of the list on 2026-11-19 ("the next big ticket items").

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

## Effort

- **A:** about 5 sessions.
  1. Slots, the generator, and the subuid range.
  2. Storage creation, the config-file helper, and the shift function.
  3. Backups, restores, moves, and imports.
  4. Devices, libraries, and "read as owner".
  5. A VM run of all 23 packages, on arm64 and amd64.
- **B:** about 8 to 10. A new process and API, every `userLaunch` site moved behind it, backups redesigned, and no protection between services.
- **C:** B plus A.

## To check on a VM before building (ASSUMPTIONS style)

| ID | Assumption | How to check |
|---|---|---|
| I1 | `useradd --system --add-subids-for-system -K SUB_UID_COUNT=67108864 -K SUB_GID_COUNT=67108864` gives a free range that large on Debian 13 (shadow 4.17.4). | Fresh VM with two login users first, as A9. |
| I2 | Adding a second subuid range to a user with running services, then `podman system migrate`, keeps the existing intermediate IDs, so old files stay valid. Intermediate IDs follow the ranges in `/etc/subuid` order. | VM with one service: add a range, migrate, check `podman unshare cat /proc/self/uid_map` and that the service still starts. |
| I3 | Quadlet's `UIDMap=`/`GIDMap=` with intermediate IDs beyond the first 65,536 work rootless, and two services with different slots run side by side. | Two services on slots 1 and 2: check owners on the host and inside. |
| I4 | A bind mount still works when the service folder's parent is `roostack`-owned 0700 and the container's IDs don't include `roostack`. That is, crun sets up the mount before dropping into the container's namespace. If not, the parent must be 0711, and folder modes become the only thing keeping one service's escaped process out of another's files. | Make `/srv/roostack/services` 0700 and start a slotted service. |
| I5 | An Intel GPU works with `GIDMap=+g<n>:@<render gid>` and `GroupAdd=keep-groups` under a slot. | Jellyfin with hardware transcoding (needs Intel graphics, amd64). |
| I6 | The agent can make an idmapped mount of a library into each service's view folder, mapping the owner to that service's subuid, with `rslave` propagating it into a running container. | One library, two readers on different slots, owner-only files. |
| I7 | `podman unshare` as `roostack` still reads and writes every slot's files with the larger range (backups, `du`, deletes). | Back up and restore a slotted service. |
| I8 | An escaped process really ends up as the slot's root and can't open the database or the agent's socket. | Simulate with `podman unshare nsenter` into a container's namespace and drop back into the host's mount namespace as the slot's root uid (`setpriv --reuid`), then try `roostack.db` and the socket. |

## Questions for the maintainer

1. **Option A (a fixed range per service), B (a second user), or C (both)?** I recommend A: it's the only one that also keeps services from each other, it fits how backups and moves already work, and it doesn't grow the agent or add a process.
2. **Slots lab-wide (the same on every server) or per server (shifting on every move)?** I recommend lab-wide.
3. **`:U` at every start: keep it for now, or replace it with one chown when the folder is made, restored, moved, or imported?** I recommend keeping it for the first version and replacing it after, so one change at a time.
4. **"Read as owner" for media libraries:** per-service idmapped mounts (more agent work, invisible to the Owner), or stop offering it and ask people to make their media world-readable (simpler, and a step back from today)? I recommend per-service mounts, if I6 passes.
5. **The Linode lab:** wait and do the whole-lab restore (item 4) onto a Roostack with A, or write the in-place upgrade? I recommend the restore, which tests the shift on real data and keeps the code to what fresh installs and old backups need.
6. **When:** before the first public release (no labs but yours to move), or after? I recommend before. After it, the in-place upgrade becomes something every lab needs.
7. **Option 0's hardening** (`NoNewPrivileges`, fewer capabilities, read-only root filesystems): fold it into this work, or leave it to the security review's findings? I recommend the review, so it's judged with the rest.
