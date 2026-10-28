# The registry's update bot

How the service registry finds new versions of its apps and proposes them
(SPEC.md §8.3.8, "An update bot in the registry"). Built in two parts:
finding and proposing (as built, 2026-10-28), then testing each update on
a real lab and merging the safe ones by themselves (planned, below).

## Who does what — Locked (2026-10-28), the Owner

**The facts in Roostack, the GitHub work in the registry's workflow.**
`roostack registry updates` (`internal/registry/updates`) decides what
each package could update to and writes the new revision; the registry's
Updates workflow and `.github/open-updates.sh` open the pull requests,
label them, and close superseded ones. The rules for each package are data
in the registry (`updates.yaml`).

- **Why the facts are Go:** labs need the same code for "follow upstream"
  (§8.3.8 part 2: list a repository's tags, pick the newest release the
  policy allows, resolve its digest, check its processors), and the bot's
  idea of a patch must be the labs' (`pkgspec.UpdateKind`), since what
  merges by itself depends on it. Registry access (`imagecheck`, with
  Docker Hub's, GHCR's and quay.io's anonymous logins and rate limits) and
  the package format (`pkgspec`) were already Go; the registry runs a
  signed, checksum-pinned release, so the code that writes packages stays
  on that one audited path.
- **Why the rest is the workflow's:** branches, pull requests, labels,
  schedules and merge rules are what's most likely to change, and they
  change there without a Roostack release.
- **Considered:** all of it as a script in the registry (the Owner's first
  thought, for flexibility): faster to change, but it would duplicate
  parsing and registry access without their tests, and read versions
  differently from labs.

## What it finds — as built (2026-10-28)

For each package's newest version:

- **The app's containers** are those whose tag carries the app's version
  (`v3.3.3`, `15.0.10-rootless`): the tag becomes a template
  (`v{version}`, `{version}-rootless`). Other images (Immich's Postgres)
  are sidecars, checked only for rebuilds. All 23 packages' images
  followed this at the start; Plex's tags end in a build hash, so its rule
  says `{version}-*`.
- **A new version:** the newest tag of the first app container's
  repository that fits the template, with the same number of
  dot-separated numbers as the app's version (so `3.4.0-rc.1` and
  `3.4.0-cuda` never count), newer by `pkgspec.CompareVersions`, allowed
  by the package's rule, and present for every app container. A version
  that's one number is a date (PhotoPrism's `261007`), so it needs as many
  digits: PhotoPrism's old tags (`20220121`) would otherwise look newer.
  It becomes `<version>-roostack.1`.
- **A rebuild:** an image whose tag now points to another digest (a
  project rebuilt the same version, often for its base OS, and may delete
  what the package pins, as quay.io did with HedgeDoc's on 21 October). It
  becomes the next revision of the same version. With a new version,
  sidecars' rebuilds come along.
- **Processors:** a new digest must have every processor Roostack runs on
  (amd64, arm64) that the pinned one had; otherwise the package is skipped,
  saying so.
- **The new file** is the newest version's text with only the version, the
  images, and the date in a "digests looked up" comment changed, so its
  comments stay; it must parse as a package (`pkgspec.Parse`) before it's
  written.
- **The kind** is `pkgspec.UpdateKind(from, to)`, as labs read it: patch,
  minor, major, `other` (versions that aren't semantic versioning, such as
  Plex's four numbers or PhotoPrism's dates), or `revision` for a rebuild.

A package it can't decide about (no image carries the app's version, a
registry error, a lost processor) is listed with why, not proposed.

The first run against the real registry (2026-10-28, read-only) found 16
updates in about five minutes, and two written for real (Audiobookshelf
2.37.2, a patch, and Immich 3.3.5, its two app images together) passed the
registry's full check, image checks included.

## The rules: `updates.yaml`

At the registry's root. The index's build doesn't read it, so a rule never
changes what labs see; `roostack registry updates` refuses a rule it
doesn't understand.

| Rule | Does |
|---|---|
| `track: minor` | Only versions with the same major (Forgejo's long-term support line, 15) |
| `track: patch` | Only versions with the same major and minor |
| `ignore: [2.1.0]` | Never proposes these app versions (a known bad release) |
| `tags: {app: "{version}-*"}` | How a container's tag is made from the app's version, by container name; `*` is anything but a hyphen, so Plex's `-amd64` and `-arm64` variants don't match |
| `rebuilds: false` | Leaves rebuilt images alone |
| `hold: true` | Nothing at all for the package |
| `reason:` | Why, for whoever reads the rule later |

## The workflow — as built (2026-10-28), in roostack/services

`.github/workflows/updates.yml`, hourly (and by hand): it gets the release
named by the binary variables (as Publish and Watch do), runs
`roostack registry updates -write`, and `.github/open-updates.sh` opens a
pull request for each update from its own branch,
`update/<package>/<version>`, holding only its new file. The pull request
says what changed (each image, its old and new digest, whether it was
rebuilt), links the project's notes for the version when the package names
them, and is labelled `update` and `update:<kind>`. An update whose branch
exists is proposed already and left alone; when a newer update of a
package comes, the older one's pull request is closed and its branch
deleted. Packages it couldn't decide about go into the run's summary.

Set up: the binary variables naming Roostack 0.1.1 or newer, and the
secret `WATCH_TOKEN` (the Watch workflow's), since pull requests opened
with the workflow's own token don't start Publish's checks.

## Next: testing each update on a lab, and merging the safe ones — Locked (2026-10-28), the Owner; not built

**The update test runs in a Debian 13 VM on GitHub's runner** (the
Owner's choice; GitHub's Linux runners offer KVM): it boots Debian's cloud
image under QEMU, installs the released Roostack from
`apt.roostack.cloud`, and drives it from the command line, as a lab would
run the package:

1. Write a lab (`lab.yaml`, with a test domain and a backup repository in a
   folder) and install the package's **previous** version: its instance
   file and package copy, then `roostack apply`, and wait until every
   container is healthy.
2. For a package with data: `roostack backup init` and `backup run`.
3. **Update it** to the pull request's version as labs do (the instance's
   version and package copy, then `roostack apply`), and wait until it's
   healthy: this is the path every lab will take, migrations included.
4. For a package with data: `roostack backup restore <service>` from the
   backup taken before the update, and wait until it's healthy again.
5. Report each step in the pull request's checks ("Update test").

**What merges by itself** (the Owner's choice): an update labelled
`update:patch` or `update:revision` (a rebuild of the same version), of an
**official** package, once Publish's checks and the update test pass. The
workflow turns on GitHub's auto-merge for it (`gh pr merge --auto`), so it
merges only when every required check is green. Minor, major and `other`
updates, and community packages, wait for a person.

**To settle when building it:**

- Whether `roostack apply` alone sets up everything a fresh install needs
  from the command line (generated secrets, database passwords, setup
  hooks), or the test needs a small `roostack install`-like command; to
  try on a dev VM first.
- Health without a real domain: containers' own health checks are enough
  for "it starts"; Caddy's certificates need a domain, so the test may use
  Caddy's internal CA, as the dev VMs did.
- Big images on the runner's disk (Immich's machine learning image is
  about 2 GB of a runner's 14 GB free); time (likely 10–15 minutes each).
- ARM: SPEC §8.3.8 leaves whether the test also covers ARM for later.

**The Owner's steps then:** in roostack/services, allow auto-merge, and
protect `main` with Publish's check and the update test required (TASKS:
"Registry settings").

## Not yet

- **Follow upstream per service** (§8.3.8 part 2), on labs: the same
  finding code, run by the controller on its update schedule.
- **New packages' revisions with changes beyond images:** an app version
  needing new settings or environment is still written by hand; the update
  test is what catches one that doesn't start.
