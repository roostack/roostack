# Change a service

Open a service from **Services** to see its page. Owners and Admins can
change it.

## Its address and details

The top of the page shows the service's **address**, where it's reachable
from, and which server it **runs on**. **Technical details** shows its
version and the folders its data is in.

## Settings

Most of a service's settings are in the service itself, and its page links
straight to them (for example, Vaultwarden's admin panel).

Some settings belong to Roostack, and are on the service's page:

Web address
:   Changing it moves the service to the new address. Links to the old one
    stop working.

The service's own options
:   Whatever the service lets Roostack set, such as whether anyone can sign
    up.

Choosing **Save** restarts the service, so it's unavailable for a moment.

## Tokens and passwords Roostack made

Some services need a password or token that Roostack creates for you, such
as Vaultwarden's admin token. Choose **Show** to see it. Showing it is
recorded in the audit log.

## Actions

Some services offer **actions**: things you'd otherwise need a command line
for, such as resetting someone's password in Forgejo. Open the action, fill
in what it asks, and choose its button. Its result shows underneath.

## Extra connections

A few services use connections other than the web, such as git over SSH in
Forgejo. These are off until you turn them on under **Extra connections**:
tick it, choose the port on the server, and **Save**. The page then shows
the address to connect to. Extra connections are open to your home network
only.

## When the registry withdraws a version

The service registry can withdraw a version of a service that turns out to
be broken or unsafe, saying why, often with a link to read more. Roostack
then says so on the service's page and under **Needs attention**. There are
three kinds:

Notice
:   Something to know, such as a step to take after updating. The service
    carries on as before.

Warning
:   The version shouldn't be used. The service keeps running, but that
    version can't be installed any more. Update it when a fixed version is
    out, or remove it.

Stop
:   The version is dangerous: malicious, or being attacked with no fix yet.
    Roostack stops the service and turns its addresses off, and says so on
    its page, under **Needs attention** and by email. Everything it stores
    is kept, and so are its backups. Wait for a fixed version and update
    it, or remove it.

If you understand the risk and need the service now, the Owner can open
**Start it anyway…** on its page. **Make it private first** is ticked when
the service is public: it keeps out whoever is attacking that version, since
only devices at home and on Roostack's network can reach it. Roostack won't
stop that version again, but keeps showing it under **Needs attention**
until you update or remove it.

The Owner can turn stops off in [Settings → Updates](../settings/updates.md#services-you-can-install):
a stop is then shown as a warning.

Roostack never removes a service or deletes its data because of the
registry.

??? note "For technical users"

    Whether a service is stopped isn't saved: on every change, the
    controller checks each service's version, and the SHA-256 of the lab's
    copy of its package, against the newest index it has. So a stop the
    registry lifts lets the service start again by itself. **Start it
    anyway** saves `started_anyway: <version>` in the service's instance
    file, and updating to another version ends it. When it's stopped, its
    containers' unit files and its Caddy site are removed. Its folders,
    secrets and Podman network stay. A database on Roostack's own image
    (Postgres or Redis, when the package names no image) keeps running, so
    backups carry on as before. A database on an image the package sets
    runs until one more backup of the server has finished (Roostack starts
    one straight away), then stops too. Later backups keep that last dump.

## Moving it to another server

If you have more than one server, the Owner can move a service to another
one: see [Move a service](../servers/move.md).

??? note "For technical users"

    Settings and the web address live in the service's instance file,
    `/var/lib/roostack/services/<name>.yaml`; saving rewrites it and applies
    the change, which restarts the containers whose unit files changed.

    **Extra connections** are the package's `ports:`. Turning one on
    publishes the container's port on the server's port (1024 or above) and
    opens it in the firewall for your home network (LAN ranges) only.

    **Actions** are package `action` hooks, run with `podman exec` in the
    service's container. Their inputs reach the command only as
    environment variables on standard input, and aren't written to the
    audit log (only the action's name and whether it worked).
