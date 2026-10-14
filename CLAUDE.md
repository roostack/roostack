# CLAUDE.md

Guidance for Claude (and other agents) working on Roostack. Read this first, then the documents it points to.

## What Roostack is

Roostack (pronounced "Roost Stack") self-hosts useful services (photos, passwords, git, media) on your own hardware: one-click installs, automatic HTTPS, backups and updates, from a simple web UI. The end user is often **not technical** (a NUC at someone's parents' house), but **a technical user must never find anything magical**: every page says what Roostack is doing, where files live, and shows the log.

Stack: Go (module `roostack.cloud/roostack`, Go 1.26), Debian 13, rootless Podman driven by systemd Quadlets, Caddy, restic, WireGuard, SQLite, server-rendered HTML with htmx and Alpine.js. Early development: nothing is released, and only fresh installs are supported (no migration of pre-release data beyond `store` schema additions).

## Documents: read before, update with every change

| File | Holds | Update when |
|---|---|---|
| [PLAN.md](PLAN.md) | The brief and working rules | Rarely (the user owns it) |
| [SPEC.md](SPEC.md) | Every decision, with its rationale and what was rejected. Sections say **Locked**, **Open** or **as built (date)** | You make or change a decision, or build something a section describes |
| [TASKS.md](TASKS.md) | What's done and what's next, by phase | You finish something, or find follow-up work (file it, don't lose it) |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Processes, data flow, packages, generated names | Packages or processes change |
| [docs/ASSUMPTIONS.md](docs/ASSUMPTIONS.md) | What the code assumes about real hosts, ✅ once verified on a VM | You rely on or verify host behaviour |
| [docs/DESIGN.md](docs/DESIGN.md) | The design system: tokens, components, rules | The UI gains a component or token |
| `NOTES.local.md` | The user's own notes | Never: don't edit or commit it |

**Roostack Cloud** (the hosted service on roost.ac) lives in its own repository. Here, design only what works standalone, without Cloud (e.g. remote access, a lab's own edge), like any feature; mention Cloud's capabilities at a high level at most. Cloud's implementation detail (plans, pricing, edges, accounts, anything sensitive) stays in the Cloud repository.

Docs are part of the change, in the same commit. Write them for a reader new to the project: plain sentences, dates on decisions (`Locked (2026-09-28)`), the reason, and what was considered instead.

## Working with the user

- **Ask when unsure; never assume.** For anything that's the user's call (product behaviour, wording that changes meaning, security trade-offs), propose options with a recommendation and let them decide. Don't ask about things with an obvious conventional answer: pick it, say so.
- **Pause for validation** between tasks and before large or hard-to-reverse steps. Show mock-ups before building a big UI change.
- **Dependencies are chosen together.** Don't add a Go module, vendored JS, or a system package without asking. The UI uses what's vendored in `internal/web/static` (htmx, Alpine); icons are drawn in `internal/web/icons.go`.
- **Be concise.** Report what changed, what was verified and how, and anything left open, with file links.
- **Bank questions** that aren't blocking and carry on; ask critical ones straight away.
- The user is learning Go (more at home in Python) and is new to Podman and Caddy: explain Go or Podman specifics briefly when they matter.

## Architecture in brief

Two processes per server (see ARCHITECTURE.md and SPEC §6.1):

- **`roostack`** (`cmd/roostack`): the unprivileged daemon (user `roostack`, uid 999 on the dev VMs) serving the web UI and lab API. Heavily sandboxed (`ProtectSystem=strict`, `NoNewPrivileges`, private /tmp). It **never runs podman directly**: containers and anything needing the user's namespaces go through the roostack user's systemd manager (`systemctl --user`, `systemd-run --user`, `podman unshare` via `userLaunch`).
- **`roostack-agent`** (`cmd/roostack-agent`): runs as root on a Unix socket and does only the few things that need root (firewall, WireGuard, OS updates, drives, mounts, time zone). Keep it small; every agent call is a privilege boundary.

A **lab** is one or more servers joined over WireGuard; one is the **controller** (it keeps the desired state, runs the manager, and serves the UI for all). Other servers are **nodes**, reached over an mTLS lab API (port 8471).

Key packages:

| Package | Role |
|---|---|
| `config`, `pkgspec`, `instance`, `state` | Lab settings, package definitions, installed services (YAML desired state) |
| `generate`, `quadlet`, `caddy`, `reconcile`, `systemd` | Desired state → Quadlet and Caddy files → running units |
| `node` | One server: applies its assignment, runs its jobs and settings; `Local` and `Remote` implement the same interfaces |
| `manager` | The controller's view of the whole lab (placement, backups, storage for other labs) |
| `web` | Handlers (thin) and templates; business logic stays in the packages above |
| `backup`, `maintenance`, `osupdate` | restic jobs, the maintenance coordinator (one job at a time, reboot window), OS updates |
| `labnet`, `labrestore`, `setup`, `store`, `agent` | Lab network and joins, full-lab restore, the first-run wizard, SQLite runtime data, the agent client/server |
| `authkit` (top level, public) | Passwords (argon2id), tokens, two-factor codes; shared with Cloud, which adds a pepper and sealed secrets |
| `cloudapi` (top level, public) | The lab ↔ Cloud API contract, imported by Cloud's repository too |
| `awssig` (top level, public) | AWS Signature Version 4 with the standard library only (Route 53, S3-compatible storage); Cloud signs Wasabi's STS and S3 calls with it |
| `cloud` | The controller's link to Roostack Cloud (linking, its certificate, unlinking); `cloudtest` fakes Cloud's side for tests |
| `units` | The one size formatter (decimal: 1 GB = 1000³ bytes) |

Where things live on a host: see the table in [README.md](README.md). Paths come from `config.Paths`; never hard-code `/srv/roostack` or `/var/lib/roostack` outside `config`.

## Commands

```sh
make build        # bin/roostack, bin/roostack-agent: Linux, CGO off; GOARCH=arm64 for ARM
make vet          # go vet + gofmt check
go test ./...     # all tests
make golden       # rewrite generator golden files after an intended change; review the diff
go test ./internal/web -run TestGalleryFile -update-gallery   # regenerate docs/design/gallery.html
make check-images # after changing an image pin: every pin has amd64 and arm64 builds (asks the registries)
```

**restic-backed tests skip silently** when restic isn't installed on the dev machine (it isn't). Copy the VM's binary once and always run the full suite with it; a skipped test has hidden a breakage before:

```sh
scripts/dev-vm.sh ssh 'cat /usr/bin/restic' > <scratch>/restic
ROOSTACK_RESTIC_BIN=<scratch>/restic go test ./...
```

**On a Mac** parts of Roostack don't build (they're Linux-only), so `make vet` and `go test` fail there: use `GOOS=linux go vet ./...`, and run the suite in the VM with `scripts/dev-vm.sh test` (it builds the test binaries on the Mac, runs them in the VM with its restic, and takes `-test.run …`).

## Dev VMs

`scripts/dev-vm.sh` runs Debian 13 VMs under plain QEMU (header comment has the details): KVM on Linux, Hypervisor.framework on macOS. A VM has the machine's own architecture (arm64 on an Apple Silicon Mac, for ARM work: [ARM64-PLAN.md](ARM64-PLAN.md)); `ROOSTACK_VM_ARCH` picks the other, emulated and slow. `ROOSTACK_VM=n` selects VM n; its ports are offset by `(n-1)*10000` (UI tunnels on 18481, 28481, 38481).

```sh
ROOSTACK_VM=1 scripts/dev-vm.sh up | ssh [cmd] | ui | down | status
ROOSTACK_VM=1 scripts/dev-vm.sh usb add|remove NAME [SIZE]     # hot-plug a virtual USB drive
# Deploy a build:
make build && ROOSTACK_VM=1 scripts/dev-vm.sh sync
ROOSTACK_VM=1 scripts/dev-vm.sh ssh 'sudo install -m755 ~/roostack/bin/roostack ~/roostack/bin/roostack-agent /usr/bin/ && sudo systemctl restart roostack-agent roostack'
```

- The real service registry can't be fetched until the Owner's key ceremony (docs/REGISTRY-KEYS.md). To test fetching, make test keys with `roostack registry keygen`, build an index from a registry checkout with `roostack registry index`, serve it in the VM (`python3 -m http.server`), build with `REGISTRY_DEV_KEY=<test root public key>` (passed through by `dev-vm.sh sync`/`install`), and set `registry.url` in the VM's `lab.yaml`.
- Commands as the roostack user need `XDG_RUNTIME_DIR=/run/user/999` and `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/999/bus`, run from its home.
- QEMU NATs everything: inside a VM all clients come from 10.0.2.2, and another VM reaches VM n's WireGuard at `10.0.2.2:<its port>`.
- **Verify on a VM, not by reasoning,** anything about Podman, systemd, networking, drives or the sandbox. Test with the unit's full sandbox options: a partial test once missed that `ProtectHome=yes` hides `/run/user`.
- The user's own lab may be on a VM: don't change its settings or data beyond what the task needs, and say what you touched. Never restore a backup of a lab that had a real domain onto another VM: it would change real DNS records and certificates.
- Never `pkill -f`/`pgrep -f` with a pattern that also appears in the same shell command (it kills that shell); use a bracket pattern like `'dev-vm[.]sh ui'`.

## Code conventions

- **Go:** gofmt; imports in three groups (standard library, third party, `roostack.cloud/roostack/...`). Small packages, plain functions, interfaces where a fake is needed in tests (`node.Node`, `web.ServiceManager`, `web.LabSetup`, `manager.BackupStore`).
- **Comments** match the surrounding density: a doc comment on every exported and most unexported names, saying *why*, and citing the SPEC section (`// ... (SPEC.md §6.9)`). No commented-out code.
- **Errors people can fix** are `FormError` (`manager.formErrorf`, `setup.CheckError`) with a sentence that says what to do; the web layer shows them via `userMessage`. Anything else is a server error, logged.
- **Long work** runs in the background with an `activity` log (`Step` for the status line, `Printf` for details), shown with the `activity` component, so people can watch what's happening.
- **Web UI:**
  - Pages use only the design system (docs/DESIGN.md): purpose tokens, never raw colours; components from `components.go` (`notice`, `status`, `badge`, `back`, `confirm`, `activity`, `icon`). A new component goes into the gallery (`gallery.go`) in the same change.
  - Handlers are thin; each POST redirects (PRG) or re-renders with a `FormError`. State-changing routes use `requireOwner` or the right `require(auth.…)`.
  - htmx attributes are inherited: a container with `hx-select`/`hx-swap` needs `hx-disinherit="*"`, or children's requests break.
  - Sizes go through `size`/`bytes` in templates and `units.Bytes` in Go; never format bytes by hand.
- **Writing (UI, docs, comments):** British English (colour, licence), short plain sentences, no jargon for end users but exact detail one click away ("Details", "Manage", "Tell me more"). Say what happened and what to do next. Name things consistently (the backup plan: "main backup", "backup 3", "extra backup").
- **Security:** never print or log secrets (setup codes, join codes, recovery keys, passwords, TOTP secrets, API tokens); point to where they are instead. The one exception is the first-run setup code, which the installer and log show on purpose (docs/ASSUMPTIONS.md K5). Secrets on disk are written 0600 (`writePrivate`). The agent validates every path and argument it's given.

## Testing

- Web tests drive the real handlers through `httptest` with fakes (`fakeManager`, `fakeSetup`, `fakeBackups` in `internal/web/*_test.go`); log in with `loggedIn(t, role)`. Add to the fakes rather than bypassing them.
- Manager and backup tests use a real SQLite store and real restic (see above).
- Generator output is golden-file tested; after an intended change run `make golden` and read the diff.
- Scripted edits (sed, Python replacements) must check that each replacement matched exactly once: a silent miss once left a feature unwired and crashed the UI.
- Before reporting done: `make vet`, the full suite with restic, a deploy to a VM, and a look at the page in the browser (desktop and a phone width) for UI work. Say plainly what was and wasn't verified.

## Commits

- Commit a finished, tested piece of work with a message that says what and why (subject ≤ 72 characters, body wrapped). Don't commit half-done work, and never commit `NOTES.local.md`.
- End the message with the trailer the session provides (e.g. `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`).
- Don't push, tag or open PRs unless asked.
