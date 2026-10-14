# Where files live

Everything Roostack generates is a plain text file you can read. This page
lists where each thing is. The folders under `/var/lib/roostack` and
`/srv/roostack` are the defaults; they can be changed in `lab.yaml`
(`paths:`).

!!! warning "Read, don't edit"

    Roostack writes most of these files from its own settings, and
    overwrites changes made by hand. Change things through Roostack's pages
    (or the files marked *desired state*), and it rewrites the rest.

## Settings and state: `/var/lib/roostack`

Owned by the `roostack` user, and readable only by it (and root).

| Path | What it is |
|---|---|
| `lab.yaml` | Lab settings: domain, DNS provider, paths, backups, restart window. *Desired state* |
| `services/<name>.yaml` | Each installed service: its package version, web address, ports, settings and server. *Desired state* |
| `packages/<name>/<version>.yaml` | The [package](packages.md) file each installed service runs from, copied from the catalog when it was installed. A newer Roostack's catalog doesn't change it, and it's backed up with the lab, so a restore brings back exactly what ran. Copies no service uses are deleted. *Desired state* |
| `roostack.db` | Roostack's database (SQLite): people and their sessions, the audit log, the lab's servers, backup destinations and results |
| `node.json` | This server's ID and name |
| `backup-key` | The [recovery key](../start/recovery-key.md), which encrypts every backup. Keep a copy elsewhere |
| `setup-token` | The first-run setup code; deleted once the Owner exists |
| `caddy/` | The generated web server config: `Caddyfile`, and `sites/<name>.caddy` for each service |
| `caddy-data/` | HTTPS certificates and the certificate account |
| `caddy-build.log` | The output of the latest build of Roostack's web proxy image (Caddy), shown on the Services page while it runs |
| `caddy-logs/` | The web server's access log, while anything is [public](../services/public.md), for CrowdSec |
| `crowdsec/` | CrowdSec's configuration and database, while anything is public |
| `labca/` | The lab's own certificate authority, on the controller: it vouches for each server on the lab's network |
| `labnet/` | This server's certificate for the lab's network, and `membership.json` (its role and address) |
| `wireguard-keys.json` | A copy of the lab network's WireGuard keys, for backups |
| `maintenance-journal.json` | The maintenance job running now, so an interrupted backup resumes after a restart |
| `db-dump/` | The consistent copy of `roostack.db` taken before each backup |
| `home/` | The `roostack` user's home folder: containers, images and generated units (below) |

### Generated units: `/var/lib/roostack/home/.config/containers/systemd`

One [Quadlet](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html)
file per container and network, all named `roostack-*`:

| File | What it is |
|---|---|
| `roostack-<service>.network` | The service's private network |
| `roostack-<service>-<container>.container` | Each of the service's containers |
| `roostack-<service>-db-<name>.container` | A database Roostack runs for the service |

systemd turns each file into a unit: `roostack-<service>-<container>.service`
for a container, and `roostack-<service>-network.service` for a network.
Roostack's own parts run the same way, such as `roostack-caddy` (the web
server) and `roostack-backup-store` (backups other servers keep here). See
their state with:

```bash
sudo -u roostack XDG_RUNTIME_DIR=/run/user/$(id -u roostack) systemctl --user status 'roostack-*'
```

## Your data: `/srv/roostack`

| Path | What it is |
|---|---|
| `services/<name>/<storage>/` | Each service's data |
| `services/<name>/db/<name>/` | A service's database files |
| `services/<name>/db-dump/` | Its database dump, made before each backup |
| `media/<name>/<storage>/` | Large data such as photos and videos (can be a separate disk) |
| `backups/` | Backups kept on this server's own disk, and other servers' and labs' backups it stores |

A folder named `<folder>.before-restore-<time>` is the data a restore
replaced, kept until you delete it from the restore page.

## Roostack itself

| Path | What it is |
|---|---|
| `/usr/bin/roostack`, `/usr/bin/roostack-agent` | The programs |
| `/usr/lib/systemd/system/roostack.service`, `roostack-agent.service` | Their systemd units |
| `/usr/share/roostack/catalog/<name>/package.yaml` | The services you can install ([package format](packages.md)). Installed services run from the lab's own copy (`/var/lib/roostack/packages`) |
| `/usr/share/roostack/caddy/Containerfile` | How Roostack's web server image is built, on each server |

## Changes to the host

`roostack-agent` is the only part of Roostack that runs as root. Besides
setting the time zone, installing OS updates and restarting the server, it
changes only these:

| Path | What it is |
|---|---|
| `/etc/roostack/firewall.nft` | The firewall (nftables), loaded again at start |
| `/etc/sysctl.d/60-roostack.conf` | Lets containers use ports 80 and 443 |
| `/etc/wireguard/roostack*.conf` | The lab's network (`roostack`), other labs storing backups here (`roostack-guests`), and links to other labs (`roostack-st1` to `roostack-st9`) |
| `/etc/fstab` | A line for each backup drive, marked `# Added by Roostack` |
| `/mnt/roostack-<drive>/` | Where backup drives are mounted |
| `/run/roostack-agent.sock` | How Roostack talks to the agent |

??? note "Logs"

    Roostack and the agent log to the systemd journal:

    ```bash
    journalctl -u roostack -u roostack-agent
    ```

    A service's containers log to the `roostack` user's journal:

    ```bash
    sudo journalctl _UID=$(id -u roostack) -t roostack-<service>-<container>
    ```
