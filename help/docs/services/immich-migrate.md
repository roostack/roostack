# Migrating photos into Immich

Roostack can bring your photos and videos into Immich from **Google
Photos** or **a folder of photos**, with their dates, places, albums,
descriptions and favourites. Apple iCloud Photos comes next.

Each person migrates into **their own Immich account**, with a key they
make in Immich. Roostack never asks for an Immich password.

## Where to start

- **Owners and Admins:** on Immich's page, **Migrate photos…** (at the
  top until someone has migrated photos), or **Migrate photos from…** on
  its **Migrate photos** card.
- **Everyone else the Owner allows:** **Migrate your photos into it** on
  your dashboard, beside Immich, or the link on the sign-in page. It's a
  page of its own, at your lab's address followed by `/immich-photos`.

The steps keep your place. You can close the page at any point, even for
days while you wait for Google, and carry on where you left off.

## Getting your photos from Google {#google}

Google gives you everything in Google Photos as a **Takeout**: a few large
zip files.

1. Open [takeout.google.com](https://takeout.google.com), signed in to the
   Google account whose photos these are.
2. Click **Deselect all**, then tick only **Google Photos**. Leave "All
   photo albums included" as it is.
3. Click **Next step** and choose **Send download link via email**,
   **Export once**, file type **.zip** and size **50 GB**. Then
   **Create export**.
4. Google emails you when it's ready. **Download every part** within 7
   days: Google lets you download each one 5 times.
5. Copy all the parts into **one folder** on a USB drive or your NAS.
   **Don't unzip them**: Roostack reads them as they are.

!!! warning "This can be very slow"
    Google can take a day or more to make the files, and downloading
    hundreds of GB from Google is often slow and sometimes stops part-way.
    It may take you several days. Roostack keeps your place meanwhile.

You need about as much room as Google Photos says you use, plus a tenth,
on the drive or NAS, and again on the disk Immich keeps its photos on.
Your Google account isn't changed: your photos stay in Google Photos until
you delete them there.

## Where your export can be

- **A USB drive** plugged into the server Immich runs on, or a second disk.
- **A network share** (SMB or NFS), with a login to it.
- **Other locations** the Owner has set up for everyone (a folder on the
  family NAS, say).
- **A folder on the server** (Owners and Admins only).

Roostack mounts it **read-only**: nothing in your export can change. People
who aren't Owners or Admins can't use the lab's backup drive, the server's
own disk, or a drive holding the lab's media libraries; the Owner chooses
whether they can use drives and shares at all.

## Your Immich key

The photos go into the account the key belongs to, so make it **signed in
to Immich as yourself**.

1. **Make a key in Immich** on the page opens Immich's settings at
   **API keys**.
2. Click **New API key**, name it `Roostack migration`, and tick the
   permissions the page lists: `asset.read`, `asset.statistics`,
   `asset.update`, `asset.upload`, `asset.copy`, `asset.delete`,
   `asset.download`, `album.create`, `album.read`, `albumAsset.create`,
   `server.about`, `stack.create`, `tag.asset`, `tag.create` and
   `user.read`.
3. **Create**, copy the key, and paste it into the page.

Roostack asks Immich whose key it is and what it may do, and names any
permission missing: edit the key in Immich to tick it, and check again.
Roostack keeps the key only until the migration is done, then forgets it;
delete it in Immich afterwards if you like.

## What comes

| | |
|---|---|
| **Dates** | When each photo was taken, from Google's sidecar files (in UTC). |
| **Places** | From the sidecars, which keep places Google added that aren't in the file. |
| **Albums** | One Immich album per Google album. |
| **Descriptions, favourites, archived photos** | As they were in Google Photos. |
| **People** | Google's names, as tags (`People/Anna`). Immich finds faces itself. |
| **Edited copies, bursts, RAW with JPEG** | Kept, stacked behind one photo. |
| **Photos Immich has already** | Skipped (from the phone app, say). |

Left out unless you choose otherwise: **Google's trash**, **photos a
partner shared with you** (they can migrate their own), and **albums
without a name**.

Photos kept in Google's "Storage saver" quality aren't the same files as
your phone's originals, so both stay; Immich's **Utilities → Review
duplicates** finds them afterwards.

## The check, and migrating

Before anything is sent, the check looks in your export and at Immich:

- every part of the Takeout is there (a gap in the numbers means one is
  missing; Google's email says how many there should be);
- there's room on Immich's disk;
- how many photos and videos, from when, and in how many albums;
- how much larger your next backup will be.

**Migrate them** starts it. It runs on the server, so you can close the
page; you're emailed when it's done. Immich shows the photos as they
arrive. One migration runs on a server at a time.

**Stop** stops it. What's in Immich stays there: **Carry on** runs it
again and skips what's in, so nothing comes twice. Carrying on reads the
whole export again to check each photo, so for a large one it takes a
while before new photos arrive.

If the server restarts (a power cut, say), the migration carries on by
itself once the server is back and the export is there; if the drive
isn't plugged in, the page waits for **Carry on**.

### Large libraries and memory {#memory}

While it runs, the migration tool keeps a list of every photo in the
export and in the account, so a large library needs more memory. It may
use half the server's memory, which is enough for a few hundred thousand
photos on a server with 4 GB. If a library needs more, it stops and says
it ran out of memory: what came stays in Immich. The Owner can give
migrations three quarters of the server's memory (**Memory for
migrations** on Immich's **Migrate photos** card), then **Carry on**.
While a migration runs, the memory it uses isn't there for other
services; if the server runs short, the migration is stopped first.

The summary at the end says what came, what was already there, what was
left out and why, and which files couldn't be migrated. Immich then makes
thumbnails and finds faces, which can take a day or two for a big library.

## For the Owner

On Immich's page, the **Migrate photos** card:

- **Who can migrate photos**: *Everyone who uses Immich* (with single
  sign-on on, those who can sign in to it), or *Owners and Admins*.
- **Where they can migrate photos from**: USB drives they plug in, and
  network shares with their own login.
- **Memory for migrations**: half the server's memory (the default), or
  three quarters for a very large library ([Large libraries and
  memory](#memory)).
- **Additional locations**: folders you set up for everyone, on a drive,
  the server or a share with your login. Everyone who can migrate sees
  what's in them, so a folder per person keeps them apart. They're listed
  on **Settings → Media libraries** too, as kept for migrating photos; no
  service reads them.

You're emailed when someone starts and ends a migration, and you can stop
or carry on anyone's from the card.

## What runs it

Roostack uses [immich-go](https://github.com/simulot/immich-go)
(AGPL-3.0), built from its source on the server the first time it's
needed, in a container of its own on Immich's network. It reads the
export read-only and talks to Immich only through its API, with your key.
Its log is under **Details**.
