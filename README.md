# Roostack

*Pronounced "Roost Stack".*

Roostack lets you self-host useful services (photos, passwords, git, media, and more) on your own hardware, with one-click installs, automatic HTTPS, backups and updates, managed from a simple web interface.

> **Status: early development.** Nothing here is ready for real use yet. See [TASKS.md](TASKS.md) for progress and [SPEC.md](SPEC.md) for the design decisions and why they were made.

## How it works

- Every service runs in **rootless Podman containers**, managed by **systemd** through [Quadlet](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html) files.
- Roostack keeps your **desired state** (which services, which settings) as readable YAML files, generates the container and proxy config from it, and makes the host match.
- **Caddy** serves every service on its own subdomain with a wildcard HTTPS certificate. Services are private (LAN/VPN only) unless you make them public.
- Everything Roostack generates is plain text you can read. Roostack tells you where it lives.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the details.

## Where things live

| Path | What |
|---|---|
| `/var/lib/roostack/lab.yaml` | Lab settings: domain, DNS provider, paths |
| `/var/lib/roostack/services/<name>.yaml` | Your installed services and their settings |
| `/var/lib/roostack/packages/<name>/<version>.yaml` | The package file each installed service runs from, copied from the catalog at install |
| `/srv/roostack/services/<name>/` | Each service's data |
| `/srv/roostack/media/<name>/` | Large data (photos, videos); can be a separate disk |
| `/var/lib/roostack/home/.config/containers/systemd/` | Generated Quadlet files (`roostack-*`) |
| `/var/lib/roostack/caddy/` | Generated Caddy config |
| `/var/lib/roostack/cloud/` | The lab's link to Roostack Cloud, if it has one (the controller only): its key and certificate, and a log of its requests |
| `/var/lib/roostack/local-packages/<name>/` | Packages the Owner added themselves ("Add your own"): one version each, with its logo; backed up |
| `/var/lib/roostack/catalog/` | What can be installed: packages fetched from the service registry and checked against its signed index (the controller only; not backed up) |

All of these can be changed in `lab.yaml`. The defaults are in [internal/config/config.go](internal/config/config.go).

## Running `roostack` commands on a server

Most commands (`backup`, `apply`) work on the `roostack` user's files and containers, so run them as that user, with its systemd session:

```bash
sudo -u roostack XDG_RUNTIME_DIR=/run/user/$(id -u roostack) roostack backup snapshots
```

Why `XDG_RUNTIME_DIR`: Roostack runs containers through the `roostack` user's own systemd manager (rootless Podman), and `systemctl --user` finds that manager through `$XDG_RUNTIME_DIR` (`/run/user/<uid>`, where its D-Bus socket is). `sudo -u` doesn't set it, so without it you get *"Failed to connect to user scope bus … $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined"*. The `roostack` service gets it from its systemd unit.

`roostack user`, `backup` and `apply` can also be run with plain `sudo`: started as root, they run themselves again as the roostack user, with its systemd session, so root never opens files the roostack user could have replaced with a link. `roostack node join` runs as root, joins as roostack and only restarts the service as root. See the help's [command line page](help/docs/reference/cli.md).

## Requirements

- A dedicated machine running **Debian 13**, on x86_64 or ARM64 (ARM mini PCs and cloud servers that run Debian's own images). Raspberry Pi and Ubuntu 26.04 support are planned.
- A domain you own, with a DNS provider Roostack supports, for HTTPS certificates: Cloudflare, Amazon Route 53, Porkbun, Gandi, GoDaddy, Namecheap or Spaceship.

## Development

You need Go 1.26 or newer, and [uv](https://docs.astral.sh/uv/) to build the help site (without it, Roostack builds without its help pages).

```bash
make build   # builds the help site, then bin/roostack and bin/roostack-agent
make test    # runs the tests
make golden  # rewrites generator golden files after an intended change
```

The help site is in [help/](help/): Markdown pages built with [Zensical](https://zensical.org), embedded in Roostack at `/help/`. Preview it while writing with `cd help && uv run zensical serve`.

Useful commands that work without a Roostack host:

```bash
# Check package files (the registry's, or the test fixtures here)
bin/roostack validate internal/testdata/packages/*/*-roostack.*.yaml

# See exactly what Roostack would generate for a service: its defaults in an
# example lab, or with -lab and -instance, a given lab and service
bin/roostack render -package internal/testdata/packages/immich/2.1.0-roostack.1.yaml
```

Run the web UI locally against a throwaway state directory: point `paths` in a copy of `docs/examples/lab.yaml` at a temp dir and run `bin/roostack serve -lab <file> -listen 127.0.0.1:8480`.

To try it on a Debian 13 VM, see [scripts/dev-install.sh](scripts/dev-install.sh) (development only, not the real installer).

### Continuous integration

GitHub Actions ([.github/workflows/ci.yml](.github/workflows/ci.yml)) checks every pull request and every push to `main`, on Ubuntu 24.04 with Go 1.27.2:

- `make vet`: `go vet` and gofmt;
- `go test ./...` with restic 0.18.0 (Debian 13's, downloaded from restic's release and checked against its SHA-256), so the backup tests run rather than skip;
- govulncheck, which fails only on a vulnerability the code can reach.

It needs no secrets. The help site isn't built: no test needs it. Actions are pinned by commit, with the release beside each in a comment.

Releases are published by [.github/workflows/apt-publish.yml](.github/workflows/apt-publish.yml), which the release workflow calls once a release's files are built and signed: it checks their signature, uploads them to `dl.roostack.cloud/releases/<version>/`, and adds the packages to Roostack's apt repository at `apt.roostack.cloud` ([scripts/build-apt-repo.sh](scripts/build-apt-repo.sh), SPEC §7.2). It runs in the `release` environment, whose required reviewer approves each release, and is the only place the signing key and the bucket's credentials are used.

## Project documents

- [PLAN.md](PLAN.md): the project brief
- [CLAUDE.md](CLAUDE.md): how to work on the project (for agents, and a quick start for people)
- [SPEC.md](SPEC.md): decisions, with rationale
- [TASKS.md](TASKS.md): what's done and what's next
- [ARCHITECTURE.md](ARCHITECTURE.md): how the code fits together
- [help/](help/): the help site, for people running Roostack
- [docs/ASSUMPTIONS.md](docs/ASSUMPTIONS.md): things the code assumes that haven't been verified on a real host yet
- [docs/examples/lab.yaml](docs/examples/lab.yaml): an example lab config
- The packages are in their own repository, [`roostack/services`](https://github.com/roostack/services); labs fetch its signed index ([docs/REGISTRY-KEYS.md](docs/REGISTRY-KEYS.md) for the keys)
