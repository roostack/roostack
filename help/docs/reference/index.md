# Technical reference

Exact details, for technical users. Nothing Roostack does should look like
magic: every page of this help has a **For technical users** section, and
these pages collect the rest.

- [Where files live](files.md): settings, data, generated files and changes
  to the host
- [Command line](cli.md): the `roostack` commands
- [Network ports](ports.md): what's open, to whom, and why
- [Package format](packages.md): how a service is described

## How Roostack is built

Two programs run on each server:

`roostack`
:   The web pages, the lab's settings and everything that isn't root's
    business. It runs as the unprivileged `roostack` user, in a tight
    systemd sandbox, and runs containers through that user's systemd.

`roostack-agent`
:   A small helper that runs as root, for the few things that need it:
    the firewall, WireGuard, OS updates, drives, the time zone and
    restarts. It listens on a local socket, accepts only root and
    `roostack`, and checks everything it's asked to do.

Services run in **rootless Podman** containers, each on its own private
network, started by **systemd** from Quadlet files. **Caddy** serves them
over HTTPS on their own addresses. Backups use **restic**. The lab's
servers talk over **WireGuard**.

Roostack keeps your *desired state* (which services, with which settings)
as YAML files, generates everything else from them, and makes the server
match.
