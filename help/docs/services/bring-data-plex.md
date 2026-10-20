# Moving Plex

The general steps are in [Moving from Docker Compose](bring-data.md). This is
what's particular to Plex.

## Your media stays where it is

Add your films, TV and music as [media libraries](../servers/media-libraries.md)
first: the NAS share or drive they're on. Nothing is copied. When you bring
Plex's data in, each old media folder (`/movies`, `/tv`…) is matched to the
library holding its files, and shown inside Plex at its old place, so Plex
finds every file where it expects it, with what everyone has watched.

If the old folders were all in one (`/data/movies`, `/data/tv`), they're
shown in a folder there. Otherwise each is at its own place, and if its drive
is unplugged, Plex can't see it again until it restarts: Roostack restarts
Plex by itself once the drive is back.

## Your files

- Copy its config folder (the old `/config`). `Cache`, `Codecs`,
  `Crash Reports`, `Logs` and `Updates` can be left out of your copy.
- Plex has no database to dump: its database is a file in that folder.
- Some of its files belong to the old `PLEX_UID` and only it can read them,
  `Preferences.xml` among them. The check shows the command to make your copy
  readable. `Preferences.xml` is Plex's identity, so it must come.

## Switching

Stop the old Plex and keep it stopped (`restart: "no"`): two servers with one
identity fight over it on plex.tv. Copy again if you copied by hand, then
switch. It's the same server to plex.tv and every app: no claim code, and the
same people have access.

## Afterwards

- Roostack serves Plex at its own address, not on port 32400, and doesn't use
  host networking: remote access goes through that address, and a TV that
  looked for servers on the network may need the server picked once.
- If a library's path must change after all, use Plex's own way: turn off
  emptying the trash after scans, add the new folder, scan, remove the old
  one, then empty the trash.
