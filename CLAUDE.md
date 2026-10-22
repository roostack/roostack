# CLAUDE.md

Guidance for Claude (and other agents) working on Roostack. Read [CONTRIBUTING.md](CONTRIBUTING.md) first: it has how to build, test and run Roostack, the dev VM, the generated files, the conventions and which documents to update, and all of it applies to you. This file adds what's specific to agents.

## What Roostack is

Roostack (pronounced "Roost Stack") self-hosts useful services (photos, passwords, git, media) on your own hardware: one-click installs, automatic HTTPS, backups and updates, from a simple web UI. The end user is often **not technical** (a NUC at someone's parents' house), but **a technical user must never find anything magical**: every page says what Roostack is doing, where files live, and shows the log.

Stack: Go (module `roostack.cloud/roostack`, Go 1.26), Debian 13, rootless Podman driven by systemd Quadlets, Caddy, restic, WireGuard, SQLite, server-rendered HTML with htmx and Alpine.js. Early development: nothing is released, and only fresh installs are supported (no migration of pre-release data beyond `store` schema additions).

## Documents: read before, update with every change

CONTRIBUTING.md's table says which document holds what and when to update it; its reading order is the way in. Two more rules for agents:

- [PLAN.md](PLAN.md) is the user's: change it only when they ask.
- `NOTES.local.md` is the user's own notes: never edit or commit it.

**Roostack Cloud** (the hosted service at roostack.cloud, which gives labs names on roost.ac) lives in its own repository, which is private. Here, design only what works standalone, without Cloud (e.g. remote access, a lab's own edge), like any feature; mention Cloud's capabilities at a high level at most. Cloud's implementation detail (plans, pricing, edges, accounts, anything sensitive) stays in the Cloud repository.

## Working with the user

The user is the project's maintainer, called "the maintainer" in the project's documents ("the Owner" is the top role in a lab).

- **Ask when unsure; never assume.** For anything that's the user's call (product behaviour, wording that changes meaning, security trade-offs), propose options with a recommendation and let them decide. Don't ask about things with an obvious conventional answer: pick it, say so.
- **Pause for validation** between tasks and before large or hard-to-reverse steps. Show mock-ups before building a big UI change.
- **Dependencies are chosen together.** Don't add a Go module, vendored JS, or a system package without asking.
- **Be concise.** Report what changed, what was verified and how, and anything left open, with file links.
- **Bank questions** that aren't blocking and carry on; ask critical ones straight away.
- The user is learning Go (more at home in Python) and is new to Podman and Caddy: explain Go or Podman specifics briefly when they matter.

## Architecture in brief

Two processes per server (see ARCHITECTURE.md and SPEC §6.1):

- **`roostack`** (`cmd/roostack`): the unprivileged daemon (user `roostack`, uid 999 on the dev VMs) serving the web UI and lab API. Heavily sandboxed (`ProtectSystem=strict`, `NoNewPrivileges`, private /tmp). It **never runs podman directly**: containers and anything needing the user's namespaces go through the roostack user's systemd manager (`systemctl --user`, `systemd-run --user`, `podman unshare` via `userLaunch`).
- **`roostack-agent`** (`cmd/roostack-agent`): runs as root on a Unix socket and does only the few things that need root (firewall, WireGuard, OS updates, drives, mounts, time zone). Keep it small; every agent call is a privilege boundary.

A **lab** is one or more servers joined over WireGuard; one is the **controller** (it keeps the desired state, runs the manager, and serves the UI for all). Other servers are **nodes**, reached over an mTLS lab API (port 8471).

The packages, grouped by layer, are in [ARCHITECTURE.md's package table](ARCHITECTURE.md#packages), with "Life of an install" and "Life of a backup run" above it. Where things live on a host: the table in [README.md](README.md).

## Building and testing as an agent

CONTRIBUTING.md has the commands. On top of them:

- **On a Mac** Roostack builds, vets and tests natively (`make vet`, `go test ./...`), and the Linux-only tests skip and say why. Homebrew's restic runs the backup tests natively. The VM is still needed for anything about Podman, systemd, networking, drives or the sandbox, and for the full suite with restic on Linux: `scripts/dev-vm.sh test` builds the test binaries on the Mac, runs them in the VM with its restic, and takes `-test.run …`.
- **The restic-backed tests skip without restic,** and silently unless you run `go test -v`. Have restic on your `PATH` (`brew install restic` on a Mac) or set `ROOSTACK_RESTIC_BIN`, and before reporting done, check that the backup tests ran rather than skipped: a skipped test has hidden a breakage before.
- **Generated files:** after a change to what Go generates (golden files, the package editor's schema, examples or corpus, the gallery, THIRD_PARTY.md), run `make generated` and read the diff; CONTRIBUTING.md's "Regenerating files" has the rest, which Python generates.
- **Sharing a VM:** each worktree tests in its own folder in the VM (`~/test-<worktree>`, removed afterwards), so sessions can share one; `sync` and `install` still share `~/roostack`, so don't deploy to a VM another session is using without saying so.
- **The user's own lab may be on a VM:** don't change its settings or data beyond what the task needs, and say what you touched.
- Never `pkill -f` or `pgrep -f` with a pattern that also appears in the same shell command (it kills that shell); use a bracket pattern like `'dev-vm[.]sh ui'`.
- **Before reporting done:** `make vet`, the full suite with restic, a deploy to a VM, and a look at the page in the browser (desktop and a phone's width) for UI work. Say plainly what was and wasn't verified.

## Writing

CONTRIBUTING.md's "Writing" applies to everything you write: British English, short plain sentences, no jargon for end users but the exact detail one click away, and the same name for the same thing everywhere. The user's wording rules (2026-10-22) apply to every sentence people read, in the UI, the help, error messages, activity logs and the docs:

- **No present simple for actions.** It sounds generated: "Roostack keeps it stopped." or "Immich starts again."
- **The future for what will happen,** especially after something a person might do: "Roostack will keep the service stopped until you choose."
- **The present continuous for what's happening now:** "Immich is starting again on server-1."
- **What's fine as it is:** states with "is" ("Immich is stopped"), past results ("its restore didn't finish") and instructions ("Choose what to do").
- **Not clipped, and not too long.** Stay concise, but don't string short statements together: join them where a person would. Not "Its databases didn't load. Roostack keeps it stopped. Choose what to do." but "Its databases didn't load, so Roostack will keep the service stopped until you choose what to do."

Existing text doesn't follow these rules everywhere yet; a later pass will rework it area by area (docs/QUALITY-REVIEW-2026-10-21.md, wave 4). Until then, write new and changed text this way, and leave other sentences alone unless the task is about them.

## Commits

- Commit a finished, tested piece of work with a message that says what and why (subject ≤ 72 characters, body wrapped). Don't commit half-done work, and never commit `NOTES.local.md`.
- End the message with the trailer the session provides (e.g. `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`).
- Don't push, tag or open PRs unless asked.
