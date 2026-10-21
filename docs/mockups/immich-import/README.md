# Migrating photos into Immich: mock-ups (2026-10-21)

For SPEC §8.5 and docs/IMMICH-IMPORT.md, before building:

- Immich's page, with the card and the Owner's choices;
- the steps for Owners and Admins;
- `/immich-photos`, the page of its own for everyone else the Owner allows;
- the sign-in page's link to it.

They're static pages on the real `tokens.css`, theme and `app.css`. They use today's sidebar and components, and follow the forms' design (docs/DESIGN.md "Forms"):

- segmented controls for small exclusive choices, with the chosen one's fields under it;
- switches for on/off;
- sections under small headings with rules between them;
- adding one thing in a dialog;
- an input with its button after it;
- one form with one Save for settings.

Also: steps of a page, drive cards, folder choice, check results, figures, activity, progress, copy box. `mock.css` holds only what the real stylesheet lacks; each part of it would become a component in the gallery (below).

The lab is the other mock-ups' Smiths' lab (`smiths.roost.ac`, server `nuc`, Owner sam). Anna migrates through the Owner's pages, and Ella, a Member, through `/immich-photos`. The data is made up, at the scale of a real Google Photos library.

**Open them** through a web server at the repo root:

```sh
python3 -m http.server 8792 --bind 127.0.0.1
```

Then open <http://127.0.0.1:8792/docs/mockups/immich-import/service.html>. The bar at the bottom right switches page, the page's state (**Next →** walks through them), and light or dark. Choices go into the address (`?s=check&m=dark`). They were generated with a script that isn't kept: edit the HTML.

## The wording (the Owner, 2026-10-21)

**Migrate**, not "bring in":

- the card is **Migrate photos**, with **Migrate photos from…**;
- the page is **Migrate photos into Immich** (for others, "Migrate your photos into Immich");
- the last steps are **Migrate them**, then *Migrating them* and *Migrated*.

It keeps it apart from §8.4's "Bring in data from elsewhere…", which moves an existing Immich.

## The pages

### `service.html`: Immich's page

A card, **Migrate photos**, comes first. It's what a new Immich is for, so the other cards follow it (shown as one muted card). The card holds:

- A line on what it does, and **Migrate photos from…**.
- *A migration running:* its progress, with **Watch**, and a list of migrations (whose, from, when, result).
- One settings form with one **Save**:
  - **Who can migrate photos:** a segmented control, *Everyone who uses Immich* (the default) or *Owners and Admins*, with the chosen one's note under it. The rest of the form shows only under *Everyone who uses Immich*.
  - **Where they can migrate photos from:** two switches, *USB drives they plug into nuc* and *Network shares, with their own login*, both on.
  - **Additional locations:** folders the Owner sets up, each a folder value with **Remove**. With none yet, a dashed box says: "You haven't defined any additional locations that people can migrate photos from. Press **Add a location** below to add one." There's no tickbox: the list is what's offered, so an empty list offers nothing (the Owner, 2026-10-21, on the first mock-up's "Places you name" tickbox, whose list wasn't clearly part of it).
  - **Add a location** opens a dialog:
    - Drive, Folder on nuc or Network share, as a segmented control: drive cards and a folder, a folder, or the share's fields with the Owner's login;
    - then its name for people, and **Check and add**.
    
    It's added (or removed) at once, like the other "add one thing" dialogs, not by Save.
  - **Send them here:** `/immich-photos`'s address in a copy box.

### `import.html`: the steps, for Owners and Admins

One page of numbered steps, as §8.4's import page has: a step that's done shrinks to a line with **Change**. Once the migration has started, there's no Change. Help is beside the steps.

1. **Where are your photos now?** A segmented control: Google Photos, A folder of photos, Apple iCloud (next, greyed). The chosen one's note says what comes.
2. **Get your photos from Google:** the Takeout guide, five steps with the exact settings (Deselect all, Google Photos, email link, once, zip, 50 GB). It ends with how long Google takes, the 7 days and 5 downloads, and to copy the parts unzipped into one folder. **I have the zip files** moves on.
3. **Where are the zip files?** Drive, folder on nuc or network share, as §8.4's step 1. The backups' drive is greyed. The folder shows how many zips and their size.
4. **Your Immich key:**
   - **Make a key in Immich** opens `/user-settings?isOpen=api-keys` in a new tab.
   - The name to give it (`Roostack migration`), and the 15 permissions to tick (folded).
   - The key's box with **Check the key** after it.
   - *Missing permissions:* whose account it is (✓), then which are missing by name, with how to add them without making a new key.
5. **Check:**
   - Four figures: photos with their date range, videos, albums, size.
   - The results:
     - all parts here;
     - room on the disk;
     - already in the account (skipped);
     - left out and why;
     - files that can't be migrated;
     - the next backup's size.
   - **What to migrate: as Roostack suggests** (folded) holds the defaults as switches: trash, partner's photos, albums without a name, people as tags, stacks.
   - Then **Migrate them**, with how long it will take.
   - *Problems:* a missing part (by its file name) and not enough room, each saying what to do.
6. **Migrating them:** the activity component, with progress (count, size, time left), **Stop** and the log under Details. You can leave the page.
   - *Stopped:* **Carry on**, which skips what's in, or **Start again from the check**.
   - *Done:* the summary as the last step, **Migrated**:
     - Immich's own count against Roostack's;
     - skipped, left out, couldn't be migrated (with **see which**);
     - the space used;
     - what Immich does next, and the `takeout/<date>` tag;
     - that the key is forgotten, with a link to delete it in Immich;
     - **Open Immich** and **Migrate more photos…**.

### `immich-photos.html`: the page of its own

The lab's name and logo, who's signed in, and nothing of the control panel. It has the same steps, with these differences:

- **Step 3** offers only what the Owner allowed:
  - *Your drive*: the backups' and the media library's drives greyed, with why;
  - *Network share*, with their own login;
  - *Sam's locations*: the additional locations.
- **The key** is "yours, when you make it signed in to Immich as yourself".
- **Three states before the steps:**
  - *Which Immich*, when the lab runs two;
  - *Not allowed*, with who to ask;
  - *Someone else's migration running*, with whose and how long, and the steps kept for later.

### `signin.html`: the sign-in page, signed in

Under the services they use, **Migrate your photos into Immich**. It's shown only to people allowed, when the lab runs Immich, and says they'll sign in to Roostack again with the same account. Single sign-on to Roostack's own pages is a separate piece of work.

## To decide from these

1. **The card first on Immich's page** (shown), or among the other cards, or as an action. Recommended: first. It's what a new Immich is for, and people who've migrated their photos come back to migrate more.
2. **The Owner's choices inside the card** (shown), or in Immich's Settings. Recommended: in the card, where the Owner sees who can use it.
3. **The Takeout guide as a step** (shown, skipped with "I have the zip files"), or only in the help. Recommended: a step. It's the part people get wrong (tgz, unzipping, missing a part).
4. **The defaults folded away** under "What to migrate: as Roostack suggests" (shown), or shown as switches. Recommended: folded.
5. **A second migration while one runs:** refused, saying whose is running and for how long (shown), or queued. Recommended: refused at first.
6. **The additional locations shown to others as "Sam's locations"** (shown), or "Other locations". Recommended: the Owner's name, so people know who set them up.

## New components (for the gallery when built)

- **Permissions:** a compact list of code names in columns.
- **An empty list's note:** a dashed box saying there's nothing yet and how to add one.
- **A page of its own:** the bare layout, with the lab's name and logo and who's signed in.
- **A link under the sign-in page's services** (dashed, with an icon).
