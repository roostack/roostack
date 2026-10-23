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

## The workflow for roostack/services

Copy this to `.github/workflows/watch.yml` in the registry repository.
It uses the same `ROOSTACK_BINARY_URL` and `ROOSTACK_BINARY_SHA256`
variables as `publish.yml`, and a secret `NTFY_URL` (the maintainers'
topic, such as `https://ntfy.sh/<a long random name>`). The watcher
commits its state to `main`, so the workflow's token must be allowed to
push there.

```yaml
# Watches for security problems in the registry's packages and drafts
# advisories as pull requests (Roostack's docs/REGISTRY-WATCH.md).
name: Watch

on:
  schedule:
    - cron: "41 * * * *"   # every hour
    - cron: "5 8 * * 1"    # the digest, Mondays
  workflow_dispatch:

permissions:
  contents: write
  pull-requests: write
  issues: write

concurrency:
  group: watch
  cancel-in-progress: false

jobs:
  watch:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          path: services
      - name: Get roostack (a release, checked by its SHA-256)
        env:
          URL: ${{ vars.ROOSTACK_BINARY_URL }}
          SHA256: ${{ vars.ROOSTACK_BINARY_SHA256 }}
        run: |
          curl -fsSL --retry 3 -o roostack-bin "$URL"
          echo "$SHA256  roostack-bin" | sha256sum -c -
          chmod +x roostack-bin
      - name: Watch, or write the digest
        id: run
        env:
          GITHUB_TOKEN: ${{ github.token }}
        run: |
          if [ "${{ github.event.schedule }}" = "5 8 * * 1" ]; then
            ./roostack-bin registry watch -repo services -digest digest.md
          else
            ./roostack-bin registry watch -repo services -summary summary.json
          fi
      - name: Open a pull request for each draft
        if: hashFiles('summary.json') != ''
        working-directory: services
        env:
          GH_TOKEN: ${{ github.token }}
          NTFY_URL: ${{ secrets.NTFY_URL }}
        run: |
          git config user.name "Roostack watcher"
          git config user.email "watcher@users.noreply.github.com"
          jq -c '.drafts[]' ../summary.json | while read -r d; do
            id=$(jq -r .advisory <<<"$d"); branch=$(jq -r .branch <<<"$d")
            mv "advisories/$id.yaml" "../$id.yaml"
            git worktree add "../pr-$id" -b "$branch" origin/main
            mkdir -p "../pr-$id/advisories" && mv "../$id.yaml" "../pr-$id/advisories/"
            git -C "../pr-$id" add advisories
            git -C "../pr-$id" commit -m "$(jq -r .pr_title <<<"$d")"
            git -C "../pr-$id" push origin "$branch"
            url=$(gh pr create --head "$branch" --title "$(jq -r .pr_title <<<"$d")" --body "$(jq -r .pr_body <<<"$d")")
            if [ "$(jq -r .urgent <<<"$d")" = true ] && [ -n "$NTFY_URL" ]; then
              curl -fsS -H "Title: Roostack: an urgent advisory to review" -H "Priority: urgent" \
                -H "Click: $url" -d "$(jq -r .title <<<"$d")" "$NTFY_URL"
            fi
          done
      - name: Open the digest
        if: hashFiles('digest.md') != ''
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh issue create -R "$GITHUB_REPOSITORY" --title "Security digest, $(date +%F)" --body-file digest.md
      - name: Keep the watcher's state
        working-directory: services
        run: |
          git config user.name "Roostack watcher"
          git config user.email "watcher@users.noreply.github.com"
          git add watch/state.json
          git diff --cached --quiet || { git commit -m "Watcher: state" && git push origin HEAD:main; }
```

## Bills of materials (next)

For exact matches inside the images, the registry's CI will run Syft
once for each new package version's images and keep the result as
`boms/<package>/<version>.json` (Syft's JSON, `-o syft-json`). The
watcher then asks OSV about each library version in them. Syft runs only
in the registry's CI, never on labs (the maintainer's answer to question
10).
