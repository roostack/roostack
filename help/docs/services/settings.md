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
