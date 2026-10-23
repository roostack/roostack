# Command line

Everything you can do from the command line, you can also do from
Roostack's pages, apart from getting back in when you're locked out. The
command line is for technical users, and for when the web page can't help.

Run `roostack <command> -h` on the server for each command's options.

## Who runs what

Most commands work on the `roostack` user's files and containers, so they
run as that user:

```bash
sudo -u roostack roostack <command>
```

The examples below write this as `roostack` for short. Run as the
roostack user, `roostack` finds that user's systemd itself (it sets
`XDG_RUNTIME_DIR`, which `sudo -u` doesn't). Other programs, such as
`systemctl --user` or `podman`, still need it given:
`sudo -u roostack XDG_RUNTIME_DIR=/run/user/$(id -u roostack) systemctl --user …`.

`roostack user`, `roostack backup`, and `roostack apply` can also be run
with plain `sudo`: started as root, they run themselves again as the
roostack user, with its systemd session, before they read anything. Root
never opens Roostack's files, because the roostack user owns that folder
and could leave a link there pointing somewhere else. `roostack node join`
runs as root: it joins as the roostack user, and only restarts Roostack as
root.

## People

When you're locked out, these are the way back in. `add` and
`reset-password` print a **temporary password**, which must be changed at
the next login.

```bash
roostack user add <name> [-role owner|admin|member]   # add someone
roostack user reset-password <name>                   # forgot their password
roostack user reset-2fa <name>                        # lost their phone
```

`reset-password` and `reset-2fa` also log the person out everywhere. See
[Locked out](../trouble/locked-out.md).

## Backups

```bash
roostack backup init                          # create the recovery key (first time) and the repository
roostack backup run                           # back up now
roostack backup snapshots                     # list backups: the server's own, then one per service
roostack backup restore <service> [-snapshot ID]
```

Each backup is a snapshot of the server's own settings and one snapshot per
service, tagged with its name. `restore` puts one service back from one of
its snapshots (the latest, unless you give an ID from `snapshots`). The service's current data is moved aside into a
`.before-restore-<time>` folder, never deleted. The **Backups** page does
the same, with a live log.

## Applying changes

```bash
roostack apply -dry-run    # show what would change
roostack apply             # make the server match its settings
```

Roostack applies changes by itself whenever you change something on its
pages. `apply` does the same by hand: it regenerates the unit files and
web server config from `lab.yaml` and `services/*.yaml`, then starts,
restarts, or stops what changed, and applies the firewall. `-dry-run` shows
the plan without changing anything.

## Looking at packages

These change nothing and don't need a Roostack server:

```bash
roostack validate [-local] <package.yaml>...
roostack render -package <package.yaml> [-instance <service.yaml>] [-lab lab.yaml] [-out DIR] [-gpu]
```

`validate` checks package files, including every template in them and
the logo each names, and the [package policy](packages.md#what-a-package-may-not-do) every package
in the registry follows. With `-local`, for a package of your own, only
what it asks for that registry packages can't have is shown, as warnings:
the same list Roostack shows before installing it. A package of your own
may leave out the facts about the project (`about:`, `requires`, `memory`).
`render` prints exactly what Roostack would generate for a service (unit
files, web server config), or writes it under `-out`. Without
`-instance`, it's the package with its default settings; without `-lab`,
in an example lab at `lab.example.com` with Roostack's usual folders. With
only `-lab`, it renders Roostack's own units for the lab, such as Caddy.

## The service registry

```bash
roostack registry check [-url URL]
```

`check` fetches the [service registry's](../settings/updates.md#services-you-can-install)
signed list as Roostack does, checks every signature and file, and prints
what it found, without changing anything. The other `registry` commands
(`keygen`, `delegate`, `index`) are for the registry's maintainers: they make
its keys and build its signed list.

## Servers

```bash
sudo roostack node join <code> [-name <name>]
```

Joins this server to a lab, with a join code from the lab's **Settings →
Servers**. See [Add a server](../servers/add.md).

## Running Roostack

```bash
roostack serve [-lab FILE] [-listen ADDR]
```

Runs Roostack itself. The `roostack` systemd service does this; you don't
need to.
