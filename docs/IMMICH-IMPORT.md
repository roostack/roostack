# Bringing photos into Immich from other platforms (2026-10-21)

The details behind SPEC.md §8.5. **Decided; the first version is built**
(SPEC.md §8.5 "As built", tried on VM1: docs/ASSUMPTIONS.md X1–X10). The Owner
agreed the proposal on 2026-10-21 with two changes: a key is always made
by the person in Immich and pasted (§6), and Apple's export follows the
first version straight away (§10). They also asked that people other than
Owners and Admins can use it from the first version, if the Owner allows
it, and chose how (§6.1).

For many people Immich is the reason to have a home lab at all, and the
hard part is leaving Google Photos or iCloud with a lifetime of photos.
The exports are huge, split into many archives, and keep dates, places,
albums and descriptions in separate files that most tools ignore. Roostack
can make this the easy part: say where the export is, check it, bring it
in, and say plainly what came and what didn't.

This is not §8.4 (bringing your own data). §8.4 moves an *existing Immich*
(its files and database) into Roostack. This brings photos from *other
platforms* into an Immich, through Immich's own API, into one person's
account. They share one piece: where the files are and how Roostack reads
them (§4).

Facts below were checked on 2026-10-21, from vendors' documentation and
the tools' source code (the main sources are at the end). **Unverified**
marks what the VM test must confirm before it's relied on.

## Words

- **Export:** what a platform gives you of your photos. Google's is
  **Takeout**: one or more archives (`takeout-20261021T…-001.zip`,
  `-002.zip`…), each up to the size you chose.
- **Sidecar:** a small file beside a photo holding what isn't inside it.
  Takeout's are JSON (`IMG_1234.JPG.supplemental-metadata.json`); other
  apps write XMP (`IMG_1234.JPG.xmp`).
- **Immich account:** one person's photos in Immich. A family lab has
  several; an import goes into exactly one.
- **Key:** an Immich API key. It belongs to one Immich account and can be
  limited to what it may do (Immich 1.135 and later).
- **The tool:** the program that reads the export and sends it to Immich
  (§3: immich-go).

## 1. What Roostack has to build on

| What | Where | What it gives an import |
|---|---|---|
| The Immich package | registry `packages/immich/3.3.3-roostack.2`; test copy `internal/testdata/packages/immich` | Immich 3.3.3; its API on container `server`, port 2283, on the service's own network `roostack-immich` (isolated, §6.4). The library is storage `library` (`location: media`, the large disk). |
| Action hooks | SPEC §8.1 `hooks:`; `node/hooks.go` | A button with inputs, run in a container. **Not enough on its own:** an action stops after 2 minutes and shows only its last lines (`actionTimeout`), and an import takes hours. |
| The kept key | SPEC §5.2 "Connecting the remaining apps"; Immich's `settings_key` | A key Roostack keeps, made by the install hook as Immich's administrator, limited to `adminConfig.read` and `adminConfig.update`. **It can't import:** a key uploads into its own account only, and there's no way for an administrator to make a key for someone else (`POST /api/api-keys` has no owner; checked in Immich 3.3.4's API description). Its *pattern* is reused: a hook signs in, makes a limited key, and Roostack keeps it as a secret. |
| A place to read files from | SPEC §8.4 "The place to bring data in from"; `agent/import.go` `MountImport` | The agent mounts a folder on a drive or on the server, or a network share, **read-only** at `/mnt/roostack-import/<service>`, in fstab and automounted, so a long copy carries on after a restart. The page picks it with the media libraries' drive cards, folder picker and share form. |
| Media libraries | SPEC §8.1 "Media libraries" | Drives and shares people already have, read-only. A library could be the source too (a NAS folder of exported photos). |
| Long work with a live log | `activity` (`Step`, `Printf`); the `activity` component | A one-line status and the full log under Details, as backups and §8.4's copy do. |
| The data's size | `node.DataSize`, the storage page | The library's disk, its free space, and what a service uses. |
| Running a container once | `systemd.UserCommand` (`systemd-run --user`) | How Roostack runs podman as its own user, with secrets passed by name, never on a command line. |
| A built image | `build/caddy/Containerfile`, `generate/system.go` | Roostack already builds an image on each server from pinned sources (Caddy with plugins, pinned by version, checked by Go's checksum database). |

## 2. What each platform gives today

### Google Photos

- **No API reads a whole library any more.** Since 31 March 2025 the
  Photos Library API lists only what the app itself uploaded; the Picker
  API lets someone pick up to 2000 items at a time, and its downloads drop
  the location. Google's own "transfer your content" goes only to services
  Google has vetted (Flickr, OneDrive, iCloud…), through the Data Transfer
  Initiative, not to a server of your own.
- **Takeout is the only complete way out,** started by hand at
  takeout.google.com:
  - zip or tgz, split into archives of a chosen size (Google suggests the
    largest, 50 GB, so it's split less);
  - delivered as download links by email, or put into Google Drive,
    Dropbox, OneDrive or Box;
  - once, or every 2 months for a year;
  - ready in "a few minutes to a few days", usually the same day;
  - each archive expires after about 7 days and can be downloaded 5 times;
  - a download may ask for the Google password again.
- **What's in it:**
  - The photos and videos as they were stored (originals, or Google's
    smaller copies if the account used "Storage saver").
  - A JSON sidecar per photo: when it was taken (UTC, no time zone), where
    (`geoData`, `geoDataExif`; 0,0 means none), its description, and
    whether it's a favourite, archived or in the trash, plus the people
    named in it and whether it came from a partner's sharing.
  - A folder per year (`Photos from 2019`) and a folder per album, each
    holding *copies* of its photos, with the album's own JSON. Folder
    names are in the account's language.
  - Edited copies beside the originals (`-edited`, `-bearbeitet`,
    `-modifié`…), sharing the original's sidecar.
  - Trash and Archive folders; whether the Locked Folder is included is
    **unverified**.
- **The traps** that make it hard by hand, all handled by immich-go (§3):
  - a sidecar's name is cut at 51 characters (`.supplemental-meta.json`,
    `.supp.json`…);
  - a "(1)" on a duplicate name moves after the extension
    (`IMG_3479(1).JPG` and `IMG_3479.JPG(1).json`);
  - a Live Photo's video half has no sidecar;
  - one photo can be split across archives from its sidecar.
- **Is the location removed from the files?** Not documented by Google,
  and reports differ. Places added in the app exist only in the sidecar,
  which is why sidecars matter.

### Apple and iCloud Photos

- **privacy.apple.com, "Get a copy of your data":** the photos and videos
  in their original formats, in zip parts, kept for 14 days. Albums come
  as CSV files (`Albums/<name>.csv`), and `Photo Details.csv` has each
  photo's original date, favourite and hidden flags. **Not available with
  Advanced Data Protection on** (end-to-end-encrypted data isn't given).
  immich-go reads it (`from-icloud`), using the albums and dates but
  **not** the favourite and hidden flags.
- **A Mac's Photos app:** File → Export Unmodified Original, with "Export
  IPTC as XMP", gives originals with XMP sidecars; osxphotos (MIT, active)
  does it all at once with albums as folders, and can read a copied
  library on Linux.
- **Apple's transfer to Google Photos** loses shared and smart albums,
  Live Photos and "some metadata": not a route worth suggesting.
- **icloudpd** downloads straight from iCloud, but needs Advanced Data
  Protection off and web access on, has 2FA trouble reported this month,
  and is looking for a maintainer: not something to rely on.

### Other platforms

| Platform | How to get it out | What's kept |
|---|---|---|
| Microsoft OneDrive, Dropbox | The desktop app's synced folder, copied to a drive (web zip downloads are capped: Dropbox at 250 GB and 10,000 files) | Files as uploaded, so their own dates and places. Albums aren't files. |
| Amazon Photos | No bulk export; the desktop app downloads in batches | Files as uploaded. |
| Flickr | Settings → "Request my Flickr data": zips of originals plus a JSON per photo and `albums.json` | Titles, descriptions and albums only in the JSON. No tool reads it well. |
| Facebook, Instagram | Accounts Center → "Download your information", in JSON | Photos re-compressed with their dates and places removed; dates only in the JSON. |

### Other self-hosted apps

- **A plain folder** of photos (from anywhere): dates and places are in the
  files; XMP sidecars, if any, are read; a folder can become an album.
- **Synology Photos, Nextcloud (Memories):** the files are ordinary
  folders on the NAS (Synology's `@eaDir` thumbnails left out); their
  albums live in their own databases, so they don't come.
- **PhotoPrism:** originals untouched, with a YAML sidecar per photo and
  albums as YAML. Immich doesn't read either: the files come, the albums
  and edits would need a converter (later).
- **LibrePhotos:** 1.4 can write its edits back to XMP first.

## 3. The tool

| | immich-go | Immich's CLI | GPTH "Neo" then Immich's CLI | Roostack's own, in Go | An external library |
|---|---|---|---|---|---|
| Licence | AGPL-3.0 | AGPL-3.0 | Apache-2.0 + AGPL-3.0 | Roostack's | (Immich) |
| Reads Takeout's sidecars | Yes, with years of fixes for the traps | No: dates from the file's modified time | Yes, writing them into the files with exiftool | We'd write and keep up every trap | No |
| Albums, favourites, archive | Yes | Albums from folder names only | Albums as folders; favourites lost | Yes | No (they'd live only in Immich's database) |
| Reads zips without unpacking | Yes (not tgz) | No | No: needs room for a full unpacked copy | Could | No |
| Apple's export, plain folders | Yes (`from-icloud`, `from-folder`) | Plain folders | No | Later | Folders |
| Footprint | One static Go binary, about 4 MB compressed, amd64 and arm64 | Node, a 160-tag image | Dart binary plus exiftool | None | None |
| Maintained | Yes: v0.32.0 (2026-06-25) added Immich 3 support; fixes merged 18–20 October, not yet released | With Immich | Yes (v6.4.0, September) | By us | With Immich |
| Talks to Immich | Its API only, with a limited key | Its API only | Its API only | Its API only | Inside Immich |

**Recommended: immich-go,** with Roostack's own checks around it (§5, §7).
Immich's own documentation points to it for Takeout, and its maintainers
close requests to read Takeout's JSON with the same advice; nothing else
reads the export as completely without unpacking it. What it does with a
Takeout:

- matches every sidecar, including the traps above;
- sets the date, description and place from the sidecar after uploading
  (Immich itself prefers the file's own date, so this is what makes
  Google's date win);
- makes albums from album folders, and marks favourites and archived
  photos;
- leaves the trash out (`--include-trashed=false`);
- tags people's names as `People/<name>`, and tags everything
  `takeout-<when Google made it>` (immich-go's tag, e.g. `takeout-20261019T091234Z`) so an import can be found afterwards;
- stacks bursts, RAW with JPEG, and HEIC with JPEG;
- skips what Immich already has, by checksum. Running it again is how it
  resumes.

Its weak points, which the design covers:

- its counts at the end can be wrong (#1384, #1411), so Roostack counts
  for itself before and after (§7);
- a run again doesn't re-apply tags that failed (#1385);
- it has run out of memory on large runs (#1395), so it runs with a
  memory limit and the VM test uses a large export;
- by default it pauses Immich's background jobs, which needs an
  administrator's key; Roostack turns that off (§6);
- its releases aren't signed. So Roostack **builds it from source** at a
  pinned version, as it builds Caddy: Go's checksum database fixes every
  byte of the source, and nothing is downloaded that Roostack didn't pin.

**Considered:**

- *Immich's CLI:* it doesn't read Google's sidecars, and it gives every
  photo without a date inside the file its file's modified time (the day
  it was unpacked).
- *GPTH, then the CLI:* two tools, exiftool, and room for an unpacked copy
  of hundreds of GB. It rewrites the files and loses favourites. Its
  `ignore` album mode deletes photos that were only in albums.
- *Roostack's own importer in Go:* the most control, no dependency; but
  Takeout's quirks are years of other people's bug reports, and Google
  changes the format (the sidecar names changed in 2024). Worth it only if
  immich-go stops being maintained; the pre-check (§5) is Roostack's own
  anyway, so a later switch keeps the pages.
- *An external library* (Immich reading the files where they are): no copy
  and no double storage, but Immich doesn't read Google's sidecars,
  albums and favourites live only in Immich's database, and edits silently
  fail on a read-only folder. It suits a library that stays on a NAS, not
  leaving Google.

### How it runs

- **An image Roostack builds on the server** that runs Immich, the first
  time it's needed: `build/immich-go/Containerfile`, a Go builder stage
  pinned by digest (the same one Caddy's build uses, if its Go is new
  enough, so nothing more is downloaded) running
  `go install github.com/simulot/immich-go@v0.32.0`, and a final stage with
  just the static binary. Tagged `localhost/roostack-immich-go:<version>`.
- **Run once per import,** as the roostack user (`systemd-run --user
  podman run --rm`), on the service's own network (`roostack-immich`), so
  it reaches Immich at `http://<server container>:2283` and nothing else
  of the lab. It sees the export read-only (`/mnt/roostack-import/…:ro`), a
  small work folder for its log, and the key as an environment variable
  from a Podman secret (`IMMICH_GO_UPLOAD_API_KEY`), never on a command
  line. A memory limit and `--concurrent-tasks` suited to the server.
  `--on-errors continue`, rather than its default `stop`, so one unreadable
  file doesn't end a six-hour import: every error is counted and named in
  the summary.
- **Its version belongs to Roostack,** not to the package: the pin is in
  Roostack's source, updated with Roostack's releases, with the Immich
  versions it's been tried with. A package moving Immich to a version
  newer than that (a new major version) makes the import page say imports
  wait for a Roostack update, rather than risk a half-made import.

## 4. Getting the export onto the server

The browser must not carry hundreds of GB. In order of what's offered:

1. **A USB drive** (or a second disk): copy the Takeout zips onto it on a
   computer, plug it into the server, choose it and its folder. The
   §8.4 drive cards and folder picker.
2. **A folder on a NAS** (SMB or NFS share): the §8.4 share form.
3. **A folder already on the server** (`/mnt`, `/media`, `/srv`): the
   §8.4 folder picker, for people who copied it there themselves.

All three are mounted read-only by the agent, as §8.4's are (its
`MountImport`, at a spot of its own, `/mnt/roostack-import/<service>-photos`,
so a photo import and a §8.4 import don't clash). The zips are read where
they are: **no unpacking, no extra space.** A tgz export is refused with a
sentence (choose zip in Takeout, or unpack it first), as immich-go can't
read it.

**Later, each with its own decision:**

- **An upload folder Roostack shares on the home network,** so a computer
  can copy the zips straight to the server. It needs Samba (a new system
  package) and a writable share that's only on while an import waits for
  files. Windows' own WebDAV client limits file sizes, so WebDAV isn't the
  easy alternative it looks.
- **A cloud drive** (Takeout delivered to Dropbox, OneDrive or Box, which
  Roostack then downloads). It needs an app registered with each provider
  and their sign-in (OAuth) on every lab: a bigger piece, possibly through
  Roostack Cloud.
- **Not proposed: Google's download links fetched by the server.** A link
  needs the signed-in browser session and asks for the password again;
  the workarounds (copying a browser's cookies, a headless browser signed
  in to Google) would give Roostack the keys to the whole Google account,
  and break whenever Google changes its sign-in.

## 5. The person's journey

On Immich's page in Roostack, a card **Migrate photos**: "Migrate photos
and videos from Google Photos or a folder (Apple iCloud next) into Immich,
with their dates, places and albums." **Migrate photos from…** opens the
steps. **The wording is "migrate"** (the Owner, 2026-10-21), not "bring
in", which keeps it apart from §8.4's "Bring in data from elsewhere…".
People who aren't Owners or Admins reach the same steps from a page of
their own (§6.1). Each step is one page of the flow, in the
forms' design (docs/DESIGN.md "Forms"), with Continue. Mock-ups:
[docs/mockups/immich-import](mockups/immich-import/README.md).

1. **Where from?** Google Photos (Takeout), a folder of photos, and (straight
   after the first version) Apple iCloud, as a segmented control with what
   comes under the chosen one.
2. **Get your export** (a guide, skipped with "I have the zip files"):
   - First, a warning (the Owner, 2026-10-21): getting the files from
     Google can be very slow, so it may take several days; Roostack keeps
     their place, and they come back when the files are ready.
   - Takeout: open takeout.google.com, **Deselect all**, tick only Google
     Photos (All photo albums included), **Next step**, *Send download link
     via email*, *Export once*, file type **.zip**, size **50 GB**,
     **Create export**. Google emails when it's ready. Download every part
     within 7 days (each can be downloaded 5 times) and copy them all into
     one folder on a USB drive or your NAS. Don't unpack them. About 1.1×
     the space your Google Photos uses.
   - "Tell me more" links to the help page.
3. **Where is it?** A drive, a folder on a NAS, or a folder on the server
   (§4).
4. **Your Immich key:** a link straight to the page in Immich where it's
   made, the permissions to tick, and a box to paste it (§6).
5. **What to migrate** (the Owner, 2026-10-21: a step of its own, before
   the check, which it changes): the choices of §8 as switches, Roostack's
   suggestions set, with counts from a quick look in the zips.
6. **Check** (the dry run, §7): Roostack reads the zips and asks immich-go
   what it would do, without uploading anything. The result:
   - "Parts 1 to 9 of 9, 412 GB. 61,204 photos and 3,977 videos from
     March 2009 to October 2026, 214 albums."
   - "Already in Immich: 8,912 (skipped)."
   - What won't come, with counts and why: as chosen in step 5, files
     Immich can't read, sidecars without a photo.
   - Space: "Needs about 395 GB on <disk>; 1.2 TB free."
   - The next backup: "Your next backup will be about 395 GB larger."
7. **Migrate them.** The status line and progress ("Uploading: 12,034 of
   52,292, 98 GB of 395 GB, about 6 hours left"), Stop, and the log under
   Details. The page can be closed; the migration carries on.
8. **Done:** a summary (§7) that stays on Immich's page until it's
   dismissed, and an email if email is set up.

**Where it starts (proposed, for the Owner's yes; they asked for it at the top, but not as a large card of its own):** at the top of Immich's page,
a slim call to action ("Moving from Google Photos or iCloud?", **Migrate
photos…**, Hide) until someone has migrated photos or the Owner hides it;
the Migrate photos card, with the Owner's choices and the list of
migrations, sits below the page's other cards. A large card of its own at
the top was "weird for one service".

**Leaving and coming back.** The steps and the migration live on the
server, not in the browser. The steps keep their place until the
migration is done or cancelled, so someone waiting days for Google comes
back to where they were. A running migration is shown in the call to
action at the top of Immich's page, with **Watch**, and on `/immich-photos`
for its person; closing the page changes nothing, and they're emailed when
it ends. One that stopped (Stop, a restart, a power cut) shows there with
**Carry on** (§9).

## 6. Whose photos: a key the person makes

An import goes into exactly one Immich account, because an Immich key can
only upload into its own account, and an administrator can't make a key
for someone else. **Decided (2026-10-21), the Owner:** the person always
makes the key themselves in Immich and pastes it. Roostack never asks for
an Immich password. Considered: Roostack signing in with the person's
email and password and making the key itself (one step less, but a
password typed into Roostack for another app, and no use for accounts made
through single sign-on, which have none).

- **The link:** **Make a key in Immich** opens Immich's own page for it,
  `<Immich's address>/user-settings?isOpen=api-keys` (the accordion's key
  in Immich 3.3.4's settings page; the VM test checks it opens there), in
  a new tab. Beside it: name it "Roostack migration", and tick these
  permissions, listed as Immich lists them:
  `asset.read`, `asset.statistics`, `asset.update`, `asset.upload`,
  `asset.copy`, `asset.delete`, `asset.download`, `album.create`,
  `album.read`, `albumAsset.create`, `server.about`, `stack.create`,
  `tag.asset`, `tag.create`, `user.read` (immich-go's own list; the VM
  test checks whether `asset.delete` and `asset.download` can be left
  out, as the import shouldn't need to delete or download anything, so
  the list is as short as it can be).
- **The check, as it's pasted:** Roostack asks Immich about the key
  itself, from the server (`GET /api/api-keys/me`, which any key may call,
  returns its name and permissions; `GET /api/users/me` whose it is). It
  then says, before going on:
  - whose account it is: "Into Anna's Immich account, anna@example.com";
  - any permission missing, by name ("Tick album.create and stack.create
    too: edit the key in Immich, then check again"); editing a key's
    permissions in Immich keeps the same key, so nothing needs pasting
    again;
  - a key that may do everything (`all`) works, with a note that a
    limited one is safer.
- **Kept only while it's needed:** a Podman secret for the import's life,
  passed to immich-go by name, never on a command line or in a log.
  Roostack forgets it when the import is done or cancelled, and the
  summary says so, with a link to delete it in Immich (Roostack can't:
  that would need one more permission, `apiKey.delete`, for little gain).
  A later import asks for a key again.

**Pausing Immich's jobs:** immich-go pauses Immich's thumbnails, video
conversion and face recognition while it uploads, which needs an
administrator's key. Roostack doesn't (`--pause-immich-jobs=false`): the
import is slower on a small server but needs no administrator's key, and
Immich keeps working through its queue as photos arrive.

## 6.1 Who can use it: everyone the Owner allows

**Asked (2026-10-21), the Owner:** people who aren't Owners or Admins
should be able to bring in their own photos from the first version, and
the Owner decides whether they can. **Decided the same day, the Owner:**
a page of its own on Roostack's site, at `/immich-photos`, linked from the
sign-in page too; open by default to everyone who uses Immich; and the
places they may read from chosen by the Owner when opening it.

What's there today: Roostack's people all have Roostack accounts, with a
role (SPEC §5). **Members** only see the dashboard: the services they can
use, and their status. With single sign-on, the same people sign in to
apps at `login.<domain>`, which serves only the sign-in pages; Roostack's
own pages are at `roost.<domain>`, usually on the home network only.
**Someone signed in on the sign-in page isn't signed in to Roostack's
pages:** the account and password are the same, but they sign in again.
"Sign in with single sign-on" on Roostack's login page is planned (SPEC
§5.2, later); with it, the link from the sign-in page would need no second
sign-in.

**The Owner's choice.** On Immich's page, in the Migrate photos card: **Who
can migrate photos**, a segmented control: *Everyone who uses Immich* (the
default) or *Owners and Admins*. Everyone who uses Immich is single sign-on's "Who can sign in"
for Immich when it's on, else every Member. Their own photos go into their
own account with their own key; the Owner is told (as Roostack's
notifications are, §6.13) when an import starts and ends, whose, and how
big.

**Where they may read from, chosen by the Owner** (shown under
*Everyone who uses Immich*):

- **USB drives they plug into the server** (a switch, on), and any folder
  on them. Never a backup drive, the system disk, or a drive holding a
  media library; those show greyed, with why.
- **Network shares, with their own login** (a switch, on).
- **Additional locations** (the Owner, 2026-10-21): folders the Owner sets
  up, each with **Remove**, added with **Add a location**, a dialog: a
  folder on a drive or on the server (the folder picker), or a share with
  the Owner's login (kept root's only, as §8.4's are), and its name for
  people ("Family NAS › Takeouts"). Others see them as **Other
  locations** (the Owner, 2026-10-21). People pick one and can step into its
  folders, never above it. No tickbox: the list is what's offered, so with
  none, nothing is; an empty list says "You haven't defined any additional
  locations that people can migrate photos from. Press Add a location
  below to add one." Everyone allowed sees what's in a location, so a
  location per person, or a folder per person inside one, is the Owner's
  choice; the page says so. *Changed* from the first mock-up's "Places you
  name" tickbox, whose list below it wasn't clearly part of it.

Owners and Admins can always use anything (drives, shares, the server's
folders, media libraries), as on §8.4's pages. The agent's checks are the
same for everyone; what someone may pick is checked again on the server
when they pick it, not only in the page.

**The page: `roost.<domain>/immich-photos`.** A short page in a layout of
its own: the lab's name and logo as the sign-in page shows them, none of
the control panel's menus, just migrating photos and the person's own
migrations (in progress and the last summary, with its log). The same steps
as §5. Where a lab runs more than one Immich (§8.2), it asks which first,
listing only those the person uses. Linked from:

- Immich's card on their dashboard;
- the sign-in page, once someone's signed in there ("Migrate your photos
  into Immich", shown only to people allowed, and only when the lab runs
  Immich);
- an address the Owner can send people, and the help.

Owners and Admins use the same page or Immich's service page, and see
everyone's imports on Immich's page. A person who isn't allowed sees who
to ask, as single sign-on's "no access" page does. One import runs per
server at a time; a second is refused with whose is running and about how
long it has left (a queue later, if wanted).

*Considered:* the page on the sign-in site (`login.<domain>/…`, signed in
by single sign-on): apart from the control panel, but nothing for a lab
without single sign-on, and it puts a page that starts work on the
server's drives on a site that may face the internet; the same pages could
be served there later. An app of its own behind single sign-on's wall:
another address and certificate, for nothing more. For where they may read from: drives
and shares only, never the server's folders (no way for an Owner to offer
a folder they'd filled); anything an Admin can use (people could browse
the server's folders and media).

## 7. Checked before and after, and the summary

**Before (Roostack's own, in Go, no dependency):** reading each zip's
directory (its last few KB, not the whole file) gives, in seconds:

- the parts, by Takeout's naming, and **any missing** ("part 8 of 9
  isn't here");
- photos and videos by kind, sidecars, albums, size unpacked, and the
  date range;
- folders immich-go will leave out (trash, the Locked Folder if any);
- the **space** it needs on the library's disk: the photos' size plus 10%
  for thumbnails and converted videos (same disk, `location: media`),
  against what's free. Not enough refuses, with the numbers.

**Not immich-go's dry run (changed while building, 2026-10-21).** The
proposal ran `immich-go --dry-run` here. But its dry run reads every photo in full, to compare
its checksum with Immich's (`app/upload/advice.go` at v0.32.0), so on a
400 GB export it takes as long as reading all of it: an hour or more from
a USB drive, longer from a NAS. The check is Roostack's quick look, the
key's account and its counts, and the space instead: seconds. What Immich
already has is counted as the migration goes, and said in the summary.
Considered: the dry run as a second, optional "thorough check".

**During:** immich-go's JSON log (`--log-type JSON`) is read for the
progress line and kept as the log. Its format isn't documented as stable,
so the pinned version's lines are a test fixture, and a line Roostack
doesn't recognise is shown as it is rather than dropped.

**After (Roostack's own again):** the account's counts from Immich
(`/api/assets/statistics`, photos and videos) before and after, and the
library's size before and after. The summary:

- "Brought in 48,301 photos and 3,140 videos into Anna's account, in 6 h
  12 min. 214 albums."
- "Already there, skipped: 8,912."
- "Left out: 1,203 in Google's trash; 14 Immich can't read (names under
  Details)."
- "Didn't come: 3 photos whose sidecar couldn't be matched" (they come
  without Google's date; names listed).
- "Uses 397 GB on <disk>; 803 GB free."
- What happens next: "Immich is now making thumbnails and recognising
  faces. With machine learning on, that takes a day or two for a library
  this size; photos appear in search as it goes."
- If Roostack's count and immich-go's disagree, it says so, rather than
  trusting either.

The summary and the log are kept (the last import per service, in the
store, like a backup's result), shown on Immich's page and in History.

## 8. What's kept

| What | How | Checked |
|---|---|---|
| Date taken | From the sidecar, set after upload (UTC); a file's own date otherwise | Dry run reports photos without a date; VM: a photo whose EXIF date differs from the sidecar's shows the sidecar's |
| Time zone | Takeout has none; the file's own offset if it has one, else the place's time zone if Immich derives it, else UTC | **Unverified:** what Immich shows for a sidecar-only date; the VM test says, and the help says it plainly |
| Place | Sidecar (`geoDataExif`, else `geoData`), 0,0 ignored; a photo without one takes its album's place | VM: a photo with a sidecar-only place shows on the map |
| Albums | One Immich album per album folder, with the album's title; untitled albums left out by default | Counts before and after |
| Descriptions | From the sidecar | VM |
| Favourites | From the sidecar | VM |
| Archived | Immich's archive | VM |
| Trash | Left out (a choice to bring them, archived) | Counted |
| Locked Folder | Left out and counted, if Takeout has it (**unverified**; immich-go doesn't know it, so it would otherwise land in the timeline). Later: into Immich's Locked Folder | VM, with a synthetic one |
| People | Names as tags `People/<name>`. Immich finds faces itself; names can't be put on its faces | VM |
| Edited copies | Both kept, the edit stacked on the original | VM |
| Bursts, RAW + JPEG, Live Photos | Stacked; a Live Photo's two halves joined | VM |
| Partner's photos | **Left out** by default (immich-go brings them): in a family lab the partner brings their own, and Immich's partner sharing shows them | Counted |
| Duplicates | Skipped by checksum, including photos the phone app already backed up. Photos kept in Google's "Storage saver" quality aren't the same file as the phone's, so both stay; Immich's Utilities → Review duplicates finds them | Counted |
| Comments, shared albums' other people | Not in Takeout's photos part | Said in the help |

## 9. Safety

- **The source is never changed:** it's mounted read-only, and
  immich-go's delete options aren't used.
- **The check first:** "Migrate them" is offered only after a check of
  the same source and account (a new check if either changed).
- **Space:** refused if the disk can't hold it, before anything is
  uploaded; the run stops by itself if free space falls under 5%.
- **One import at a time per server,** shown on Immich's page and on Home.
- **Stopped or interrupted** (Stop, Roostack restarting, a power cut): the
  photos already in stay; **Carry on** runs it again, skipping them by
  checksum, and re-applies albums. The page says what was done. Roostack's
  maintenance coordinator **doesn't reboot** for OS updates while an import
  runs (it waits, as it waits for a backup).
- **Backups:** the photos are Immich's library (`critical`), so the next
  backup carries them; the check says how much that adds and whether the
  backup's place has room.
- **Undo:** everything brought in is tagged `takeout-<when Google made it>` (immich-go's tag, e.g. `takeout-20261019T091234Z`), so Immich can
  show (and the person can delete) exactly that import. Roostack doesn't
  offer a one-click undo: deleting photos is the person's call, in Immich.
- **Secrets:** Roostack never asks for an Immich password. The pasted
  key is a Podman secret for the import's life, forgotten at the end (the
  summary links to deleting it in Immich), and never logged or put on a
  command line.

## 10. A first version, and what follows

**First version:**

- Google Takeout (zip) and a plain folder (with XMP sidecars, a folder per
  album as a choice), the plain folder costing little once the rest
  exists.
- From a drive, a share or a folder on the server.
- Into one Immich account per import, with a key the person makes.
- For Owners and Admins, and for everyone else the Owner allows (§6.1).
- The pre-check, dry run, import, progress, summary and help page above.
- The Immich package unchanged (no format change, so nothing for the
  package editor): Roostack recognises the `immich` package and reaches
  its `server` container by the route's port.

**Straight after it: Apple (decided 2026-10-21, the Owner: a fast-follow).**

- **The export:** privacy.apple.com → *Request a copy of your data* →
  *iCloud Photos* → a size for each part → wait (Apple says it can take a
  few days) → download every part within 14 days, onto a drive or NAS, as
  with Takeout. The guide says plainly that it **doesn't work with
  Advanced Data Protection on** (Apple leaves end-to-end-encrypted photos
  out): turn it off for the export, or use a Mac (below).
- **What's in it:** the originals, `Albums/<name>.csv` and
  `Photo Details.csv` (each photo's original date, favourite, hidden and
  deleted flags). immich-go `from-icloud` reads it, using the albums and
  dates; whether it reads the zip parts in place, as it does Takeout's, is
  **unverified** (else they're unpacked onto the same drive first, and the
  space check counts it).
- **What Roostack adds:** after immich-go, Roostack applies the flags it
  ignores, through Immich's API with the same key (`asset.update`):
  favourites as favourites; *Recently deleted* left out, as Google's trash
  is; *Hidden* into Immich's Locked Folder, which needs the person to have
  set Immich's PIN (**unverified**; else left out and counted, as Google's
  Locked Folder is).
- **From a Mac:** Photos' *Export Unmodified Original* with *Export IPTC
  as XMP*, or osxphotos, into a folder with XMP sidecars: the plain-folder
  import, with a guide of its own. Keeps dates, places, descriptions and
  keywords; albums only as folders.

**Then, each its own piece:**

1. An upload share on the home network (Samba).
2. Google's Locked Folder into Immich's Locked Folder, and the trash into
   Immich's trash.
3. Flickr, Facebook and Instagram, whose dates live only in JSON (a
   converter to XMP, then the plain folder).
4. A cloud drive as the source (Takeout delivered to Dropbox, OneDrive or
   Box).
5. PhotoPrism's YAML sidecars and albums.
6. The import pages on the sign-in site too (§6.1), and a queue for
   imports.

## 11. New dependencies (agreed 2026-10-21, the Owner)

- **immich-go v0.32.0** (AGPL-3.0, Go), built from source on the server in
  `build/immich-go/Containerfile`, pinned by version, checked by Go's
  checksum database; listed in THIRD_PARTY.md. It runs as its own program
  in its own container and isn't linked into Roostack, so Roostack's
  Apache-2.0 licence isn't affected; its source is public, unchanged.
  Moved to its next release before this ships.
- **The builder's base image,** pinned by digest: Caddy's builder if its Go
  is 1.26 or newer, else the official `golang` image. The final image holds
  only the binary.
- Nothing else: the pre-check and summary are Roostack's Go (the standard
  library's `archive/zip`).

## 12. Decisions, and what's still open

**Decided (2026-10-21), the Owner:** "In general I agree with everything",
except whose photos:

1. **The tool:** immich-go, built from source at a pinned version.
2. **Its version:** v0.32.0 now; its next release (with this week's Live
   Photo and resume fixes) before this ships.
3. **A Roostack feature for Immich,** not a package-format feature ("It's
   explicit to Immich"). Considered: generic "long actions" in the package
   format (a tool container that runs on demand, an action that runs in
   the background with a folder input), with Roostack showing only a log.
4. **Whose photos:** the person makes a key in Immich and pastes it, with
   a link straight to Immich's page (§6). Roostack never signs in to
   Immich with a password. *Changed from the proposal,* which offered
   signing in as the person too.
5. **The scope:** Takeout and a plain folder, from a drive, share or server
   folder; **Apple's export a fast-follow** (§10).
6. **The defaults:** trash left out, partner's photos left out, untitled
   albums left out, people as tags, archived as archived, everything tagged
   `takeout-<when Google made it>` (immich-go's tag, e.g. `takeout-20261019T091234Z`), Immich's jobs not paused.
7. **The upload share (Samba):** later, as its own piece.
8. **People other than Owners and Admins** can bring in their own photos
   from the first version, if the Owner allows it.

9. **Where others do it** (the same day): a page of its own on Roostack's
   site, `/immich-photos`, linked from the sign-in page too; open by
   default to everyone who uses Immich; the Owner chooses where they may
   read from when opening it (§6.1).
10. **The mock-ups** ([docs/mockups/immich-import](mockups/immich-import/README.md),
    the same day): "Migrate", not "bring in"; additional locations as a
    list with Add a location, not a tickbox; the forms' design (docs/DESIGN.md
    "Forms") throughout.
11. **On the reworked mock-ups** (the same day): at the top, but not as a
    large card (a slim call to action proposed); the Owner's choices in the card; the guide
    as a step, with the slow-download warning; what to migrate as a step of
    its own before the check; a second migration refused; "Other
    locations".

**Still open:** "Sign in with single sign-on" on Roostack's own login page
(SPEC §5.2, later) would let the sign-in page's link open the import page
without a second sign-in. It's its own piece of work, not part of this.

## 13. The test

On VM 1 (`ROOSTACK_VM=1 scripts/dev-vm.sh`), destroyed afterwards. A
**synthetic Takeout**, made by a script in `docs/immich-import-test/`
(never real photos): generated JPEGs, HEICs and short videos with and
without EXIF dates and GPS; sidecars in both naming styles, with
truncated names, "(1)" duplicates, edited copies, a Live Photo pair split
across two zips, album folders with `metadata.json` in English and German,
trash, archive, a partner's photo, an untitled album, a Locked Folder;
split into several zips, one left out to test the missing-part check, and
a large run for memory. Then: a key with permissions missing, and one with
all of them; the check, the dry run, the import, a stop and carry-on, a
second run (nothing new), and Immich's own pages and API to confirm every
row of §8. As an Owner and as a Member (the Owner's switch both ways, a
backup drive refused to the Member), desktop and phone width. Apple's
export, when it's built, the same way with a synthetic privacy export.

## Sources (read 2026-10-21)

- Immich 3.3.4: its API description (`open-api/immich-openapi-specs.json`),
  `server/src/services/metadata.service.ts` (how dates are chosen),
  `packages/cli/src/commands/asset.ts`, and the docs on the CLI, XMP
  sidecars and libraries, at the `v3.3.4` tag of
  https://github.com/immich-app/immich; the roadmap, https://immich.app/roadmap.
- immich-go: https://github.com/simulot/immich-go (README, `docs/`,
  `adapters/googlePhotos`, `.goreleaser.yaml`, issues #1384 to #1473).
- Google Takeout: https://support.google.com/accounts/answer/3024190;
  transfers: https://support.google.com/accounts/answer/9666875; the
  Photos APIs: https://developers.google.com/photos/support/updates and
  https://developers.google.com/photos/picker/guides/media-items.
- Apple: https://support.apple.com/en-us/102208 (a copy of your data),
  https://support.apple.com/en-us/118257 (transfer to Google Photos).
- GPTH "Neo": https://github.com/Xentraxx/GooglePhotosTakeoutHelper_Neo;
  osxphotos: https://github.com/RhetTbull/osxphotos; icloudpd:
  https://github.com/icloud-photos-downloader/icloud_photos_downloader.
- Dropbox's limits: https://help.dropbox.com/desktop-web/download-entire-folders;
  PhotoPrism's export: https://docs.photoprism.app/user-guide/backups/export/.
