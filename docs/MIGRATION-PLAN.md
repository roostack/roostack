# Bringing your own data: moving from Docker Compose (decided 2026-10-20)

The details behind SPEC.md §8.4. **Decided, not built:** the Owner chose
every recommendation in §11 on 2026-10-20.

Many people who'd use Roostack already run their services with Docker
Compose, the Owner included (Immich and Plex). They want to move a
service into Roostack **keeping everything**: photos, libraries, watch
history, accounts, settings and, where possible, the addresses their
phones and TVs already use.

**The direction (the Owner, 2026-10-20).** A first draft proposed a page
that reads the old machine over SSH and moves each app with a recipe of
its own. The Owner's view: moving is too dependent on each app for that
to be the starting point. Instead, Roostack gives a **technical user the
tools to do it themselves**, step by step: they take their own backup
(the files, and a database dump), put it where Roostack can see it (a USB
drive, a network share, a folder on the server), and Roostack brings it
into a service, like a restore from files rather than from a backup.
**The tools are generic** (the Owner asked whether they could be, the same
day): they match the old setup to the package by where each folder appears
inside the container, so there's no code for any one app, and help pages
keep only what can't be done in general. The first draft's automation stays
possible later, on top of the same tools (§7).

**Unverified** marks what needs a rehearsal on a VM before it's relied on
(§10).

## Words

- **Old service:** the app as it runs today, in Docker Compose, on the
  **old machine** (which may be the same computer, see §2 and §6).
- **Compose project:** one `docker-compose.yml` and the containers it
  makes (Immich's server, machine learning, Postgres and Redis are one
  project).
- **Bind mount:** a folder on the old machine shown inside a container
  (`/srv/photos:/data`). **Named volume:** a folder Docker keeps for a
  container under `/var/lib/docker/volumes/<name>/_data`.
- **Dump:** a database written out as one file (`pg_dump`), which another
  database server of the same kind can load.
- **Container path:** where a folder appears inside the container
  (`/data`). A Compose file and a package both name folders this way, so
  it's how an old folder is matched to the package's storage.
- **Storage:** one of a package's named folders (Immich's `library`,
  `thumbs`, `encoded`; Plex's `config`), each with a title the pages show
  ("Your photos and videos").
- **Bringing data in** (the feature): filling a service's storage and
  databases from files the person brings, instead of from a backup.

## 1. What Roostack already has to build on

| What | Where | What it gives a migration |
|---|---|---|
| A service's folders | `pkgspec` `storage:` (`class`, `location`); `generate.go:723-733` | Each storage is `/srv/roostack/services/<service>/<storage>`, or `/srv/roostack/media/<service>/<storage>` for `location: media`. Databases are `<service>/db/<name>`. A migration fills these folders. |
| Moving a service between servers | `node/move.go`: `ImportData` (a tar stream into `<dir>.incoming`), `CommitData` (old data aside as `<dir>.before-move-<time>`), `LoadDumps` | The same three steps bring data in from outside: fill `.incoming`, swap it in, load the dump. Each stream is checked before it's unpacked (no links out of the folder, no `..`). |
| Restoring one service | `backup/restore.go` `RestoreService`; SPEC §6.9 | Stop, move the current data aside, put files back, load dumps, start; undo on failure. The same shape, and the same "keep what was there" rule. |
| Loading a database dump | `backup/restore.go:174-221` | Postgres: `pg_restore --clean --if-exists --no-owner` of a custom-format dump, as the app's user (a superuser in the official image). MySQL and MariaDB: drop, create, load. No handling of roles or extensions beyond what the dump holds. |
| Database containers | `generate/generate.go` `postgres()`, `mysql()`; `defaults.go` | One per service; user and database named after the service (`pgIdent`: `immich` for Immich's `main`); password a Podman secret. A package may pin its own image: Immich pins the image Immich's own Compose file pins. |
| Installing over data from before | `node/leftdata.go`, SPEC §8.3.4 "Installing again over data from before" | If the service's folders already hold data, the install page says so, `install_only` settings become optional, and install hooks are skipped: Immich's "make the administrator" hook doesn't run over an Immich that has one. |
| Media libraries | SPEC §8.1 "Media libraries"; `agent/library.go`, `agent/shares.go` | A folder on a drive, an SMB or NFS share, or a folder on the server, mounted read-only and shown to chosen services at `/media/<library>`. Plex and Jellyfin read them. Media on a NAS can stay there. |
| Reading another user's files | Media libraries' "read as owner" (`X-mount.idmap`) | The agent can already show a drive's folder read-only, owned by Roostack, without changing anything on the drive. |
| The Compose import | `internal/editor/static/compose.js`; SPEC §8.3.7 | Turns a Compose file into a *package* (containers, databases, storage, routes). It doesn't copy data and doesn't record the old host paths. Useful later for apps the registry doesn't have. |
| GPU | `devices: [{gpu: intel}]` | Intel (and AMD) graphics through `/dev/dri`. **No NVIDIA.** |
| Subdomains | `instance.RouteOverride.Subdomain` | A service's address can be changed from the package's default (`photos`), so an old address can be kept. |

**Not there yet:** anything that reads an old setup, copies data in from
outside Roostack, or loads a dump that didn't come from Roostack's own
backups. No `pg_upgrade` or dump-and-reload between Postgres versions. No
hooks other than `install` and `action`. Immich's package has no media
libraries (Immich's "external libraries").

## 2. What a Compose homelab looks like, and what moving each part takes

### Folders: bind mounts and named volumes

A Compose project keeps its data in bind mounts (often beside the Compose
file: `./library`, `./postgres`, or a `.env` value such as Immich's
`UPLOAD_LOCATION`) and named volumes (Plex's `config` in many guides,
Immich's `model-cache`).

- **What matters is the container's side of each mount.** It says what
  the folder is to the app (`/data` is Immich's library, `/config` is
  Plex's), whatever the old machine called it.
- **The Compose file isn't the whole truth:** `.env` files, overrides
  (`docker-compose.override.yml`), several files, `${VAR:-default}`, and
  edits made since the containers started. **`docker inspect` of the
  running containers is:** the image and its digest, every mount with its
  real host path (named volumes included), the environment after
  interpolation, devices, network mode, the Compose project's labels.
  So Roostack reads that, when the person pastes it (§3.2).
- **Each package storage gets the old folder at the same container path**
  (§3.3), split where the package mounts subfolders separately; what can
  be skipped (caches, machine-learning models) follows from the storage's
  class.

### Who owns the files

- **On the old machine** files belong to whoever the container ran as:
  root (Docker's default; Immich's server), or the `PUID`/`PGID` of
  linuxserver.io images (usually 1000), or an image's own user (Postgres
  is 999 inside its container).
- **In Roostack** containers are rootless: a container's root is the
  `roostack` user on the host, and other users inside are the host's
  subordinate IDs (200000 and up). Immich's server and Plex (with
  `PLEX_UID: 0`) run as the container's root, so their files must belong
  to `roostack` on the host. A package using `UserNS=keep-id:uid=1000`
  also wants `roostack`; one running as another user inside wants that
  user's mapped ID (`podman unshare chown`).
- **So ownership is never copied:** the copy writes files as `roostack`,
  then sets the owner each storage needs (from the package), as moves and
  restores already do under `podman unshare`.
- **Reading the old files needs root on the old machine** (Postgres and
  Plex keep private files), so the person's copy, or a share's or drive's
  mount, must leave them readable to Roostack (§3.1).

### Databases inside Compose

- **Dump and load, never copy the database's files.** Copying Postgres's
  folder works only with the same major version, the same extensions at
  the same versions, the same processor and matching owners. A dump works
  across all of those except going *down* a major version. It's also how
  Roostack's own backups and restores already work, so the loader is
  there and tested.
- **The dump comes from the running old database**, read-only:
  `docker exec <db> pg_dump --format=custom --no-owner --username=<user> <db>`,
  taken after the old app stops (§3.5).
- **Versions:** the old Postgres's major version must be the package's or
  older. A custom-format dump from Postgres 16 can't be read by Postgres
  14's `pg_restore` (its file format is newer). Immich's Compose file has
  pinned `ghcr.io/immich-app/postgres:14-vectorchord…` for a long time,
  the same image as Roostack's package, so most installs match.
- **Extensions:** the dump creates them (`CREATE EXTENSION vchord`,
  `vector`, `cube`, `earthdistance`), which needs the superuser: Roostack's
  app user is one. An Immich still on **pgvecto.rs** (`vectors`, before
  Immich 1.133) must finish Immich's own VectorChord migration first, in
  Compose (Immich 3 reportedly no longer supports pgvecto.rs; to check in
  its release notes).
- **Owners and roles:** Compose's user is usually `postgres`; Roostack's
  is `immich`. `--no-owner` (as Roostack's loader already uses) makes
  everything belong to the loading user. Unverified for Immich: whether
  anything in its schema names a role.
- **Immich's own database backups** (`UPLOAD_LOCATION/backups/*.sql.gz`,
  made nightly by Immich) are `pg_dumpall` plain SQL, not custom format.
  They matter when the old machine is being wiped (see "Where"): loading
  one needs `psql`, and Immich's documented fix for its `search_path`
  line. A later addition.
- **MariaDB/MySQL** (PhotoPrism, Ghost): `mariadb-dump` from the old
  container, loaded as Roostack's restore does. **SQLite** apps (Plex,
  Jellyfin, Vaultwarden, Navidrome): the database is a file in the app's
  folder, copied with the folder while the app is stopped.
- **Redis/Valkey:** a job queue; nothing to move.

### App configuration and environment

- **Settings the app keeps in its own folder or database** (most of
  them: Immich's admin settings, Plex's `Preferences.xml`) move with the
  data, untouched.
- **Environment variables** in Compose are mostly what the package already
  sets (database host and password, time zone, ports). The few that are
  the user's choices map to package settings (Immich's machine learning
  on or off). The check lists the rest, with their values (§3.6).
- **Settings that name the old world** need a look after the move:
  Immich's *external domain* (used in share links), its machine-learning
  URL if it was changed from the default, an OAuth provider; Plex's
  custom server access URLs and remote-access port. The help pages list
  them.
- **A config file mounted into the container** (Immich's
  `IMMICH_CONFIG_FILE`): its settings go back into the database through
  Immich's admin page before moving.

### Large media: left in place, or copied

- **Media an app only reads** (Plex's films and TV, Jellyfin's,
  Navidrome's music) **stays where it is**, as a Roostack media library:
  a NAS share (SMB or NFS), a drive moved to the new machine, or a folder
  on it. Nothing is copied. The library is shown inside the container at
  the old path (`/movies`), so the app's own paths stay right (§3.4).
- **Media an app writes** (Immich's uploads) is the app's own storage:
  **copied** into `/srv/roostack/media/immich/…`, so the original is never
  changed. That needs the space twice, which is the main thing to check.
  Moving it in place instead (no copy) is possible only on the same disk,
  and only after a backup: later, if needed.
- **An app's own storage can't be on a network share** in Roostack today
  (only read-only libraries can). An Immich whose library is on a NAS
  must come onto the server's disk, or wait for "storage on a share"
  (not proposed here: a database-backed app writing over NFS or SMB has
  its own problems).

### People and accounts inside the apps

- **They're data, so they move with it.** Immich's users, password hashes,
  albums, shares and API keys are in its database; Plex's people are
  plex.tv accounts, and the server's database keeps what each watched.
  Nobody makes accounts again, and passwords keep working.
- **Single sign-on:** an Immich that signed in through Authelia or
  Authentik keeps that setting, pointing at the old provider; people can
  still sign in with passwords. Roostack's own single sign-on (§5.2) for
  Immich isn't built yet; when it is, it must **link the existing
  accounts** (by email) rather than make new ones. Noted for that work;
  the migration doesn't need it.

### Addresses, reverse proxies, and what breaks

- **The old proxy** (Traefik labels, nginx-proxy-manager, Caddy, a
  Cloudflare tunnel) isn't brought over: Roostack's Caddy serves each
  service at `<subdomain>.<lab's domain>` with its own certificates.
- **What uses the address:** the Immich phone app (its server address and
  sign-in), Immich share links (their URL), Plex (apps find the server
  through plex.tv, which the server tells its addresses, so they follow),
  bookmarks, other tools (an Immich-Go upload script, Home Assistant).
- **Keeping the address is the best way to break nothing.** If the lab's
  domain is the one already in use and the service gets the same
  subdomain (Roostack lets it be changed), then at the move the name
  points at Roostack instead (Roostack manages its domain's records, §6.5.1;
  a router or Pi-hole entry is changed by hand), and phones, share links
  and bookmarks carry on. A new address works too: the help then
  lists what to change (each phone's server address; share links already
  sent out stop working; Immich's *external domain*).
- **Plex's remote access** in Compose is often port 32400 forwarded on the
  router, and `network_mode: host` (for discovery on the home network).
  Roostack serves Plex through Caddy at `plex.<domain>` and refuses host
  networking, so remote access goes through that address (or Plex's
  relay), and TVs that discover servers on the network may need the
  server picked once. Unverified: what Plex's apps do on a home network
  without host networking.

### GPUs

- **Intel and AMD graphics** (`/dev/dri`) work in Roostack: Plex's
  hardware transcoding (with Plex Pass), Immich's video transcoding.
- **NVIDIA** (`runtime: nvidia`, `deploy.resources…devices`) isn't
  supported: Plex would transcode on the CPU, and Immich's
  CUDA machine-learning image isn't the package's. NVIDIA support is its own piece of work (the
  container toolkit with rootless Podman), not proposed here.
- **Immich's machine learning on a GPU** (CUDA, OpenVINO): the package
  runs it on the CPU. The faces and smart search already computed are in
  the database and move with it; only new photos use the CPU.

## 3. The tools: bringing data into a service

Together a "restore from your own files" for any package. None of them
knows about Immich or Plex: they read the package (its storage, its
databases, its containers' images and paths) and, when the person pastes
it, the old setup, and match the two by where each folder appears inside
the container (§3.2-3.4).

### 3.1 Where Roostack can see your files (the source)

The person chooses where their files are, using what Roostack already
has for media libraries and backup drives. Every source is **mounted
read-only** for the import and unmounted afterwards, so Roostack can't
change the original even by mistake.

| Source | How it's chosen | Good for | Owners |
|---|---|---|---|
| **A USB drive or second disk** plugged into the server | The drive cards (as backup drives and media libraries), then the folder picker | Carrying a copy from the old machine; the old machine's own data disk moved over | ext4: the old owners (see below). exFAT, NTFS: everything Roostack's |
| **A network share** (SMB or NFS) | The media libraries' share form: server, share, optional login, **Check** | A NAS holding the data already (often the original folders themselves); a share on the old machine | SMB: everything Roostack's. NFS: the NAS's owners |
| **A folder on the server** | The folder picker, under `/mnt`, `/media` or `/srv` (as media libraries) | Files copied with `rsync` or `scp` by the person; the same machine | As copied |

- **The original can be the source.** Nothing needs a second copy just for
  Roostack: a share holding the old `UPLOAD_LOCATION` can be pointed at
  directly, as it's only read. Only the dump is a new file.
- **Files Roostack can't read** stop the check with a count and the owner
  of most ("312 files only root can read, in `postgres/`…"): the person
  makes them readable on their copy, or copies with `rsync --chown`, or
  for a drive ticks "read them as <owner>" (the media libraries'
  idmapped mount, one owner from 1000 up). **Considered:** the agent
  copying as root, which reads anything; not proposed, as it puts a large
  file copy behind the root boundary the agent is meant to keep small
  (SPEC §6.1), and the person can always fix the owners on their own copy.
- **Later:** a dump uploaded through the browser (for when the files are
  already on a share but the dump is on a laptop); another machine over
  SSH (the first draft's way, with `rsync`).

### 3.2 Reading the old setup

A Compose file and a Roostack package describe a service the same way:
images, environment, and folders by where they appear **inside the
container**. That's the common ground the tools work from, so they need
no code for any one app.

- **The person pastes one of:**
  - the output of `docker inspect` for the old project's containers
    (`docker inspect immich_server immich_postgres …`). Recommended: it
    has the real host paths, the folders behind named volumes, the image
    and its tag, the environment with `.env` already filled in, devices
    and the network mode;
  - the Compose file, with its `.env`, read by the package editor's own
    Compose reader (in the browser, as the editor does).
- **Roostack keeps, for the import only:** each container's image, its
  mounts (host path, container path, read-only or not), its devices, and
  its environment variables. Variables named like secrets are shown as
  `•••` and never stored or logged; the pasted text itself isn't kept.
- **Containers are matched to the package's** by image repository
  (`ghcr.io/immich-app/immich-server` is the package's `server`), else by
  name, else chosen. Database containers (the Postgres, MariaDB, MySQL and
  Redis images the editor recognises) are matched to the package's
  databases.
- **Pasting is a help, not a requirement:** without it, every folder is
  chosen by hand.

### 3.3 Matching folders by container path

- **Where the old paths are now.** The person says where the old
  machine's folders can be seen, one line per prefix: "`/mnt/tank` on
  the old machine is this share", "`/srv/compose/immich` is this folder on
  the USB drive". Roostack then finds each old folder in the source, and
  says which it can't.
- **Each storage is filled from the old mount at the same container
  path.** Nested mounts are split by the same rule: Immich's old
  `UPLOAD_LOCATION:/data` gives the package's `library` (at `/data`)
  everything except `thumbs/` and `encoded-video/`, which go to `thumbs`
  (at `/data/thumbs`) and `encoded` (at `/data/encoded-video`), because
  those are the package's mounts at those paths. No Immich-specific code.
- **A storage no old mount matches** is chosen by hand (an Immich mounted
  at `/usr/src/app/upload`; an old image that used other paths), or left
  out if it's replaceable. Leaving out critical storage needs a tick
  ("Start Immich without its photos and videos").
- **An old mount no storage matches** is listed: media for a library
  (§3.4), a single file such as a mounted config file, or data the
  package keeps elsewhere. The person decides.
- **Named volumes** are folders too (`/var/lib/docker/volumes/<name>/_data`
  on the old machine): the same prefix lines find them, as long as the
  source includes them.
- **Databases get a dump each,** matched by name (`main.dump`, `main.sql`,
  `main.sql.gz`) or chosen. A database's own data folder (Postgres's
  `/var/lib/postgresql/data`) is never copied, and the page says why
  (§2, "Databases inside Compose").

The page shows what the service needs, from its package, and what it
found for each:

```
Immich needs                                   found in the old setup
  Your photos and videos  (library)  /data                 /mnt/tank/immich, without thumbs/ and encoded-video/
  Thumbnails              (thumbs)   /data/thumbs          /mnt/tank/immich/thumbs
  Converted videos        (encoded)  /data/encoded-video   /mnt/tank/immich/encoded-video
  Smart search models (model-cache)  /cache                not a mount before: downloaded again
  Database "main"   Postgres 14                            main.dump
```

- **Later, if wanted:** an optional hint in the package format for each
  storage and database, shown in this table, for what the container paths
  can't say. A format change: its own decision.

### 3.4 Media libraries at their old paths

- **Apps that only read media** (Plex, Jellyfin, Navidrome,
  Audiobookshelf) keep every file's path in their database. A package
  shows libraries at `/media/<library>`, but the old container may have
  had them at `/movies` or `/data/tv`. Changing the paths afterwards
  needs each app's own way, or a rewrite of its database: app knowledge.
- **So a service can show a library at its old path:** per service and per
  library, where it appears inside the container (`films` at `/movies`).
  It's filled in from the old mounts when a library holds the old
  folder's files (a sample of file names looked up in it), and can be
  changed on the service's settings. Plex's database, with its watch
  history, is then right as it is, and the same holds for any app that
  reads media.
- **Stored in the instance:** `libraries:` gains an optional container
  path per library; libraries without one stay at `<package path>/<library>`.
  Each library at its own path gets a `Volume=` line of its own
  (read-only, `rslave`, as now). Unverified: what a library bound at its
  own path does when its drive is unplugged. Roostack's view folders
  exist because binding a library's mount point directly went stale, so a
  folder per library holding its mount, bound at the path, may be needed
  (SPEC §8.1, "On the server").
- **Checked like the editor's Compose import checks folders:** an
  absolute path, not `/`, nothing under `/proc`, `/sys`, `/dev`, `/etc`,
  `/usr`, `/bin`, `/lib`, `/run` and the like, not overlapping the
  package's own mounts; always read-only; the Owner only.
- **Writable storage can't move this way:** the app is configured for its
  package's paths. Immich rewrites its own; for anything else the check
  warns (§3.6), and the help says what to do.

### 3.5 Taking the backup on the old machine

Roostack never runs anything on the old machine, but the import page
shows the commands to run there, filled in with the package's names and
the database's type (the old container's name stays a placeholder):

| Database | Command (on the old machine) | Format |
|---|---|---|
| Postgres | `docker exec <your postgres container> pg_dump --format=custom --no-owner --username=<user> <database> > main.dump` | Custom: what Roostack's restores already load |
| MariaDB, MySQL | `docker exec <container> mariadb-dump --single-transaction --routines --triggers --events -u<user> -p<password> <database> > main.sql` | SQL |
| SQLite (Plex, Jellyfin, Vaultwarden) | Nothing: stop the app, then copy its folder | The file comes with its storage |
| Redis, Valkey | Nothing | |

- **Stop the app before the dump and the last copy** (its database can
  keep running for `pg_dump`). The page and help say so, and why: a dump
  taken while the app writes is consistent in itself, but the files
  copied after it may not match it.
- **Two passes for big data** (§3.7): copy while the old service runs, then
  stop it, dump, and copy again; only what changed is copied the second
  time.

### 3.6 The check (nothing changes)

Before anything is copied, Roostack reads the source and says what it
found, as a list with ✓ or what to do:

- **Each folder:** found, its size and number of files, how many Roostack
  can't read (§3.1), links pointing out of it (copied as links, never
  followed; shown).
- **Space** on each disk the service's storage is on (the media disk for
  `location: media`): the copy plus 10%, or it stops.
- **Each dump:** its format, and for Postgres, from `pg_restore --list` in
  the package's own database container: the Postgres version it came from
  (newer than the package's: refused, with why; the same or older: fine)
  and the extensions it creates, each looked up in that container's
  `pg_available_extensions` ("Immich's dump needs `vectors`
  (pgvecto.rs), which this Postgres doesn't have: finish Immich's
  VectorChord migration first"). A MariaDB or MySQL dump: its header.
- **The service has data already:** it's kept aside, not lost (§3.7).
- **The image and version,** when the old setup was pasted (§3.2): each
  old container's image against the package's for the same container.
  The same repository and tag: fine. An older tag: the app upgrades its
  data at first start, if it supports that jump (the help says). A newer
  tag: likely to fail, so wait for the package or update the lab's. A
  different repository (linuxserver's Plex instead of Plex's own) is said
  plainly, as its folders and users may differ. Without a pasted setup:
  "Bring data from Immich 3.3.3, the version this lab runs."
- **Environment:** the old container's variables that the package doesn't
  set, with their values (secrets masked), for the person to compare.
  Those the package sets itself (database, address, time zone) are left
  out.
- **Old paths left in the data:** each dump (as text, streamed through
  `pg_restore -f -` in the package's database container) and each SQLite
  file brought (found by its header) is searched for the old container
  paths that won't exist in the new container: "Plex's database names
  `/movies/…` 41,200 times, and this service has no `/movies`: show the
  library there (§3.4), or change the paths in Plex". Only a warning: an
  app may fix its own paths (Immich does, and its help says so).
- **Data outside any mount:** a container path the package keeps as
  storage that the old container had no mount for. Its data was inside the
  old container, so it isn't in any folder ("Immich's `/cache` wasn't a
  mount: its models are downloaded again").

### 3.7 Copy, then switch

- **Copy:** each folder goes into the service's `<storage>.incoming`
  while everything keeps running (the service in Roostack, and the old
  one). Long, with progress (bytes, files, an estimated finish) in the
  `activity` component. It survives a restart of Roostack and carries on.
  **Copy again** copies only what changed (same size and time: skipped),
  so the person can copy for hours while the old service runs, then stop
  it and copy again in minutes.
- **Switch to this data** (the downtime): stop the service, copy what
  changed one last time, move its current data aside
  (`<dir>.before-import-<time>`), swap `.incoming` in, load each dump
  (Roostack's restore loader), start, and check that it answers its health
  check. One maintenance job (SPEC §3.2): no backup or reboot in the middle. A
  failure puts the data from before back and starts the service again, as
  a restore does.
- **Owners:** the copy writes everything as Roostack's user, which is the
  container's root, as in folders Roostack makes; the owners on the source
  are never copied. A package whose app runs as another user inside its
  container without `keep-id` gets that user's mapped owner (`podman
  unshare chown`), from the package's own lines.
- **Undo:** the data from before the switch is listed with the restore's
  kept copies ("Before bringing data in, 20 October 14:02"), and can be put
  back or deleted there. Nothing is deleted without the Owner.

### 3.8 Starting a service with your data

- **On the install page:** "I'll bring its data" (beside the settings).
  The service is installed and **left stopped**, its `install_only`
  settings optional and its install hooks not run, as installing over
  data from before already does (SPEC §8.3.4): Immich doesn't make a new
  administrator, Plex needs no claim code. Its page says "Waiting for its
  data" with **Bring data in…**.
- **On a service's page:** Manage → **Bring in data from elsewhere…**,
  for a service already running (replacing its data; the check says so).
- **Help:** a section, "Moving from Docker Compose": the steps in general,
  then a page per app (§5).

### 3.9 What stays the person's job, and the problems it brings

The tools move files and databases, and the check (§3.6) warns about
what it can see. What the data means inside each app is left to the
person and the help. These are the problems the tools can't solve in
general:

| Problem | Where it bites | What helps |
|---|---|---|
| **Upgrading the app** | Data from an older version that needs the app's own steps (Immich's pgvecto.rs to VectorChord), or from a newer one | The check compares images; the help names the steps |
| **Paths inside writable data** | An app whose own storage moved to another container path and doesn't fix its database itself | Immich does (since 1.136); otherwise the check counts the old paths and the help says what to do. Media apps avoid it with libraries at their old paths (§3.4) |
| **Settings inside the app that point to the old world** | Immich's external domain (share links), an OAuth provider, a changed machine-learning address | The app's help page lists them |
| **Postgres newer than the package's** | A dump from 16 into 14 | Refused by the check, with why |
| **pg_dumpall and plain SQL dumps** | Immich's own nightly backups are these | The help says to make a custom dump instead; loading plain SQL is "later" |
| **Files only root can read** | Docker's root-owned folders; NFS with root squash | The check counts them and says what to do (§3.1) |
| **A different image before** | linuxserver's images instead of the maker's: other folders, other users | The check says so; the help for that app, if there is one |
| **A config file mounted into the container** | Immich's `IMMICH_CONFIG_FILE` | Listed as an old mount no storage matches; the help: move its settings into the app first |
| **Addresses** | Phone apps, share links, bookmarks | Keep the same address where possible: the lab's domain the old one, and the service's subdomain changed to match. Otherwise each phone is changed by hand |
| **The old service starting again** | Plex (one identity on plex.tv, two servers fight); Immich (phones upload to the old one) | The page and help: stop it and set `restart: "no"` before the switch |
| **NVIDIA GPUs** | Plex's transcoding, Immich's machine learning | Not supported: CPU instead; the check says so when the old container had one |
| **Writable storage on a NAS** | An Immich library on a share | Copied onto the server's disk; Roostack keeps an app's own storage local |
| **Downtime** | Big libraries | Copy, then copy again (§3.7) |

## 4. Safety

- **The original is only read:** every source is mounted read-only, and
  Roostack runs nothing on the old machine.
- **Nothing replaces anything until it's switched:** the copy lands in
  `.incoming`; the service's own data is moved aside at the switch, kept
  until the Owner deletes it, and can be put back.
- **Space first:** the check refuses without room for the copy plus 10%,
  and the copy stops (to carry on later) if a disk reaches the lab's
  "running out of space" level. `.incoming` folders are left out of
  backups until they're switched in, so a half-done copy doesn't land in
  tonight's backup.
- **Untrusted input:** whatever is on the source is copied as data, never
  run. Links are copied as links and never followed (as a move's checks
  do: SPEC §10.10); device files, sockets and names with `..` are refused.
- **The switch is one maintenance job**, and on failure the data from
  before comes back.
- **Owner only**, audited: bringing data in replaces a service's data.

## 5. Immich and Plex with these tools (the help pages)

### Immich

**From** Immich's own Compose file: `immich_server`, `immich_postgres`
(`ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0`, the
same image Roostack's package pins), `UPLOAD_LOCATION` at `/data` (or
`/usr/src/app/upload` before Immich 1.133).

1. Update the old Immich to the package's version (3.3.3), and finish
   Immich's VectorChord migration if it's older than 1.133.
2. Install Immich in Roostack with "I'll bring its data".
3. Make Roostack able to see the old `UPLOAD_LOCATION` (the NAS share
   holding it, or a drive), with `main.dump` beside it: `docker exec
   immich_postgres pg_dump --format=custom --no-owner --username=postgres
   immich > main.dump`. Paste `docker inspect immich_server
   immich_postgres` and say where the old path is now. Matching by
   container path splits the folder by itself: `thumbs/` and
   `encoded-video/` go to their own storage. They're replaceable, but
   bring them: making thumbnails again for 100,000 photos takes many
   hours.
4. Copy while the old Immich runs. Then stop it (`docker compose stop
   immich-server immich-machine-learning`), take the dump, copy again,
   switch.
5. Check: sign in, open albums, search for something, upload from a
   phone. Immich's `.immich` files come with the folders. If the old mount
   was `/usr/src/app/upload`, Immich rewrites the paths itself at start
   (since 1.136; unverified with a loaded dump). Look at Administration →
   Settings for the external domain (share links) and the
   machine-learning address.

Timings (estimates): 1 TB of photos takes about 3 hours over 1 Gb/s
Ethernet, 1½-2 hours from a USB 3 hard disk, plus 10-20% for thumbnails
(many small files). The second copy takes minutes, the dump and load
5-20 minutes (vector indexes are built again), so **about half an hour of
downtime**.

### Plex

**From** `plexinc/pms-docker` or `lscr.io/linuxserver/plex`: `/config`,
media at `/movies`, `/tv` or similar.

1. Add the media as **media libraries** in Roostack (the NAS share or the
   drive), so Plex reads it where it is; nothing is copied.
2. Install Plex in Roostack with "I'll bring its data", reading those
   libraries. Paste `docker inspect plex`: each old media mount is matched
   to the library holding its files and **shown at its old path** (`films`
   at `/movies`), so Plex's database, with its watch history, needs no
   change at all.
3. Bring `config/`: the old `/config`, matched by its container path.
   `Cache/`, `Codecs/`, `Crash Reports/`, `Logs/` and `Updates/` can be left
   out of the copy. `Preferences.xml` carries the server's identity and its
   sign-in, so it's **the same server** to plex.tv and every app: no claim
   code, same people with access.
4. Stop the old Plex **and keep it stopped** (`restart: "no"`), copy again,
   switch. Never run both.
5. Only if a library's path must change after all: Plex's own way (its
   article "Move an install to another system": turn off "Empty trash
   automatically after every scan", add the new folder, scan, remove the
   old one, then empty trash, clean bundles and optimise the database).
6. Remote access: Roostack serves Plex at `plex.<domain>` through its own
   proxy, not on port 32400, and doesn't allow host networking, so a
   forwarded port and discovery on the home network work differently
   (unverified: what Plex's apps do).

Timings: the config's first copy is 20 minutes to 2 hours for a big
library (it's the number of files in `Metadata/` and `Media/`); the
downtime 10-20 minutes.

### Then

Jellyfin (Plex's shape, without plex.tv), Vaultwarden, Navidrome, and the
rest of the registry: the tools do the same for each, so a help page is
needed only where an app has something the tools can't do (§3.9).

## 6. Where: same machine, new machine, NAS

Roostack assumes it owns its server (SPEC §3: a fresh Debian 13).

- **A new machine:** the old one keeps running until the switch, and going
  back is starting it again. The files reach Roostack on a drive, a share
  (the old machine can offer one), or with `rsync` into a folder.
- **The same machine, reinstalled:** stop the services, take the dumps
  onto the data disk, reinstall the system disk with Debian 13 and
  Roostack, then bring the data in from that disk (a folder on the
  server, or the drive). The copy needs the space twice on that disk;
  moving in place instead of copying isn't proposed (it would change the
  original). Going back means reinstalling the old system, so a backup of
  it first.
- **Docker and Roostack side by side:** outside SPEC §3 and probably unsafe:
  both want ports 80 and 443, and Docker's published ports skip the
  input firewall Roostack's rules assume it controls (unverified,
  rehearsal item 4).
- **Media on a NAS:** stays there as a media library; only an app's own
  writable data (Immich's library) is copied to the server.

## 7. Options considered

| | What | Why not first |
|---|---|---|
| **A. Help pages only** | Commands to copy into `/srv/roostack/…`, `chown`, and load a dump by hand | Roostack's folders, owners and database names are internal; doing it by hand behind a running Roostack is easy to get wrong, and nothing is checked |
| **B. The tools above** (recommended) | Sources, reading the old setup, matching by container path, libraries at their old paths, the check, copy then switch, undo; help per app | |
| **C. A page per app that does it all** (the first draft) | Reads the old machine over SSH, recipes per app, stops the old containers, rewrites Plex's paths | Each app is its own project; the Owner, 2026-10-20: start with tools. With B matching by container path, most of what a recipe did is generic; what's left is help |
| **D. An app with no package** | The package editor's import makes a package from the same Compose file; B brings its data | Works once B exists, and matches well: the editor keeps the Compose file's container paths |

## 8. A first version

- **Sources:** a drive or disk, a network share, a folder on the server.
- **Reading the old setup:** pasted `docker inspect` output (read in Go),
  or the Compose file and `.env` (read by the editor's Compose reader, in
  the browser).
- **Matching by container path,** with the prefix lines, nested mounts
  split, and anything unmatched chosen by hand.
- **Media libraries at their old paths,** per service (an instance field
  and a generator change), once the rehearsal shows how an unplugged
  drive behaves.
- **What's brought:** a folder for each storage, a dump for each database:
  Postgres custom format, MariaDB or MySQL SQL; SQLite with its folder.
- **The check** with its generic warnings (image and version,
  environment, old paths left in the data, data outside any mount), **copy
  and copy again, switch, undo**, from the install page ("I'll bring its
  data") and a service's Manage menu.
- **Help:** "Moving from Docker Compose", and pages for Immich and Plex
  with only what the tools can't do for them.
- **Built from:** the drive and share code of media libraries (a
  read-only mount for the import), the folder picker, the editor's
  Compose reader, `node.CommitData` and the restore loader (`LoadDumps`),
  the kept copies of restores, the left-data install path. New: the
  setup reader and matcher, the copy (a Go copy that skips unchanged
  files, run as Roostack's user, with progress), the check, the import
  page, libraries at their own paths, a store table for imports in
  progress.
- **No new dependencies:** the copy is local (Go's standard library), and
  `pg_restore` runs in the package's own database image.

## 9. Later

- A dump uploaded through the browser; plain SQL and `pg_dumpall` dumps
  (Immich's own backups).
- Another machine over SSH (`rsync`), so nothing has to be carried, and
  `docker inspect` read there rather than pasted.
- A hint per storage and database in the package format (§3.3).
- Help pages for Jellyfin, Vaultwarden, Navidrome and the rest of the
  registry, each short: the tools do the rest.

## 10. Rehearsal on VM 3 (not run yet: it needs Docker)

To verify before building, on a throwaway VM with Docker and Roostack
(Docker and Immich's and Plex's Compose images are new to the project, so
they need the Owner's yes):

1. **Immich:** a Compose Immich at the package's version with its library
   at `/usr/src/app/upload` and a few hundred photos; `docker inspect`
   read and matched; a custom dump with `--no-owner`, loaded as user
   `immich` instead of `postgres`; the folders brought in: do photos,
   faces and smart search come back, and does Immich rewrite the paths to
   `/data` itself? Then the same with `/data`, matched with no choices.
2. **Plex:** a Compose Plex with a library at `/movies`, its config
   brought in and the library shown at `/movies`: the same identity,
   history kept, nothing to change in Plex? (A Plex account is needed to
   claim the test server.)
3. **A library at its own path,** on a USB drive: unplugged and plugged in
   again while the service runs, and after a reboot.
4. **The old-paths search** on Plex's database and Immich's dump: does it
   find what it should, and how long does it take on a big database?
5. **Timings** for ~10,000 files, from a USB drive and from an SMB share.
6. **Docker beside Roostack** on one Debian 13: ports, and what reaches
   Docker's published ports through Roostack's firewall.

## 11. Decisions (the Owner chose every recommendation, 2026-10-20)

1. **The direction:** the tools (§3), generic for any package, with short
   help per app, as the first version.
2. **Sources in the first version:** drive, share, folder on the server.
   Add uploading a dump through the browser now, or later?
3. **Reading the old setup:** `docker inspect` output and the Compose
   file both (recommended), or one of them?
4. **Media libraries at their old paths** in the first version
   (recommended: it's what keeps Plex's and Jellyfin's databases right
   without app knowledge), or later?
5. **Files Roostack can't read:** the person fixes the owners on their copy
   (recommended), or the agent copies as root?
6. **Postgres dumps:** custom format only at first (recommended), or plain
   SQL and `pg_dumpall` too (Immich's own backups)?
7. **The rehearsal:** Docker and Immich's and Plex's Compose images on
   VM 3?

## Sources

- Immich, [errors: inconsistent media location](https://docs.immich.app/errors)
  (paths rewritten automatically since v1.136.0) and
  [upgrading](https://docs.immich.app/install/upgrading) (pgvecto.rs to
  VectorChord); checked 2026-10-20.
- Plex, [Move an install to another system](https://support.plex.tv/articles/201370363-move-an-install-to-another-system/)
  (the page refused our fetch; its steps as quoted in search results and
  Plex's forums, 2026-10-20).
- Roostack: SPEC §6.7, §6.8, §6.9, §8.1 (media libraries), §8.3.4,
  §8.3.7, §10.10; the code named in §1.
