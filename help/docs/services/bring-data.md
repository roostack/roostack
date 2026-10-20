# Moving from Docker Compose

If you already run a service with Docker Compose (or anywhere else), you can
bring its data into Roostack: its files, its database and so its accounts,
settings and history. You make a copy, put it where Roostack can see it, and
Roostack checks it, copies it in and switches the service over. It never
changes your copy, and never runs anything on your old machine.

This is for people comfortable with a terminal: you'll run a few commands on
the old machine. Only the Owner can bring data in.

## Before you start

- **Use a new machine, or reinstall this one.** Docker and Roostack on the
  same server don't mix safely: Docker's published ports get past
  Roostack's firewall. Moving to a new machine is simplest: the old one keeps
  running until you switch.
- **Update the old service** to the version Roostack runs (the install page
  says which), or at least not newer. An older version usually upgrades
  its data as it first starts; a newer one usually can't go back.
- **Keep the address if you can.** If the old service had the same domain,
  give it the same web address in Roostack, and phones and bookmarks carry
  on.

## The steps

1. **Install it with "I'll bring its data".** On the install page, under
   *Its data*. It's installed and left stopped; it doesn't make a new
   administrator, as its accounts come with its data. (For a service already
   running, choose **Bring in data from elsewhere…** on its page, in the
   Backups card.)
2. **Your files.** Copy the old service's folders onto a USB drive, into a
   folder on the server (with `rsync`), or leave them on your NAS and choose
   the share. Put a dump of its database beside them: the page shows the
   command, filled in once you've given the old setup. Roostack mounts all of
   it read-only.
3. **Your old setup** (optional, but it saves work). Paste what
   `docker inspect $(docker compose ps -aq)` prints in the old service's
   Compose folder, or its `docker-compose.yml` and `.env`. Roostack reads the
   images, folders and settings; passwords are masked and nothing you paste is
   kept. Then say where the old machine's folders are now: Roostack guesses
   from the folder names.
4. **What goes where.** Each of the service's folders gets the old folder
   that was at the same place inside the container. A folder inside another
   (Immich's thumbnails inside its library) goes to its own place. Change
   anything that's wrong. A media app's films and music stay where they are,
   as [media libraries](../servers/media-libraries.md), shown inside the app
   where they were before.
5. **Check.** Roostack reads everything and says what it found: sizes, room,
   files it can't read (with the command to fix them), the dump's database
   version and extensions, versions and settings that differ, and old folder
   names left in the data.
6. **Copy.** It copies while everything keeps running. You can stop and
   carry on, and **Copy again** copies only what changed.
7. **Switch.** Stop the old service and keep it stopped (`restart: "no"`),
   take the dump again, then **Switch to this data**. Roostack stops the
   service, copies what changed once more, puts the copy in place, loads the
   dump and starts it. If it doesn't come up, it goes back to how it was.

Afterwards, check it works: sign in, look at what you keep there. Then look in
the app's own settings for anything that names the old machine.

## Undo

The data the service had before the switch is kept, until you delete it. On
**Backups → Restore** for the service, under *Kept copies*, choose **Put
back…** on "Before bringing data in". What it replaces is kept in turn.

## Files Roostack can't read

Containers often leave files only root (or another user) can read. The check
counts them and shows the command to make them readable on your copy, such
as `sudo chmod -R a+rX immich`. On a drive, you can also tick **Read them as
…** in the first step, which shows that user's files as Roostack's without
changing anything on the drive.

## Help for particular apps

- [Moving Immich](bring-data-immich.md)
- [Moving Plex](bring-data-plex.md)

??? note "For technical users"

    The copy is mounted at `/mnt/roostack-import/<service>` (read-only, in
    `/etc/fstab` with the comment "bringing data into"), and copied into
    `/srv/roostack/services/<service>.incoming/` and
    `/srv/roostack/media/<service>.incoming/` (each storage a folder there;
    each dump as `db-dump/<database>.sql`). The switch renames the current
    folders to `.before-import-<time>` and the `.incoming` ones into place,
    then loads each dump with `pg_restore --clean --if-exists --no-owner`
    (or the MariaDB/MySQL client) in the service's own database container.
    Files are copied as Roostack's user (the containers' root); owners are
    never copied, links are copied as links. Postgres dumps must be custom
    format (`pg_dump --format=custom`).
