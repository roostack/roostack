# Contributing to Roostack

Thanks for wanting to help. Roostack lets people self-host services (photos, passwords, git, media) on their own hardware from a simple web UI, and the person running it is often not technical. A technical person must never find anything magical, though, so every page says what Roostack is doing, where its files live, and shows the log. Changes are judged against both of those people.

This guide covers building, testing and running Roostack on Linux and on a Mac, the dev VM, the generated files, the conventions, and which documents to update. AI agents working on the project also read [CLAUDE.md](CLAUDE.md), which adds what's specific to them.

Roostack is in early development: nothing is released, and only fresh installs are supported.

## Where to start reading

The documents are large, so read them in this order and stop when you have what you need:

1. [README.md](README.md): what Roostack is, where things live on a server, and the commands.
2. [ARCHITECTURE.md](ARCHITECTURE.md): the two processes, how desired state becomes running services, and the packages by layer. "Life of an install" and "Life of a backup run" follow one action through the code, file by file, and are the quickest way into it.
3. This guide's [conventions](#conventions).
4. The package you're changing: every package has a doc comment, and `internal/manager`'s maps its files to features.
5. [SPEC.md](SPEC.md), for the area you're working on: its contents list at the top takes you there. It records every decision, why it was made, and what was considered instead.
6. [docs/DESIGN.md](docs/DESIGN.md), before changing a page: the design system's tokens, components and rules.
7. [TASKS.md](TASKS.md): what's next ([TASKS-DONE.md](TASKS-DONE.md) has what's finished).

[docs/ASSUMPTIONS.md](docs/ASSUMPTIONS.md) lists what the code assumes about real hosts, and which of those have been checked on a VM. [PLAN.md](PLAN.md) is the original brief.

## Before you start

For anything bigger than a small fix, please [open an issue](https://github.com/roostack/roostack/issues/new/choose) first, so we can agree the approach before you spend time on it. Roostack's behaviour, its wording and its security trade-offs are decided with the maintainer and recorded in SPEC.md, and some of what looks missing has been decided against for a reason that's written there.

New dependencies are chosen together too: please ask before adding a Go module, vendored JavaScript or a system package. The UI uses only what's vendored in `internal/web/static` (htmx and Alpine.js), and icons are drawn in `internal/web/icons.go`.

Security problems go to [SECURITY.md](SECURITY.md), not to an issue. Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md).

## What you need

- **Go** 1.26 or newer, the module's minimum. CI and releases build with Go 1.27.3.
- **Node** 20 or newer, for the package editor's JavaScript tests (`make test` runs them). They need no packages installed.
- **restic**, so the backup tests run rather than skip: `brew install restic` on a Mac, `apt install restic` on Debian. CI uses restic 0.18.0, Debian 13's.
- **[uv](https://docs.astral.sh/uv/)**, to build the help site. Without it, Roostack builds without its help pages and says so at `/help/`.
- **QEMU**, for the dev VM: `brew install qemu` on a Mac; on Debian, `qemu-system-x86` (or `qemu-system-arm` with `qemu-efi-aarch64`); on Arch, `qemu-system-x86_64` (or `qemu-system-aarch64` with `edk2-aarch64`).

## Building and running

```sh
make build   # the help site, then bin/roostack and bin/roostack-agent
make vet     # go vet and the gofmt check
make test    # the editor's tests, then go test ./...
```

`make build` always writes **Linux** binaries, because Roostack runs on Debian 13: for your machine's processor, or another with `GOARCH=arm64` or `GOARCH=amd64`. The build is reproducible (`-trimpath`, no build ID), so the same commit and Go version give the same bytes anywhere. `make release` builds a release from a checkout of its tag, and `make verify-release VERSION=x.y.z` checks a published one against its source ([docs/RELEASING.md](docs/RELEASING.md)).

**On Linux**, the binaries in `bin/` run as they are. A few commands are useful without a Roostack host:

```sh
# Check package files (the registry's, or the test fixtures here)
bin/roostack validate internal/testdata/packages/*/*-roostack.*.yaml

# See what Roostack would generate for a service
bin/roostack render -package internal/testdata/packages/immich/2.1.0-roostack.1.yaml

# The web UI against a throwaway state folder: point `paths` in a copy of
# docs/examples/lab.yaml at a temporary folder first
bin/roostack serve -lab <file> -listen 127.0.0.1:8480
```

**On a Mac**, Roostack builds, vets and tests natively: `make vet` and `make test` work as they do on Linux, and the tests that need Linux (peer credentials, `/proc`, ICMP and the like) skip and say why. The binaries in `bin/` are Linux ones, so run commands with `go run ./cmd/roostack validate …` instead. Anything that touches Podman, systemd, networking, drives or Roostack's sandbox needs a Debian 13 host, which is what the dev VM is for.

## Testing

- `go test ./...` runs every Go test, and `make editor-test` the editor's JavaScript tests, including its checks against `pkgspec`'s on every package of the corpus.
- **The backup tests use the real restic** and skip without it, silently unless you run `go test -v`. With restic on your `PATH` they run; `ROOSTACK_RESTIC_BIN=/path/to/restic` picks a particular binary. A skipped test has hidden a breakage before, so make sure they ran.
- **Web tests** drive the real handlers through `httptest`, with fakes for the layers underneath (`fakeManager`, `fakeSetup`, `fakeBackups` and the rest in `internal/web/fakes_test.go`, where `fakeManager` is one embedded fake per area), and the harness in `harness_test.go`, where `loggedIn(t, role)` signs in. The harness fails a page that shows `<no value>` or a printf's `%!`, which is what a template shows for data it wasn't given. Add to the fakes rather than going around them.
- **Manager and backup tests** use a real SQLite store and real restic.
- **The generator** is tested against golden files ([regenerating them](#regenerating-files) is below).
- **The whole suite on Linux**, in the dev VM: `scripts/dev-vm.sh test` builds the test binaries on your machine, runs them in the VM with its restic, and takes the test binaries' flags, such as `-test.run TestApply`.

Before asking for a review, run `make vet`, the full suite with restic, and, for anything a host or page would show, try it on the dev VM. For a page, look at it on a desktop and at a phone's width. Say plainly what you checked and what you couldn't.

## The dev VM

`scripts/dev-vm.sh` runs Debian 13 under plain QEMU: KVM on Linux, Hypervisor.framework on macOS. A VM has your machine's own processor (arm64 on an Apple Silicon Mac); `ROOSTACK_VM_ARCH=amd64` or `arm64` picks the other, emulated and slow. Its header comment has every detail.

```sh
scripts/dev-vm.sh up        # create it (the first time) and start it
scripts/dev-vm.sh install   # build here, copy to ~/roostack in the VM, run scripts/dev-install.sh
scripts/dev-vm.sh ui        # tunnel Roostack's UI to http://127.0.0.1:18481
scripts/dev-vm.sh ssh [cmd] # a shell, or a command, as the user "dev"
scripts/dev-vm.sh test [flags]
scripts/dev-vm.sh usb add|remove NAME [SIZE]   # plug a virtual USB drive in or out
scripts/dev-vm.sh down | destroy | status
```

To deploy a new build to a VM that already has Roostack:

```sh
make build && scripts/dev-vm.sh sync
scripts/dev-vm.sh ssh 'sudo install -m755 ~/roostack/bin/roostack ~/roostack/bin/roostack-agent /usr/bin/ && sudo systemctl restart roostack-agent roostack'
```

Things worth knowing:

- **Several VMs:** `ROOSTACK_VM=n` picks VM n, from 1 to 9 (disk space allows about four at once), for example a second server joining the lab. VMs 1 to 5 use VM 1's ports plus `(n-1)*10000` (their UI tunnels are 18481, 28481 … 58481); VMs 6 to 9 use 6x481 and so on, with x = n − 6 (UI tunnels 60481 to 63481).
- **Networking:** QEMU puts every connection through NAT, so inside a VM all clients seem to come from 10.0.2.2. Another VM reaches VM n's WireGuard at `10.0.2.2:<its UDP port>`.
- **Commands as the roostack user** need its systemd session: run them from its home with `XDG_RUNTIME_DIR=/run/user/<its uid>` and `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<its uid>/bus`.
- **The service registry** can't be fetched until the release keys exist ([docs/REGISTRY-KEYS.md](docs/REGISTRY-KEYS.md)). To test fetching, make test keys with `roostack registry keygen`, build an index from a checkout of [roostack/services](https://github.com/roostack/services) with `roostack registry index`, serve it in the VM (`python3 -m http.server`), build with `REGISTRY_DEV_KEY=<the test root public key>` (`sync` and `install` pass it through), and set `registry.url` in the VM's `lab.yaml`.
- **Test on a VM, not by reasoning,** anything about Podman, systemd, networking, drives or the sandbox, and use the unit's full sandbox options when you do: a partial test once missed that `ProtectHome=yes` hides `/run/user`.
- **Never restore a backup of a lab with a real domain onto another VM:** it would change that domain's real DNS records and certificates.

## Regenerating files

Some files are generated and checked into the repository. A test fails when one is out of date, so after a change that affects one, regenerate it and read the diff. Run `make generated` to rewrite everything Go generates, the first four rows, in one go:

| What | When | How |
|---|---|---|
| The generator's golden files | You changed what Roostack generates | `make golden` |
| The package editor's `schema.js`, `examples.js` and corpus | You changed the package format or the package policy | `go test ./internal/editor -update-editor`, then `make editor-test` |
| The design system's gallery, `docs/design/gallery.html` | You changed a component or a token | `go test ./internal/web -run TestGalleryFile -update-gallery` |
| `THIRD_PARTY.md` | You added or updated a dependency | `go test ./internal/web -run TestThirdPartyFile -update-credits` |
| The help site, embedded at `/help/` | You changed `help/` | `make build` (needs uv); `cd help && uv run zensical serve` previews it while you write |
| The error pages' scenes, `internal/errpage/scenes.go` | You changed `assets/errors` | `assets/errors/generate.py` |
| The brand mark and its PNGs | You changed `assets/brand` | `assets/brand/generate.py` (needs `rsvg-convert`; [assets/brand/README.md](assets/brand/README.md)) |

Two checks ask the outside world, so they don't run with the tests: `make check-images` asks the registries whether every pinned image has amd64 and arm64 builds (run it after changing a pin), and `make caddy-check` checks the generated Caddy config against a real Caddy build (`ROOSTACK_CADDY_BIN=…`).

## Conventions

### Go

- `gofmt`, and imports in three groups: the standard library, third party, then `roostack.cloud/roostack/...`.
- Small packages and plain functions, with interfaces where a test needs a fake (`node.Node`, `web.ServiceManager`, `web.LabSetup`, `manager.BackupStore`).
- A doc comment on every exported name and most unexported ones, saying *why*, and citing the SPEC section it follows: `// ... (SPEC.md §6.9)`. No commented-out code.
- **Errors people can fix** are `usererr.Error`, made with `usererr.New`, with a sentence saying what to do; the web layer shows them through `usererr.Message`. Anything else is a server error, which is logged.
- **Long work** runs in the background with an `activity` log (`Step` for the status line, `Printf` for the details), shown with the `activity` component, so people can watch what's happening.
- **Paths** come from `config.Paths`: never write `/srv/roostack` or `/var/lib/roostack` outside `internal/config`.
- **Sizes** go through `units.Bytes` in Go and `size` or `bytes` in templates (decimal units: 1 GB is 1000³ bytes); never format bytes by hand.
- **Secrets** are never printed or logged (setup codes, join codes, recovery keys, passwords, two-factor secrets, API tokens): say where they are instead. The one exception is the first-run setup code, which the installer and the log show on purpose (docs/ASSUMPTIONS.md K5). Secrets on disk are written 0600 with `fsutil.WritePrivate`. The agent checks every path and argument it's given, since each of its calls crosses into root.
- **The package editor's checks** (`internal/editor/static/check.js`) must say what `pkgspec`'s say: a change to the package format or the package policy changes `check.js` too, and the corpus test checks they agree.
- **Scripted edits** (sed, Python replacements) must check that each replacement matched exactly once: a silent miss once left a feature unwired and crashed the UI.

### The web UI

- Pages use only the design system ([docs/DESIGN.md](docs/DESIGN.md)): purpose tokens, never raw colours, and the components in `components.go` (`notice`, `status`, `badge`, `back`, `confirm`, `activity`, `icon`). A new component goes into the gallery (`gallery.go`) in the same change.
- Handlers are thin, and business logic stays in the packages under them. Each POST redirects (post, redirect, get) or shows the form again with a `usererr.Error`'s sentence. Routes that change something use `requireOwner` or the right `require(auth.…)`.
- htmx attributes are inherited: a container with `hx-select` or `hx-swap` needs `hx-disinherit="*"`, or its children's requests break.

### Writing

This covers the UI, the help, the docs and comments.

- British English (colour, licence), in short, plain sentences. No jargon for people running Roostack, but the exact detail one click away ("Details", "Manage", "Tell me more").
- Say what happened and what to do next, and name things the same way everywhere (the backup plan: "main backup", "backup 3", "extra backup").
- For what Roostack does, use the future for what will happen ("Roostack will keep the service stopped until you choose") and the present continuous for what's happening now ("Immich is starting again on server-1"), not the present simple ("Immich starts again"). States, past results and instructions are fine as they are ("Immich is stopped", "its restore didn't finish", "Choose what to do"). Join short statements where a person would, rather than stringing them together. CLAUDE.md's "Writing" section has more examples.
- In the project's own documents, "the maintainer" is the person who runs the project; "the Owner" is the top role in a lab.

## Which documents to update

Docs are part of the change, in the same commit. Write them for someone new to the project: plain sentences, a date on each decision (`Locked (2026-09-28)`), the reason, and what was considered instead.

| File | Holds | Update when |
|---|---|---|
| [SPEC.md](SPEC.md) | Every decision, with its reasons and what was rejected. Sections say **Locked**, **Open** or **as built (date)** | You make or change a decision, or build something a section describes |
| [TASKS.md](TASKS.md) | What's next, by phase; [TASKS-DONE.md](TASKS-DONE.md) has what's finished | You finish something (move it to TASKS-DONE.md), or find follow-up work (add it, so it isn't lost) |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Processes, data flow, packages, generated names | Packages or processes change |
| [docs/ASSUMPTIONS.md](docs/ASSUMPTIONS.md) | What the code assumes about real hosts, ✅ once checked on a VM | You rely on or check how a host behaves |
| [docs/DESIGN.md](docs/DESIGN.md) | The design system: tokens, components, rules | The UI gains a component or a token |
| [help/](help/) | The help site, for people running Roostack | What people see or do changes |
| [PLAN.md](PLAN.md) | The original brief | Rarely: the maintainer owns it |

## Commits and pull requests

- Commit a finished, tested piece of work, with a message that says what changed and why: a subject of 72 characters at most, then a wrapped body. Don't commit half-done work.
- A pull request says what changed, how you checked it, and anything you couldn't check. CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs `make vet`, staticcheck, the whole suite with restic, the help site's strict build, a check that the files Python generates are up to date, shellcheck, actionlint and govulncheck on every pull request.

## Licence

Roostack is under the [Apache License 2.0](LICENSE) (SPEC.md §3.3). By contributing, you agree that your contribution comes under the same licence, as its section 5 says.
