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

Some settings belong to Roostack. The service's page sums them up under
**Settings**; choose **Edit settings** to change them:

Web address
:   Changing it will move the service to the new address, and links to the
    old one will stop working.

The service's own options
:   Whatever the service lets Roostack set, such as whether anyone can sign
    up.

The media libraries it reads
:   For a service that plays media (see [Media libraries](../servers/media-libraries.md)).

How it updates
:   See [Updating by itself](#updating-by-itself) below.

One **Save** will keep all of it. It will restart the service, making it
unavailable for a moment, only when its address, options, or libraries
changed.

Below is its **Name**: what it's called
on your pages. Renaming it won't change its name in folders, containers,
and backups.

## Tokens and passwords Roostack made

Some services need a password or token that Roostack creates for you, such
as Vaultwarden's admin token. Choose **Show** to see it. Showing it is
recorded in the audit log.

## Actions

Some services offer **actions**: things you'd otherwise need a command line
for, such as resetting someone's password in Forgejo. Open the action, fill
in what it asks, and choose its button. Its result will show underneath.

## Scheduled tasks

Some services need upkeep that their app doesn't do by itself, such as
Seafile freeing the space that deleted files leave behind. Their page has
**Scheduled tasks**, each with how often it runs, when it last ran, and how
long it took. Roostack runs them at night, after the backup, one at a time
and only while the service is running; a task that couldn't run (the
server was off, or the service stopped) will run the next night instead.
**Run now** runs one as soon as nothing else is happening on the server,
such as a backup or an update.

A task that runs for longer than its package allows will be stopped by
restarting the container it runs in, which takes the service away for a
moment, and it counts as not working.

If a task doesn't work twice in a row, it will show under **Needs
attention** and be emailed like other problems. Its page says what went
wrong; **Try again** runs it again, and otherwise Roostack will try again
the next time it's due.

## Extra connections

A few services use connections other than the web, such as git over SSH in
Forgejo. These are off until you turn them on under **Extra connections**:
tick it, choose the port on the server, and **Save**. The page will then
show the address to connect to. Extra connections are open to your home
network only.

## Updates

When the service registry has a newer version of a service, its page will
say so, with what kind of update it is (a fix release, a feature release, a
new major version, or an improved Roostack package for the same version).
Some updates come with **notes**: things to know before updating, such as a
step the app takes afterwards. Read them first. Services with an update are
also listed under **Needs attention** and marked **Update available** on
**Services**.

Choose **Update now** to update it. Roostack will:

1. download the new version while the old one keeps running;
2. back up the service's server (only what's changed since the last backup
   will be uploaded);
3. switch the service to the new version, which will make it unavailable
   for a minute or so;
4. check it works: it must run for a minute without stopping and answer its
   own health checks.

If the new version doesn't work, Roostack will go back to the version you
had and say so on the page and under **Needs attention**. It won't try that
version again by itself; **Try again** will. The app may already have
changed its data for the new version: if the service isn't working as it
did, the Owner can put back the backup taken just before the update.

Without backups set up, you can still update, but there's no copy of the
service's data to go back to.

If your servers have different kinds of processor, the newest version may
have no build for the one this service's server has. The page will then say
so instead of offering it; it will be offered once the service registry has
a build for that processor.

A service installed from **your own** package updates when the Owner adds a
newer version of that package under **Add a service → Add your own**; it
never takes a version from the service registry. If the newer version asks
for things the one it runs didn't (such as an image that isn't pinned), the
page will list them, and only the Owner can update it, after ticking that
they've read them. Such an update never happens by itself.

### Updating by itself

On the service's page, choose **Edit settings**, then under **Updates**
choose how it updates, and **Save**:

Tell me
:   The default. Updates will be shown here and under **Needs attention**,
    and you'll apply them.

Fixes
:   Fix releases and improved Roostack packages for the same version will go
    on by themselves.

Fixes and features
:   Not new major versions, which can change how the app works.

Everything
:   Including new major versions.

Automatic updates happen at night, after the server's backup. They wait for
you when backups aren't set up, when an update has notes to read, or when
that version didn't work before. If Roostack stopped a service because the
registry says its version is dangerous (below), and a fix is out that the
service's choice would take, it will update straight away instead of waiting
for the night.

??? note "For technical users"

    The update is to the version the registry offers, the same one **Add a
    service** would install. The kind comes from the app's version (the part
    before `-roostack.`), read as major.minor.patch; a version that doesn't
    look like that only updates automatically on **Every update**. While it
    switches, the update holds the server's maintenance queue, so no backup,
    OS update, or restart starts in the middle. The check is every unit
    active, `health.cmd` passing inside its container, and `health.http`
    answering below 500 through the route's port, with no restarts, for a
    minute within five. Going back uses the lab's copy of the previous
    package and the image Podman still has; data isn't rolled back. The
    service's instance file (`/var/lib/roostack/services/<name>.yaml`) holds
    the version and the choice (`updates: notify`, `patch`, `minor`, or
    `all`), and the full log is in **Settings → History**.

## When the registry withdraws a version

The service registry can withdraw a version of a service that turns out to
be broken or unsafe, saying why, often with a link to read more. Roostack
will then say so on the service's page and under **Needs attention**. There
are three kinds:

Notice
:   Something to know, such as a step to take after updating. The service
    will carry on as before.

Warning
:   The version shouldn't be used. The service will keep running, but that
    version can't be installed any more. Update it when a fixed version is
    out (the notice will then have **Update now**), or remove it.

Stop
:   The version is dangerous: malicious, or being attacked with no fix yet.
    Roostack will stop the service, turn its addresses off, and say so on
    its page, under **Needs attention**, and by email. Everything it stores
    will be kept, and so will its backups. Wait for a fixed version and
    update it (the notice will then have **Update now**, and it may update
    by itself: see [Updating by itself](#updating-by-itself)), or remove
    it.

If you understand the risk and need the service now, the Owner can open
**Start it anyway…** on its page. **Make it private first** is ticked when
the service is public: it will keep out whoever is attacking that version,
since only devices at home (or on a VPN) can reach it. Roostack won't stop
that version again, but will keep showing it under **Needs attention** until
you update or remove it.

The Owner can turn stops off in [Settings → Updates](../settings/updates.md#services-you-can-install):
a stop will then be shown as a warning.

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
    secrets, and Podman network stay. A database on Roostack's own image
    (Postgres, MySQL, MariaDB, or Redis, when the package names no image)
    keeps running, so backups carry on as before. A database on an image the
    package sets runs until one more backup of the server has finished
    (Roostack starts one straight away), then stops too. Later backups keep
    that last dump.

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
    environment variables on standard input and aren't written to the audit
    log (only the action's name and whether it worked).

    **Scheduled tasks** are package `schedule` hooks, run the same way. Each
    server queues the ones due on its maintenance coordinator right after
    the nightly backup and OS updates, so they never overlap a backup, a
    restore, or an update, and a pending restart waits for a task to
    finish. A weekly task's weekday and a monthly task's day (1 to 28) come
    from its service's and task's names, so a server's tasks are spread out.
    Each run is in **Settings → History** under Scheduled tasks, with its
    log; what each task last did is kept in the server's database, under
    `hooks.<service>.scheduled.<task>` in its settings.
