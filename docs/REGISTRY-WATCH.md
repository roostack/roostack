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
| OSV, for the library versions in `boms/` | Exact matches inside the images, once the CI keeps bills of materials |

Not read yet: VulnCheck's list (it needs an account), oss-security,
projects' own feeds beyond GitHub, and packages whose source isn't on
GitHub (Forgejo is on Codeberg). Each run's notes say what it couldn't
read.

## What it does with them

- **A strong match** (a registry version is affected exactly, and it's
  critical in Roostack's meaning or being used in attacks) is written into
  `advisories/<YYYY-NNNN>.yaml` with a comment saying it's a draft, and
  the summary gives its pull request's branch, title and text. When it's
  critical and being used in attacks it's **urgent**, and the CI sends a
  push notification through ntfy.
- **A weaker match** waits in `watch/state.json` for the weekly digest,
  which `-digest` writes as markdown and then clears.
- **Severity** is suggested from the CVSS vector: critical only when
  anyone on the network can use it without an account or anyone's help,
  and it reaches data or the system. The maintainer checks it against how
  Roostack runs the app (its private paths stay private).
- **Nothing twice:** `watch/state.json` keeps every problem handled, by
  each of its IDs, and published advisories are matched by their CVEs
  and link. The first run looks back 30 days (`-since`), and later runs
  look back to the run before.

## The workflow in roostack/services

The registry runs it with `.github/workflows/watch.yml` (on the branch
`watch-workflow` in the maintainer's checkout, 2026-10-23, not pushed
yet). Every hour it runs the watcher, opens a pull request for each draft
from its own branch (`watch/<id>`), and pushes an urgent one to ntfy;
each Monday it opens the digest as an issue instead. Then it commits
`watch/state.json` to `main`. Its header says how to set it up:

- the variables `ROOSTACK_BINARY_URL` and `ROOSTACK_BINARY_SHA256`, as for
  Publish, naming a Roostack release that has `roostack registry watch`;
- the secret `NTFY_URL`, the maintainers' topic, such as
  `https://ntfy.sh/<a long random name>`;
- the secret `WATCH_TOKEN`: a fine-grained token for the repository with
  contents, pull requests and issues write. Pull requests opened with the
  workflow's own token don't start other workflows, so without it
  Publish's checks won't run on the drafts;
- `main` accepting the state's commit from that token.

## Bills of materials (next)

For exact matches inside the images, the registry's CI will run Syft
once for each new package version's images and keep the result as
`boms/<package>/<version>.json` (Syft's JSON, `-o syft-json`). The
watcher then asks OSV about each library version in them. Syft runs only
in the registry's CI, never on labs (the maintainer's answer to question
10).
