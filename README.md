# Roostack

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
| `/srv/roostack/services/<name>/` | Each service's data |
| `/srv/roostack/media/<name>/` | Large data (photos, videos); can be a separate disk |
| `/var/lib/roostack/home/.config/containers/systemd/` | Generated Quadlet files (`roostack-*`) |
| `/var/lib/roostack/caddy/` | Generated Caddy config |
| `/usr/share/roostack/catalog/` | Package definitions shipped with Roostack |

All of these can be changed in `lab.yaml`. The defaults are in [internal/config/config.go](internal/config/config.go).

## Requirements

- A dedicated machine running **Debian 13** (x86_64). Ubuntu 26.04 support is planned.
- A domain you own, with a DNS provider Roostack supports (currently Cloudflare), for HTTPS certificates.

## Development

You need Go 1.25 or newer.

```bash
make build   # builds bin/roostack and bin/roostack-agent
make test    # runs the tests
make golden  # rewrites generator golden files after an intended change
```

Useful commands that work without a Roostack host:

```bash
# Check package files
bin/roostack validate docs/examples/*.yaml

# See exactly what Roostack would generate for a service
bin/roostack render -lab docs/examples/lab.yaml \
  -package docs/examples/immich.yaml \
  -instance internal/generate/testdata/immich.instance.yaml
```

Run the web UI locally against a throwaway state directory: point `paths` in a copy of `docs/examples/lab.yaml` at a temp dir and run `bin/roostack serve -lab <file> -listen 127.0.0.1:8480`.

To try it on a Debian 13 VM, see [scripts/dev-install.sh](scripts/dev-install.sh) (development only, not the real installer).

## Project documents

- [PLAN.md](PLAN.md): the project brief
- [SPEC.md](SPEC.md): decisions, with rationale
- [TASKS.md](TASKS.md): what's done and what's next
- [ARCHITECTURE.md](ARCHITECTURE.md): how the code fits together
- [docs/ASSUMPTIONS.md](docs/ASSUMPTIONS.md): things the code assumes that haven't been verified on a real host yet
- [docs/examples/](docs/examples/): reference package definitions
