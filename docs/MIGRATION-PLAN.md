# Bringing services from Docker Compose: the plan (proposal, 2026-10-20)

The details behind SPEC.md §8.4. **A proposal, nothing built:** the
Owner's answers to the questions at the end decide the first version.

Many people who'd use Roostack already run their services with Docker
Compose, the Owner included (Immich and Plex). This plan is how they move
a service into Roostack **keeping everything**: photos, libraries, watch
history, accounts, settings and, where possible, the addresses their
phones and TVs already use.

**Unverified** marks what needs a rehearsal on a VM before it's relied on
(listed in "Rehearsal on VM 3", near the end).

## Words

- **Old service:** the app as it runs today, in Docker Compose, on the
  **old machine** (which may be the same computer, see "Where").
- **Compose project:** one `docker-compose.yml` and the containers it
  makes (Immich's server, machine learning, Postgres and Redis are one
  project).
- **Bind mount:** a folder on the old machine shown inside a container
  (`/srv/photos:/data`). **Named volume:** a folder Docker keeps for a
  container under `/var/lib/docker/volumes/<name>/_data`.
- **Dump:** a database written out as one file (`pg_dump`), which another
  database server of the same kind can load.
- **First copy / final pass:** copying the data while the old service
  keeps running (hours), then stopping it and copying only what changed
  (minutes). Roostack already moves services between servers this way
  (SPEC §10.10, "Copy directly").

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
  The plan reads that, and the Compose file only when Docker can't be
  asked.
- **Each package storage gets one or more old folders.** A recipe per app
  (below) says which container path goes where, what's skipped (caches,
  machine-learning models, logs) and what's renamed.

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
  Plex keep private files). Over SSH that means a user with `sudo`; on
  the same machine, the agent's read-only, read-as-owner mount.

### Databases inside Compose

- **Dump and load, never copy the database's files.** Copying Postgres's
  folder works only with the same major version, the same extensions at
  the same versions, the same processor and matching owners. A dump works
  across all of those except going *down* a major version. It's also how
  Roostack's own backups and restores already work, so the loader is
  there and tested.
- **The dump comes from the running old database**, read-only:
  `docker exec <db> pg_dump --format=custom --username=<user> <db>`,
  streamed to Roostack. Taken twice: once with the first copy (to check
  it loads), and again after the old app stops (the real one).
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
  on or off). The recipe says which; the rest are listed in the plan as
  "not brought over", with their values, so nothing is silently lost.
- **Settings that name the old world** need a look after the move:
  Immich's *external domain* (used in share links), its machine-learning
  URL if it was changed from the default, an OAuth provider; Plex's
  custom server access URLs and remote-access port. The check step lists
  each one it finds.
- **A config file mounted into the container** (Immich's
  `IMMICH_CONFIG_FILE`) is shown in the plan; the first version stops and
  asks for it to be removed (its settings go back in the database through
  Immich's admin page) rather than guessing.

### Large media: left in place, or copied

- **Media an app only reads** (Plex's films and TV, Jellyfin's,
  Navidrome's music) **stays where it is**, as a Roostack media library:
  a NAS share (SMB or NFS), a drive moved to the new machine, or a folder
  on it. Nothing is copied. The app's paths change (`/movies` becomes
  `/media/films`), and the app is told (Plex below).
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
  and bookmarks carry on. A new address works too: the check page then
  lists what to change (each phone's server address; share links already
  sent out stop working; Immich's *external domain*).
- **Plex's remote access** in Compose is often port 32400 forwarded on the
  router, and `network_mode: host` (for discovery on the home network).
  Roostack serves Plex through Caddy at `plex.<domain>` and refuses host
  networking, so remote access goes through that address (or Plex's
  relay), and TVs that discover servers on the network may need the
  server picked once. Unverified: what Plex's apps do on a home network
  without host networking. To ask the Owner how they reach Plex from
  outside.

### GPUs

- **Intel and AMD graphics** (`/dev/dri`) work in Roostack: Plex's
  hardware transcoding (with Plex Pass), Immich's video transcoding. The
  plan reads the old container's devices and turns the package's setting
  on.
- **NVIDIA** (`runtime: nvidia`, `deploy.resources…devices`) isn't
  supported: Plex would transcode on the CPU, and Immich's
  CUDA machine-learning image isn't the package's. The plan says so before
  anything is copied. NVIDIA support is its own piece of work (the
  container toolkit with rootless Podman), not proposed here.
- **Immich's machine learning on a GPU** (CUDA, OpenVINO): the package
  runs it on the CPU. The faces and smart search already computed are in
  the database and move with it; only new photos use the CPU.

## 3. Immich, step by step

**From** a typical Compose install (Immich's own `docker-compose.yml`):
`immich_server`, `immich_machine_learning`, `immich_postgres`
(`ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0`),
`immich_redis`; `UPLOAD_LOCATION` mounted at `/data` (or, before Immich
1.133, at `/usr/src/app/upload`); `DB_DATA_LOCATION` for Postgres.

**To** the registry's `immich` package (3.3.3-roostack.1; 2.1.0 also
listed): its `library` storage at `/data`, `thumbs` at `/data/thumbs`,
`encoded` at `/data/encoded-video`, all on the media disk; `model-cache`;
its Postgres (the same image); Valkey.

### What moves where

| Old (inside `UPLOAD_LOCATION`) | New | Why |
|---|---|---|
| `library/`, `upload/`, `profile/`, `backups/` and the `.immich` files | `media/immich/library/` (the same names) | Originals, uploads in progress, profile pictures, Immich's own DB backups. Critical. |
| `thumbs/` | `media/immich/thumbs/` | Replaceable, but **copied**: making them again for 100,000 photos takes many hours of CPU. |
| `encoded-video/` | `media/immich/encoded/` | Replaceable, copied for the same reason. |
| The database | Loaded from a dump into Roostack's `immich` database | Users, albums, faces, smart search, settings. |
| `model-cache` volume, Redis | Not copied | Downloaded again; a job queue. |

Immich writes a `.immich` file in each folder and checks them at start;
they come with the folders.

**Paths in the database.** Immich stores each file's full path as the
container sees it (`/data/library/…`, or `/usr/src/app/upload/library/…`
on old installs). Roostack's package mounts at `/data`, so an install
already at `/data` needs nothing. One at `/usr/src/app/upload`: since
Immich 1.136, Immich notices at start that its media location has moved
and rewrites the paths itself (Immich's documentation, "Inconsistent media
location"). Unverified with a loaded dump: rehearsal item 1.

### Versions

- **The old Immich must be the package's version** in the first version
  (3.3.3, or 2.1.0 if the lab installs that). "Never change the version
  and the machine in one step": if something's wrong afterwards, it's
  clear which step did it, and Immich's upgrade notes apply to their
  usual setup. The plan reads the old version from the image (and
  `/api/server/version`) and says which version to update the Compose
  install to first, with a link to Immich's release notes.
- **Postgres:** the same major version (14 for every Immich Compose file
  since 1.133). VectorChord, not pgvecto.rs, in `pg_extension`.
- Later: allowing an older Immich, letting Immich's own migrations run at
  the first start in Roostack (as an update does), for the jumps Immich
  supports.

### Order

1. **Check** (nothing changes): versions, extensions, sizes, space on the
   media disk (originals + thumbnails + encoded videos + 10%), the
   address, GPU, a mounted config file.
2. **Install** the `immich` instance without starting it (its database
   container and secrets are made), with machine learning set as before.
3. **First copy** while the old Immich runs: the folders above into
   `<storage>.incoming`, resumable. Then a **trial dump** loaded into the
   new database, to find any problem before the downtime (then emptied
   again).
4. **The move** (downtime): stop the old `immich_server` and machine
   learning (the database keeps running); copy what changed; take the
   real dump; stop the old database; swap the folders in; load the dump;
   start Immich in Roostack.
5. **Check:** Immich answers; its version; the number of photos and videos
   and of people in the new database equals the old's; **every original's
   file exists** (each `originalPath` in the database, looked up on disk:
   seconds for 100,000 files); the paths were rewritten if the location
   moved; settings that name the old world (external domain, OAuth,
   machine-learning URL).
6. **The person checks:** signs in, opens a few albums, uploads a photo
   from a phone with the new (or same) address. Then **Keep it** or **Go
   back**.

### How long

Estimates, to be measured in the rehearsal:

| | 100,000 photos, ~1 TB | Notes |
|---|---|---|
| First copy over 1 Gb/s Ethernet | ~3 hours | ~100 MB/s; while Immich keeps running. 2.5 Gb/s: ~1¼ hours. |
| First copy from a USB 3 hard disk | ~1½-2 hours | ~150-200 MB/s |
| Thumbnails and encoded videos | +10-20% of that | Hundreds of thousands of small files: slower per byte |
| Final pass | a few minutes | Mostly listing files; only new uploads are copied |
| Dump and load | 5-20 minutes | A 1-3 GB database; the vector indexes are built again on load |
| **Downtime** | **about half an hour** | |

### Going back

Until **Keep it**: stop Immich in Roostack (its copy kept, or removed by
choice) and start the old containers. The old machine's files and
database were never written to, so it's exactly as it was at the move.
After **Keep it**, going back loses what changed in Roostack since (new
uploads), so it's no longer offered as a button; the help says how.

## 4. Plex, step by step

**From** a typical Compose install: `plexinc/pms-docker` or
`lscr.io/linuxserver/plex`, a `/config` folder or volume, media folders
(`/movies`, `/tv`, `/data/media`…, often from a NAS mounted on the old
machine), sometimes `network_mode: host`, `/dev/dri`, `PLEX_CLAIM`.

**To** the registry's `plex` package (1.43.4): Plex's own image, its
`config` storage at `/config`, `transcode`, and the chosen media
libraries at `/media/<library>`, read-only.

### What moves where

| Old | New | Why |
|---|---|---|
| `/config/Library/Application Support/Plex Media Server/` (`Preferences.xml`, `Plug-in Support/Databases/`, `Metadata/`, `Media/`) | `services/plex/config/` (the same layout: both images use it) | The server's identity and sign-in, the library database (watch history, collections, playlists, edits), artwork and preview thumbnails. Critical; can be many GB and a million small files. |
| `Cache/`, `Codecs/`, `Crash Reports/`, `Logs/`, `Updates/` | Not copied | Made again. Codecs depend on the processor. |
| `/transcode` | Not copied | Temporary. |
| The media folders | **Not copied:** media libraries in Roostack (the NAS share, or the drive) | Read-only, left where they are. |

**Who Plex is.** `Preferences.xml` holds the server's identity
(`MachineIdentifier`, `ProcessedMachineIdentifier`) and its sign-in to
plex.tv (`PlexOnlineToken`). Copied as they are, the new server **is** the
old one to plex.tv and every app: same name, same libraries, same people
with access, no claim code. So **the two must never run at once**; the old
one is stopped before the new one starts, and kept from starting again
(`docker update --restart=no`, undone on going back).

**Library paths.** Plex's database names every file by its path inside
the container (`/movies/Alien (1979)/Alien.mkv`). In Roostack the same file
is `/media/<library>/Alien (1979)/Alien.mkv`. Two ways to tell Plex:

- **A. Rewrite the paths in the copy** (recommended): with the copy's
  Plex stopped, run Plex's own SQLite (`Plex SQLite`, in Plex's image, as
  Plex's search tables need its own build) from the pinned image on the
  copied database: each library's folder (`section_locations.root_path`)
  and each file (`media_parts.file`, and stream `url`s) from the old prefix
  to the new. Seconds. Everything stays as it was: watch history,
  "recently added", analysis, chosen artwork. It's not Plex's documented
  way, but it runs only on the copy, and the check below proves it. A
  library whose old path is already `/media/<name>` needs nothing.
  Unverified: rehearsal item 2.
- **B. Plex's own way** (Plex's "Move an install to another system"
  article): turn off "Empty trash automatically after every scan", add the
  new folder to each library, scan, remove the old folder, then empty
  trash, clean bundles and optimise the database. Plex matches the files
  again; people report watch history kept, but moved items may show as
  recently added. Hours for a big library, and done in Plex's pages. The
  fallback if A fails a check, and what the help describes for a manual
  move.

**Matching old folders to libraries.** The plan lists each old media
mount and suggests the Roostack library holding the same files (a sample
of file names looked up in each library), for the person to confirm or
add (a share, a drive). A folder no library holds stops the plan, saying
which.

### Versions

The old Plex must be the package's version **or older** (Plex upgrades
its database itself at start, as every Plex update does; a newer
database in an older Plex isn't safe). Plex updates often, so an old
server newer than the package is likely: the plan says so, and the
answer is waiting for the package's next version, or the "follow
upstream" update choice (§8.3.8).

### Order

1. **Check:** version, the media folders matched to libraries (each old
   file path has a new one), space for the config folder, GPU (Intel:
   on; NVIDIA: says it's not supported), host networking (says what
   changes), the address.
2. **Install** `plex` without starting it, with the chosen libraries.
3. **First copy** of the config folder (without the caches) while the old
   Plex runs.
4. **The move:** stop the old Plex and keep it stopped; copy what changed
   (the database files are now quiet); rewrite the paths in the copy;
   swap it in; start.
5. **Check:** `/identity` gives the old `MachineIdentifier`; it's claimed;
   the number of libraries, items and files equal the old database's;
   **every file in the database exists** in the new container (a list
   from the database, looked up in the libraries); Plex answers at its
   address.
6. **The person checks:** a TV or phone app finds the server, plays
   something, shows what's been watched. **Keep it** or **Go back**.

### How long

| | A big library (5,000 films and shows, 20-50 GB config) | Notes |
|---|---|---|
| First copy | 20 minutes to 2 hours | Mostly the number of files in `Metadata/` and `Media/` (video preview thumbnails are the biggest part) |
| Final pass | a few minutes | |
| Path rewrite | seconds | |
| **Downtime** | **10-20 minutes** | |

### Going back

Stop Plex in Roostack, set the old container's restart policy back and
start it. The old config was never written to. Watch history made in
Roostack meanwhile stays in Roostack's copy (and on plex.tv for what
plex.tv syncs).

## 5. The person's journey

**Where it starts:** "Bring a service from Docker Compose" on **Add
services** (the catalog), beside "Add your own", and as a line in the
first-run wizard's last step ("Already run services with Docker? Bring
them over"). Owner only: it reads another machine with root rights.

1. **Where are they now?**
   - **Another machine** (the first version): its address and an SSH user
     who can use `sudo`. Roostack makes a key for this move and shows the
     one line to add to that user's `~/.ssh/authorized_keys` (a
     **Copy** button, and "Tell me more" saying what Roostack will run
     there: only reads, plus stopping and starting the containers being
     moved). **Check the connection** confirms SSH, `sudo`, Docker and
     `rsync`, and shows the machine's fingerprint to compare.
   - Later: **a disk or folder on this server** (the old machine's disk
     moved over, or the same machine), and **a database dump file**.
2. **What runs there.** Roostack lists the Compose projects and their
   containers (`docker ps`, `docker inspect`): those it can bring (Immich,
   Plex: "Bring to Roostack") and those it can't yet ("No package for
   this yet: you can write one, the editor imports its Compose file",
   linking the package editor with the file).
3. **The plan** (a dry run: nothing changes anywhere). One page, sections
   with a status each:
   - **What moves:** each old folder → its new place, size and number of
     files; what's left out and why (caches, models); the database and
     its size.
   - **Versions:** old and package versions, Postgres, extensions. A
     mismatch says what to do ("Update Immich in your Compose file to
     v3.3.3 first: Immich's notes →").
   - **Space:** what's needed on each disk and what's free.
   - **Media:** each old media folder and the library it becomes, to
     confirm.
   - **Address:** the old one and the new; "Keep `photos.example.com`"
     when it's under the lab's domain; what changes otherwise.
   - **What will be different:** NVIDIA, host networking, remote access,
     settings not brought over (with their values).
   - **Time:** the first copy's estimate (the service keeps running) and
     the downtime's.
   - Problems stop it here, each with what to do. **Start the first
     copy.**
4. **First copy:** the `activity` component, with progress (bytes, files,
   an estimated finish) and Details. It survives a restart of Roostack and
   continues where it was; "Copy what's changed" can be run again any
   time, so the person chooses when the downtime is. The page can be
   left; Home shows it under way.
5. **The move:** says exactly what happens and for how long ("Immich will
   be unavailable for about 25 minutes. Roostack will stop it on
   old-nuc, copy what's changed, …"), then **Move now**. One activity,
   one step at a time. A step that fails stops there, starts the old
   service again, and says so.
6. **Check:** the automatic checks, each ✓ or what's wrong, then what to
   check yourself (sign in, open the phone app; "your phones' Immich app
   needs the new address: …"). **Keep it** / **Go back to the old one**.
7. **Done:** the service is a Roostack service like any other (backed up
   tonight: the page suggests a backup now, as the photos are new to the
   backups). Roostack forgets its SSH key and reminds the person to remove
   the line from the old machine. It says which folders on the old
   machine can be deleted when they're happy; **Roostack never deletes
   them.**

A record of each move (its plan, the commands run on the old machine, the
checks' results) is kept in History (§6.14).

## 6. What Roostack automates, and what stays manual

### How much to automate: the options

| | What | For | Against |
|---|---|---|---|
| **A. A help guide** per app | Pages with the exact commands: dump, copy, `chown`, install over the data | Small; useful for any app; technical users can follow it | Every step by hand as root on two machines; ownership, versions and paths are easy to get wrong; Roostack can't check anything. Doesn't suit "nothing magical, but nothing hard" |
| **B. A command** (`roostack import compose …`) | The same engine as C, run in a terminal on the server | Scriptable; quicker to build than pages | Compose users are technical, but Roostack's rule is that managing the lab needs no terminal (§2); a second interface to keep |
| **C. A page in Roostack** (recommended) | The journey above: reads the old machine, plans, copies, moves, checks, goes back | Checks before anything changes; the downtime short and chosen; going back is a button; everything shown with its log | The most to build; one recipe per app |
| **D. Any Compose service** | The editor's import makes a package, then the same copy for its folders and database | Covers apps the registry lacks | Without a recipe, Roostack can't know paths inside the app's data, its version rules or how to check it; later, on top of C |

**Recommended: C, with a help page per app that says what each step does
(A's content, for the curious and for apps without a recipe).** B can
come later from the same engine if wanted.

### Recipes: in Roostack's code first

A **recipe** says, for one app: which images it recognises; which
container paths go to which storage, and what's skipped; which container
is the database and how to dump it; which settings come from the old
environment; its version rules; any rewrite (Plex's paths); and its
checks.

- **First in Roostack's code** (a small package, `internal/migrate`, one
  file per app), for Immich and Plex. Two apps will show which parts are
  just data.
- **Then, once the shape is clear, the data part moves into the package
  format** (a `migrate:` part: images, path mapping, what to skip,
  settings), so the registry can add apps without a Roostack release;
  checks and rewrites stay in code, named by the package. A format
  change needs its own decision (policy, editor, `requires.roostack`).

### Automated, and why

- **Reading the old setup** (`docker inspect`): exact, and the source of
  most mistakes when done by hand.
- **Every check** before and after: versions, space, ownership, every
  file accounted for.
- **Copying, with resuming and passes:** hours of data; a broken
  connection mustn't mean starting again.
- **Owners, the database dump and load, path rewrites:** where manual
  moves usually go wrong.
- **Stopping and starting the old containers** (`docker stop`, `start`,
  `update --restart`): the downtime and going back depend on doing it at
  the right moment. Nothing else is changed on the old machine.

### Manual, and why

- **Giving Roostack access to the old machine** (adding the key line):
  only the person can, and should, decide that.
- **Updating the old app to the right version first:** in their own
  Compose setup, with the app's own notes; Roostack says which version.
- **DNS outside Roostack** (a router, Pi-hole, another provider):
  Roostack says what to change.
- **Each phone's app**, if the address changes.
- **Router port forwards** for Plex.
- **Deleting the old data:** never automatic.

## 7. Safety

- **The original is only ever read.** Over SSH, `rsync` reads (no
  `--remove-source-files`, ever) and `pg_dump` reads. On this server
  (later), the old folder is mounted **read-only** by the agent. The only
  changes on the old machine are stopping and starting the containers
  being moved, and their restart policy, each undone on going back.
- **Nothing replaces anything until the copy is checked:** data lands in
  `<storage>.incoming` and is swapped in only at the move; anything
  already in the service's folders is kept aside (`.before-import-<time>`),
  as restores do.
- **Space first:** the plan refuses to start without room for the copy
  plus 10% on each disk, and the copy stops (resumable) if the disk gets
  within the lab's "running out of space" level. The first copy is
  outside the service's folders' backups until it's swapped in, so it
  doesn't bloat a backup half-way.
- **Downtime is short and chosen:** only the final pass, the dump and the
  start. The move says how long beforehand.
- **One move at a time**, as a maintenance job (§3.2): no reboot or backup
  in the middle of the move itself. The first copy runs beside other work
  (it reads the old machine and writes only the new folders).
- **Never both running:** for Plex (one identity on plex.tv), and for
  Immich (phones would upload to whichever answered). The old one is
  stopped first and kept stopped.
- **Going back** is a button until **Keep it**.
- **Secrets:** the SSH key is Roostack's, 0600, made for one move and
  deleted after; the old database's password is never needed
  (`docker exec` as the database's own user); nothing is logged that
  `docker inspect` shows as a secret (variables named like passwords
  are shown as `•••`).
- **Untrusted input:** the old machine is the person's, but a stream from
  it is checked like a move's (no links out of the folder, no `..`), and
  nothing it says is run.

## 8. Where: same machine, new machine, NAS

Roostack assumes it owns its server (§3: a fresh Debian 13). That
shapes the choices:

- **A new machine (recommended, the first version).** The old machine
  keeps running until the move; going back is instant; the copy is over
  the network. The common case for someone getting a NUC for Roostack.
- **The same machine, reinstalled.** The old machine becomes the Roostack
  server: Debian 13 and Roostack on its system disk, its data disks
  kept. The old Docker can't be asked any more, so: stop the old services
  first, keep a database dump (Immich's own backup is one), then after
  installing Roostack, read the old disk through a read-only mount and
  load the dump file. Copying needs the space twice on that disk; moving
  in place would need a backup first. **Later**, and riskier: the old
  setup can't be started again without reinstalling.
- **Docker and Roostack side by side on one Debian 13.** Not supported by
  §3, and probably not safe: both want ports 80 and 443, and Docker's
  published ports skip the host's input firewall (they're forwarded),
  which Roostack's firewall assumes it controls. Unverified: rehearsal
  item 4. If it works with care, it could be a short-lived bridge
  ("install Roostack, move each service, remove Docker"); if not, the
  page says to use another machine.
- **Media on a NAS:** stays there. Roostack mounts the share as a media
  library (read-only), and Plex's paths are rewritten to it. The NAS
  can be read by the old and new servers at once, so the media is never
  copied or moved. An **Immich library on a NAS** has to come onto the
  server's disk (see "Large media").

## 9. Which apps first

1. **Immich and Plex** (the Owner's own; they cover the hard parts:
   Postgres with extensions, big writable media, SQLite, server
   identity, media left on a NAS, path rewrites).
2. **Jellyfin** (Plex's shape, without plex.tv: its SQLite database also
   names files by path), **Vaultwarden** (a folder and SQLite, or
   Postgres; passwords matter most to people), **Navidrome** (a folder;
   music libraries).
3. **Paperless-ngx, Forgejo/Gitea, Mealie, Audiobookshelf, Uptime Kuma,
   PhotoPrism** (MariaDB), the rest of the registry as people ask.

Each app is a recipe and a help page, so later ones are small once the
engine exists.

## 10. A first version, small enough to build soon

- **Immich and Plex, from another machine over SSH,** with the journey
  above: plan, first copy (repeatable), move, check, keep or go back.
- **Versions must match** (Immich exactly; Plex the same or older;
  Postgres the same major).
- **Media libraries** must already be added in Roostack (the plan links to
  Settings → Media libraries when one's missing).
- **Recipes in code** (`internal/migrate`); the copy and dump stream into
  `node.ImportData` and `LoadDumps`; one new store table for moves in
  progress; the page is one template with the `activity` component.
- **Help:** "Bring a service from Docker Compose", with a page each for
  Immich and Plex saying what Roostack does at each step (and how to do
  it by hand).
- **Needs the Owner's yes:** `rsync` as a dependency of the Roostack
  package (Debian's; resumable, and only sends what changed; the
  alternative is tar over Go's SSH client, `golang.org/x/crypto/ssh`,
  already a dependency, with resuming written by hand). The old machine
  needs `rsync` too (one `apt install`; the plan checks).
- **Not in it:** the same machine, dump files, apps beyond the two, NVIDIA,
  an older Immich, a "try it first" run of the copy, moving in place.

Rough size: the engine (read, plan, copy, move, back) and both recipes,
one page with its steps, help; about the size of moving services between
servers (§10.10) plus the recipes.

## 11. What this needs from other work

- **Single sign-on (§5.2, another session):** nothing for the first
  version. When Immich's single sign-on is built, it should link people
  who arrive with their accounts (by email) rather than make new ones,
  and replace an OAuth setting pointing at an old provider.
- **Media libraries:** used as they are. "Storage on a network share" for
  an app's own data isn't proposed.
- **The package format:** unchanged in the first version (recipes in
  code).

## 12. Rehearsal on VM 3 (not run yet: it needs Docker)

To verify before building, on a throwaway VM with Docker and Roostack
(Docker itself and Immich's and Plex's Compose images are new to the
project, so they need the Owner's yes):

1. **Immich:** a Compose Immich at the package's version, with the
   library mounted at `/usr/src/app/upload` and a few hundred photos.
   Dump with `pg_dump --format=custom`, load with Roostack's loader
   (`--no-owner`, user `immich` instead of `postgres`), copy the folders,
   start the package: do the photos, faces and smart search come back, and
   does Immich rewrite the paths to `/data` itself?
2. **Plex:** a Compose Plex with a library at `/movies`; copy its config,
   rewrite the paths with `Plex SQLite` to `/media/films`, start the
   package: same identity, history kept, files play? (Needs a Plex
   account to claim the test server: the Owner's, or a throwaway one.)
3. **Timings** for a library of ~10,000 files, to calibrate the
   estimates.
4. **Docker beside Roostack** on one Debian 13: ports, Roostack's
   nftables rules against Docker's, what reaches Docker's published ports.
   Decides whether "the same machine" can ever be side by side.

## 13. Questions for the Owner

About your homelab (they shape the first version):

1. **Same machine or another?** Will Roostack run on a new machine, with
   the old one kept running until the move, or will the old machine be
   wiped and reinstalled with Roostack?
2. **Where do the files live, and how much?** Immich's `UPLOAD_LOCATION`
   (a local disk, a USB disk, a NAS?), roughly how many photos and how
   many TB; Plex's media (a NAS share, SMB or NFS? a local disk?) and the
   size of Plex's config folder.
3. **Immich:** which version, which Postgres image, external libraries
   (folders Immich reads but didn't upload)? Machine learning on a GPU?
   Sign-in through an OAuth provider?
4. **Plex:** which image (Plex's or linuxserver's) and version? A GPU
   (Intel or NVIDIA)? Plex Pass? `network_mode: host`? How do you reach it
   from outside home (a forwarded port 32400, Plex's relay, a VPN)?
5. **Addresses:** what are they now, through which proxy, and on which
   domain? Should Roostack's lab use that domain, so the addresses stay?
6. **Access:** can Roostack reach the old machine over SSH as a user with
   `sudo`? Is it fine for Roostack to stop and start the old containers
   itself?
7. **The network between them:** 1 Gb/s, 2.5 Gb/s, Wi-Fi?

About the design:

8. **How much to automate:** the page (C), recommended, or a guide (A) or
   command (B) first?
9. **Plex's paths:** rewrite them in the copy (A, recommended) or Plex's
   own add-and-remove (B)?
10. **Versions:** require the old app's version to match the package's in
    the first version (recommended)?
11. **Dependencies and the rehearsal:** `rsync` as a dependency, and
    Docker plus Immich's and Plex's Compose images on VM 3 for the
    rehearsal?

## Sources

- Immich, [errors: inconsistent media location](https://docs.immich.app/errors)
  (paths rewritten automatically since v1.136.0) and
  [upgrading](https://docs.immich.app/install/upgrading) (pgvecto.rs to
  VectorChord); checked 2026-10-20.
- Plex, [Move an install to another system](https://support.plex.tv/articles/201370363-move-an-install-to-another-system/)
  (the page refused our fetch; its steps as quoted in search results and
  Plex's forums, 2026-10-20).
- Roostack: SPEC §6.7, §6.8, §6.9, §8.1 (media libraries), §8.3.4,
  §8.3.7, §10.10; the code named in "What Roostack already has".
