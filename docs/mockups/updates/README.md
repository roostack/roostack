# Updates: a page to choose several at once (2026-11-08)

The maintainer asked for a list of available updates with enough detail to choose several, by ticking them, and apply them together (SPEC §7.1 shows each service's update only on its own page). **Decided (the maintainer, 2026-11-08):** a separate page under Services, linked from the services list whenever there are updates; both **Update now** and **Update tonight**; fix releases (and improved packages) ticked when the page opens.

These pages are proposed for the maintainer to look at before anything is built. They reuse the sidebar, header and styles saved from a running lab for the lab-notes mock-ups, with the real `tokens.css`, themes and `app.css`; only each page's middle is mocked, and what's new is outlined with a dashed line. The mock lab has two servers (nuc, mum-and-dad) and six services with updates, one turned off.

- **Services:** a notice above the list while there are updates; each Update badge opens the Updates page at its row.
- **Updates:** a row per service with an update: the versions and kind, what to read first, and whether its own update choice would apply it tonight anyway. Rows that can't be chosen say why. On a phone it keeps every column and scrolls sideways (the maintainer, 2026-11-08).
- **Notes:** ticking an update with upgrade notes opens them first.
- **Update now:** a confirmation listing what will update on each server, then progress, one service at a time after one backup per server.
- **Tonight:** the chosen ones wait for the nightly backup, each with Cancel.

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/updates/updates.html>. The bar at the bottom right switches the page, and light or dark.

**Built (2026-11-08)**, as SPEC §7.1 "The Updates page" says. Where the build differs from these pages: it's at `/updates` (a service named "updates" would own `/services/updates`); a row whose server isn't backed up says so under "Before you update"; and an update chosen for tonight that a newer version replaces is dropped, so that nobody updates to a version they didn't see.
