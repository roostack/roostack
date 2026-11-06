# The registry's update bot

How the service registry finds new versions of its apps and proposes them
(SPEC.md §8.3.8, "An update bot in the registry"). Built in two parts:
finding and proposing (as built, 2026-10-28), then testing each update on
a real lab and merging the safe ones by themselves (as built, 2026-11-07).

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
them, and is labelled `update` and `update:<kind>`. An update whose branch has had
a pull request is left alone (proposed, or closed by someone, so a
rejected update isn't opened again); a branch a run pushed before failing,
without its pull request, gets one (the Watch workflow does the same, after
its first runs wedged on one, 2026-10-29). When a newer update of a
package comes, the older one's pull request is closed and its branch
deleted. Packages it couldn't decide about go into the run's summary.

Set up: the binary variables naming Roostack 0.1.1 or newer, and the
secret `WATCH_TOKEN` (the Watch workflow's), since pull requests opened
with the workflow's own token don't start Publish's checks.

## Testing each update on a lab, and merging the safe ones — Locked (2026-10-28), the Owner; as built (2026-11-07), in roostack/services

**The update test runs in a Debian 13 VM on GitHub's runner** (the
Owner's choice; GitHub's Linux runners offer KVM). `.github/update-test/run.sh`
boots Debian's cloud image under QEMU and installs the released Roostack from
`apt.roostack.cloud`. `drive.py` then goes through Roostack's own pages and
commands, as a person would:

1. Make an Owner with `roostack user add`, sign in, set up two-factor, and
   finish the wizard, leaving the domain and backups for later. A lab with no
   domain can install services, so there are no certificates or DNS to fake.
2. Install the version labs can install now (`from.sh`: the newest in the
   published index that isn't withdrawn or `image_gone`), as one of the
   Owner's own packages, with made-up settings, and wait until it's healthy.
3. For a package with data: `roostack backup init` and `backup run`.
4. Update it to the pull request's version through the service's page, the
   path every lab will take, migrations included, wait for the update's job
   to finish (a failed job fails the test), and wait until it's healthy. The
   version in the instance file changes well before the job ends, so it isn't
   enough on its own.
5. For a package with data: `roostack backup restore <service>` from the
   backup taken before the update, and wait until it's healthy again.
6. Report each step and its time in the run's summary.

**Healthy** means the service has finished setting up, every container is up
and passing its health check, each of its ports answers with something other
than a server error, and all of that lasts 30 seconds. The test has no real
address, so it doesn't check certificates, and it doesn't check that the app's
data came through the update or the restore.

**When labs can't install any version** (HedgeDoc's image gone, and its only
version with it), it installs the update itself and skips step 4, so that
a rebuild can still fix a package whose image has gone.

**What merges by itself** (the Owner's choice): an update labelled
`update:patch` or `update:revision` (a rebuild of the same version), of an
**official** package, once the update test and Publish's check have passed.
`.github/update-merge.sh` waits for Publish's check and merges, so GitHub's
auto-merge and a protected `main` aren't needed for this (a change from the
first plan). It also regenerates the update from `main` and leaves it for a
person if the file differs, since an update written before a hand-made revision
would otherwise undo that revision for labs. Minor, major and `other` updates,
and community packages, wait for a person. **The repository variable
`UPDATES_AUTO_MERGE` must be `true`**; without it, the test still runs and
nothing merges.

**Left for later:** ARM (SPEC §8.3.8 leaves this open), and checking that an
app's own data survives the update, rather than only that the app is healthy.

## Not yet

- **Follow upstream per service** (§8.3.8 part 2), on labs: the same
  finding code, run by the controller on its update schedule.
- **New packages' revisions with changes beyond images:** an app version
  needing new settings or environment is still written by hand; the update
  test is what catches one that doesn't start.
