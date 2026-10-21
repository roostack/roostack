# Bringing photos into Immich: mock-ups (2026-10-21)

For SPEC §8.5 and docs/IMMICH-IMPORT.md, before building:

- Immich's page, with the card and the Owner's choices;
- the steps for Owners and Admins;
- `/immich-photos`, the page of its own for everyone else the Owner allows;
- the sign-in page's link to it.

They're static pages on the real `tokens.css`, theme and `app.css`. They use today's sidebar and components: steps of a page, segmented control, drive cards, folder choice, check results, figures, activity, progress, switches, copy box. `mock.css` holds only what the real stylesheet lacks; each part of it would become a component in the gallery (below).

The lab is the other mock-ups' Smiths' lab (`smiths.roost.ac`, server `nuc`, Owner sam). Anna imports through the Owner's pages, and Ella, a Member, through `/immich-photos`. The data is made up, at the scale of a real Google Photos library.

**Open them** through a web server at the repo root:

```sh
python3 -m http.server 8792 --bind 127.0.0.1
```

Then open <http://127.0.0.1:8792/docs/mockups/immich-import/service.html>. The bar at the bottom right switches page, the page's state (**Next →** walks through them), and light or dark. Choices go into the address (`?s=check&m=dark`). They were generated with a script that isn't kept: edit the HTML.

## The pages

### `service.html`: Immich's page

A card, **Bring in photos**, comes first. It's what a new Immich is for, so the other cards follow it (shown as one muted card). The card holds:

- A line on what it does, and **Bring in photos from…**.
- *An import running:* its progress, with **Watch**, and a list of imports (whose, from, when, result).
- **Who can bring in photos:** *Everyone who uses Immich* (the default) or *Owners and Admins*.
- **Where they can bring photos from:**
  - *USB drives they plug into nuc*, ticked;
  - *A network share, with their own login*, ticked;
  - *Places you name*, with **Add a place…** (the folder picker, or the share form with the Owner's login), each with Remove.
- **Send them here:** `/immich-photos`'s address in a copy box.

### `import.html`: the steps, for Owners and Admins

One page of numbered steps, as §8.4's import page has: a step that's done shrinks to a line with **Change**. Once the import has started, there's no Change. Help is beside the steps.

1. **Where are your photos now?** Google Photos, a folder of photos, or Apple iCloud (greyed, "Coming next"). Each says what comes.
2. **Get your photos from Google:** the Takeout guide, five steps with the exact settings (Deselect all, Google Photos, email link, once, zip, 50 GB). It ends with how long Google takes, the 7 days and 5 downloads, and to copy the parts unzipped into one folder. **I have the zip files** moves on.
3. **Where are the zip files?** Drive, folder on nuc or network share, as §8.4's step 1. The backups' drive is greyed. The folder shows how many zips and their size.
4. **Your Immich key:**
   - **Make a key in Immich** opens `/user-settings?isOpen=api-keys` in a new tab.
   - The name to give it, and the 15 permissions to tick (folded).
   - A box to paste the key, and **Check the key**.
   - *Missing permissions:* whose account it is (✓), then which are missing by name, with how to add them without making a new key.
5. **Check:**
   - Four figures: photos with their date range, videos, albums, size.
   - The results:
     - all parts here;
     - room on the disk;
     - already in the account (skipped);
     - left out and why;
     - files that can't be brought;
     - the next backup's size.
   - **What to bring: as Roostack suggests** (folded) holds the defaults as switches: trash, partner's photos, albums without a name, people as tags, stacks.
   - Then **Bring them in**, with how long it will take.
   - *Problems:* a missing part (by its file name) and not enough room, each saying what to do.
6. **Bringing them in:** the activity component, with progress (count, size, time left), **Stop** and the log under Details. You can leave the page.
   - *Stopped:* **Carry on**, which skips what's in, or **Start again from the check**.
   - *Done:* the summary as the last step:
     - Immich's own count against Roostack's;
     - skipped, left out, couldn't be brought (with **see which**);
     - the space used;
     - what Immich does next, and the `takeout/<date>` tag;
     - that the key is forgotten, with a link to delete it in Immich;
     - **Open Immich** and **Bring in more photos…**.

### `immich-photos.html`: the page of its own

The lab's name and logo, who's signed in, and nothing of the control panel. It has the same steps, with these differences:

- **Step 3** offers only what the Owner allowed:
  - *Your drive*: the backups' and the media library's drives greyed, with why;
  - *Network share*, with their own login;
  - *Sam's places*: the places the Owner named.
- **The key** is "yours, when you make it signed in to Immich as yourself".
- **Three states before the steps:**
  - *Which Immich*, when the lab runs two;
  - *Not allowed*, with who to ask;
  - *Someone else's import running*, with whose and how long, and the steps kept for later.

### `signin.html`: the sign-in page, signed in

Under the services they use, **Bring your photos into Immich**. It's shown only to people allowed, when the lab runs Immich, and says they'll sign in to Roostack again with the same account. Single sign-on to Roostack's own pages is a separate piece of work.

## To decide from these

1. **The card first on Immich's page** (shown), or among the other cards, or as an action. Recommended: first. It's what a new Immich is for, and people who've brought their photos in come back to bring more.
2. **The Owner's choices inside the card** (shown), or in Immich's Settings. Recommended: in the card, where the Owner sees who can use it.
3. **The Takeout guide as a step** (shown, skipped with "I have the zip files"), or only in the help. Recommended: a step. It's the part people get wrong (tgz, unzipping, missing a part).
4. **The defaults folded away** under "What to bring: as Roostack suggests" (shown), or shown as switches. Recommended: folded.
5. **A second import while one runs:** refused, saying whose is running and for how long (shown), or queued. Recommended: refused at first.
6. **Places named by the Owner shown to others as "Sam's places"** (shown), or "Places for photos". Recommended: the Owner's name, so people know who set them up.

## New components (for the gallery when built)

- **A choice with what it gives:** bordered radio rows, each with a line on what comes. A choice that's coming later is greyed, with a badge.
- **Permissions:** a compact list of code names in columns.
- **A page of its own:** the bare layout, with the lab's name and logo and who's signed in.
- **A link under the sign-in page's services** (dashed, with an icon).
