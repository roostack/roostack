# MyLab

MyLab lets you self-host useful services (photos, passwords, git, media, and more) on your own hardware, with one-click installs, automatic HTTPS, backups and updates, managed from a simple web interface.

> **Status: early development.** Nothing here is ready for real use yet. See [TASKS.md](TASKS.md) for progress and [SPEC.md](SPEC.md) for the design decisions and why they were made.

## How it works

- Every service runs in **rootless Podman containers**, managed by **systemd** through [Quadlet](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html) files.
- MyLab keeps your **desired state** (which services, which settings) as readable YAML files, generates the container and proxy config from it, and makes the host match.
- **Caddy** serves every service on its own subdomain with a wildcard HTTPS certificate. Services are private (LAN/VPN only) unless you make them public.
- Everything MyLab generates is plain text you can read. MyLab tells you where it lives.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the details.

## Where things live

| Path | What |
|---|---|
| `/var/lib/mylab/lab.yaml` | Lab settings: domain, DNS provider, paths |
| `/var/lib/mylab/services/<name>.yaml` | Your installed services and their settings |
| `/srv/mylab/services/<name>/` | Each service's data |
| `/srv/mylab/media/<name>/` | Large data (photos, videos); can be a separate disk |
| `/var/lib/mylab/home/.config/containers/systemd/` | Generated Quadlet files (`mylab-*`) |
| `/var/lib/mylab/caddy/` | Generated Caddy config |
| `/usr/share/mylab/catalog/` | Package definitions shipped with MyLab |

All of these can be changed in `lab.yaml`. The defaults are in [internal/config/config.go](internal/config/config.go).

## Requirements

- A dedicated machine running **Debian 13** (x86_64). Ubuntu 26.04 support is planned.
- A domain you own, with a DNS provider MyLab supports (currently Cloudflare), for HTTPS certificates.

## Development

You need Go 1.25 or newer.

```bash
make build   # builds bin/mylab and bin/mylab-agent
make test    # runs the tests
make golden  # rewrites generator golden files after an intended change
```

Useful commands that work without a MyLab host:

```bash
# Check package files
bin/mylab validate docs/examples/*.yaml

# See exactly what MyLab would generate for a service
bin/mylab render -lab docs/examples/lab.yaml \
  -package docs/examples/immich.yaml \
  -instance internal/generate/testdata/immich.instance.yaml
```

To try it on a Debian 13 VM, see [scripts/dev-install.sh](scripts/dev-install.sh) (development only, not the real installer).

## Project documents

- [PLAN.md](PLAN.md): the project brief
- [SPEC.md](SPEC.md): decisions, with rationale
- [TASKS.md](TASKS.md): what's done and what's next
- [ARCHITECTURE.md](ARCHITECTURE.md): how the code fits together
- [docs/ASSUMPTIONS.md](docs/ASSUMPTIONS.md): things the code assumes that haven't been verified on a real host yet
- [docs/examples/](docs/examples/): reference package definitions
