# Add a media library: mock-ups (2026-10-19)

For SPEC §8.1 "Media libraries", before building. The Owner (2026-10-19): the "Or a drive / Or a folder / Or a network share" page is confusing; choose where it is with a button group, browse folders in a dialog rather than a page per folder, and keep the rest on one form.

They're made from the page as VM3 serves it (the real `tokens.css`, themes and `app.css`; its drives MEDIA and ROOSTACK), with the folders as the agent lists them (`agent.FolderList`: each folder's folder and file counts, and the readability check of the folder). `mock.css` holds only what the real stylesheet lacks (the segmented control, the folder picker, fields side by side); `mock.js` the mock folders and the switch at the bottom right. They work: switch the control, open folders, choose one.

**Open them** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/media-add/a-dialog.html>. `?m=dark` for dark mode; `?w=folder` or `?w=share` picks the control's choice; `?open=1` opens the dialog; `?chosen=1` (A) has Films chosen.

## The directions

**Chosen: A** (the Owner, 2026-10-19), and built (docs/DESIGN.md "Forms").

Both share the top: **a segmented control** ("Where is it?": Drive, Folder on <server>, Network share), each choice with a one-line hint and only its own fields under it; then the readability check, then a rule, the name and the services, and one button ("Add library", or "Check and add the share"). A network share is a smaller segmented control (SMB or NFS) and its fields two to a row (server and share; username and password). On phones the control's three choices are equal columns, icon above the words.

- **A, Dialog** (recommended): the drive's cards as radios (the backup drive greyed out with why), then a **Folder** row: the folder chosen (the whole drive to start with, with its counts) and **Choose a folder…**, which opens the **folder picker**: a dialog with the folders opened so far as crumbs, the folders here (each with its counts, opening on a click), what this one holds, Cancel and **Use <folder>**. The check of the folder chosen shows under the row, with "Let services read them as <owner>" for a drive. For a folder on the server the row starts as "No folder chosen yet" and the dialog starts at `/mnt`, `/media` and `/srv`. On phones the dialog fills the screen.
  - *Why recommended:* choosing the folder is one step with a clear end (Use), the form stays short and readable around it, and the chosen folder reads as a value ("MEDIA › Films, 2 folders, 1 file") with Change beside it. It's what the Owner asked for.
- **B, In the card**: the same folder list sits in the card, and the folder you're in is the library (like a "Save as" box). One click fewer, nothing to open or close; but the card grows and shrinks as you go in and out, the form's lower half moves, and "the folder you're in is the one" is easy to miss.
- **Today**: the page as it is now, for comparison.

## Without JavaScript

The segmented control is radio buttons, and which fields show is CSS (`:has(:checked)`), so it works without scripts. The folder picker needs scripts; without them **Choose a folder…** is a link to today's page-per-folder browsing (each folder a page, "Use <folder>" back to the form), which stays as the fallback.
