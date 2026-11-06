# A real lab's notes: proposals and mock-ups (2026-11-06)

The maintainer's notes from using `lab.grahamhq.com` asked for nine things. Four were small and are built on the branch `claude/lab-notes-1106` (memory and CPU on Home, Seafile's and others' admin kept home-only, landing on the service after installing, a wider and taller sign-in logo). These are the other five, proposed here for the maintainer to choose before anything is built.

**Decided (the maintainer, 2026-11-06):** build all five as recommended, with one change: item 5's confirmation to delete a removed service's data is a modal dialog over the list (`showModal`), not a dialog in the page.

**Built (2026-11-07)** on the branch `claude/lab-notes-1106`, and checked on dev VM 9; SPEC §5.2, §6.3.5, §6.7 and §8.1 say how. Where the build differs from the pages here:

- Servers links to each server's Clean up page without saying how much could be freed (that would ask Podman on every view of Servers).
- A service turned off has no monitoring checks while it's off, so the status page leaves it out rather than showing "Off".
- The settings behind Name and look keep their names (`signin.*`): renaming them would only have changed the names.

The pages were saved from dev VM 9 running that branch, so the sidebar, header and styles are the real ones; only what's in each page's middle is mocked. What's new is outlined with a dashed line. **Open them** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8797 --bind 127.0.0.1
```

then <http://127.0.0.1:8797/docs/mockups/lab-notes/service-turn-off.html>. The bar at the bottom right switches between the pages, and between light and dark. They were made by a script from the VM's pages (kept with the session's notes, not here), so a change is best described rather than edited in each file.

## 3. Turning a service off for a while

**What's there:** nothing a person can use. Roostack can already stop a service and keep it stopped without touching its data: that's what the registry's **Stop** does (SPEC §8.3.4, "What stopping does": `generate.Stop` drops its containers and Caddy site, keeps its folders, secrets, and network, and keeps its names and ports reserved). Pausing public access is different: the service keeps running for the household.

**Proposal** (`service-turn-off.html`, `service-off.html`):

- **Turn it off for a while** under the service's Actions (Owners and Admins), saying what will happen and what's kept. Turning it off saves `off: true` in its instance file (desired state, so it's backed up and survives restarts and updates) and uses the registry's stop path, so nothing new is needed below the manager.
- While it's off: an **Off** pill, a notice at the top saying who turned it off and when, and **Turn it on** where Open was. It isn't a problem and isn't under Needs attention; monitoring and the status page show it as off rather than down, so nobody is emailed; updates wait until it's on again.
- **Backups:** as for a registry stop, files carry on being backed up, and a Postgres database (which needs its container to be dumped) keeps its last dump, since nothing changes while it's off.
- **Visitors** get a page saying it's turned off, rather than a connection error: Caddy keeps the site with a page like the public pause's.
- `roostack service off <name>` and `on`, recorded in the audit log and History.

**Questions:**

1. The words: **Turn off / Turn on** (recommended: plain, and "Stop" is already the registry's), or Pause, or Disable?
2. Should it be able to turn itself on again on a date ("off until Monday")? Recommended: not now; it's a later addition to the same field.
3. Updates while it's off: wait until it's on (recommended: an update starts the app to check it), or apply without starting it?

## 5. Data left by removed services

**What's there:** removing a service keeps its folders (`/srv/roostack/services/<name>` and `/srv/roostack/media/<name>`), and installing it again under the same name on the same server uses them (SPEC §8.1, "Installing again over data from before"). Nothing lists them, and nothing deletes them: the only way is a shell on the server. Removing deletes the instance file, so nothing remembers which package a folder came from.

**Proposal** (`services-removed.html`, `services-removed-delete.html`):

- Services gets two views, **Installed** and **Removed, data kept** (shown only when there's something removed). Each row: the service, its package and version, the server, when and by whom it was removed, and its size (counted with the disks' nightly count).
- **Install again** opens the install form for that package with its name and server filled in, which already says "Its data from before is still on nuc".
- **Delete…** asks once, listing the folders and their sizes, saying that its kept secrets go too and that backups keep it until they're pruned (with the oldest one's date), and needs a tick ("I understand that this can't be undone"). The node deletes the folders through `systemd-run --user` (the files belong to the containers' users), logged in History and the audit log.
- **A record of removals:** from now on, removing a service keeps a row on the controller (name, package, title, version, server, when, who). Folders found on a server without a row (removed before this) are listed by folder name, with Delete only.

**Questions:**

1. A tick to confirm (recommended), or typing the service's name, as some apps ask for?
2. Also offer **Delete its data too** on the Remove page itself, unticked? Recommended: yes, since that's when people decide.
3. Owners only for deleting (recommended, as with deleting kept copies), or Admins too?

## 6. Storage clean-up

**What's there:** Roostack never deletes container images (SPEC §7, "nothing prunes images"): each update keeps the version before so that it can go back if the new one fails its check, and every earlier version stays for good, as do the images of removed services. Kept copies from restores and moves can be deleted on Servers already.

**Proposal** (`servers.html`, `clean-up.html`):

- Each server's card on Servers says how much could be freed, with **Clean up nuc…**, and so does Home's low-disk warning.
- The **Clean up** page lists, by kind: **old versions of services** (images no container uses, named by service and version rather than by image, ticked), **data from removed services** (a link to item 5's list), **kept copies** (a link to the list there), and **Roostack's own** leftovers (the web proxy's build cache). Each with its size, and **Delete** for what's chosen. The exact commands are one click away.
- The version before each service's last update is kept for a week and not offered, so going back still works.
- **By itself:** delete old versions a week after nothing uses them, every night after the backup, and say so in History.

**Questions:**

1. The automatic clean-up: on by default (recommended: images are the biggest leftover, and the week keeps going back safe), off, or not offered at all?
2. How long to keep the version before an update: a week (recommended), until the next update, or for good?
3. Also show logs (the journal) here? Recommended: not now; systemd already limits the journal's size.

## 7. Single sign-on's own page

**What's there:** what single sign-on needs is spread out: the sign-in page's look is a card on People, its two settings are under People → Signing in with Roostack's own, each service turns it on from its own page, and who can use a service is chosen by groups on People or on the service's page. Nothing shows the whole picture.

**Proposal** (`sign-in.html`, `people.html`):

- **Settings → Single sign-on** (`/settings/sign-in`), after People: figures (how many services use it, how many people can sign in and who can't yet because they have no email address, whether the sign-in page is working and where), a table of services with single sign-on on or off and who can use each, the sign-in page with a picture of it, and its two settings (keeping people signed in for 30 days, two-factor for everyone).
- **People** keeps people and groups, and gets a small **Single sign-on** card in place of the sign-in page's look: which services, where the sign-in page is and whether it's working, who needs two-factor, and a link to the page. **Signing in to Roostack** keeps only Roostack's own pages' settings.
- The sign-in page's look moves to item 9's page, which this page links to.

**Questions:**

1. The name in the sidebar and the page: **Single sign-on** (recommended: what the service pages and help already call it) or "Sign-in page"?
2. Should turning single sign-on on or off for a service be possible from this table, or only link to the service's page (recommended: link, since turning it on can need the app's own settings)?

## 9. The lab's name and look

**What's there:** the lab's name is its own page (Settings → Lab name); the logo and colours are on People's sign-in page card and used only by the sign-in page. The status page shows the name only, and Roostack's own pages show Roostack's hen beside the name.

**Proposal** (`look.html`):

- **Settings → Name and look** (`/settings/look`) in place of Lab name: the name, the logo (a mark, a wide logo with its dark version and tab icon, or none), and the colours, moved from People.
- **Used everywhere the lab shows itself:** the sign-in page as now; the status page (the logo above "Whether its services are working", and its colours); Roostack's sidebar (the mark in place of the hen, or the wide logo at most 40 px high, with "Roostack" small underneath); and the browser tab's icon on all of them.
- **Colours:** the lab's palette for the sign-in and status pages, and Roostack's own pages for anyone who hasn't chosen their own under Your account → Appearance.
- A **Where it's shown** column previews the three.
- The settings keep their keys (`signin.*` become `look.*`, copied once by a store migration), and `/settings/lab` redirects.

**Questions:**

1. Roostack's sidebar: show the lab's logo in place of the hen (recommended, with "Roostack" kept small so people know what they're using), or keep the hen there?
2. The lab's colours as the default theme for Roostack's own pages (recommended), or for the sign-in and status pages only?
3. The page's name: **Name and look** (recommended), "Brand", or "Your lab"?

## Order, if all are agreed

7 and 9 together (they move the same card), then 5 (6 links to it), 6, and 3. Each would be its own commit with tests, SPEC, the help pages, and a look on a VM at desktop and phone widths.
