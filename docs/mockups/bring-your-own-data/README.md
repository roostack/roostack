# Bringing your own data: mock-ups (2026-10-20)

For SPEC §8.4 and docs/MIGRATION-PLAN.md §8, before building: the install page's "I'll bring its data", the page that brings data in (Immich, then Plex's libraries), a service's page while it waits for its data or for a drive, and undoing through the kept copies.

They're static pages on the real `tokens.css`, themes and `app.css`, with today's sidebar and components (segmented control, drive cards, folder choice, copy box, activity, install steps, table list, notices). `mock.css` holds only what the real stylesheet lacks; each part of it would become a component in the gallery (below). The lab is the SSO mock-ups' Smiths' lab (`smiths.roost.ac`, server `nuc`); the data is made up, at the rehearsal's scale or larger.

**Open them** through a web server at the repo root:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/bring-your-own-data/install.html>. The bar at the bottom right switches page, the page's state (with **Next →** to walk through them) and light or dark; choices go into the address (`?s=check&m=dark`).

## The pages

- **`install.html`: installing Immich.** A new section, *Its data*, is a segmented control: **Start afresh** or **I'll bring its data** (the Owner only). Bringing it hides the install-only settings (Immich's administrator comes with its data), the summary beside the form says Immich waits for its data, and the button reads "Install, then bring its data".
- **`service.html`: a service's page.**
  - *Immich waiting for its data:* a status pill "Waiting for its data" (plain, not a warning) and a notice with **Bring its data in…**; the address isn't a link yet.
  - *While copying:* the notice shows the copy's progress, linking to the import page.
  - *Plex waiting for a drive:* Films is shown at `/movies` and its drive is unplugged (ASSUMPTIONS W5). A warning notice says Plex can't see Films and that Roostack restarts Plex once the drive is back; Settings lists each library with where it is inside Plex.
  - *Plex running:* where **Bring in data from elsewhere…** sits for a service already running (in the Backups card, under Restore).
- **`import.html`: bringing Immich's data in,** one page of numbered steps. A step done shrinks to one line with **Change**; steps not reached yet are only their heading. Beside them, *On the old machine*: what the person does there, with the dump command filled in from the pasted setup.
  1. **Your files:** drive, folder on the server or network share, as when adding a media library, mounted read-only.
  2. **Your old setup (optional):** `docker inspect $(docker compose ps -aq)` pasted, or the Compose file and `.env`, or neither. Once read: the old containers and what each is to Immich, and **where the old machine's folders are now**, one line per prefix (`/opt/immich` → T7 Shield › immich), guessed from the folder names.
  3. **What goes where:** each of Immich's storage and databases, where it is inside Immich, and what it's brought from (the nested split shown: `library` without `thumbs` and `encoded-video`). Below, what the old setup had that isn't brought, and why.
  4. **Check:** a list, ✓ or what to do. *Ready* (sizes, room, the dump's Postgres version and extensions, the version, settings the package doesn't set) and *Problems* (unreadable files with the command to fix them, a pgvecto.rs dump, no room, an older version, old paths left in the dump, data there already).
  5. **Copy:** progress in the activity component (bytes, files, time left), then **Copy again**.
  6. **Switch to this data:** what to do first on the old machine (stop it and keep it stopped, take the dump again, bring the copy up to date), what Roostack will do, a tick that the old one is stopped, then the switch's steps; *Done* says what to check.
- **`import-plex.html`: Plex's step 3 and its check.** The config folder, then **Media it read**: each old media mount, the library holding its files (by a sample of names), and where it'll be inside Plex. Two states: `/movies` and `/tv` (each at its own path, with the note that Roostack restarts Plex when the drive is back) and `/data/movies` and `/data/tv` (the media folder at `/data`, which comes back by itself). Its check counts Plex's own unreadable files (`Preferences.xml`, W4) and the old paths in its database, matched or not.
- **`undo.html`: Restore Plex's kept copies,** where undo lives: "Before bringing data in" is listed with the restore's and move's copies, and each kept copy gains **Put back…** (today they only have Delete). Putting one back keeps the data it replaces as another kept copy.

## To decide from these

1. **One page of steps** (shown) or a page per step. One page: what was chosen stays in view, and Change goes back to any step. A page per step is calmer, but the check and the copy refer back to steps 1-3 all the time. Recommended: one page.
2. **Install-only settings when bringing data:** hidden (shown), since install hooks are skipped and the administrator comes with the data. Installing over data left from before shows them as optional instead ("filled in, its first-time setup runs again"). Recommended: hidden here.
3. **Where "Bring in data from elsewhere…" goes** on a running service: the plan says Manage, but a service's page has no Manage menu now. Shown in the Backups card, under Restore (it's a restore from your own files). Alternative: a row in Actions.
4. **Put back on every kept copy** (restores' and moves' too, not only imports'). Recommended: yes, it's the undo the plan promises, and the same code.
5. **Stop copying** while a copy runs (shown): it carries on from where it was next time. Recommended: yes.
6. **Leaving subfolders out** (Plex's `Cache`, `Logs`): not in the plan. Shown as help ("can be left out" of your copy); a copy straight from the original share would include them. Recommended: later, as "Leave out…" on a row.
7. **A waiting service on Home:** no attention item for "Waiting for its data" (it's waiting for you, on purpose), but one for a library's drive that's gone (warning). Recommended as said.
8. **A library's place inside the service** is changed on the service's settings, beside the libraries it reads (not mocked: a field per library, "Inside Plex at", empty for `/media/<library>`).

## New components (for the gallery when built)

- **Steps of a page:** numbered step cards, done (a tick, one line, Change), now, and not reached.
- **Check results:** a list with a mark each, ok, warning, problem (stops what's next) or note.
- **Pasted text:** a monospace text area.
- **A path and where it is now:** an input, an arrow, a folder choice; one under another on phones.
- **Progress:** the meter at full width with the figures under it, inside the activity component.
- **A table list whose rows stack on phones** (what goes where), and middle cells that wrap (`td.wrap`, existing).
