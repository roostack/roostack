# Media libraries

Films, TV, music, books and photos you already have can be played by
services like Plex, Jellyfin and Navidrome without copying them. A **media
library** is a folder with a name: a folder on a USB drive or second disk,
a network share on a NAS, or a folder already on the server. Services can **read** a library but
never change it, and Roostack **doesn't back libraries up**: they're things
you have elsewhere, or keep safe yourself.

Each server has its own libraries. A service reads those of the server it
runs on.

## Adding a library

On **Settings → Media libraries**, choose **Add a library…** for the server
the files are on:

1. Choose **where it is**: one of the server's drives, or one of the places
   folders on the server can be (`/mnt`, `/media` or `/srv`). Roostack mounts
   a drive **read-only**, so nothing on it changes.
2. Open folders until you're in the one you want, or stay at the top for the
   whole drive.
3. Give the library a **name**. It's also the library's folder's name inside
   services, in lower case (`Films` becomes `films`).
4. Tick the **services that can read it**, then **Add library**.

A drive that holds your backups can't be a library: keep media on another.

## A share on a NAS

Under **Or a network share** on the same page:

1. Choose the kind: **SMB** (Windows sharing, what most NASes offer) or
   **NFS**.
2. Type the NAS's name or address (`nas.local`, `192.168.1.20`) and the
   share's name as the NAS calls it. The whole share becomes the library, so
   share just the folder you want.
3. For SMB, a **username and password** on the NAS, or leave them empty for
   a share anyone on your network can open. An account that can only read is
   best.
4. Give it a name, tick the services, then **Check and add the share**.

Roostack first mounts the share for a moment to check it, and tells you if
the NAS refused the login, has no such share, or didn't answer; nothing is
saved until it works. The password is kept on that server only, in a file
only the system can read, and Roostack's pages never show it again.

If the NAS is switched off, services see an error instead of their files
and the list says **Not answering**. Switch it back on and the share is
mounted the next time a service looks.

## Files only their owner can read

On a drive formatted on Linux, some folders may be readable only by the
person who owns them. Roostack looks at the files and says how many services
couldn't open, and whose they are. You can then tick **Let services read
them as …**: Roostack mounts the folder so that person's files show as its
own, for that library only. Services can then read everything that person
can, and still change nothing. Nothing on the drive changes, and removing
the library undoes it. Files belonging to other people stay unreadable.

This is only offered for a folder on a drive, and only for an ordinary
user's files, never the system's.

## Choosing what a service reads

A service that plays media asks which libraries it can read when you install
it: all of them are ticked unless you untick some. Change it later in the
service's **Settings**, under **Media libraries**, or from a library's page
(**Manage** on the list).

Inside the service, each library is a folder of its name in the service's
media folder (for most, `/media`): add them there as the service's own
libraries, e.g. Plex's *Films* library from `/media/films`.

## Unplugging a drive

Services see an empty folder while a library's drive is unplugged, and the
list says **Not connected**. Plug it back in and the next time a service
looks, the library is there again: nothing needs restarting.

## Removing a library

On the library's page, **Remove library**. Services stop seeing it; the
files stay where they are.

## Where things are

| What | Where |
|---|---|
| A library | `/mnt/roostack-library/<name>`, read-only |
| A drive used for libraries | `/mnt/roostack-media-<drive>`, read-only, in `/etc/fstab` by its UUID |
| What a service can read | `/mnt/roostack-library-views/<service>`, mounted into the service |
| A share's login (SMB) | `/etc/roostack/shares/<name>.cred`, readable by root only |
| A share | a line in `/etc/fstab` marked `# Added by Roostack: media library <name> (a share)` |
