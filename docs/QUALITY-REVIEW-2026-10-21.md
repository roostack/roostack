# Code quality review: Roostack and Roostack Cloud

Date: 2026-10-21. Repositories as reviewed: roostack `06aa380`, roostack-cloud `32f9129`. roostack-services was read only where it bears on these.

This is a review of **code quality**: simplicity, understandability, maintainability, reuse rather than duplication, and how easily a new contributor could pick the code up before both repositories go public. It isn't a security review (that was Cloud's `docs/REVIEW-2026-10-16.md`) or a feature review. Nothing was changed: every suggestion here waits for the Owner's decision.

**How it was done.** The docs were read first (each README, Roostack's CLAUDE.md, ARCHITECTURE.md and SPEC.md, Cloud's DESIGN.md and TASKS.md, and the earlier review). Eight scoped reviews then ran in parallel:

1. Roostack's `internal/web`.
2. Roostack's controller and node: `manager`, `node`, `backup` and their neighbours.
3. Roostack's other packages, `cmd/` and the public packages.
4. Duplication between the two repositories.
5. Cloud's Go.
6. The non-Go parts: the editor's JavaScript, the site, the status page, shell, Quadlets, OpenTofu and the workflows.
7. The new contributor's experience, and the tests.
8. Dead code and static analysis.

Each finding was then checked by reading the cited lines; the ones in §1 and §2 were checked twice.

**Tools that were run:**
- `go vet` (with `GOOS=linux` for Roostack).
- `staticcheck` and `deadcode`, run from a scratch folder, not added to either module.
- `shellcheck`, `tofu fmt -check` and `tofu validate` (no init, no state).
- A small `go/ast` program that lists function lengths, parameter counts and nesting.
- Both test suites (Roostack's natively on a Mac, where it partly builds, and Cloud's against PostgreSQL 18).
- Both repositories' Node tests.

Nothing was run on a VM: findings that need one say so.

**Reading the findings.** Paths are relative to their repository; a path in Cloud starts `cloud:` where it could be confused with Roostack's. Each finding gives:
- **where** it is;
- **why** it matters;
- the **change** suggested;
- **effort**: S is under half a day, M is a day or two, L is several days;
- **risk**: the risk of making the change, not of leaving it;
- **decision**: whether the Owner must choose before anyone starts.

---

## 1. Summary

**The code is in better shape than its growth rate would suggest:**
- Comments say *why*, with dates and the alternatives considered. All 1,160 `§` citations in Roostack's Go resolve to real SPEC headings.
- `generate` really is pure and golden-tested.
- The `trust` package is one definition the firewall and Caddy are both checked against.
- Interfaces are mostly small and declared where they're used.
- Both suites are real: SQLite, restic and PostgreSQL rather than mocks, and Cloud's end-to-end tests reach 77% of its code.
- `go vet` is clean in both repositories, and staticcheck found no bugs.

**The debt is of three kinds:**
- **The same small helper written many times.** Parallel sessions each wrote what they needed:
  - "write a file atomically": about 35 copies, which have drifted;
  - capitalise-first-letter: 6 copies;
  - "a, b and c" list joiners: 5;
  - byte-size formatters: 5 across the two repositories;
  - "every d until ctx ends" loops: about 30.
- **A few types and functions that every feature has been added to:**
  - `web.Server`: 60 fields;
  - `ServiceManager`: 110 methods;
  - `manager.Manager`: 162 exported methods;
  - `node.Local`: 201 methods;
  - `node.Server.Handler`: 610 lines;
  - `serve()`: 574 lines;
  - Cloud's `web` and `admin` packages, which copy each other's plumbing.
- **Onboarding that works only for the people already here:**
  - 13 of Roostack's packages don't build on a Mac, because of eight Linux-only lines in `agent`;
  - Cloud has no licence file, no CLAUDE.md and no ARCHITECTURE.md;
  - neither repository has a CONTRIBUTING or SECURITY file;
  - Cloud can't be built without a hand-made `go.work` pointing at a private repository.

**What's worth doing first,** in order. Each is small or medium, low-risk and needs no product decision, except where noted:

| # | Do | Why first | Effort |
|---|---|---|---|
| 1 | Make Roostack build and test on a Mac (§5.1) | Eight lines of `syscall` keep `manager`, `web` and `node` (whose tests use fakes) off every Mac. A trial in a scratch copy made `go vet` clean on both systems and 40 packages pass | S–M |
| 2 | One atomic-write helper in Roostack (§3.1) | 35 copies, which differ in temp name, cleanup and permissions; none fsyncs. CLAUDE.md already names it as if it existed | S–M |
| 3 | A check in CI for each generated file, and shellcheck (§7) | Cloud's sqlc output has slipped once; the help site's broken-link check runs only at release; Cloud's copies of Roostack's CSS are stale today | S |
| 4 | Cloud: one `internal/ui` package for the website's and console's shared plumbing (S15) | The copies have already drifted: the console's expired passkey ceremonies are never deleted | M |
| 5 | Roostack: a round-trip test for every lab API call, then one wire file per feature (S9) | Each node operation is written in four places, and bodies match only because Go's JSON ignores case. A typo in a path passes every test today | S, then M |
| 6 | Cloud: LICENSE, NOTICE, CLAUDE.md and ARCHITECTURE.md; both: CONTRIBUTING.md and SECURITY.md (§6.1) | Needed before launch, and they're where the conventions below get written down | S–M, decision |
| 7 | Put the lab ↔ Cloud access rules in `cloudapi` and test both sides against them (§3.4) | Roostack's fake Cloud has drifted from the real one: it refuses storage keys to a suspended lab that Cloud would serve | S–M, decision |
| 8 | Pin which Roostack Cloud builds against, and record it (X5) | Deploys build against whatever the local Roostack checkout holds, uncommitted changes included | S, decision |

**Found in passing.** §2 lists 16 things that are behaviour, not quality: real bugs, or inconsistencies the Owner should rule on. They're small, but a quality pass mustn't change behaviour without agreement, so they're listed apart. The most pressing:
- **Cloud's first production deploy would stop with every service down** (§2, P1).
- **When a restore's or a move's databases fail to load,** the service is stopped, but nothing keeps it stopped. The next apply on that server, or a reboot, starts it on files without their databases: the half-restored state the 2026-10-16 fix meant to prevent (P3).
- **`/etc/fstab` is edited in seven places** with no lock and no atomic write (P5).

**What not to do.** Splitting `internal/web` into feature sub-packages now: it's days of work for little more than file moves and narrower interfaces give (§4.1). Also don't share code between the repositories for its own sake: the Cloudflare clients, the lab CAs, the S3 fakes and the session code look alike but serve different threat models (§3.5).

---

## 2. Found in passing: behaviour to fix or decide

These aren't quality findings. Each needs the Owner's agreement before it's changed.

**P1. Cloud's deploy runs `ca dev` on every deploy; on production it fails with everything stopped.**
- **Where:** `cloud:ops/deploy.sh:215-216`.
  - Line 215 stops the control plane, console and migrate units.
  - Line 216 runs `cloud ca dev`, which refuses when `name_suffix` is `roost.ac` (`cloud:cmd/cloud/ca.go:142`).
  - Under `set -e` the script then exits before line 217 starts them again.
- **Why:** there's only a `test` environment today, so it hasn't bitten. The first production deploy would leave roost.ac down until someone starts the units by hand.
- **Change:** run `ca dev` only for the test environment.
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in Cloud's `049d194` (2026-10-22, roostack-cloud branch `claude/wave0-fixes-177bbd`).

**P2. The console's expired passkey ceremonies are never deleted.**
- **Where:** `cloud:internal/db/queries/passkeys.sql:46-50` has no purge for `admin_passkey_ceremonies`. The website's ceremonies are purged by `DeleteExpiredPasskeyCeremonies` (`:27`), called hourly.
- **Why:** the table only grows. It's a result of the console's passkey code being a copy of the website's (S15).
- **Change:** add the query and call it from the console's hourly job (`cloud:cmd/cloud/adminconsole.go:351`).
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in Cloud's `18985dd` (2026-10-22, roostack-cloud branch `claude/wave0-fixes-177bbd`).

**P3. After a failed database load, a restored or moved service is stopped, but only until the next apply.**
- **Where:**
  - `node.Local.LoadDumps` (`internal/node/move.go:502-519`) stops the service's units, loads the dumps, and starts the units only if that worked. Both a restore (`internal/manager/restore.go:355-363`) and a move through a backup (`finishMove`, `internal/manager/move.go:250-256`) call it after the service has been switched to its new server.
  - Nothing then holds it stopped: every apply starts all of a server's desired units (`internal/reconcile/reconcile.go:175`, "StartUnits are all desired units"), and the units are `WantedBy=default.target` (`internal/generate/generate.go:862`). The next apply on that server (any install or setting change there) or a reboot starts the service on files without their databases.
  - The restore's error promises it "stays stopped; restore it again to retry", and the comment cites the security review of 2026-10-16 ("it would run half-restored"). The move's error doesn't say it's stopped at all.
  - In a move, the old copy is still intact on the old server at that point: it's set aside only after the load succeeds (`move.go:257`). A restore has no such copy.
- **Why:** the 2026-10-16 fix holds for minutes, not until someone acts. For an app that sets itself up on an empty database (e.g. offering its first-run admin page), that's the state the review meant to rule out.
- **Change:**
  - For a move: switch the service back to the old server and start it there, so a failed move changes nothing; leave the new server's copy as kept data.
  - For a restore: hold the service stopped in its desired state, as a withdrawn package already is (`state.Service.Stop`; `generate.Stop` leaves out its containers), with a "needs attention" item that says why and offers "Restore again". A start by hand clears the hold.
  - Fold both into one `transferJob` later (D11).
- **Effort:** S (the move), M (the hold, with its page). **Risk:** low. **Not verified:** read from the code; check on a VM by making a dump fail, then applying an unrelated change.
- **Decided (the Owner, 2026-10-22):** a failed move switches back; a failed restore is held stopped, with **Restore again**, **Put back what was there** and **Start it anyway**. Owners and Admins see it; only the Owner gets the buttons ("The Owner can restore it again or start it anyway"). "Restore again…" opens the restore page with the service and the same backup chosen. The new server's copy after a failed move is kept as a new kind, `failed-move` ("From a move that didn't finish"). Each choice, and a successful restore, ends the hold, recorded in the audit log ("Started Immich after its restore didn't finish"). Wording, agreed (future tense for what will happen, present continuous for what's happening; Immich, dates and servers are examples):
  - Restore log, last line: "Immich's files are back from the backup of Mon 20 Oct 03:00, but its database couldn't be loaded, so Roostack is keeping it stopped. Choose what to do on Immich's page."
  - Needs attention (a problem, emailed with problems): "Immich is stopped: its files are back from the backup of Mon 20 Oct 03:00, but its database couldn't be loaded." Link: "What to do". Its status: **Stopped**, hint "Its restore didn't finish".
  - Service page: "**Immich is stopped: its restore didn't finish.** Its files were put back from the backup of Mon 20 Oct 03:00, but its database couldn't be loaded: *(first line of the error)*. If it started now, Immich would be running on files without the database that goes with them: it could lose track of what it keeps, or offer to set itself up again for whoever opens it first. So Roostack will keep it stopped until you choose." Then "See the restore's log"; **Restore again…** ("Try the same backup again once the cause is fixed (a full disk, say), or an older one."); **Put back what was there…** (only with a kept copy); "Start Immich anyway…" (a disclosure, then a danger button **Start it anyway**).
  - Put back's confirmation: "Put back Immich's data from before the restore, as it was at Wed 22 Oct 14:12? Immich will start again as it was before you restored it. The files from the backup will be kept, so you can try the restore again."
  - Start anyway's confirmation: "Start Immich anyway, without its database from the backup? It will run on the backup's files with whatever is in its database now, which may be empty or half loaded. Immich may offer to set itself up again, or lose track of its files. Do this only if you'll put its database right yourself."
  - Move log: "Its database didn't load on server-2: *(error)*", "Moving Immich back to server-1", then "Immich couldn't be moved to server-2: its database didn't load there. It's running on server-1 again, as before. What was copied to server-2 will stay there, at `/srv/roostack/services/immich.failed-move-20261022-1412`, until you delete it."
  - Needs attention after a failed move (a warning): "Immich couldn't be moved to server-2. It's running on server-1, as before." Link: "See what went wrong".
  - If moving back fails too, it's held on server-2: "Immich is stopped on server-2: its database didn't load there, and moving it back to server-1 failed too: *(error)*. Roostack will keep it stopped until you choose." Choices: **Move it again…** and **Start it anyway**.
- **Status:** Fixed in `ae621c3` (2026-10-22). Also found: a restore onto the same server from its own main backup runs as that server's own maintenance job, which this finding didn't cite; the controller now asks the server how it ended (`Restores.Outcome`) and holds it then. Checked on VM3: a dump that failed to load held Forgejo through an unrelated apply, `roostack apply` and a reboot, and each of the three choices worked. The failed move was checked with fakes only (one VM).

**P4. `config.Update` says concurrent edits are safe, but nothing locks it.**
- **Where:** `internal/config/config.go:617-631`. It reads `lab.yaml`, applies the change and saves. It's called from `manager` (through `UpdateLab`), `node/settings.go:274, 301, 324, 373, 437`, `setup` and `mail`, all in one process.
- **Why:** two overlapping updates can each read the old file and the second save loses the first change. The comment's claim ("callers change only their own fields, so concurrent edits … don't overwrite each other") is true only if they don't overlap in time.
- **Change:** a package-level mutex around read-change-save; fix the comment.
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in `8de6bb8` (2026-10-22).

**P5. `/etc/fstab` is edited in seven places, unlocked and in place.**
- **Where:** `internal/agent/drives.go:394/417`, `import.go:82/87`, `library.go:226/237, 259/282, 648/678, 873/959`, `shares.go:213/225`.
  - Each reads the file, edits the string, writes it back with `os.WriteFile` and reloads systemd.
  - The agent has no lock around any of them, and its HTTP server handles requests concurrently.
- **Why:**
  - Two requests at once can lose one edit.
  - `os.WriteFile` truncates in place, so a crash or power cut mid-write leaves a short fstab, on a server that may be at someone's parents' house.
  - About eight hand-written scanners re-split the file by its `# Added by Roostack:` tags, and that literal appears 16 times.
- **Change:**
  - One `editFstab(func(tab string) (string, error))` with a mutex, an atomic write and the reload.
  - One block parser with typed tag constructors.
- **Effort:** M. **Risk:** medium: it's the privilege boundary; verify on a VM. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in `be6a0d1` and, for the atomic write, `0757cbb` (2026-10-22): one `editFstab` with a lock and the reload. The agent's sandbox can't write `/etc`, so the agent writes the new file in `/etc/roostack` and a short systemd job renames it over `/etc/fstab` (the Owner's choice); `/etc/fstab` left the agent's `ReadWritePaths`. The block parser waits for wave 2. Checked on VM3 with the installed unit: a backup drive and a media library added and removed, and a reboot.

**P6. A port clash is reported as a web-address clash.**
- **Where:** `internal/manager/manager.go:387, 856` match `strings.Contains(err.Error(), "already used")` and say "That web address is already used". The port message at `internal/state/state.go:160` contains the same words.
- **Change:** a `HostInUseError` beside the existing `PortInUseError` (`state.go:208`), and `errors.As`.
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in `e3384cb` (2026-10-22).

**P7. Atomic writes can leave a private file with the wrong permissions.**
- **Where:** every hand-written `os.WriteFile(tmp, data, 0o600)` then `os.Rename` (§3.1). `os.WriteFile` applies the mode only when it creates the file, so a leftover `.tmp` from an earlier crash, if it was 0644, keeps 0644. Only `reconcile.writeAtomic` (`internal/reconcile/reconcile.go:708`) uses `CreateTemp` and `Chmod`.
- **Change:** fixed by the helper in §3.1.
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in `8de6bb8` (2026-10-22): `internal/fsutil`, used by every 0600 and 0640 writer; the 0644 copies wait for D1.

**P8. Cloud's quota email shows 400 kB as "0 MB".**
- **Where:** `cloud:internal/storage/meter.go:255` formats with `%.0f MB` below a gigabyte.
- **Change:** fixed by one size formatter (§3.3).
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in Cloud's `60f4c66` (2026-10-22, roostack-cloud branch `claude/wave0-fixes-177bbd`).

**P9. Which month does relay bandwidth count in?**
- **Where:** the portal's "this month's bandwidth" (`cloud:internal/web/portal.go:530`) sums `relay_usage`, which has one row per lab per UTC day, so it can only be a UTC month. Visits, on the portal and in the lab API's `relayStats` (`cloud:internal/api/api.go:755-859`), are hourly, so both show them in the lab's own time zone. The code is consistent with its data. What's unwritten is which month the relay's monthly allowance (and later its bill) counts: DESIGN §3c says "monthly" without a zone.
- **Why:** in Sydney the first 11 hours of each month count towards the month before, and a lab can change its time zone.
- **Change:** if UTC, write it in DESIGN §3c and say "(UTC)" where the figure is shown; nothing else changes. If the lab's or the account's zone, the edges would have to report bytes hourly: a change to the data model.
- **Effort:** S (UTC), M (another zone). **Risk:** low. **Decided (the Owner, 2026-10-22):** UTC.

**P10. Cloud's email isn't wrapped or encoded for 7-bit servers.**
- **Where:** `cloud:internal/mail/mail.go:133` sends `Content-Transfer-Encoding: 8bit` without checking the server's `8BITMIME`, and doesn't wrap lines. RFC 5322 limits a line to 998 octets. Roostack's mail uses quoted-printable (`internal/mail/mail.go:209-214`).
- **Change:** port Roostack's encoding.
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in Cloud's `2f1c4f2` (2026-10-22, roostack-cloud branch `claude/wave0-fixes-177bbd`).
- **Status:** Fixed in Cloud's `aad1a52` (2026-10-22, roostack-cloud branch `claude/wave0-fixes-177bbd`).

**P11. A restore email in Cloud is sent outside any transaction.**
- **Where:** `cloud:internal/api/restore.go:170-188` calls `Mail.Send` in a goroutine. Everywhere else mail is queued in the outbox in the same transaction as the change, then the outbox is woken.
- **Change:** queue it like the rest (S16, "Owner and the lab's recipients").
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in Cloud's `81bbc36` (2026-10-22, roostack-cloud branch `claude/wave0-fixes-177bbd`).

**P12. Some htmx partials send half a page with 200 OK on a template error.**
- **Where:** `internal/web/pages.go:886` `renderPart` executes the template straight into the response, where `render` and `renderLive` buffer first.
- **Change:** buffer it like the others.
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in `e073709` (2026-10-22).

**P13. The package editor's Compose import puts a service name into a regular expression unescaped.**
- **Where:** `internal/editor/static/compose.js:404` (`dsvc`) and `:432` (`^${o}:\d+$`). The same file escapes names elsewhere (`:432`'s first pattern).
- **Why:** a Compose service name with a `.` matches more than meant. Compose's names allow `.`, `-` and `_`.
- **Change:** one shared `escapeRegExp`, used everywhere the escape is inlined (five places).
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in `c1d0225` (2026-10-22).

**P14. The website's copy of the editor uses a design token the site doesn't have.**
- **Where:** `cloud:site/src/styles/roostack/tokens.css` was synced from Roostack `51a7674`, 401 commits ago; the editor from `06aa380`. `editor.css` uses `var(--text-xs)` (lines 157, 162, 197) with no fallback, and the site's tokens don't define it.
- **Why:** those labels render at the inherited size on roostack.cloud.
- **Change:** have `cloud:tools/sync-editor.sh` also sync the tokens, and check drift in CI (G1).
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in Cloud's `28c7517` (2026-10-22, roostack-cloud branch `claude/wave0-fixes-177bbd`).

**P15. Remote node calls drop errors and use a fresh context.**
- **Where:** `internal/node/remote.go:389` (`remoteRestores.Pending`), `:424` and `:433` (`remoteUpdates.Pending`, `RestartStatus`) discard the error.
  - These and their siblings (`Backups.RunNow`, `Restores.Restore/Kept`, `Settings.Timezones/SSHAccess/RestartPolicy`) take no `ctx`, so `Remote` uses `context.Background()` with a 3-minute timeout.
  - A request that asks for them can't be cancelled when the person leaves the page; the 3-minute timeout is the only bound.
- **Change:** add `ctx` to those interface methods; return the errors.
- **Effort:** M. **Risk:** low. **Decision:** no.
- **Decided (the Owner, 2026-10-22):** approved.
- **Status:** Fixed in `d43dece` (2026-10-22).

**P16. The deb's subordinate ID range can overlap one that was there before.**
- **Where:** `packaging/deb/postinst:25` gives the `roostack` user a fixed `200000-265535` with `usermod --add-subuids`, which doesn't check for overlaps.
  - By Debian's defaults the first login user gets 100000–165535 and a second 165536–231071.
  - So the fixed range overlaps when a second login user, or anyone's own rootless Podman with a range in 165536–265535, existed before Roostack was installed.
  - A user made afterwards should get a free range, since shadow skips ranges already taken.
  - Nothing in Roostack depends on 200000. ASSUMPTIONS B1's example (`100998`) comes from a dev VM set up before the package.
- **Why:** two accounts sharing subordinate IDs means the same host uid owns files in both's containers, so one can read or change the other's container files. It's rare on a NUC with one person, plausible on a reused server.
- **Change:** `useradd --system --add-subids-for-system` when the user is made (line 19), so shadow picks a free range, and drop the `usermod`. Only fresh installs are supported, so nothing needs migrating. Update ASSUMPTIONS B1.
- **Effort:** S. **Risk:** low. **Decision:** yes. **Not verified:** that Debian 13's shadow has `--add-subids-for-system` (shadow 4.11 and later should), and that it skips taken ranges. Check on a VM with a second user made before the install.
- **Decided (the Owner, 2026-10-22):** as proposed.
- **Status:** Fixed in `7d6aa9f` (2026-10-22). Checked on VM3: Debian 13's shadow (4.17.4) has the option, and with two login users made first the package gave `roostack:231072:65536`, no overlap.

---

## 3. Duplication and reuse

### 3.1 Roostack: the same helper written many times

**D1. Atomic file writes: about 35 copies.**
- **Where:**
  - Named copies:
    - `writePrivate`: `internal/mail/mail.go:219`, `internal/manager/keychange.go:427`, `internal/node/settings.go:451`;
    - `writeFile`: `internal/labrestore/restore.go:268`, `internal/cloud/link.go:879`, `internal/labnet/ca.go:191`, `internal/registry/fetch.go:513`;
    - `writeFileAtomic`: `internal/web/signinlook.go:312`;
    - `writeAtomic`: `internal/reconcile/reconcile.go:708`.
  - About 25 more are inline, e.g. `internal/config/config.go:610`, `internal/instance/instance.go:141`, `internal/state/local.go:82`, `internal/agent/agent.go:877, 935`, `internal/store/store.go:460`, `internal/sso/key.go:77`, `cmd/roostack/nodeserve.go:44`.
  - `internal/backup/job.go:417` `writeJSON` isn't atomic at all.
- **Why:**
  - They've drifted: temp names `.tmp`, `.new` or `.restore`; two clean up on failure; one uses a unique temp name (so two writers race on the others); none fsyncs.
  - P7's permissions problem.
  - CLAUDE.md says "Secrets on disk are written 0600 (`writePrivate`)" as if there were one.
- **Change:** a leaf package (e.g. `internal/fsutil`) with `WriteFile(path, data, perm)`: `CreateTemp` in the same folder, `Chmod`, `Sync`, `Rename`. Add `WritePrivate` and `CopyFile`, replace the copies, and update CLAUDE.md to name it.
- **Effort:** S–M (many files, each a few lines). **Risk:** low; deploy to a VM. **Decision:** no.

**D2. Words for people: capitalising, joining, counting, durations.**
- **Where:**
  - First letter upper-cased: `internal/web/backup.go:123` `capitalise` and `internal/web/handlers.go:406` `capitalize` (identical), `internal/node/import.go:608`, `internal/node/install.go:430`, `internal/manager/rollback.go:135`, `internal/manager/copies.go:134`, `internal/setup/setup.go:493`, inline at `internal/manager/backups.go:679`.
  - "a, b and c": `internal/web/serviceupdates.go:171` `joinAnd`, `internal/web/people.go:110` `joinWords`, `internal/manager/accountkey.go:193`, `internal/node/importcheck.go:565` (the last three are token-identical), `internal/manager/arch.go:152` and `internal/web/junk.go:46` `orList`.
  - Counting nouns: `plural` (`internal/web/notify.go:393`, `internal/node/details.go:360`), `countWords` (`internal/web/photos.go:1001`), `countOf` (`internal/web/relaystats.go:276`).
  - `firstLine` three times (`internal/web/notify.go:400`, `internal/setup/setup.go:557`, `internal/manager/backups.go:695`).
  - Five duration formatters in `web` alone: `tookText`, `roughDuration`, `durationText`, `idleText`, `whenText`. The *template* function `durationText` (`internal/web/photos.go:1152`) calls `roughDuration`, not the Go `durationText`.
- **Why:** the project's writing rules (British spelling, plain sentences) are applied in a dozen places, each with its own edge cases. A newcomer can't tell which to use.
- **Change:** a small `internal/words` beside `internal/units`: `Capitalise`, `And`, `Or`, `Count(n, one, many)`, `FirstLine`, and one or two duration styles with names that say which. The template funcs call the same functions.
- **Effort:** S–M. **Risk:** low; wording must not change, so compare page output before and after (§11). **Decision:** yes (the name, and whether to pick one duration style).

**D3. Four "error a person can fix" types.**
- **Where:**
  - `node.FormError` (`internal/node/local.go:119`; `manager` aliases it at `manager.go:42`);
  - `setup.CheckError` (`internal/setup/setup.go:34`);
  - `mail.FormError` (`internal/mail/mail.go:79`);
  - `photoimport.KeyError` (`internal/photoimport/immich.go:137`).
  - All four are `struct{ Msg string }`.
  - `web.userMessage` (`internal/web/wizard.go:684`, 57 call sites) recognises the first two. `internal/web/email.go:112, 141` handle `mail.FormError` separately, and `node/photos.go:1179` converts `KeyError`.
  - Some handlers return a bare `errors.New("A sentence.")`, which `userMessage` treats as a server error (`internal/web/people.go:136`, `settings.go:515-522`, `signinlook.go:181-289`).
- **Why:**
  - CLAUDE.md's rule ("`FormError` (`manager.formErrorf`, `setup.CheckError`)") is the convention every feature follows, but it's three types plus exceptions. It's the first thing a contributor adding a form needs.
  - `manager.formErrorf` and `node.formErrorf` (`internal/node/local.go:123`) are two copies.
- **Change:** one leaf type, e.g. `internal/usererr.Error` with `usererr.New(format, args…)`. Make the four existing types aliases or replace them; `userMessage` checks one thing; update CLAUDE.md.
- **Effort:** S. **Risk:** low. **Decision:** yes (the name).

**D4. "Is this path inside that folder?", and service names: security-relevant, written several ways.**
- **Where:**
  - Two functions called `within` with different edge cases: `internal/instance/instance.go:350` trims a trailing slash; `internal/importer/match.go:393` special-cases `/`.
  - Inline copies at `internal/manager/restore.go:305`, `internal/node/importcheck.go:401`, `internal/labrestore/restore.go:159`, `internal/agent/folder.go:122`, `internal/agent/library.go:406`, `internal/web/servicesettings.go:40`.
  - Service names are validated by `instance.ValidName` (`internal/instance/instance.go:130`, up to 40 characters), `pkgspec` (`{0,30}`, `internal/pkgspec/validate.go:16`), `manager.validName` (`internal/manager/manager.go:976`, looser), `node` `safeService` (`internal/node/move.go:78, 89`, no length limit) and the agent's `serviceName` (`internal/agent/library.go:50`, `{0,62}`).
  - Also `agent.whoName` (`internal/agent/photos.go:17`) equals `auth.usernameRe`.
- **Why:** each check guards a filesystem or privilege boundary. Differing edge cases are how one of them ends up wrong.
- **Change:**
  - One tested `Within(path, dir)`, e.g. in the `fsutil` of D1.
  - Every service-name check calls `instance.ValidName`.
  - Where a looser rule is deliberate (the agent accepts what any version might send), say so in a comment.
- **Effort:** S. **Risk:** low. **Decision:** no.

**D5. Small pairs, and the standard library reinvented.**
- **Identical bodies:**
  - `oldContainer` (`internal/node/importcheck.go:414` = `internal/web/import.go:571`);
  - `newPassword` (`internal/manager/backups.go:685` = `internal/labnet/guests.go:599`);
  - `fstabUnescape` (`internal/agent/library.go:711` = `internal/node/libwatch.go:181`);
  - `labnet.HashToken` (`internal/labnet/join.go:97`) = `authkit.HashToken`;
  - `serviceViewOf` (`internal/web/import.go:271`) and `photoServiceView` (`internal/web/photos.go:1130`) differ by one flag;
  - `hostOnly` (`internal/web/signin.go:133`, `internal/public/waf.go:111`) and `printable` (`internal/importer/setup.go:198`, `internal/agent/drives.go:113`).
- **The standard library already has:**
  - `cmpOr` (`internal/importer/setup.go:210`, `internal/public/attacks.go:89`) is `cmp.Or`;
  - `b2.contains` (`internal/b2/b2.go:240`) is `slices.Contains`;
  - certificate serials made by hand (`internal/labnet/ca.go:244`, `internal/web/firstrun.go:83`) can be left nil: since Go 1.24 `x509.CreateCertificate` makes an RFC 5280 one;
  - `if _, err := rand.Read(b); err != nil` can't fail since Go 1.24;
  - `c := c` (`internal/web/plan.go:427`) is unneeded since Go 1.22.
- **Lists defined twice:**
  - `agent.FolderRoots` and `LibraryFolderRoots` (`internal/agent/folder.go:32`, `library.go:54`);
  - `caddy.RemoteHeaders` and `sso.Headers` (`internal/caddy/caddy.go:134`, `internal/sso/forward.go:25`; a test keeps them equal);
  - `public.cgnat` (`internal/public/public.go:123`) is `trust.CGNATRange`.
- **Change:** one copy each.
- **Effort:** S. **Risk:** low. **Decision:** no.

**D6. Facts spelt out in several places.**
- **Where:**
  - A service's data folders are listed five times (`internal/manager/manager.go:891`, `internal/node/move.go:81`, `internal/node/leftdata.go:36`, `internal/manager/move.go:335`, `internal/manager/restore.go:304`).
  - Backup destination references (`kind:id`) are parsed in nine places in `manager`.
  - "An empty host means the controller" appears 15 times.
  - `roostack.db` is spelt out in four places.
  - The setup token's path is spelt out in `internal/web/setup.go:66` and `labrestore.go:149`.
  - `agent.SystemBackupDir` (`internal/agent/drives.go:65`) hard-codes `/srv/roostack/...`, which `config.Paths.BackupStoreDir()` also gives. They part if `media_dir` changes, despite CLAUDE.md's rule that paths come from `config.Paths`.
- **Change:**
  - `Paths.ServiceDataDirs(name)`, `Paths.DatabaseFile()` and `Paths.SetupTokenFile()`.
  - A `destination.Ref` type with `Parse`.
  - `Instance.HostOr(controller)`.
  - For the agent (which may not import `config`'s defaults), a comment saying why, or pass the path in the request.
- **Effort:** S. **Risk:** low. **Decision:** no.

**D7. JSON kept in the settings table, read and written by hand.**
- **Where:**
  - `node.loadSetting` and `saveSetting` (`internal/node/libraries.go:147-166`) exist, but `saveSetting` swallows errors and always logs "recording the media libraries" whoever calls it.
  - Hand-written copies: `internal/node/photos.go:345, 836, 1100`, `import.go:203`, `install.go:312-344`, `ssohooks.go:61-75`, `internal/manager/photos.go:96`, `update.go:343`, `quarantine.go:231`, `internal/osupdate/series.go:223` and `osupdate.go:314, 357`.
- **Change:** generic `store.GetJSON[T](kv, key)` and `PutJSON` that return errors.
- **Effort:** S. **Risk:** low. **Decision:** no.

**D8. Loops and waits.**
- **Where:**
  - About 15 hand-written "every d until ctx ends" loops in Roostack (e.g. `cmd/roostack/serve.go:514`, `controller.go:125`, `internal/cloud/relay.go:254, 314`, `internal/osupdate/osupdate.go:277`, `internal/labnet/serve.go:39`) and about 14 in Cloud (S16).
  - Transient units are all named `roostack-backup-<pid>-<n>` (`cmd/roostack/backup.go:315` `userLaunch`), including image pulls and CrowdSec queries. `systemctl --user list-units` during an install then shows a "backup", the kind of surprise the project means to avoid.
- **Change:** a small `Every(ctx, d, fn)` in each repository; name a transient unit after what it runs.
- **Effort:** S. **Risk:** low. **Decision:** no.

**D9. DNS: each provider's TXT check written beside its record code.**
- **Where:**
  - `internal/dns/probes.go` (288 lines) creates and deletes a TXT record per provider, which `records.go`'s `recordsAPI` already does: the Cloudflare zone lookup is at `probes.go:153-162` and `records.go:132-143`, Route 53's change XML at `probes.go:175-208` and `records.go:202-261`.
  - `dns.fqdn` (`records.go:118`) and `dnsrecords.fqdnOf` (`internal/dnsrecords/dnsrecords.go:586`) are the same.
  - `records.go:126` recognises "not found" by the text `"404 "`.
  - `internal/dns/cloud.go:67` adds a provider to the list from `init()`.
  - `internal/dnsrecords/dnsrecords_test.go:140-200` has its own `fakeGandi` beside `dnstest`.
- **Change:**
  - Make the check `upsert` then `remove`, with each provider's hint text; Namecheap keeps its read-only probe.
  - Put the Cloud provider in the slice literal.
  - A typed not-found error.
  - Check two differences on the way: Porkbun's probe creates where records upserts, and Cloudflare's records add a comment.
- **Effort:** M. **Risk:** medium (`dnstest` covers every provider). **Decision:** no.

**D10. S3: one listing signed by hand.**
- **Where:**
  - `internal/labrestore/find.go:467` `s3List` signs `ListObjectsV2` itself, with a hard-coded empty-body hash, beside `internal/s3api`, which has no `List`.
  - `awssig.Sign` hashes the payload (`awssig/sign.go:27`), yet both S3 callers set `X-Amz-Content-Sha256` by hand, and `s3api.go:262` hashes the body twice.
- **Change:** `s3api.Client.List`. Optionally have `Sign` set the header for service `s3`: that's a public API Cloud also uses, so it's the Owner's call.
- **Effort:** S. **Risk:** low. **Decision:** for `awssig` only.

### 3.2 Roostack: bigger copies inside one feature

**D11. Move through a backup and restore are two copies of one procedure.**
- **Where:** `internal/manager/move.go:221-349` (`finishMove`, `moveViaBackup`) and `internal/manager/restore.go:274-378` (`restoreFrom`).
  - Both: start it again if we fail after stopping it; the snapshot-dump loop (`move.go:335-347`, `restore.go:304-317`); secrets; `CommitData`; `switchHost` and `waitApplied`; `LoadDumps`; `SetAsideData`.
  - They've drifted: only restore clears leftovers first; they detect a missing folder differently; and P3.
  - `move.go:221` and `:295` take 9 parameters, `:148` 8.
- **Change:** one `transferJob` struct used by move, move through a backup, restore and the lost-server restore (`internal/manager/lost.go:224`). The struct also replaces the long parameter lists.
- **Effort:** M. **Risk:** medium: the tests are real restic ones; run them and a VM move and restore. **Decision:** after P3.

**D12. Two "kept copy" systems.**
- **Where:**
  - Restore keeps `<dir>.before-restore-…` folders (`internal/backup/restore.go:238-335`: `KeptCopy`, `KeptCopies`, `PutBack`, `RemoveKept`; routes `internal/node/api.go:214-243`).
  - Moves and imports keep `<service>.before-move-…` (`internal/node/move.go:523-640`: `KeptFolder`, `KeptData`, `PutBackKeptData`, `RemoveKeptData`; routes `api.go:466-494`).
  - Putting back is stop, rename, rename, start in both. Removing is `podman unshare rm -rf` after an allow-list check in both.
- **Change:** one type with a `Why` field and one API.
- **Effort:** M–L. **Risk:** medium. **Decision:** yes (how the pages show kept copies).

**D13. The web layer's folder picker, four times.**
- **Where:**
  - `mediaFoldersPage` (`internal/web/media.go:338`), `photoFoldersPage` (`internal/web/photos.go:562`), `importFoldersPage` (`internal/web/import.go:628`), and partly `importBrowsePage` (`:709`): about 300 lines. Only the URLs, the server and the template name differ.
  - `server := ps.Host` (`photos.go:613`) followed by `_ = server` (`:636`) is left over from copying.
  - Breadcrumbs are built by hand 17 times.
  - The source forms are copied too: `importSource` (`import.go:319`) and `photoSource` (`photos.go:402`) in Go. The share fields' markup appears in `settings-media-add.html:65-75`, `import.html:361-370` and `photos.html:357-366`.
- **Change:** a `picker` helper that fills the shared `folder-picker` template's data, a `crumbsFor`, a `shareForm` type and a `share-fields` template part.
- **Effort:** S–M. **Risk:** low; compare the pages' HTML before and after (§11). **Decision:** no.

**D14. Smaller web copies.**
- **Where:**
  - The "sweep expired entries" loop over a map is written five times (`internal/web/handlers.go:192`, `signin.go:458`, `pagessignin.go:398`, `setup.go:77`, `firstrun.go:250`).
  - `a.Running() || time.Since(a.Finished) < d` appears five times (`pages.go:207, 412`, `plan.go:228, 363, 692`).
  - Email is validated three ways with three messages (`email.go:175`, `users.go:79`, `people.go:276`).
  - The settings page and the server switcher each summarise updates in different words: "A restart is needed…" (`settings.go:202-220`) against "Needs a restart…" (`switcher.go:39`).
- **Change:** an `expiring[K,V]` map type; `activityView.Recent(d)`; one `emailProblem`; one update summary.
- **Effort:** S. **Risk:** low. **Decision:** for the wording only.

### 3.3 Between Roostack and Cloud

Cloud imports `authkit`, `cloudapi` and `awssig` from Roostack. What else is shared, and what shouldn't be:

**X1. Byte sizes: five formatters with five answers.**
- **Where:** Roostack's `internal/units/units.go:18` `Bytes`; Cloud's `cloud:internal/web/portal.go:820` ("under 1 MB"), `cloud:internal/admin/admin.go:222` (`%.2f TB`), `cloud:internal/storage/meter.go:255` (P8), and `cloud:cmd/cloud/storage.go:213` (no TB, so 1.5 TB is "1500.0 GB"). `cloud:internal/admin/admin.go:237` `thousands` duplicates `units.Count`.
- **Change:** move `internal/units` to a public top-level `units` (43 lines, standard library only). If the portal's "as storage is sold" style is wanted, add it there as a second function.
- **Effort:** S. **Risk:** low. **Decision:** yes (a new public package).

**X2. The design system's copies in Cloud are stale, and one token has drifted.**
- **Where:**
  - The copies are scripted (`cloud:tools/sync-design.sh`, `sync-brand.sh`, `sync-editor.sh`), which is good.
  - Cloud's `internal/web/static/css/app.css` was synced from `bcdedeb` on an unmerged branch (`claude/modest-poincare-0a90ce`). It's 941 lines against Roostack's 1,392, lacks `.segmented`, `.inline-form` and `summary.button`, and its `app.js` lacks the dialog code.
  - The site's tokens are from `51a7674` (P14).
  - `--edge-strong` is 17% in Roostack (`internal/web/static/css/tokens.css:99`) and redefined as 16% in `cloud:site/src/styles/home.css:16`. `--site-bg` (`home.css:9`) is Roostack's `--canvas` under another name. `--glass`, `--glass-2` and `--edge` are redefined with the same values.
  - Cloud's icons are a copy of 20 of Roostack's, 6 under new names, and `close` has drifted. `cloud:internal/web/icons.go:8-11` says "Go can't share them across modules", which is true only of `internal/` packages and embeds.
- **Change:**
  - A CI job in Cloud that runs the three sync scripts against the checked-out Roostack and fails (or warns) on `git diff --exit-code` (G1).
  - Have the scripts refuse a dirty source tree and sync from a commit on `main`.
  - Delete the site's redefinitions.
  - Fix the comment.
  - Sharing through a public package exporting an `embed.FS` would end the copying, but not before the build is pinned (X5).
- **Effort:** S. **Risk:** low. **Decision:** whether drift blocks CI or only warns.

**X3. Mail.**
- **Where:** `internal/mail/mail.go:117, 197` and `cloud:internal/mail/mail.go:106, 154`: the same SMTP dialogue, STARTTLS rule and headers. Cloud's encoding is weaker (P10).
- **Change:** port the encoding now. A shared public `mailkit` is possible but not needed yet.
- **Effort:** S (port), M (share). **Risk:** low. **Decision:** for sharing.

**X4. Two-factor sealing reaches into `authkit`'s format.**
- **Where:** `authkit.Sealer` is used only by Cloud. `cloud:internal/totpseal/totpseal.go:31` binds a secret to its owner by sealing `owner + "\n" + secret`, and hard-codes `authkit`'s unexported `"sealed1:"` prefix.
- **Change:** `authkit.Sealer.SealFor(owner, secret)` and `OpenFor`, with the owner as associated data, plus an exported `IsSealed`.
- **Effort:** S. **Risk:** medium: existing sealed values must still open, so keep reading the old form. **Decision:** yes (public API).

**X5. Cloud builds against whatever Roostack is on disk.**
- **Where:**
  - `cloud:go.mod:13` requires the zero pseudo-version of `roostack.cloud/roostack`; the per-machine, gitignored `go.work` replaces it with an absolute path.
  - CI checks out Roostack's moving `main`.
  - `cloud:ops/deploy.sh:97, 117-119` builds the production image with the local checkout, dirty trees and other branches included.
  - `buildVersion` (`cloud:cmd/cloud/adminconsole.go:253`) reports only Cloud's commit.
  - The go.work in a main checkout points at the main Roostack checkout, so Cloud is tested against whatever branch that is on.
- **Why:** nothing records which Roostack a running Cloud was built from. Every package moved under X1, X3 or X4 widens this.
- **Change:**
  - Now: `deploy.sh` refuses a dirty Roostack tree and stamps Roostack's commit into the binary beside Cloud's; CI pins a Roostack ref in a file both CI and deploy read.
  - At launch, once Roostack is public: require a real pseudo-version and make `go.work` optional, for changing both at once.
- **Effort:** S now, S at launch. **Risk:** low. **Decision:** yes.

**X6. Smaller cross-repository copies.**
- `authoritativeTXT` is in `internal/cloud/check.go:94` and `cloud:internal/names/domains.go:109`. Cloud's has an NXDOMAIN fix: port it back (S).
- `cloud:internal/web/qr.go` is an identical 30-line copy of Roostack's: fine to keep.
- The Content-Security-Policy string and `sameOrigin` are in `internal/web/server.go:995`, `cloud:internal/web/server.go:412` and `cloud:internal/admin/admin.go:380`.
  - Go 1.25's `http.CrossOriginProtection` could replace all three, but it lets through a request with neither `Origin` nor `Sec-Fetch-Site`, which Roostack refuses today.
  - Keep them, and share Cloud's two through S15's package. **Decision:** whether the standard library's rule is acceptable.
- The password gate (`gate.js`) is in both `cloud:site/worker/` and Roostack's `help/worker/`, identical; only Cloud's is tested. Test Roostack's too, or have one sync the other (S).
- The two CSP builders for static sites have drifted: Node in `cloud:site/tools/headers.mjs`, inline Python in `scripts/help-deploy.sh:20-47`. The help site lacks the website's `Permissions-Policy` and COOP headers (S, decision).

### 3.4 The lab ↔ Cloud contract

**X7. Roostack's fake Cloud has drifted from the real one, and nothing tests both against the same rules.**
- **Where:**
  - `internal/cloud/cloudtest/fake.go:461-468`: with `SuspendedFor` set, the fake answers 423 to everything except `PathLab`, `PathLabReport` and `PathLabRenew`. Real Cloud (`cloud:internal/api/api.go:150-165`) also serves `POST /v1/lab/storage/keys` and `GET /v1/lab/storage` while suspended, and storage keys then follow the suspension's backups choice (`cloud:internal/abuse/abuse.go:104`).
  - So Roostack can't test "suspended, but backups carry on" or "restore only", and its storage page gets 423 from the fake where Cloud gives 200.
  - The fake has no per-lab write limit (429).
  - `cloudapi.StorageRequest.Server` is documented as the server's name, but Roostack sends its ID (`internal/manager/backups.go:314`).
  - Roostack's 2,500-line Cloud client has only ever been tested against the fake.
- **Why:** the paths and bodies are shared through `cloudapi`, but the access rules (certificate, suspension, writes) aren't stated anywhere both sides read.
- **Change:**
  - A route table in `cloudapi`: method, path, needs the lab certificate, allowed while suspended, counts as a write.
  - Cloud's test checks its mux against the table; the fake uses the table.
  - Fix `StorageRequest.Server`'s comment.
  - Later, if wanted: a `cloudapi/cloudapitest` set of scenarios run against both the fake and the real server.
- **Effort:** S–M. **Risk:** low. **Decision:** yes (what belongs in the public contract).

### 3.5 What not to share

These look alike but shouldn't be merged:
- **Cloudflare DNS clients** (`internal/dns/records.go`, `cloud:internal/dnszone/cloudflare.go`): different shapes and threat models.
- **The two lab CAs:** different designs.
- **The S3 fakes** (`internal/s3api/s3fake`, 274 lines; `cloud:internal/storage/storagetest`, 760 lines, with STS and a policy evaluator).
- **The two S3 clients** (about 150 lines overlap): revisit only if Roostack ever needs session tokens.
- **Sessions and two-factor in Cloud's website and console:** different tables, cookies and steps.

Rate limiting, passkeys and the error scenes exist in one repository only. Passwords, tokens and TOTP all go through `authkit`, with nothing left over.

---

## 4. Size and shape

Of Roostack's 3,306 functions, 58 are over 80 lines. The longest:
- `node.Server.Handler`: 610 lines.
- `serve`: 574.
- `agent.Server.Handler`: 368.
- `web.Server.attention`: 310.
- `web.Server.handler`: 266.
- `runImportCheck`: 203.
- `reconcile.Plan`: 181.

Cloud's longest worth splitting:
- `admin.Server.usage`: 233.
- `adminCommand`: 185.
- `names.ClaimName`: 137.
- `names.SetRelay`: 118.
- `web.ask`: 118.
- `api.relayStats`: 105.

Many long functions are linear and well commented (`reconcile.Plan`, `generate`'s, the route tables) and are fine as they are.

### 4.1 Roostack's web layer

**S1. `web.Server` is a god object.**
- **Where:**
  - `internal/web/server.go:110`: 60 fields (42 exported), about 20 of them optional ("nil hides it"), set field by field after `NewServer` in `cmd/roostack/serve.go:140-175`. One `mu` guards unrelated state (lab name, setup token, server-issue cache, pending logins, hand-offs, setup passes, step-up failures).
  - `ServiceManager` (`server.go:262`) has 110 methods across 11 sub-interfaces. `fakeManager` implements 131 methods spread over six test files.
  - There are 54 `s.Manager == nil` checks, though production always sets it (`serve.go:171`), and `pages.go:603` calls `s.Manager.ImportsOf` without one.
- **Change:**
  - Required dependencies become constructor arguments, and the nil checks go.
  - Group the optional ones by feature into small structs, each with its own mutex.
  - Each feature file uses its own sub-interface (they're already named), and `fakeManager` becomes embedded per-area fakes in `fakes_test.go`.
- **Effort:** M. **Risk:** low. **Decision:** which dependencies are required.

**S2. Background jobs and rules live in the web package.**
- **Where:**
  - The email notifier (`internal/web/notify.go:124` `RunNotifier`, `:152` `notify`) builds a fake `http.Request` (`:307`, `:432`) to call `s.attention(r)`, and sorts notices into "waiting for you" by matching the words "needs a restart" (`notify.go:301`). Other notices get a stable key by stripping numbers and month names with a regular expression (`:287-296`). `attentionItem.Key` exists for this, but only 6 items set it.
  - The 3-2-1 backup rules are computed in `internal/web/plan.go:276-327`.
  - The server's location is read and written straight from the settings table (`location.go:24-64`), under a key `manager` also reads (`internal/manager/links.go:41`).
  - The low-space thresholds are in `space.go:21-24`.
  - Second-copy naming and default placement are in `services.go:372-419`.
  - A service's status precedence is computed in `cmd/roostack/serve.go:657-700`, with constants scattered across `server.go:62-69`, `serviceupdates.go:34` and `withdrawals.go:28`.
- **Why:** ARCHITECTURE's first rule is "business logic stays out of HTTP handlers". These are the rules a contributor would look for in `manager` and not find, and the notifier's grouping breaks silently when a sentence is reworded.
- **Change, in steps:**
  1. `attention(ctx, u)` instead of `attention(r)`, and a `Group` and `Key` on every item (S).
  2. `manager.PlanChecks`, `ServerLocation` and `SetServerLocation` (M).
  3. One `status.go` for service status, tested where the fakes are (S).
  4. Later, if wanted: the attention rules and the notifier in their own package, leaving only rendering in web (M–L).
- **Risk:** low. **Decision:** for step 4.

**S3. Every page asks the whole lab for its services.**
- **Where:** `pageData` (`internal/web/pages.go:178`) calls `s.Services`, which loads every state file, asks every node, reads withdrawals and updates, and runs `generate.Service` for each service (`cmd/roostack/serve.go:657`). It does this on all 37 pages that use it, folder pickers and settings pages included. Only the Services sidebar uses the list (`server.go:1170`). Some handlers call it again: `photos.go:222` (with `context.Background()`), `:348`, `:1131`, `import.go:272`.
- **Change:** fetch it lazily, and add one `serviceByName`.
- **Effort:** M. **Risk:** low. **Decision:** no.

**S4. Page data as untyped maps, and page metadata in four places.**
- **Where:**
  - 133 `map[string]any{` literals and 32 type assertions back out (e.g. `pages.go:487, 531, 543`); newer pages use typed structs (`photoPage`, `importPage`, `planView`).
  - `installData` (`services.go:330`) returns a 27-key map.
  - A page is registered in `NewServer`'s template list (`server.go:552-562`), `navTab` (`pages.go:105`), `navFor` (`pages.go:71`) and `wizardSteps`, sometimes also `firstRunPaths` (`firstrun.go:352`).
  - Cloud has the same pattern (130 maps).
- **Change:**
  - A `basePage` struct embedded in per-page structs, converted as pages are touched.
  - A test helper, in both repositories, that fails when a rendered page contains `<no value>`.
  - One `[]pageInfo` table.
- **Effort:** M, gradual. **Risk:** low. **Decision:** no.

**S5. Template functions are built by eight `init()`s.**
- **Where:** `templateFuncs` (`internal/web/backup.go:23`) is mutated by `init()` in `components.go:59`, `install.go:25, 268`, `media.go:23`, `photos.go:1143`, `public.go:532`, `serviceupdates.go:36` and `sso.go:23`. `components.go:67` needs `sync.OnceValues` only because of the order these run in.
- **Change:** one `funcs.go` with one map literal.
- **Effort:** S. **Risk:** low. **Decision:** no.

**S6. Where core helpers live.**
- **Where:**
  - `requireOwner` and `requireWizard` are in `wizard.go:41-62`; `userMessage` in `wizard.go:684`; `templateFuncs` in `backup.go:23`; `capitalize` in `handlers.go:406`, which is really the login code.
  - First-run setup is spread over `handlers.go`, `setup.go`, `firstrun.go` and `wizard.go`.
  - `pages.go` mixes navigation, home, the services list, the service page, backups, account and partials.
  - `attention`'s helpers take two different callback shapes; `updatesAttention` and `updateAttention` are different things one letter apart.
- **Change:** pure moves: `middleware.go`, `render.go`, `login.go`, `servicepage.go`; an `attentionList` type with `add`; a rename.
- **Effort:** S–M. **Risk:** none. **Decision:** no.

**S7. Should `internal/web` split into sub-packages? Not now.**
- **Why not:** feature sub-packages would need `render`, `pageData`, the middleware, `currentUser`, `userMessage`, navigation and the template file system exported from a base package. That's several days for little more than S1, S5 and S6 give.
- **Cheap candidates later:** `htmlfmt.go` (only the gallery uses it), `icons.go`, `countries.go`, `gzip.go`, `qr.go` and D2's words.
- **Decision:** yes.

### 4.2 Roostack's controller

**S8. `manager.Manager` does about ten jobs.**
- **Where:**
  - `internal/manager/manager.go:56-174`: about 30 dependency fields, about 25 state fields and five mutexes; 162 exported methods (306 in all) across 29 files.
  - The package doc (`manager.go:1-4`) still describes only the catalog, install and apply.
  - The seams already exist as the web layer's sub-interfaces. Three features barely touch the shared core:
    - photos (`photos.go`): `installedService`, `node`, `Settings`, the libraries;
    - storage for other labs (`guests.go`, `guesthost.go`);
    - recovery key changes (`keychange.go`).
- **Smaller simplifications:**
  - `applyReason` is a parameter passed through a struct field before `startApplyLocked()` (29 call sites, `servers.go:171-182`): pass it as an argument.
  - Three runtime copies of `lab.yaml` values: `public` (`public.go:171`), `noStops` (`withdrawals.go:75`), `statusPage` (`manager.go:151`). Keep one `atomic.Pointer[config.Lab]` that `UpdateLab` swaps after saving.
  - Three package-level maps that should be fields: `hiddenCache` (`destination.go:184`), `switching` (`import.go:52`), `rollbackChecks` (`rollback.go:145`).
  - The secret checks at install and on retry are duplicated (`manager.go:349-358`, `retry.go:56-63`).
  - The server-naming helpers live in `backups.go:642-682`.
- **Change, in steps:**
  1. Rewrite the package doc as a map of files to features (S).
  2. The smaller simplifications (S each).
  3. Group fields by feature (M).
  4. Move photos, guests and key changes into their own types, each taking a small interface for the shared core (L).
- **Risk:** low for steps 1–3. Step 4 is a mechanical move with many merge conflicts while parallel sessions are active. **Decision:** for step 4.

**S9. Each node operation is written in four places.**
- **Where:**
  - An operation needs a method on an interface (e.g. `Restores.Kept`, `internal/node/maintenance.go:22`), the local code (`internal/backup/restoreservice.go:94`), a client method (`remoteRestores.Kept`, `internal/node/remote.go:395`), a route (`api.go:214`) and fakes.
  - `Server.Handler` (`internal/node/api.go:63-672`) is one 610-line function of about 40 routes. Each repeats "get the capability, or not found", then a decode.
  - The bodies are anonymous on both sides. They match only because Go's JSON decoding ignores case: `RunAction` replies `"output"` (`api.go:156`) and the client decodes `struct{ Output string }` (`remote.go:324`); also `"value"` (`:160`), `"changed"` (`:599`), `"Deleted"` (`:465`).
  - Small inconsistencies: history cull has its own 4,096-byte limit (`api.go:461`); `updater()` gives a 500 where the others give 404 (`:534`).
  - `TestRemote` (`internal/node/remote_test.go:140`) covers about a fifth of the API: none of backups, restores, OS updates, `Mover` (`remote.go:599-711`), `Updater` (`:715-739`) or imports. `manager`'s tests use a fake remote, so a path typo passes every test.
  - `importwire.go` (`:75-141`) already shows the better shape: client and routes side by side, a small helper, one path builder.
- **Change:**
  1. A table test that calls every `Remote` method against a `Server` wrapping a recording fake (S).
  2. A `<feature>wire.go` per capability, like `importwire.go`, with named request and response types and a generic `capRoute[T]`/`body[T]` helper (M).
  - Keep the JSON names as they are: servers in a lab can briefly run different versions.
- **Risk:** low once step 1 exists. **Decision:** no.

**S10. Two ways to ask a node for a capability.**
- **Where:**
  - Some capabilities are `Node` methods that return nil when absent (`Backups`, `Restores`, `Updates`, `Settings`, `Media`, `Imports`, `internal/node/local.go:51-62`).
  - Others are type assertions (`Mover`, `Updater`, `Disks`, `History`, `Linked`, `GuestHost`, `Memory`, `LeftData`, `Processor`): about 20 assertions in `manager` and `web`, including an anonymous `n.(interface{ Photos() node.Photos })` at `internal/manager/photos.go:83`.
  - `Remote`'s nil-returning methods quietly check the cached status first (`remote.go:333-359`).
- **Change:** one convention, written on `Node`'s doc: every capability a `Node` method that returns nil when absent.
- **Effort:** S–M. **Risk:** low. **Decision:** yes.

**S11. `node.Local` has 201 methods.**
- **Where:** `internal/node/local.go:140-251`: about 30 dependency and 25 state fields. Photos (`photos.go`, 1,222 lines) and imports (`import*.go`, about 1,800) hang on `Local`, and `Photos()` and `Imports()` return the node itself (`photos.go:305`, `import.go:179`). `Remote` already uses separate types (`remoteBackups`, `remoteImports`).
- **Change:** make photos and imports their own types, as `Remote` does.
- **Effort:** M. **Risk:** low. **Decision:** no.

### 4.3 Roostack's other large pieces

**S12. `serve()` is 574 lines.**
- **Where:**
  - `cmd/roostack/serve.go:48` wires about 15 subsystems in one function. The order matters and isn't visible. A package-level mutable `cloudBackupSource` (`cmd/roostack/backup.go:172-182`) exists only because the backup repository is built before the Cloud link.
  - Untested logic lives in `main`: `serviceViews`' status rules (`serve.go:657`), `setupToken` (`:630`) and `pullImage` (`local.go:111`, which parses Podman's error text).
  - `startMaintenance` is 162 lines with 7 parameters.
- **Change:** a `controller` struct with `wireCloud`, `wireNetwork`, `wireBackups`, `wireSetup`, `wireDNS`, `wireMonitoring` and `listen`; `serviceViews` into `web` or `manager`, where fakes can test it; `pullImage` into `podman`, with a test.
- **Effort:** M. **Risk:** low; deploy to a VM. **Decision:** no.

**S13. The agent's `Handler` is 368 lines of three repeated shapes.**
- **Where:** `internal/agent/agent.go:134-503`, 33 routes.
  - Some routes that change something log and others don't (DELETE imports and photos, PUT/DELETE library-views and POST libraries/folders are silent).
  - Any error from `UseDrive` or `MountLibrary` is a 400, internal failures included.
  - Names are checked in `LinuxHost` (`import.go:39`, `photos.go:37`, `library.go`), other values in the handler, so the fake-host tests can't show a bad name is refused at the boundary.
  - The server decodes anonymous structs (`agent.go:448`) where the client sends `map[string]string` (`client.go:188`).
  - `agent.go` also holds `LinuxHost`'s firewall, time zone, update, reboot and WireGuard methods (`:697-1003`), although `firewall.go`, `wireguard.go` and `updates.go` exist.
- **Change:**
  - Generic `route[Req, Resp]` and `action[Req]` helpers, so the privilege boundary fits on one screen.
  - Named request types shared by client and server.
  - Every route that changes something logs.
  - Validation at the boundary.
  - Move `LinuxHost`'s methods to their files.
- **Effort:** M. **Risk:** low; it's the privilege boundary, so verify on a VM. **Decision:** no.

**S14. `store.go` is 1,745 lines, with 37 migrations.**
- **Where:**
  - `internal/store/store.go` holds every table except `sso.go`, `monitor.go` and `libraries.go`.
  - The 37 migrations include renames (#13, machines → servers) and a data fix-up (#23) from before any release.
  - `CreateSession`, `SessionInfo` and `SessionUser` (`:943, 988, 1025`) have no callers outside tests; `RecentAudit` (`:1079`) is test-only.
  - The row-scan loop is written 21 times.
- **Change:**
  1. Split by table: `schema.go`, `users.go`, `sessions.go`, `audit.go`, `settings.go`, `servers.go`, `backups.go`, `guests.go`. This is a pure move (S).
  2. Optionally a generic `queryAll[T]` (S).
  3. Squash the migrations to one schema before the first release, or generate a `schema.sql` that a test keeps current (S). CLAUDE.md already says only fresh installs are supported.
- **Risk:** none for the split; low for the squash. **Decision:** for the squash.

### 4.4 Cloud

**S15. The website and the console copy each other's plumbing.**
- **Where:**
  - `sameOrigin`: `internal/web/server.go:412` = `internal/admin/admin.go:380`.
  - The security headers and CSP: `server.go:428-445` and `admin.go:390-406`, with two more CSP variants in `admin/monitor.go:75, 93`.
  - `render` (`server.go:355`, `admin.go:307`), `serverError` (`:400`, `:338`), `redirect` (`:406`, `:343`).
  - `limited`: `server.go:146`, `admin.go:347` and `api.go:101`.
  - The template funcs `initial`, `date` and `day`: `server.go:164-185` and `admin.go:163-175`.
  - `truncate`: `web/session.go:50` and `admin/auth.go:411`.
  - The passkey ceremony code: `web/passkeys.go:42-87, 148-168` and `admin/passkeys.go:29-66, 114-126`. This copy has already drifted (P2).
  - Two `localPath`s with different rules: `web/session.go:59` returns `""` and checks `//`, `\` and CR/LF; `admin/auth.go:111` returns `/` and uses `url.Parse`.
  - `admin` imports `web` (`admin.go:31`) for icons, QR codes, the brand mark, times, static files and `Wait`.
- **Change:** an `internal/ui` package with `SameOrigin`, `SecurityHeaders` (with options), one CSP, `Render`, `ServerError`, `Redirect`, `LocalPath` and the base `FuncMap`. The design-system pieces move there too, so neither web package imports the other; then merge the passkey ceremonies' HTTP half. Keep sessions and two-factor separate (§3.5) and cross-reference them.
- **Effort:** M. **Risk:** low. **Decision:** no.

**S16. Cloud's helpers written several times.**
- `web.Wait` (`internal/web/server.go:191`, hours from 90 minutes) and `limit.Wait` (`internal/limit/limit.go:161`, hours from 120). Both are used on the website (`accounts.go:303`, `server.go:150`). Delete `web.Wait`.
- Four JSON reply pairs (`web/passkeys.go:42, 49`, `admin/passkeys.go:29, 36`, `api/api.go:186, 191`, `names/http.go:37, 43`). Bodies are decoded three ways, with limits of 256 KiB, 4 KiB and 64 KiB. Make one `JSON`, `JSONError` and `ReadJSON`.
- The relay's statistics are computed twice (P9), and there are two SVG chart builders (`admin/chart.go`, `web/portal.go:561-575`).
- "Email the Owner and the lab's recipients" is written four times: `web/notifications.go:184-219`, `storage/meter.go:235-249`, `admin/abuse.go:57-67, 412-426` and `api/restore.go:170-188` (P11). Make one `labs.Tell` that queues in the caller's transaction, beside `labs.Recipients` (`labs/recipients.go:140`).
- "Commit, then `Outbox.Wake()`" is done by hand in 12 places, including `web/labs.go:109`, which wakes by hand although the web package has a helper for it. Make an `outbox.In(ctx, d, scope, fn)`.
- About 14 periodic loops (`cmd/cloud/main.go:362`, `forconsole.go:52, 88, 143`, `adminconsole.go:351`, `web/notifications.go:125`, `names/domains.go:75`, `names/names.go:799`, `admin/intake.go:81`, `admin/names.go:275`, `storage/meter.go:48`, `edge/run.go:93`, `outbox.go:140`). Make an `Every` helper, and list the jobs in ARCHITECTURE.md (N2).
- **Smaller ones:**
  - `plural` three times;
  - `pgDate` twice;
  - `or` (`admin/intake.go:338`) is `cmp.Or`;
  - `failedLoginDelay` twice, plus a bare 500 ms in `api.go:590`;
  - the date layout "2 January 2006, 15:04 UTC" written four times;
  - `labs`, `names` and `api` call `time.Now()` directly instead of an injected clock;
  - `names.go:750` compiles a regular expression for every label on every call, although `hostLabel` (`relay.go:345`) is the same pattern, compiled once;
  - hostname and label rules come in six shapes (`abuse.go:49`, `names.go:368`, `relay.go:345`, `names.go:750`, `storage.go:64`, `abuse/mail.go:49`), and there are both `abuse.Under` and `abuse.under`;
  - `newCode` (`labs/labs.go:167`) duplicates `randomFrom` (`names/relay.go:368`).
- **Effort:** S each, M together. **Risk:** low. **Decision:** no.

**S17. Cloud's package boundaries.**
- `web` holds account logic and jobs: the continuity hand-over (`continuity.go`, 837 lines, jobs at `:707`), closing an account (`close.go`), switching (`switch.go`) and the hourly jobs (`notifications.go:122-181`). The console reaches it through callbacks (`admin.Server.CloseLabs`). An `internal/accounts` package, moved over as each area next changes (M–L, medium risk).
- API handlers hold business logic: `edgeUsage` (`api.go:606-703`), `plausible` and `counts` (`:705-737`), `relayStats` (`:755-859`). `api_test.go` is 32 lines (coverage comes from e2e). Move it into `edge` or a `relay` package, with table tests (M).
- `names` is five things: names, the DNS writer, the relay, domains and acme-dns. `names.Service` has 20 fields (`names.go:36-81`). Split out `relay` (M; decision, since README and DESIGN describe the package map).
- `labs.FormError` (`labs/labs.go:56`) is everyone's error type, used about 22 times in `names`. Move it to a leaf package, as D3 does for Roostack (S).
- `api.Names` (`api.go:111-119`) has one implementation and no fake, yet there are 13 nil checks (`api.go:406-507`). The comment at `:47` still says "nil until step 4 exists". Use the concrete type and delete the branches (S).
- `cmd/cloud`:
  - Commands come in four signatures, needing three adapters (`main.go:88-110`).
  - `adminCommand` (`admin.go:39-223`) is a 185-line switch of closures, with admin-add's logic inline while admin-disable's is in `admin.Disable`.
  - The file names confuse: `admin.go` is the Owner's CLI, `adminconsole.go` is `cloud admin`, `forconsole.go` is what `serve` does for the console, and `mailtest.go` is a command, not a test.
  - Fix with one signature, one function per subcommand, an `admin.Add` and clearer names (M).
- The control plane is wired twice: in `serve` (`main.go:242-271`) and in the e2e harness (`e2e_test.go:88-146`). The copies already differ: `names.Mail` is a recorder in one and the outbox in the other; e2e's `api.Server` has no `Mail`; e2e builds its own TLS config instead of `api.TLSConfig` (`api.go:127`, untested). `serve` is 2.3% covered. One `controlplane.New(cfg, deps)` used by both (M).
- Abuse handling passes long parameter lists: `suspendByItself` takes 10 (`admin/intake.go:487`), `tellOwner` and `tellOwnerAs` 9 (`abuse.go:407, 412`). A case-context struct would carry them (S).
- `admin/monitor.go:27` keeps a page template in a Go string, unlike every other page (S).

---

## 5. Tests

### 5.1 Running them on a Mac

**T1. Eight lines stop 13 Roostack packages building on a Mac.**
- **Where:** `internal/agent/agent.go:598-601` (`SO_PEERCRED`, and the pidfd code in `peerInHostNS`), `internal/agent/drives.go:294` (`Statfs_t.Bsize` is `uint32` on darwin; the same bug is in `internal/node/guesthost.go:42`), `internal/agent/folder.go:94, 97` (`Mkdirat`, `Openat`) and `internal/monitor/probe.go:204` (`SOCK_CLOEXEC`).
  - Twelve packages import `agent` for its types only, so `cmd/roostack`, `cmd/roostack-agent`, `agent`, `cloud`, `firewall`, `labnet`, `labrestore`, `manager`, `monitor`, `node`, `osupdate`, `setup` and `web` all fail.
  - A trial in a scratch copy put these in `_linux.go` files with a refusing stub for other systems, and fixed `Bsize`: `go vet` was clean on darwin and Linux, and 40 packages passed.
  - The rest are Linux-only tests that should skip: peer credentials; GNU `find -printf` (`internal/backup/quarantine.go:203`, so `TestCleanJunk` fails rather than skips); `/proc/meminfo`; ICMP; macOS's `/var` symlink in temp paths; `TestEveryModuleCredited` (darwin compiles in two more modules).
- **Why:** `manager`, `web` and `node` test with fakes, but no contributor on a Mac can run them, and the README's `bin/roostack validate` and `render` are Linux binaries there (§6.2).
- **Change:**
  - The build tags.
  - `runtime.GOOS` skips for the Linux-only tests.
  - `filepath.EvalSymlinks(t.TempDir())` where tests compare paths.
  - Replace `find -printf` with `os.ReadDir`.
  - Update CLAUDE.md's "On a Mac parts of Roostack don't build".
  - Later: split `agent` into portable wire types plus client, and the Linux server.
  - `golang.org/x/sys/unix` is already an indirect dependency; using it directly would be tidier than `syscall`.
- **Effort:** S–M. **Risk:** low. **Decision:** only for `x/sys/unix`.

### 5.2 Speed

**T2. Nothing runs in parallel, and restic dominates.**
- **Where:**
  - Neither repository calls `t.Parallel()` anywhere.
  - Roostack's `internal/backup` takes 164–202 s on a Mac, about 40 restic tests one after another (`TestKeyHint` 16.5 s, `TestFenceRestored` 15.5 s, `TestLearnKey` 12.3 s). Every other package is under 10 s.
  - `internal/reconcile/reconcile_test.go:504` waits out a 3-second settle twice.
  - Package-level timing variables in `manager` and `node` would block parallel tests.
- **Change:**
  - `t.Parallel()` on the restic tests that don't change package variables.
  - One template repository per package in `TestMain`.
  - Move timings onto structs.
  - Settle 100 ms in tests.
- **Effort:** M. **Risk:** low. **Decision:** no.

**T3. Cloud migrates 48 times per test.**
- **Where:** `cloud:internal/pgtest/pgtest.go:115` `NewDatabase` runs every migration for each test, about 0.2 s; a trivial test takes 0.21 s, the median web or e2e test 0.48 s. Uncontended: `web` 35–43 s, `e2e` 36–39 s, `admin` 23 s, `cmd/cloud` 15 s.
  - Failed logins sleep a real 500 ms.
  - `dnszone.BatchLimit` and `cloudflareAPI` are package variables tests change (`cloudflare.go:219, 28`; `e2e_test.go:689`).
- **Change:**
  - Migrate once into a template database and `CREATE DATABASE … TEMPLATE` per test.
  - Make the login delay injectable.
  - Move the variables onto structs.
  - Then `t.Parallel()`.
- **Effort:** M. **Risk:** low. **Decision:** no. The speed-up is estimated from per-test times, not measured.

### 5.3 Flaky or fragile

**T4. Sleeps and silent deadlines.**
- **Where:**
  - A sleep used to prove something didn't happen: `internal/manager/update_test.go:350`, padded at `:357`.
  - A redundant sleep: `internal/backup/service_test.go:108`.
  - Polling loops that give up silently after 3 s and then assert something weaker: `internal/web/cloud_test.go:79, 112-117, 155`, and the `break`-on-deadline loops in `internal/manager/backups_test.go`.
  - About 30 hand-written wait loops, although two `waitFor` helpers already exist (`internal/maintenance/coordinator_test.go:80`, `internal/cloud/link_test.go:57`).
  - `authkit`'s `TestVerifyNoAccountTakesAsLong` (`authkit/authkit_test.go:55`) compares single wall-clock samples and failed once under load.
- **Change:**
  - One `waitFor(t, timeout, cond)` that fails on its deadline.
  - Replace the absence-by-sleep with an explicit signal.
  - Compare the minimum of N runs, or check that the no-account hash uses the same parameters.
- **Effort:** S. **Risk:** low. **Decision:** no.
- **Status:** Partly done in `9716696` (2026-10-22): `authkit`'s test does both; the backup set-up and restore waits in `manager` that gave up silently share a `waitDone` that waits two minutes and fails clearly (`TestRemoteStorageServerUsers` had failed that way under load). The rest stays for wave 1.

**T5. 833 `strings.Contains(body, …)` checks in Roostack's web tests.**
- **Why:** they tie tests to exact wording, so rewording a sentence breaks unrelated tests. Only `between` (`internal/web/pagessignin_test.go:308`) finds a section of a page; the rest slice with `strings.Index`.
- **Change:** a small set of helpers (find an element by `id` or `data-test`, read its text) to use as tests are touched. Not a rewrite.
- **Effort:** S to add. **Risk:** none. **Decision:** no.

### 5.4 Gaps

**T6. Important paths with no test of their own.**
- **Roostack:**
  - `manager.CheckPhotoSource` (who may migrate photos from where) and `PhotoDrives` are tested only through web's fakes (`internal/web/photos_test.go:78`).
  - `manager.SwitchImport` (`import.go:114`, 110 lines, 8 parameters) only through `internal/web/services_test.go:260`.
  - `EnableRelay` and `recheckS3` aren't tested.
  - `imagecheck` has no tests.
  - S9's round trip covers a fifth of the lab API.
- **Cloud** (from union coverage):
  - 0%: `adminCommand` (admin-add is the only way to create an admin; also edge-add and audit-verify), `publicServers` (production's listeners), `consoleCommand`, `reportHealth`, `RunDomainChecks` and `authoritativeTXT`, `Meter.Run`, the edge loop, `RunJobs`;
  - five web handlers (`verifyEmailResend`, `continuityResend`, `continuityStopSubmit`, `continuityPublic`, `signupPage`);
  - `serve` at 2.3%;
  - `names` (1,786 lines), `outbox` and `totpseal` have no unit tests, only e2e and handler coverage. Table tests for the pure rules in `names` (`recordName`/`recordValue` `names.go:744-790`, the label rules `:368`, `relay.go:180-340`) would be fast.
- **Change:** an admin-add test that then logs in; a smoke test of `serve` once S17's `controlplane.New` exists; table tests for the pure validators; direct tests for the Roostack manager functions above.
- **Effort:** M. **Risk:** none. **Decision:** no.

**T7. Long story tests without steps.**
- **Where:** `TestProtectedNames` (319 lines), `TestAbuse` (315), `TestRelay` (297), `TestPortalLabs` (277), `TestArchiveDrill` (264) in Cloud.
- **Change:** wrap each stage in `t.Run`, so a failure names the stage.
- **Effort:** S. **Risk:** none. **Decision:** no.

### 5.5 Fakes and helpers

**T8. Fakes written more than once.**
- **Shared fake packages:**
  - Roostack: `cloudtest` (920 lines), `dnstest` (387), `s3fake` (274), `pkgtest` (84);
  - Cloud: `storagetest` (760), `intaketest` (186), `passkeytest` (138), `pgtest` (136).
- **Package-local fakes:** 51 in Roostack and 6 in Cloud. The ones that overlap:
  - a fake B2 written three times (`internal/b2/b2_test.go:20`, `internal/manager/accountkey_test.go:39`, `internal/manager/backups_test.go:160`): make a `b2test`;
  - `fakeGandi` (`internal/dnsrecords/dnsrecords_test.go:140-200`) and `fakeAPIs` (`internal/dns/probes_test.go:16`) beside `dnstest`;
  - `node.Node` fakes three times (`internal/node/remote_test.go:31`, `internal/manager/servers_test.go`, web's): an embeddable base fake;
  - `fakeSecrets` four times; `fakeSystemd`, `fakeAgent`, `fakeBackups`, `fakeLibraries` and `fakeRegistry` twice each.
- **Web's fakes are scattered:**
  - `fakeManager`'s 131 methods are spread over `services_test.go` (100; its first 765 lines are the fake and harness), `public_test.go`, `serviceupdates_test.go`, `keychange_test.go`, `withdrawals_test.go` and `junk_test.go`;
  - `fakeDNS`, `fakeUpdates` and `fakeSetup` are in `wizard_test.go`;
  - `loggedIn` is in `services_test.go:743`, `newHarness` in `web_test.go:57`.
- **Change:** `fakes_test.go` and `harness_test.go` in web; `b2test`; fold the DNS fakes into `dnstest`.
- **Effort:** S–M. **Risk:** none. **Decision:** no.

**T9. Helpers reinvented per test file.**
- **Roostack:**
  - the block that finds restic (and skips without it) is pasted about 13 times. Make `pkgtest.Restic(t)`, which is also the one place to fail, rather than skip, when CI sets the variable;
  - `store.Open(filepath.Join(t.TempDir(), "roostack.db"))` appears 34 times.
- **Cloud:**
  - three browser and login harnesses (web's `site`, admin's `console`, e2e's `cloud`);
  - five near-identical `TestMain`s;
  - `testKeys` twice (`web/keys_test.go:10`, `e2e_test.go:635`);
  - the three-role database open (`db.Open` ×3) in 11 files;
  - signing up four ways;
  - fix with `pgtest.Database.Open(t)`, a shared test-keys helper and an `internal/webtest` browser.
- **Effort:** S–M. **Risk:** none. **Decision:** no.

---

## 6. For a new contributor

### 6.1 What's missing at the top of each repository

**N1. Licence and community files.**
- **Where:** Cloud has **no LICENSE or NOTICE** and states no licence anywhere. Neither repository has `CONTRIBUTING.md`, `SECURITY.md` (how to report a vulnerability), a code of conduct or issue and pull request templates.
- **Change:** add them. Cloud's licence is the Owner's call: the brief says Apache-2.0 for Roostack; is Cloud's the same?
- **Effort:** S. **Risk:** none. **Decision:** yes (Cloud's licence, the reporting address, a code of conduct, and whether outside pull requests are accepted at launch).

**N2. Cloud has no CLAUDE.md or ARCHITECTURE.md.**
- **Where:** a newcomer has README (79 lines) and DESIGN.md (about 44,700 words). Nothing lays out:
  - the processes (`serve`, `admin`, the edge, the timers) and the config file each reads;
  - the console nudging `serve` through `NOTIFY` (`cmd/cloud/forconsole.go:19-27`);
  - the background jobs;
  - the database roles and row-level security scopes;
  - the conventions: email queued in the change's transaction then `Wake`, audit entries in the action's transaction, `FormError`, `web.Time` and `times.js`, an injected `Now`;
  - how to add a migration, a query, a page (the template lists at `internal/web/server.go:102` and `internal/admin/admin.go:146`), an endpoint or a command;
  - the test setup.
- **Also stale:**
  - README's package table (lines 50-65) leaves out `storage`, `edge`, `edgecaddy`, `outbox`, `passkey`, `limit`, `protected`, `disposable`, `totpseal`, `ops/` and `tools/`, and its `web` row is out of date;
  - the package docs of `internal/web/server.go:1-4` and `internal/labs/labs.go:1-3`;
  - `internal/db/db.go:36, 40` ("as AppRole", but the type is used for every role);
  - DESIGN.md:78 is still headed "Proposed (2026-10-07)" though it's built;
  - two DESIGN citations don't match a section: `internal/admin/intake.go:26` cites "The feeds' sources and terms", and `internal/web/server.go:205` cites a bullet under another section.
- **Change:** write both with the Owner. Roostack's are the model.
- **Effort:** M. **Risk:** none. **Decision:** yes.

**N3. Roostack's CLAUDE.md is doing CONTRIBUTING's job, addressed to an agent about one person.**
- **Where:** "The user is learning Go…" (CLAUDE.md:34); "the user's own lab" (`:106`); the trailer; `.gitignore:13`, which mentions the Owner by name. The essential facts for humans live only there: the Mac caveat, restic, the VM's port scheme.
- **Change:** a CONTRIBUTING.md for people (build, test, the VM, conventions, docs to update); CLAUDE.md shrinks to what's specific to agents and points at it.
- **Effort:** S–M. **Risk:** none. **Decision:** yes.

### 6.2 Building and running

**N4. Roostack's README development section doesn't work on a Mac, and doesn't say so.**
- **Where:**
  - `README.md:62-87`: `make test` fails to build 13 packages (T1).
  - `make build` writes Linux binaries (`Makefile:20`), so the README's `bin/roostack validate`, `render` and `serve` give "exec format error" on a Mac, and `go run ./cmd/roostack` doesn't compile there.
  - `scripts/dev-vm.sh` isn't mentioned.
  - The README says Go 1.26 where CI uses 1.27.2.
  - `make test` needs Node 20+, which it doesn't say.
- **Also:**
  - CLAUDE.md's restic advice (`:80-86`) is out of date: Homebrew's restic runs the tests natively on a Mac, and copying the VM's binary gives a Linux one that can't run there. CI pins 0.18.0.
  - Four "regenerate" flags exist (`-update`, `-update-editor`, `-update-gallery`, `-update-credits`); the last isn't in CLAUDE.md.
- **Change:** a "Platforms" note and the VM in README; fix the restic advice; a `make generated` that runs all four (§7).
- **Effort:** S. **Risk:** none. **Decision:** no.

**N5. Cloud can't be built from outside, and its tools sit outside its workspace.**
- **Where:**
  - X5. README lines 32-48 (a private repository, a deploy key) will be wrong at launch. The go-import page already exists (`cloud:site/src/pages/roostack.astro`).
  - `go vet` in `edgecaddy` fails without `GOWORK=off`, and `go test ./...` at the root skips the edge plugin.
  - README's development section mixes local setup with CI secrets and Wasabi.
  - `make vet`'s `gofmt -l .` (Makefile:19, the same in Roostack's) walks into `.claude/worktrees` and `site/node_modules`, so another session's work in progress fails this checkout's vet.
- **Change:**
  - A `go.work.example`.
  - A line in `edgecaddy`'s README.
  - Move CI and Wasabi setup to a runbook.
  - Use `git ls-files '*.go' | xargs gofmt -l` in both Makefiles.
  - Add `.claude/worktrees` to Cloud's `.gitignore`.
- **Effort:** S. **Risk:** none. **Decision:** no.

### 6.3 The architecture documents

**N6. Roostack's ARCHITECTURE.md is accurate about packages and stale about the rest.**
- **What's right:** all 47 internal packages, the 3 public ones and both commands are present, and every file it names exists.
- **What's stale:**
  - The `cmd/roostack` row (line 50) lists 5 of 9 commands; it misses `backup`, `node`, `registry` and `backup-helper`.
  - The web UI table (`:139-147`) misses `/catalog`, `/servers`, `/monitoring`, `/about`, `/design` and seven settings areas.
  - Line 161 says a remote node "will receive" its assignment; `node.Remote` has existed for weeks. `internal/node/local.go:30-32` says the same.
  - The diagram says the agent does "sysctl, nftables, apt" only.
  - "`systemd`: `systemctl --user` wrapper" leaves out its `systemd-run` launcher.
  - "`podman`: the few direct podman calls": they go through `systemd-run`.
  - The `manager` row (`:91`) leaves out guests, photos, single sign-on, monitoring, the relay, libraries, junk, rollbacks and key changes.
  - The package table is in no particular order, and CLAUDE.md keeps a second, grouped copy.
  - `internal/cloud`, `internal/node` and `internal/setup` have no package doc. `manager`'s describes only the catalog, install and apply.
  - SPEC §10.3 says nodes pull their assignment and that actions wait for the next pull (SPEC.md:1695, 1698). As built, the controller pushes (`PUT /v1/assignment`, `remote.go:186`) and actions are direct calls.
  - SPEC's numbering is off: "### 2.1 Licence" sits under "## 3." (line 34), and there's no §10.6.
- **Change:**
  - Group the package table by layer (as CLAUDE.md does) and keep one copy.
  - Fix the rows above.
  - Add package docs.
  - Add two short traces with file links, "life of an install" and "life of a backup run". Tracing an install took 14 packages, about 15 files and 40 minutes; ARCHITECTURE's data-flow section helped most, and nothing said the apply is asynchronous.
  - SPEC's §10.3 and numbering are the Owner's.
- **Effort:** S–M. **Risk:** none. **Decision:** for SPEC.

**N7. The documents are too big to be a way in.**
- **Where:** SPEC.md is about 79,000 words (19 lines over 2,000 characters), TASKS.md 25,000 (479 done to 163 open), ASSUMPTIONS.md 19,000; Cloud's DESIGN.md 44,700 and TASKS.md 15,000. Nothing says "read this first". "The Owner" means both the project's maintainer and the product's top role (e.g. `docs/MIGRATION-PLAN.md:3` against ARCHITECTURE.md:161).
- **Change:**
  - A "start here" reading order in README or CONTRIBUTING.
  - A table of contents at the top of SPEC.
  - Finished TASKS phases moved to `TASKS-DONE.md`.
  - Another word for the maintainer in project documents ("the maintainer"), keeping "Owner" for the role.
- **Effort:** S–M. **Risk:** none. **Decision:** yes.

**N8. Things in the wrong place.**
- **Code in `docs/`:** `docs/immich-import-test/maketakeout` is a 1,245-line Go program, with a test, inside `docs/`. `go test ./...`, vet and govulncheck all cover it, but nobody looking in `cmd/` or `internal/` finds it. Move it to `tools/maketakeout` and update the README's `go run` line.
- **Historical documents** that could move to `docs/archive/` or `docs/features/`:
  - `docs/migration-rehearsal/` ("a record, not a tool");
  - `ARM64-PLAN.md` (phase 1 done; its line 14 says its decisions should move to SPEC);
  - `PLAN.md` (the original brief, mostly rules for an agent);
  - `docs/MIGRATION-PLAN.md`, `IMMICH-IMPORT.md` and `SSO-RESEARCH.md`;
  - in Cloud, `docs/REVIEW-2026-10-16.md` and `docs/storage-pricing.html`.
  - Every relative link resolves today, so moving them means updating links (four Go comments link to `docs/mockups`).
- **Stale TASKS entry:** Roostack's TASKS.md:584 says Cloud's review isn't committed; it is.
- **Effort:** S. **Risk:** none. **Decision:** yes (which documents are history).

---

## 7. Generated files and CI

**G1. What's checked and what isn't.**

| Generated | How it's regenerated | Stale fails CI? |
|---|---|---|
| Roostack generator golden files | `make golden` | Yes (`go test`) |
| The editor's `schema.js`, `examples.js`, `corpus.json` | `go test ./internal/editor -update-editor` | Yes |
| `docs/design/gallery.html` | `-update-gallery` | Yes |
| `THIRD_PARTY.md` | `-update-credits` | Yes |
| The help site (49 Markdown files → 3 MB) | `make help` (needs uv) | **No**: CI has no uv, so its `--strict` broken-link check runs only in `release.yml`. `go build`, `go test` and `dev-vm.sh test` embed whatever old build is lying around |
| The help links in the editor's `help.yaml` (about 140) | by hand | **No** (all valid today) |
| `internal/errpage/scenes.go` | `assets/errors/generate.py` | **No** |
| `brandmark.go` and the brand PNGs | `assets/brand/generate.py` (needs `rsvg-convert`) | **No** |
| `TestCaddyAdapt` (generated Caddy config against a real Caddy) | `make caddy-check` | **No** (skipped in CI) |
| Cloud's sqlc code | `make generate` | **No**: it slipped once (commit `3be72d0`); it matches today |
| Cloud's copies of Roostack's editor, tokens, brand and app CSS | `tools/sync-*.sh` | **No**: two are stale today (X2, P14) |
| Cloud's site | `npm run build` | **No**; it also rewrites the committed `site/public/rates.json` every time |
| The status page's palette (hex copied from `teal.css` into `status/worker/page.js:131-150`) | by hand | **No** (matches today) |

- **Change:**
  - Roostack: a uv job running `make help`; a Go test that `help.yaml`'s links name real help pages (beside `TestPageHelpTargets`); a job that regenerates the Python outputs and runs `git diff --exit-code`.
  - Cloud: `make generate && git diff --exit-code internal/db`; a job that runs the sync scripts against the checked-out Roostack and diffs; `npm ci && npm run build`; write `rates.json` into `dist/` instead.
  - Both: a `make generated` that regenerates everything, named in CONTRIBUTING.
- **Effort:** S. **Risk:** none. **Decision:** whether sync drift blocks or warns.

**G2. Shell and workflows.**
- **shellcheck** (0.11), Roostack's 19 scripts:
  - 1 error: `scripts/r2-put-once.sh` has no shebang;
  - 6 warnings, 5 of them in `docs/migration-rehearsal/`;
  - `scripts/dev-vm.sh:355` doesn't quote `$UI_PORT`; `:277`'s SC2064 is intentional and wants a disable comment;
  - `dev-vm.sh`'s usage fallback (`sed -n '2,30p'`) cuts its header mid-sentence.
- **shellcheck**, Cloud's 24 scripts: 2 warnings (`ops/deploy/db-backup.sh:96`, `ops/deploy/monitor-push.sh:35`).
- **CI:** neither runs shellcheck on its scripts (Roostack's actionlint checks only shell inside workflows), and Cloud has no actionlint.
- **What's good:** the scripts are otherwise clean, with `set -euo pipefail`, header comments and reasoned disable comments.
- **Cloud's ops scripts repeat the same SSH helpers:**
  - `host=` from tofu, `ssh_opts`, `remote()`, `as_cloud()` and `problem()` are copied across `ops/deploy.sh`, `deploy-monitor.sh`, `wg-ops.sh` and `deploy-edge.sh`: make one `ops/lib.sh`;
  - `deploy.sh:227` and `:245` repeat a 300-character remote command;
  - three Quadlets repeat the DNS-wait `ExecStartPre` and its comment;
  - `ops/tofu.sh:15` passes `$*` inside one string, so arguments with spaces break.
- **Versions pinned by hand in many places, and no Dependabot or Renovate:**
  - `GO_VERSION` in three places;
  - the Debian image digest in `release.yml` and `apt-publish.yml`;
  - `wrangler@4.143.0` five times across the two repositories;
  - every action's commit SHA.
- **Change:**
  - Fix the warnings; run shellcheck in both CIs (it's on the runners); add actionlint to Cloud; `ops/lib.sh`.
  - One versions file per repository that the workflows read.
- **Effort:** S. **Risk:** low. **Decision:** whether to add Dependabot or Renovate.

**G3. Static analysis in CI.**
- **staticcheck** found no bugs. Two `SA` items in tests read like mistakes but aren't:
  - `authkit/authkit_test.go:47`, `HashToken(a) != HashToken(a)`, which checks determinism;
  - an unused `resp` at `internal/web/sso_test.go:181`.
- **Its style check `ST1005`** fires 84 times in Roostack, on purpose: errors people can fix are whole sentences.
- **Recommendation:** staticcheck in CI with a `staticcheck.conf` that turns off ST1005 (about a minute per run, about 10 findings to fix once in Roostack and 7 in Cloud). Not `deadcode`: about 130 intentional hits (test fakes, public API only Cloud calls).
- **Effort:** S. **Risk:** none. **Decision:** yes (a new tool in CI).

---

## 8. The non-Go parts

**J1. The package editor's JavaScript.**
- **What's good:** the drift guard. Go generates `schema.js`, `examples.js` and the corpus, and `corpus.test.js` checks that `check.js` gives exactly `pkgspec`'s messages on 3,993 cases. It runs in CI and takes about 11 s.
- **Sizes:**
  - `check.js` is 1,248 lines (`decode` 185);
  - `editor.js` is 1,237 lines and untested (DOM code);
  - `compose.js` is 805 lines, and `importCompose` alone is 511 (`:191-702`), the main thing to split into phases;
  - `suggest.js`'s `suggestions` is 204 lines.
- **Lists kept by hand:**
  - the list of editor scripts is written out four times: `internal/editor/editor.go:38`, `internal/editor/test/load.js:8`, `cloud:tools/sync-editor.sh:23` and `cloud:site/src/pages/package-editor.astro:12`. A new script would silently be missing on the website. Generate a manifest;
  - the corpus's base packages are listed three times (`corpus_test.go:51`, `corpus.test.js:14`, `schema_test.go:142`).
- **Corpus blind spots:** a new `pkgspec` rule that no mutation in the corpus triggers goes unnoticed. Nothing measures which branches of `check.js` the corpus reaches: Node's coverage doesn't see scripts loaded through `vm.runInThisContext`.
- **No formatter or linter:** the editor and site use two spaces and double quotes; `status/` uses tabs and single quotes. Adding one is a new dependency.
- **Effort:** S–M. **Risk:** low. **Decision:** for a formatter.

**J2. Cloud's site and status page.**
- **What's good:** two exactly pinned dependencies with a lockfile, a CSP computed from the build, and a password gate that fails closed.
- **Mock-ups:** they stay, as the Owner wants. They're excluded from production only by `rm -rf dist/mockups dist/og` after the build, and `_headers` is computed before that deletion. It's harmless today; a build-time exclude would be clearer.
- **CSS:**
  - the `.head`/`h1` block is copied in four pages, and `.ic` is defined nine times;
  - the site's own token redefinitions (X2).
- **Node version:** the README says Node 24+, but there's no `engines` field or `.nvmrc`.
- **Untested:** the site's `worker/index.js` and `rates.js`, and the status page's `index.js` and `checks.js` (push limits, the token, `statusJSON`). The status page's 12 tests pass in 0.3 s.
- **Status page config:** production's D1 database ID is all zeros. `status-deploy.sh` could refuse production until it's set.
- **Effort:** S each. **Risk:** low. **Decision:** no.

**J3. OpenTofu.**
- **What's good:** `tofu fmt -check` and `tofu validate` pass; state is committed but encrypted; variables are validated.
- **Repeats, before a production environment arrives:**
  - the encryption block is duplicated between `ops/tofu/test` and `releases`;
  - the SSH and ping rules repeat between the control and edge firewalls;
  - "OpenTofu: ops/tofu/test" appears five times;
  - the edge is one fixed `edge_syd1`;
  - `ssh_keys` defaults to a key file on one machine (`~/.ssh/roostack-claude.pub`, `ops/tofu/test/variables.tf:26`), as the earlier review's M3 also noted.
- **Change:** a module, before production. It touches live state.
- **Effort:** M. **Risk:** medium. **Decision:** yes (layout and timing).

---

## 9. Dead code

Safe to delete. Each is small, and nothing calls it:
- **Roostack code:**
  - `(*Local).liveOutput` (`internal/node/import.go:615-624`);
  - `asStrings` (`internal/web/history.go:126`);
  - `fakeSetup.backupErr` (`internal/web/wizard_test.go:48`);
  - `Identity.CAPEM` (`internal/labnet/tls.go:92`);
  - `var WireGuardConf` (`internal/agent/wireguard.go:60-61`);
  - `ErrNoAccess` (`internal/sso/provider.go:750-751`);
  - `caddy.SignInSubdomain` (`internal/caddy/caddy.go:144-146`, which also duplicates the `"login."` literal in `config.go:521`).
- **Roostack templates:** `service-summary` and `service-details` (`internal/web/templates/parts.html:34-55`, unused since `6798ee86`; their comment now sits above `install-line`).
- **Roostack CSS** (`internal/web/static/css/app.css`):
  - `.nav-heading` (83; DESIGN.md says it was replaced on 2026-10-19);
  - `.code-action` (611);
  - `.user-actions` (676-693, including inside four `:not()` lists);
  - `.service-head` and `.service-actions` (1049-1057);
  - `.tile-links` (1060), `.server-facts` (1065), `.form-row` (1200).
  - `.span-6` is unused but part of the grid set: keep it.
- **Superseded, used only by tests:**
  - `backup.NewJob` (`internal/backup/job.go:258`), replaced by `Service.Job`;
  - Cloud's `storage.Service.Measure` (`internal/storage/storage.go:307`).
  - Point their tests at the replacements.
- **Cloud:**
  - `storagetest.Fake.Allows` (`internal/storage/storagetest/fake.go:637`);
  - the query `SetEdgeToken` (`internal/db/queries/relay.sql:68`), unused since `e4d03be`. Delete it, or file "rotate an edge's token" in TASKS and keep it: the Owner's call.
- **Misplaced comments:**
  - `RemoveKeptData`'s doc sits on `PutBackKeptData` (`internal/manager/move.go:431`), and `RemoveKeptData` (`:442`) has none;
  - `icons.go:21`'s "new-window" comment sits above `manage`;
  - `errpage.Names` says it's "for the design gallery", which doesn't call it;
  - `backup.go:46`'s "percent shows progress" sits above `size`.

Also checked and clean:
- Every config field in both repositories is read.
- All 48 icons are used.
- Cloud's templates and `cloud.css` classes are all used. Its `app.css` is a synced copy, so its unused classes are expected.
- No CLI flag is dead.
- `go mod tidy -diff` is clean in Roostack and edgecaddy. It can't run in Cloud while Roostack is private. The only cosmetic issue: `rsc.io/qr` sits in the indirect block without `// indirect`.
- No two dependencies do the same job.
- Cloud's migrations 00033–00039 and 00048 were never written: parallel branches took numbers. goose doesn't mind, but the convention should be written down: take the next number when merging, never renumber an applied one.

**Used only by tests, and should stay:**
- `trust.Classify` and `trust.May`: the model the firewall's tests check it against. Say so in their doc comments.
- `dns.All`, `pkgspec.Describe` and `ValueTests` (the editor's generators), `writeThirdParty`, `authkit.TOTPCode`, Cloud's bucket probes and `mail.Recorder`.

---

## 10. What's good, and should be kept

- Comments that say why, with dates and the alternatives considered, and SPEC or DESIGN citations that resolve.
- `generate` pure and golden-tested; `reconcile`'s side effects behind small interfaces with fakes; `trust` as the one definition, with tests of both its users against it.
- Small interfaces declared where they're used. No interface was found with one implementation and no test reason, apart from Cloud's `api.Names` and `web.RelayStopper`.
- `Remote.call` as the one place every lab API call goes through (down detection, a read cache, error kinds).
- Components as template functions that validate their arguments, and a gallery generated from the real components and tested.
- `render` buffering whole pages; `renderLive`'s fingerprint and 204.
- The editor's drift guard against `pkgspec`.
- Tests against real SQLite, restic and PostgreSQL; Cloud's `pgtest` fails rather than skips without PostgreSQL; Cloud's sqlc plus `db.Scope` making row-level security the default.
- Few dependencies, all chosen deliberately; `awssig` with the standard library only.
- Actions pinned by commit; reproducible releases built twice.
- Cloud's small single-purpose packages (`limit`, `totpseal`, `protected`, `disposable`, `audit`, `labca`, `outbox`, `passkey`).

---

## 11. Proposed plan

Batches that could each be one session, working in parallel without touching the same files. Every batch:
- changes no behaviour, apart from the §2 items the Owner approves;
- updates the docs its change touches, in the same commit;
- reports what was and wasn't verified.

**How to check that nothing changed:**
- **Go:** `make vet` and the full suite with restic (Roostack in the VM, `scripts/dev-vm.sh test`; Cloud against PostgreSQL 18). Golden files, `gallery.html`, `schema.js` and the corpus must regenerate with no diff.
- **Web pages:** before starting, a throwaway test writes the HTML of a fixed set of pages (from the existing harness and fakes) to a scratch folder. It's run again after the change and the two are diffed; any difference must be explained. Then deploy to a VM and look at the pages on a desktop and at a phone's width.
- **The agent, fstab and atomic writes:** deploy to a VM and run the flows they serve (install, backup, a backup drive, a media library, a move and a restore).

### Wave 0: the §2 fixes the Owner approves

Small and separate, one session per repository:
- **0a, Roostack:** P3–P7, P12, P13, P15, P16.
- **0b, Cloud:** P1, P2, P8–P11, P14.

They touch few files, but they overlap with waves 1 and 2, so they go first.

### Wave 1: in parallel (disjoint files)

| Batch | Repository | Scope | Files | Tests and checks |
|---|---|---|---|---|
| **1a. Mac build** | Roostack | T1 | `internal/agent/*` (new `_linux.go`/`_other.go`), `internal/monitor/probe.go`, `internal/node/guesthost.go`, `internal/backup/quarantine.go`, Linux-only test skips in `agent`, `labnet`, `manager`, `monitor`, `node` tests | `go vet` on darwin and `GOOS=linux`; the suite natively and in the VM |
| **1b. Web tidy** | Roostack | §9's web dead code, D13, D14, S1 (constructor and groups), S3, S5, S6, T8 (web's fakes) | `internal/web/**` only | Page HTML before/after; gallery unchanged; VM look |
| **1c. Lab API wire** | Roostack | S9 (round-trip test first, then wire files), S10 if decided | `internal/node/api.go`, `remote.go`, new `*wire.go`, `remote_test.go` | The new round-trip test before and after; VM with a second server joined |
| **1d. Store split** | Roostack | S14 step 1 (pure move), dead store functions | `internal/store/*` | Suite; `git diff --stat` shows only moves |
| **1e. Docs and layout** | Roostack | N3, N4, N6, N7, N8, package docs for `cloud`, `node`, `setup` (new `doc.go`s), CONTRIBUTING, SECURITY; CLAUDE.md's writing rules gain the Owner's tense rule (2026-10-22: the future for what will happen, the present continuous for what's happening, not the present simple) | `README.md`, `ARCHITECTURE.md`, `CLAUDE.md`, `docs/**`, `tools/maketakeout` (moved), new `doc.go`s | Links resolve; `go test ./tools/...` |
| **1f. CI and generated files** | Roostack | G1, G2, G3 (if decided), the Makefile's `gofmt` | `.github/**`, `Makefile`, `scripts/*` (shellcheck fixes) | CI green on a branch |
| **1g. Editor** | Roostack, then Cloud's sync | J1 (escape helper, split `importCompose`, script manifest) | `internal/editor/**`, then `cloud:tools/sync-editor.sh`, `cloud:site/src/pages/package-editor.astro` | `make editor-test`; corpus unchanged |
| **1h. Cloud ui** | Cloud | S15, S16's JSON helpers and `Wait`, S4's `<no value>` test helper | `internal/web/**`, `internal/admin/**`, new `internal/ui` | Full suite; e2e; look at the website and the console |
| **1i. Cloud CI, ops and build** | Cloud | X5 (stamp and refuse dirty), G1's Cloud rows, G2's Cloud rows, `ops/lib.sh`, N5 | `.github/**`, `Makefile`, `ops/*.sh`, `tools/*`, `.gitignore` | CI green; a deploy to test |
| **1j. Cloud docs** | Cloud | N1, N2, the stale comments | new `CLAUDE.md`, `ARCHITECTURE.md`, `LICENSE`, `NOTICE`, `CONTRIBUTING.md`, `SECURITY.md`, `README.md`, `docs/DESIGN.md` headings | Read by the Owner |

1b's and 1c's only shared file would be `internal/web`'s use of node capabilities if S10 is done: do S10 in 1c and leave web's call sites to it.

### Wave 2: cross-cutting helpers, one session at a time

These touch many packages, so they run when wave 1 has merged and nothing else is in flight, one after another:

1. **Roostack leaf helpers:** D1 (`fsutil`, which also fixes P7), D3 (one user error), D4 (`Within`, names), D5, D6, D7, D8. Verify with the suite plus a VM run of install, backup, restore and a move.
2. **Roostack words:** D2. Page HTML before and after must match.
3. **Shared with Cloud:** X1 (public `units`, then Cloud uses it), X4 (`SealFor`), X7 (the contract table in `cloudapi`, then Cloud's test and the fake). Roostack first, then Cloud against that commit, with X5's pin in place.

### Wave 3: structural, after the Owner decides

Each its own session, in any order, once waves 1 and 2 have landed:
- **D11 and D12:** one transfer procedure and, if decided, one kept-copy system (`internal/manager/move.go`, `restore.go`, `lost.go`, `internal/node/move.go`, `internal/backup/restore.go`). Real restic tests, plus a VM move and restore.
- **S8 and S11:** split photos, guests and key changes out of `Manager`; photos and imports out of `Local`. Mechanical, with many conflicts, so it's best done when little else is in flight.
- **S2:** attention and the notifier out of web; the 3-2-1 checks into manager.
- **S12 and S13:** the `serve` controller struct; the agent's route helpers with P5's `editFstab`.
- **D9:** DNS probes onto the records code.
- **S17:** Cloud's `accounts`, `relay` and `controlplane` packages and the `cmd/cloud` tidy; T3 (template database, `t.Parallel`) and T6's tests alongside.
- **T2:** parallel restic tests and a template repository.

---

## 12. Decisions for the Owner

Grouped. Everything not listed here can go ahead once the plan is agreed.

**Behaviour (§2):** all decided by the Owner on 2026-10-22 (each finding says how).
- P3: after a failed database load, should a move switch back to the old server, and should a restore hold the service stopped until someone acts?
- P9: does the relay's monthly allowance count in UTC?
- P16: may shadow choose the `roostack` user's subordinate ID range?
- And approval for the rest of §2.

**Public API and sharing:**
- X1: a public `units`.
- X4: `authkit`'s `SealFor`.
- X7: what the `cloudapi` contract states.
- D10: `awssig` setting the S3 payload header.
- X6: whether `http.CrossOriginProtection`'s rule is acceptable.
- X3: whether a shared mail package is wanted later.

**Building Cloud:**
- X5: pinning Roostack and stamping its commit.
- What happens at launch, when Roostack is public.

**Shape:**
- S1: which web dependencies are required.
- S2 step 4: attention and the notifier in their own package.
- S7: whether web splits later (recommended: not now).
- S8 step 4: splitting `Manager`.
- S10: one capability convention.
- S17: splitting Cloud's `names`, and an `accounts` package.
- D12: one kept-copy system, and how pages show it.

**Names and wording:**
- D2: the words package and its duration styles.
- D3: the user-error type's name.
- D14: the update summary's wording.
- N7: "the maintainer" for the project role.

**Tools and dependencies:**
- T1: `golang.org/x/sys/unix` directly.
- G3: staticcheck in CI.
- G2: Dependabot or Renovate.
- J1: a JavaScript formatter.
- X2, G1: whether sync drift blocks CI or warns.
- §9: keep or delete `SetEdgeToken`.

**Documents:**
- N1: Cloud's licence, a vulnerability-reporting address, a code of conduct, and outside pull requests.
- N2: Cloud's CLAUDE.md and ARCHITECTURE.md.
- N3: CONTRIBUTING versus CLAUDE.md.
- N6: SPEC's §10.3 and numbering.
- N7: the TASKS archive.
- N8: which documents are history, and where `maketakeout` goes.
- S14 step 3: squashing the store's migrations before the first release.
- J3: the OpenTofu module layout and its timing.
