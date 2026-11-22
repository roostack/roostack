# The registry's watcher

`roostack registry watch` reads free sources of security problems every
hour in the service registry's CI. It matches them to the registry's
packages and drafts advisories for a maintainer to approve
(docs/RED-BUTTON-PLAN.md §10.1, SPEC.md §6.16 "phase 4"). It never
publishes anything: a draft is a pull request, and merging it is the
approval.

## What it reads

| Source | What for |
|---|---|
| GitHub's advisories for each package's `about.source` repository | The app's own problems, with the version ranges they affect |
| GitHub's advisories for Caddy, CrowdSec, restic and Roostack | Roostack's own parts: a person decides which Roostack releases they affect (`affects: roostack`) |
| CISA's list of known exploited vulnerabilities | "Being used in attacks", and new entries naming something the registry has |
| CrowdSec's virtual-patch rules (the Hub's index) | A new rule usually means a public exploit; new rules naming something the registry has |
| EPSS | How likely each problem is to be exploited, to sort them |
| OSV, for the library versions in `boms/` | Exact matches inside the images |
| The release notes of packages whose `about.source` is on Codeberg | Forgejo's security fixes (below) |

Not read yet: VulnCheck's list (it needs an account), oss-security,
projects' own feeds beyond GitHub and Codeberg, and packages whose source
is elsewhere. Each run's notes say what it couldn't read.

**Codeberg — as built (2026-11-19), the maintainer's request:** Forgejo,
which the maintainer's own lab runs on the internet, was a blind spot.
Codeberg has no security advisories to read (its API answers 404 for
them, and OSV has nothing for Forgejo), but Forgejo's release notes list
its security fixes under a "Security bug fixes" entry. For a package
whose source is on Codeberg (`forgeHosts` in `forge.go`), the watcher
reads the repository's releases (`/api/v1/repos/<repo>/releases`), follows
a release whose text only points to its notes file ("See …/15.0.12.md",
read raw), and takes the entries under any heading or entry naming
security (`SecurityFixes`). A release since the last run with any is a
weak match, `codeberg.org/forgejo/forgejo@v15.0.12`, for the registry's
older versions on the same major line (a release fixes its own line:
15.0.12 for 15.0.x, 17.0.2 for 17.0.x), since the notes don't say how
serious each problem is. The update bot reads the same notes to say what
an update fixes (REGISTRY-UPDATES.md, "Security fixes").

## What it does with them

- **A strong match** (a registry version is affected exactly, and it's
  critical in Roostack's meaning or being used in attacks) is written into
  `advisories/<YYYY-NNNN>.yaml` with a comment saying it's a draft, and
  the summary gives its pull request's branch, title and text. When it's
  critical and being used in attacks it's **urgent**, and the CI sends a
  push notification through ntfy.
- **A weaker match** waits in `watch/state.json` for the weekly digest,
  which `-digest` writes as markdown and then clears (below).
- **Severity** is suggested from the CVSS vector: critical only when
  anyone on the network can use it without an account or anyone's help,
  and it reaches data or the system. The maintainer checks it against how
  Roostack runs the app (its private paths stay private).
- **Nothing twice:** `watch/state.json` keeps every problem handled, by
  each of its IDs, and published advisories are matched by their CVEs
  and link. The first run looks back 30 days (`-since`), and later runs
  look back to the run before.

## The digest — as built (2026-11-19), the maintainer's request

The digests of 2026-11-09 and 2026-11-16 stayed open with nothing done:
the second had 439 matches, most needing nothing, such as problems the
registry's newest versions had fixed since, and DoS bugs in libraries deep
inside images. Now `-digest` reads the registry as it is that Monday and
sorts each match (`digest.go`):

- **Needs a decision**, listed: the registry's newest version is still
  affected, or isn't known to be fixed. One entry for each problem, with a
  line for each package it concerns, giving the newest affected version and
  how many older ones.
- **Already fixed in the registry's newest version**, collapsed: the
  newest version is outside the advisory's ranges (kept with each match
  since, as `ranges`), past its fixed version, or its image's bill of
  materials no longer has the library version (`purl`). Labs that update
  get the fix.
- **In the background**, collapsed: a library inside an app's image (a
  base system's package, or one the app uses), neither critical nor being
  used in attacks. Only the app's project can update it, and the update
  bot proposes its new image when it does.

Critical problems and those being used in attacks are always listed,
wherever they come from. Rebuilt this way, the digest of 2026-11-16 had
26 problems to decide (21 of them in Kan's image), 103 fixed since and
235 in the background.

**A mis-paired library, fixed (2026-11-19):** one OSV problem in several
packages' images was given the library version of whichever image was
read last, so an entry's title ("… (in node-forge 1.4.0)") and its "from
OSV, for …" could name another package's library: 46 of the 154 OSV
entries in the digest of 2026-11-09 named a version their package didn't
carry. Each match now names its own image's library (`purl`), and a match
still waiting from before isn't taken as fixed when its affected versions'
bills of materials don't have its library. Its end lists the update bot's pull requests that
are stuck (REGISTRY-UPDATES.md, "Stuck updates"), and `fit-digest.sh`
keeps the collapsed sections whole when it cuts. Considered: leaving out
the fixed and background matches altogether (they'd be invisible, and one
might still deserve an advisory for labs on older versions).

## The workflow in roostack/services

The registry runs it with `.github/workflows/watch.yml` (on the branch
`watch-workflow` in the maintainer's checkout, 2026-10-23, not pushed
yet). Every hour it runs the watcher, opens a pull request for each draft
from its own branch (`watch/<id>`), and pushes an urgent one to ntfy;
each Monday it opens the digest as an issue instead. Then it commits
`watch/state.json` to `main`. An issue holds at most 65,536 characters,
so the registry's `tools/fit-digest.sh` keeps as many matches as fit and
links the state from before the digest for the rest (since 2026-11-04:
the digest of 2026-11-02 had 2,888 matches, and its issue was refused).
Its header says how to set it up:

- the variables `ROOSTACK_BINARY_URL` and `ROOSTACK_BINARY_SHA256`, as for
  Publish, naming a Roostack release that has `roostack registry watch`;
- the secret `NTFY_URL`, the maintainers' topic, such as
  `https://ntfy.sh/<a long random name>`;
- the secret `WATCH_TOKEN`: a fine-grained token for the repository with
  contents, pull requests and issues write. Pull requests opened with the
  workflow's own token don't start other workflows, so without it
  Publish's checks won't run on the drafts;
- `main` accepting the state's commit from that token.

## Bills of materials

Syft lists what each image holds: its libraries, OS packages, and the
runtimes it recognises (`pkg:golang/stdlib`, `pkg:generic/node`). The
registry keeps that, cut down to each one's name, version, type and
package URL, as `boms/<package>/<version>/<image>.json` (about 120 KB an
image), for each package's newest version (branch `boms` in the
maintainer's checkout, 2026-10-23, not pushed):

- `roostack registry images -missing -newest` lists the images still
  without one, with the file each goes in.
- `.github/get-syft.sh` puts a pinned Syft release in place, checked by
  its SHA-256; `.github/scan-boms.sh` scans each image for linux/amd64
  and writes the files.
- **On a pull request,** Publish scans the new versions' images and runs
  `roostack registry bom-check -only <those versions>`, writing to the
  run's summary what an image has that `built_with:` doesn't name, and
  what `built_with:` names that Syft found nothing for (it may be bundled
  or built in). It never fails the check: a maintainer decides.
- **After a merge,** the Bills of materials workflow scans the same way
  and commits the files to `main`; run it by hand for the first ones.

`built-with.yaml` says which package URLs show each name (a URL ending in
`/` or `*` is a prefix); a name with none isn't compared. SQLite's library
is in nearly every image, so SQLite isn't compared. Syft runs only in the
registry's CI, never on labs (the maintainer's answer to question 10).
