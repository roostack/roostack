# Add a service

1. On **Services**, choose **Add a service**. You'll see everything you can
   install, each with its logo, what it does and its categories; ones you
   already have are marked **Installed**. Type in the search box, or choose
   a category such as **Photos and videos**, to narrow the list.
2. Choose **Install** next to the one you want.
3. Fill in the form:

    Run it on
    :   Which of your servers runs it, if you have more than one. Its data
        stays on that server, and is backed up there.

    Web address
    :   The first part of its address, such as `passwords` for
        `passwords.smith.family`. Letters, digits and dashes.

    Settings
    :   Anything the service needs to know from the start, such as a name
        for its first administrator account. Passwords you type here are
        kept locked away on the server, never in a file anyone can read;
        ones only needed for setup (such as the first administrator's) are
        deleted once setup has finished.

4. Check **What it sets up**, just above the button (see below), then
   choose **Install**.

Roostack downloads the service, starts it and gives it its web address.
This usually takes a few minutes. While it does, the service shows
**installing**, and its page lists each step: done, in progress (with how
long so far) or still to come, with the full log under **Details**. The
home page and **Services** show which step it's on. Its address becomes a
link once it's running.

On a brand-new server, the first install may also wait for Roostack's web
proxy to finish building (once only, a few minutes). If a step takes much
longer than usual, the page says so, and whether it's still moving.

## Official and community services

Every service in the list, except [your own](#your-own), comes from
Roostack's service registry, where each package is reviewed before it's
published. Each one says who looks
after its package:

Official
:   Looked after by the Roostack project, which tries it on a real lab
    (install, backup, restore, update, remove) for each Roostack release.

Community
:   Written and kept up to date by its author. Roostack's maintainers
    review each version before it's published, but don't try it on a lab
    themselves. Read what it sets up before installing it.

Either way, the app itself is made by its own project, credited under
its description when you choose **Install**.

## Your own

The Owner can add a service the registry doesn't have, from a package
file: one you wrote (see [Make your own service](../packages/index.md)),
or one someone you trust gave you. At the end of **Add a service**, under
**Add your own**, choose **Add a package…**, then the package file and,
if it has one, its logo (a square PNG, at least 256×256 pixels and at
most 100 KB), and **Add**.

Nobody has reviewed a package you add. Roostack checks that it's a valid
package it can set up, but not what it runs: the package decides which
app runs and what it's given. Only add one you wrote, or got from someone
you trust.

It's then listed with the others, marked **Your own**, and anyone who can
add services can install it. If it asks for something packages from the
service registry can't have (an image that isn't pinned, a folder from
the server, extra container options), installing it lists those, and only
the Owner can install it, by ticking **I've read what it asks for, and
want to install it anyway**.

- **A new version:** add the new file. It replaces the one you added
  before; services installed from it keep running as they are.
- **Removing it:** **Remove** beside it. Services installed from it keep
  running; remove those from their own pages.
- **Names:** yours can't have the name of a package in the service
  registry. Pick another, such as `myapp-test`.
- **Where it's kept:** `/var/lib/roostack/local-packages/<name>/`, which
  is backed up with your lab.

To check a package before adding it, run `roostack validate -local
package.yaml` on a Roostack server, or any Linux computer with the
`roostack` command: it shows the same list.

## Needs a newer Roostack

A service marked **Needs a newer Roostack** uses something this version
of Roostack can't do yet. It can be installed once Roostack is updated to
the version it names. The Owner can see the details under **Settings →
Updates**, in **Services you can install**.

## What it sets up

Before you install, the page says what the service will do on your
server, worked out from its package and what you've chosen on the form.
It changes as you change the form: turning off Immich's smart search, for
example, lowers the memory it needs.

Keeps
:   What it stores, and whether that's **backed up**. Some things aren't,
    because the service makes them again if they're lost, such as
    thumbnails.

Address
:   Its web address, and who can reach it.

Connection
:   Any extra way in besides the web address, such as Forgejo's Git over
    SSH, and whether it starts on or off.

Graphics
:   Whether it uses the server's graphics chip (GPU), such as for
    converting videos.

Memory
:   About how much memory it needs while it runs, adding up the parts
    that would run with your choices. It's the package's estimate, not a
    limit: a service can use more while it's busy. Below it, how much
    memory the server it would run on has, and how much is free right
    now. If the service needs more than is free, a warning says so: it may
    be slow, or stop working. You can still install it.

Under the description, the page also says who makes the app, its licence,
and links to its website, its source code and a way to support it.

??? note "For technical users"

    **Details** under the summary shows each part that would run and its
    memory, every folder and database with whether it's backed up, a link
    to the package file itself, and every file Roostack would write (its
    Quadlet units and Caddy site), exactly as
    [`roostack render`](../reference/cli.md#looking-at-packages) shows
    them, for the choices on the form. It also shows what the services
    already on that server expect. The free memory is the server's own
    figure (`MemAvailable` in `/proc/meminfo`), which counts what's
    running; another server reports it over Roostack's network. The summary is generated
    (`generate.Summarize`), never written by hand, so it can't disagree
    with what's installed. Passwords on the form aren't sent for it: no
    generated file ever holds one.

## Another copy of a service

You can install a service more than once: two Forgejos, say, one for
work and one for home. Each copy is completely separate: its own data,
settings, backups and web address.

On **Add a service**, choose **Add another…** next to one you already
have. As well as the usual form, it asks for:

Title
:   What it's called on your pages, such as "Forgejo (work)".

Name
:   Its short name, used for its folders, containers and backups. It always
    starts with the service's own (`forgejo-`), and you choose the rest:
    lowercase letters, digits and dashes, such as `work`. It's filled in
    from the title as you type. It can't change later.

It also needs a web address of its own, which follows the name (such as
`git-work`) unless you change it.

Every install asks for a title and name, not only another copy: leave
them as they are for the usual name (`forgejo`). To change a service's
title later, open its page, choose **Edit settings**, and change its **Name**; its name in folders and backups stays.

??? note "For technical users"

    The name is the service's ID: its instance file is
    `/var/lib/roostack/services/<name>.yaml` (with `name:` and `package:`),
    its containers `roostack-<name>-…`, its data
    `/srv/roostack/services/<name>`, its secrets `roostack-<name>-…` and
    its backups tagged `service=<name>`. The first copy of a package is
    named after it.

## Finishing setup

Some services need a step after they start, such as creating their first
administrator account. The service's page says **Finishing setup** while
Roostack does this, which can take a few minutes after installing.

If a setup step fails, the page shows what went wrong, with **Try again**.
If it was something you typed (an invalid username, say), open **Change
what you entered, then try again**, correct it, and choose **Try again with
these**. Passwords you leave empty stay as you typed them before.

## Opening it

Once it's running, open its address from the service's page (or the home
page). The first visit may take a few seconds while its secure certificate
is ready.

??? note "For technical users"

    Installing writes the service's *desired state* to
    `/var/lib/roostack/services/<name>.yaml`: its package version, the server
    it runs on (`host`), its web address and a local port (allocated once,
    from 20000–29999), and its settings. Secrets (generated or typed) become
    Podman secrets named `roostack-<name>-<setting>`, never files; the
    package's `install_only` ones are deleted once the install hooks
    succeed.

    Roostack then applies the change on the service's server: it generates
    the Quadlet files in `~roostack/.config/containers/systemd/`
    and a Caddy snippet in `/var/lib/roostack/caddy/sites/<name>.caddy`,
    reloads systemd and starts the units. Each new container's image is
    downloaded first (`podman pull`, run as the `roostack` user), then its
    unit started, one at a time; a new Caddy starts last. The apply's log
    shows on the home page and under the service's **Details** while it
    runs. The step list is kept in memory: if Roostack restarts during an
    install, the service shows its containers' state instead.

    Setup steps ("install hooks") run with `podman exec` inside the
    service's containers, retried every 10 seconds for up to 10 minutes
    while the app starts. Values you typed reach them only as environment
    variables, passed on standard input. Their progress is in the database
    (`settings` table, `hooks.<name>.install`).

    See exactly what a service generates with
    [`roostack render`](../reference/cli.md#looking-at-packages).
