# Keeping Roostack responsive when memory runs short (proposal, 2026-11-19)

A proposal for the maintainer: nothing here is built yet. The questions are at the end.

## The problem

On the maintainer's lab, a 2 GB Linode, programs have held more than the server has. On 2026-11-18, 1.2 GB was in swap and Home's first view after a quiet spell took 13 seconds, because the kernel had moved Roostack's own memory to disk along with the services' memory. 0.1.9 made Home quicker and shows swap (DESIGN.md, "Swap, and Home sent before it's ready"), but nothing stops it happening again. A service that grows (Immich importing, Outline on a big document) can push Roostack, Caddy, and the sign-in page out of memory, and the person who'd open Roostack to fix it is the one waiting.

The goal: **Roostack's pages, the sign-in page at `my.`, Caddy, and the agent stay quick whatever the services do**, and a service that grows past what it should is slowed or restarted, with its page saying so, rather than the whole server.

## What the lab looks like now (read on 2026-11-19, nothing changed)

- **Debian 13, systemd 257.13, 1 processor, 1.97 GB.** cgroup v2 is mounted with `memory_recursiveprot`, so memory protection set on a unit works without setting it on the slices above it too.
- **No limits or protection anywhere.** `roostack`, `roostack-agent`, `user@999.service`, `user.slice`, and `system.slice` all have `MemoryMin=0`, `MemoryLow=0`, and `MemoryHigh`/`MemoryMax` at `infinity`, and no container has a limit (`memory.max` and `memory.high` are `max`). No processor or disk weights are set.
- **The user manager delegates `cpu memory pids`**, not `io` (`systemctl show user@999.service -p DelegateControllers`), so memory limits and processor weights work on the services' units inside it, but `IOWeight=` there would do nothing.
- **Where things run.** `roostack` (and with it the sign-in page) and `roostack-agent` are in `system.slice`. **Caddy and CrowdSec are containers in the user manager's `app.slice`, beside the services**, so anything that caps or deprioritises the services today would hit Caddy too.
- **Out-of-memory scores.** The user manager has `OOMScoreAdjust=100` (systemd's default for user managers), and every container process has 200, Caddy's included. `roostack` and `roostack-agent` are 0, so the kernel already picks a container first when it has to kill something. An unprivileged process can't lower its score below what root last set for it (100 here), so a container's score can be raised, or set back to 100, but never lower.
- **Swap:** a 4 GB swap file and a 496 MB partition, with `vm.swappiness` at 60. zswap is off and there's no zram. The disk counts as an SSD.
- **systemd-oomd isn't installed.** The Debian package `systemd-oomd` (257.13) is available.
- **Pressure was low when read** (memory `some avg300=0.02`), and no OOM kills have happened in 14 days. With 4.5 GB of swap, the kernel's OOM killer would only act once swap was full too, long after the server had become unusable from swapping.
- **What's using memory** (in memory, then in swap): Outline 171 + 168 MB, CrowdSec 163 + 20 MB, Plik 122 + 7 MB, Forgejo 109 + 79 MB, Jotty 84 + 94 MB, Caddy 81 + 9 MB, Glances 30 + 80 MB, and the databases 15–19 MB each. `roostack` held 147 MB of its own (plus 49 MB of file cache). `roostack-agent` showed 305 MB, but only 14 MB is its own: the rest is file cache from the journal, read by the `journalctl` the SSH guard follows, which the kernel can drop at any time.
- **Slow pages logged since 0.1.10** (`web.timePages`): three, all around the upgrade at 16:42 on 2026-11-19, the worst Home's live part at 34 s, all of it in "attention". That looks like the upgrade rather than memory, so it isn't followed up here.

## What packages already say

Each container may declare `memory:` ("250 MB", `pkgspec.Container.ExpectedMemory`), and each database has a default when it doesn't (PostgreSQL 200 MB, Valkey 50 MB, MySQL 500 MB, MariaDB 150 MB: `pkgspec/about.go`). Most of the registry's packages declare it, many with the figure measured beside it. The install summary adds up the parts (`generate.Summarize`) and warns when that's more than the server's **free memory right now** (`install.go`, `TooBig`). It doesn't compare with what services already installed expect, though `ServerMemory.Expected` already holds that figure for the Details. The field is described everywhere as "an estimate, not a limit".

## The options

Each has its effect, its risk, and a rough effort. They build on each other, so the recommended set at the end picks some in order.

### A. Protect Roostack's own units (system side)

Add to `roostack.service` and `roostack-agent.service`:

- **`MemoryLow=`**: the kernel won't take this much of the unit's memory (its own memory, and the cache of its code) while it can take from elsewhere. Suggested: `roostack` 256M, the agent 64M. It's soft: under real pressure with nothing else to take, the kernel will still reclaim it, so it can't cause an OOM by itself.
- **`MemoryMin=`** would be the hard version: never reclaimed, even if that means killing something else. Not recommended: on a small server a too-high figure would turn into OOM kills of services, and Roostack's own memory changes with the lab's size.
- **`OOMScoreAdjust=-500`** on both, so if the kernel does have to kill, it picks a container (200) long before Roostack (it's 0 now, so this mostly makes the order certain).
- **`CPUWeight=200`** on `roostack` (the default is 100), so on a busy single processor, the pages get their share ahead of a busy service. `system.slice` and `user.slice` are siblings, so this only works fully with B's slices.
- **`IOWeight=`** would help pages read back from disk, but the user manager doesn't delegate `io`, so it can't deprioritise the services (see G).

**Effect:** Roostack's pages and the sign-in page stay in memory: the 13-second first view goes away. **Risk:** low. The figures need to fit a 1 GB arm64 server too (ARM64-PLAN.md, Decision 6), and memory protected for Roostack is memory services can't use. **Effort:** half a day: two unit files and a note in SPEC §6.1, plus checking on a VM with memory pushed into swap (as was done for 0.1.9).

### B. Two slices in the user manager: Roostack's containers and the services

Put Caddy and CrowdSec (`internal/generate/system.go`) in a slice of their own in the user manager, say `roostack-core.slice`, and every service's containers and databases in `roostack-services.slice` (`Slice=` in each Quadlet file's `[Service]` section, which Quadlet passes through to the unit). Then:

- **`roostack-core.slice`:** `MemoryLow=` (Caddy uses about 50 MB of its own; suggested 96M), `CPUWeight=200`, and `OOMScoreAdjust=100` on Caddy's unit, back down from Podman's 200 and as low as an unprivileged unit can set.
- **`roostack-services.slice`:** this is where C goes.

**Effect:** Caddy, which every visitor and the sign-in page go through, is protected like Roostack, and the services can be capped together without capping Caddy. **Risk:** low. Moving a running unit to another slice needs it restarted, so the first apply after the upgrade restarts every service once (Roostack's updates already restart services, SPEC §7). A package's own `quadlet:` passthrough mustn't set `Slice=` (add it to the refused list in `pkgspec/policy.go`). **Effort:** a day, with golden files, the policy, and a VM check.

CrowdSec is the question: it's 163 MB, more than Caddy, and part of keeping the lab safe, but the lab still works without it for a while. I'd put it with the services (see question 4).

### C. A cap on all the services together

On `roostack-services.slice`, set **`MemoryHigh=`** to the server's memory less what's kept for the system and Roostack. For example, total − 400 MB: 1.6 GB on the maintainer's Linode. Past `MemoryHigh`, the kernel slows the services down and reclaims their memory (moving it to swap) **without killing anything**, so the rest of the server stays quick while the services wait. Roostack would write the figure into the slice (a drop-in it generates, as it does the Quadlet files) and recalculate it from `/proc/meminfo` when it starts, since a VPS can be resized.

**`MemoryMax=`** on the slice would be the hard version: past it, the kernel kills something inside the slice, which would be the biggest process, often a database. Not recommended without D's group kills.

**Effect:** whatever the services do, the system and Roostack keep about 400 MB. Services that need more than the cap will be slow (swap) rather than taking the server down. **Risk:** medium. A lab whose services genuinely need more than the cap gets slower than today, since they're held to it even when the system has some spare. The reserve has to be right for small servers: on a 1 GB server, total − 400 MB leaves services 600 MB. **Effort:** a day, plus a test that holds the services at the cap and times Home.

### D. Per-service limits from the package's figures

Give each service's units a limit from its `memory:` (the sum of its parts, as the summary adds it up):

- **`MemoryHigh=` at twice the expected figure** (or the figure plus 256 MB, whichever is larger) per unit: a service past it is slowed and reclaimed, not killed. A package that doesn't say gets no per-service limit, only C's cap.
- **`MemoryMax=` per unit, optionally, at three times the figure**, for a service that's out of control (a leak): the kernel kills inside the unit. With systemd's `OOMPolicy=` the unit restarts (`Restart=always`, which the units already have). Their policy is `continue` now, so it should be set to `stop`, with `memory.oom.group` so the whole container is killed and restarted cleanly rather than one process inside it.

**The risk is a kill in the middle of a write.** PostgreSQL recovers from its write-ahead log, but every connection drops and it replays on start. MySQL and MariaDB's InnoDB recovers the same way, more slowly. SQLite in WAL mode recovers. An app that writes its own files without care (a JSON store, a half-written upload) may not. So: never `MemoryMax` on a database's unit, only on apps' containers, and only where the package's figure has been measured. A figure that's wrong (Immich during an import, Jellyfin transcoding) would turn a slow service into one that keeps restarting, which is worse.

**What people will see:** systemd counts how often a unit went past `MemoryHigh` and records `oom-kill` as the reason a unit stopped. Roostack would read them (`systemctl --user show -p Result -p MemoryCurrent -p MemoryPeak`, and `memory.events` for the counts) and say so:
- on the service's page: "Outline used more memory than Roostack allows it (500 MB), so Roostack restarted it at 14:02", with the figures and the unit's journal under Details;
- under Needs attention after a repeat (as monitoring already does for a service that doesn't answer), with **Allow it more memory**.

**Effect:** one runaway service can't take the others with it. **Risk:** as above; the figures in the registry were measured idle. **Effort:** 2–3 days: generation, policy, reading the events, the page and attention wording, help, and testing a deliberately leaking container on a VM. **The per-service override** (a figure on the service's settings page) is another day.

### E. systemd-oomd

systemd's userspace OOM killer watches pressure (PSI) and kills the whole unit under a slice that has been stalled on memory for a while (`ManagedOOMMemoryPressure=kill`, default 60% for 30 s), or the unit using the most swap when swap is nearly full (`ManagedOOMSwap=kill`). It acts long before the kernel would on a server with 4.5 GB of swap, and always kills a whole unit, which then restarts.

**Effect:** a server thrashing on swap recovers within a minute by itself, instead of crawling until someone notices. **Risk:** medium. It's a new system package (a dependency, to agree), and it kills by pressure, so a service that's merely busy during a big import could be killed if C's cap holds it under pressure. That's why it'd go on `roostack-services.slice` only, with a high limit (say 80% for 60 s), never on Roostack's units. Ubuntu and Fedora ship it on by default; Debian doesn't. **Effort:** a day, plus the `.deb`'s `Depends` and a note in README's table.

### F. Swap: keep it, make it cheaper

- **Keep swap.** Moving idle services' memory to disk is what lets a 2 GB server run seven services, and it's fine as long as Roostack's own memory isn't moved (A, B).
- **zswap** (compressed swap pages kept in memory, built into Debian's kernel, no package): pages moved out are compressed in memory first and go to disk only when that's full, so swapping in is mostly a decompression rather than a disk read. Turning it on is a kernel setting at boot (`zswap.enabled=1`), which the agent would have to write into the boot configuration, or set through `/sys/module/zswap/parameters/enabled` at each start. **Risk:** low, but it's a change to the host's boot settings, which Roostack doesn't make today. **Effort:** half a day in the agent.
- **zram** (a compressed swap device in memory) needs `zram-tools` or systemd's `zram-generator`, a package, and replaces disk swap rather than sitting in front of it. Not recommended over zswap.
- **`vm.swappiness`:** leave it at 60. With A and B protecting Roostack, lowering it would mostly mean the kernel drops file cache (Roostack's own code included) before idle services' memory, which is what hurt here.
- **Installs without swap** (some VPS images have none): the installer could say so and offer a swap file, since without swap the kernel's OOM killer is the only answer to a spike. A separate question.

### G. Disk weights inside the user manager

Delegating `io` to the user manager (a drop-in `Delegate=cpu memory pids io` for `user@999.service`, which the package can ship) would let B's slices have `IOWeight=` too, so a service reading heavily (Immich indexing) doesn't starve Roostack paging its code back in. **Risk:** low, but it only works with a disk scheduler that honours weights (BFQ on SSDs isn't Debian's default for every disk; `none` or `mq-deadline` ignore `io.weight`), so it might do nothing on many servers. **Effort:** half a day to try on a VM. I'd leave it until A–C are measured.

### H. The install summary

Today the warning compares the new service with free memory right now, which swings with the cache. Better: compare **what all services on the server would expect, the new one included** (`ServerMemory.Expected` + `Summary.Memory`), with **what the services may use** (C's cap):

- within the cap: no warning;
- past the cap: the warning, worded for what will happen: "Services on server-1 would need about 2.1 GB together, and it can give them 1.6 GB, so they'll be slower while busy. Run Immich on another server, or free some memory first.";
- **refusing** only when the new service on its own expects more than the server's whole memory (Immich's 2.8 GB on a 2 GB server) is a product question (question 6).

**Effort:** half a day, plus wording and tests.

## Recommended, in order

1. **A: protect `roostack` and `roostack-agent`** (`MemoryLow`, `OOMScoreAdjust`, `CPUWeight`). It's the smallest change and fixes what the maintainer saw.
2. **B: Caddy (and maybe CrowdSec) in a core slice, services in their own**, so Caddy and the sign-in path are protected and the services can be capped without capping Caddy.
3. **C: `MemoryHigh` on the services' slice** at total − 400 MB, so the services together can't starve the rest.
4. **H: the install summary compares against that cap.**
5. **D: per-service `MemoryHigh`** from the package's figures, with the page and Needs attention saying when a service is held back. `MemoryMax` per app container only later, once figures are measured under load.
6. **E (systemd-oomd)** and **F (zswap)** if 1–5 still leave a thrashing server on a 1–2 GB machine, after measuring on a VM. Both need a decision (a package; the host's boot settings).

1–4 together are about three days with VM checks. A test for all of it: on a 2 GB VM with a few services, push 1.5 GB into a container that allocates and holds memory, then time Home, the sign-in page, and a page through Caddy, before and after.

## Questions for the maintainer

1. **Protect with `MemoryLow` (soft) rather than `MemoryMin` (hard)?** I recommend `MemoryLow`, with `roostack` 256M, the agent 64M, and Caddy 96M.
2. **How much should the system and Roostack keep?** I suggest the services' cap be the server's memory less 400 MB, or less a quarter of it on servers with 4 GB or more, whichever is larger. Should a 1 GB server keep less?
3. **Per-service limits: `MemoryHigh` only (slowed, never killed), or `MemoryMax` too for apps' containers (killed and restarted)?** I recommend `MemoryHigh` only for now, and never `MemoryMax` on a database.
4. **CrowdSec: protected with Caddy, or with the services?** It's 163 MB on the Linode. I'd put it with the services: Caddy carries on without it, and the bouncer's bans stay in Caddy.
5. **systemd-oomd: add the Debian package (a new dependency) now, later, or not at all?** I'd say later, after measuring 1–5.
6. **The install summary: refuse a service that expects more than the server's whole memory, or keep it a warning?** I'd keep it a warning, worded more firmly, since the figures are estimates.
7. **A per-service memory figure the Owner can change on its settings page:** now, with D, or later?
8. **zswap: may the agent turn it on (a host boot setting Roostack doesn't touch today)?**
9. **Swap files:** should the installer offer to make one on a server with no swap?

## Decided and built (2026-11-19 to 21)

The maintainer's answers: 1, 2 (Caddy in its own protected group), and 3 (a cap on all the services at memory less 400 MB) as proposed. Questions: `MemoryLow` with the figures above; 400 MB flat; slow only, never kill; CrowdSec with the services; oomd later; no refusal at install, a firmer warning; the Owner can choose a service's limit on its settings page; zswap on; the installer offers a swap file. SPEC §6.1, "Memory", has what was built.

- **The install summary (4), changed:** the maintainer asked whether what's free right now isn't more relevant than the services' figures against the total. It now uses both and warns on either, saying which: free right now counts caches and idle services' low use, and the figures catch the peaks a quiet moment hides (SPEC §8.3.4, "Both checks").
- **Per-service limits (5), and services whose needs vary:** twice the package's figure, at least 256 MB above it, **only while the server is tight** (the services on it together expect more than the cap), and only when every part says what it expects; the Owner's choice always wins. On a big server nothing is held to its figure, so Immich's machine learning, Jellyfin's and Plex's transcoding, Frigate, and Seafile's indexing use what they need; on a tight one, their doubled figure is usually more than the cap anyway. A package-declared peak was the alternative (a new field every package and a release would need, and still a guess).
- **Found on the VM:**
  - the kernel honours `MemoryLow` only as far as every slice above has as much, so the package sets it on `system.slice`, `user.slice`, `user-<uid>.slice`, and `user@<uid>.service`, and Roostack on `roostack.slice`;
  - a systemd reload doesn't apply a changed slice to a running one, so Roostack sets it live too;
  - 400 MB kept was not quite enough on a 2 GB server, since the kernel's own memory comes out of it, and Roostack's protection was broken through now and then; 600 MB held. A question for the maintainer (TASKS.md, "Memory").
