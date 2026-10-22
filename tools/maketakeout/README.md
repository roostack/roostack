# A synthetic Google Photos Takeout

`maketakeout` writes a made-up Google Photos Takeout, with generated
pictures and never real photos, for testing the photo migration into Immich
on a VM ([IMMICH-IMPORT.md](../../docs/IMMICH-IMPORT.md) §2 "Google Photos" and §13
"The test"). Beside the zips it writes `manifest.json`, which says exactly
what's in them and what Immich should show afterwards, so a test can compare
the two through Immich's API.

```sh
go run ./tools/maketakeout -out /tmp/takeout -photos 300 -split 5MB -seed 1
go test ./tools/maketakeout
```

| Flag | Default | |
|---|---|---|
| `-out DIR` | (required) | Where the zips and `manifest.json` go. Parts from an earlier run with the same name are removed first. |
| `-photos N` | 300 | Ordinary photos, besides about 15 special cases that are always there. |
| `-split SIZE` | 5MB | Largest size of one zip (compressed, decimal units, approximate). |
| `-seed N` | 1 | The same seed and flags always give the same bytes. |
| `-skip-part N` | 0 | Leave part N out, to test the missing-part check. |
| `-lang en\|de` | en | The account's language: folder names and album metadata file. |
| `-big N` | 0 | N extra small photos (64×48) for a memory and size run, e.g. 20000 (about 5 seconds). |
| `-videos-from DIR` | none | `.mp4` and `.mov` files to copy in; the first `.mov` becomes a Live Photo's video half. |

## What's in it

The zips are named as Google names them (`takeout-20261019T091234Z-001.zip`,
`-002.zip`…) and hold `Takeout/archive_browser.html` and
`Takeout/Google Photos/` (`Google Fotos` in German), with:

- **Year folders** (`Photos from 2014` … `Photos from 2024`, `Fotos von …`),
  each photo followed by its sidecar. Half the sidecars use the old name
  (`IMG_6000.JPG.json`), half the current one
  (`….supplemental-metadata.json`), cut to 51 characters
  (`.supplemental-metada.json`, `.supplemental-meta.json`, `.supp.json`).
- **Photos:** 480×360 JPEGs, about half with EXIF written by hand (the date
  taken, and for some a GPS position), the rest with none; some PNG
  "screenshots". About a quarter of the EXIF dates differ from the sidecar's
  (a camera on local time, a wrong clock): Immich should show the sidecar's.
- **The traps:** two different `IMG_3479.JPG` in one folder
  (`IMG_3479(1).JPG` with `IMG_3479.JPG(1).json`), the same in the current
  style (`IMG_3480.JPG.supplemental-metadata(1).json`), a name over 47
  characters, an edited copy (`IMG_2041-edited.jpg`, `-bearbeitet`) sharing
  its original's sidecar, a photo with no sidecar, and one photo whose
  sidecar is forced into the next part.
- **Sidecars** with Google's fields: `title`, `description`, `imageViews`,
  `creationTime`, `photoTakenTime` (Unix seconds as strings, and
  "11 Jun 2014, 09:12:30 UTC"), `geoData` and `geoDataExif` (0.0 when there's
  no place), `people`, `url`, `googlePhotosOrigin` (`mobileUpload`,
  `webUpload`, or `fromPartnerSharing` for a partner's photo), and
  `favorited`, `archived`, `trashed` (left out when false, as Google does).
  Places cover every case: in the file and the sidecar, only in the sidecar
  (added in Google Photos), only in the file, and none.
- **Albums:** "Lisbon 2014", "Family", "Garden" and an untitled one
  (`Untitled(2)`, `Ohne Titel(2)`, empty title), each holding copies of its
  photos (same bytes, same sidecar) and `metadata.json` (`Metadaten.json`).
  "Garden" holds both `IMG_3479.JPG`, so the "(1)" happens there too.
- **Files that shouldn't become assets**, listed in `notImported`: a Pixel
  motion photo's `.MP` part (just bytes), a fake `.psd` and its sidecar, a
  sidecar with no photo, `print-subscriptions.json` and
  `shared_album_comments.json`.

Trashed and archived photos are only **flagged in their sidecars** inside
the year folders, which is what immich-go reads; there are no separate
Trash or Archive folders, and no Locked Folder.

**No videos or HEICs by default:** a real MP4 needs an encoder, and the
standard library has none. The VM test makes a few with the ffmpeg in
Immich's own server image, for example
`ffmpeg -f lavfi -i testsrc=duration=3:size=640x360:rate=25 -c:v libx264 -pix_fmt yuv420p clip1.mp4`
(and a `.mov` the same way), copies them out of the container and passes the
folder with `-videos-from`.

## The manifest

- `parts`: each zip's name, whether it was written, its entries and bytes.
- `photos`: one per asset Immich should have. `files` are its paths in the
  Takeout with their part (the year folder first, then album copies),
  `sidecars` the same; `sha1` (Immich's checksum), `dateTaken` (UTC, the
  date Immich should show) and `dateSource`, `exifDate` and
  `exifDateDiffers`, `gps` (or null) and `gpsSource`, `description`,
  `favourite`, `archived`, `trashed`, `partner`, `albums` (folder names),
  `people`; `editedOf` and `livePhotoOf` link to another photo's `id`;
  `noSidecar`, `inSkippedPart` and `splitAcrossParts` mark the hard cases.
- `albums`: folder, title, description and the photos' ids.
- `notImported`: files that should not become assets, and why.
- `totals`: counts of all the above, for a quick look.

Reading it against immich-go's defaults (§3): trashed photos stay out,
people become tags (`people/<name>`), a photo without a sidecar is left out
(`noSidecar`), and anything `inSkippedPart` is missing unless that part is
supplied. Compare positions to within 0.0001°.

## Assumed about Takeout's format

Synthetic data can only be as right as what we know. Unverified against a
real Takeout: the 47-character cut of media names (the base, before the
extension); the "(1)" after a cut sidecar stem (`….supp(1).json`); the
German names `Fotos von`, `Metadaten.json`, `Ohne Titel(2)` and the
`02.01.2006` date format; that `.supplemental-metadata` stays English; the
`.MP` file beside `….MP.jpg`; the contents of the two top-level JSON files;
and the zips' naming (newer exports may add a number, `-1-001.zip`).
