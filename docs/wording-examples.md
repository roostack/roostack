# Wording examples

Approved by the maintainer (2026-10-23). These before-and-after pairs come from wave 4's sample ([QUALITY-REVIEW-2026-10-21.md](QUALITY-REVIEW-2026-10-21.md), §11), and the wording batches (4a–4g) follow them. The rules themselves are in the "Writing" sections of [CLAUDE.md](../CLAUDE.md) and [CONTRIBUTING.md](../CONTRIBUTING.md).

Each pair is filed under the main rule it shows, and says so when it shows more than one. Names in the examples (server-2, Immich, smith.family) stand for what the code fills in. Paths starting `cloud:` are in Roostack Cloud's repository. Line numbers are as of Roostack 347e4d1 and Cloud 59f9762.

## The line, as decided

- **Actions take the future or the present continuous.** Use the future for a single action that will follow something a person does or an event ("Roostack will try again tomorrow"), and the present continuous for something going on now ("these backups are going through server-1").
- **Habits and standing facts stay in the present simple:** a routine that always runs ("Every minute, Roostack checks…"), what Roostack is ("Roostack runs useful services…"), a fact that always holds ("Codes change every 30 seconds"), and a check's name ("New names and certificate records reach DNS").
- **Field help takes the future,** since it says what will happen once a package uses the field ("Roostack will create a random value").
- **Commas where grammar needs them, not for a pause** (the maintainer, 2026-10-23):
  - no comma before a clause of purpose ("has finished so that it isn't interrupted");
  - none before "because" after the main clause ("can't reach it because its address is out of DNS");
  - none before an "or" that joins two times or two things ("tomorrow or as soon as you change the address");
  - move a phrase rather than fence it off with commas ("Unlink from either side whenever you like").

  A comma stays after an opening clause ("Once you've saved it, Roostack will…"), before "so" when it means "and so" ("didn't connect, so these backups are going through server-1"), around a "which" or "where" that adds detail, and wherever leaving it out would let the sentence be misread.
- **The Oxford comma** (the maintainer, 2026-10-23): a list of three or more takes a comma before its last "and" or "or" ("stop the service, turn its addresses off, and say so on its page, under Needs attention, and by email").

## The future for what will happen

Especially after something a person might do or an event Roostack is waiting for.

- `internal/web/templates/service.html:136` (Roostack's pages). After something a person does.
  - Before: Plug it back in and Roostack restarts Immich by itself, within a minute, so it sees it again. That takes a few seconds, and nothing in Immich is lost.
  - After: Plug it back in and Roostack will restart Immich by itself within a minute so it sees it again. That will take a few seconds, and nothing in Immich will be lost.
- `internal/web/templates/restarting.html:10` (Roostack's pages). Also joined.
  - Before: Roostack restarts to use the new settings once the current job (the backup) has finished, so it isn't interrupted. This can take a while; this page continues by itself.
  - After: Roostack will restart to use the new settings once the current job (the backup) has finished so that it isn't interrupted. This can take a while, and this page will carry on by itself.
- `internal/web/templates/backups-key-change.html:38` (Roostack's pages). A passive present ("is changed") hides who acts.
  - Before: Roostack makes a new key and shows it; once you've saved it, every backup is changed to open with the new key, and the old one stops working.
  - After: Roostack will make a new key and show it to you. Once you've saved it, Roostack will change every backup to open with the new key, and the old one will stop working.
- `internal/attention/attention.go:242` (Roostack's attention items). Also joined.
  - Before: Roostack Cloud can't find the record proving smith.family is this lab's any more. Public services stop coming through the relay a day after it went: put it back.
  - After: Roostack Cloud can't find the record proving smith.family is this lab's any more, so public services will stop coming through the relay a day after it went: put it back.
- `internal/attention/cloud.go:79` (Roostack's attention items).
  - Before: Roostack Cloud has suspended this lab's account: (reason). Its names and relay don't work until it's lifted.
  - After: Roostack Cloud has suspended this lab's account: (reason). Its names and relay won't work until it's lifted.
- `internal/manager/keychange.go:225` (Activity logs). Also joined.
  - Before: Roostack Cloud couldn't be told about the new key yet (error); Roostack tries again at its next daily check.
  - After: Roostack couldn't tell Roostack Cloud about the new key yet (error), so it will try again at its next daily check.
- `internal/backup/service.go:145` (Activity logs). A step in a log whose other steps say what's happening ("Removing old backups (keeping 7 daily…)").
  - Before: Old backups are removed by the controller
  - After: Not removing old backups here: the controller will do that
- `internal/manager/keychange.go:114` (Errors people can fix).
  - Before: The lab has no recovery key yet: it's made with the first backup.
  - After: The lab has no recovery key yet: Roostack will make one with the first backup.
- `internal/manager/import.go:82` (Errors people can fix). Same pattern at node/importcopy.go:48 and node/photos.go:694. Also joined.
  - Before: Copy your files first; switching copies only what changed since.
  - After: Copy your files first: when you switch, Roostack will copy only what's changed since.
- `cmd/roostack/user.go:100` (Command line). After `roostack user add` or `reset-password`.
  - Before: They choose their own password when they next log in.
  - After: They'll choose their own password when they next log in.
- `help/docs/settings/domain.md:125` (Help site). Also joined.
  - Before: DNS changes can take a few minutes. Roostack checks again by itself.
  - After: DNS changes can take a few minutes, and Roostack will check again by itself.
- `help/docs/settings/domain.md:177` (Help site).
  - Before: Roostack keeps the login and tries again every hour. To finish it yourself, delete the records at the old DNS provider, then choose I've deleted them: Roostack stops trying, and deletes the login.
  - After: Roostack will keep the login and try again every hour. To finish it yourself, delete the records at the old DNS provider, then choose I've deleted them, and Roostack will stop trying and delete the login.
- `help/docs/services/settings.md:162` (Help site). What will happen if the registry says Stop.
  - Before: Roostack stops the service and turns its addresses off, and says so on its page, under Needs attention and by email.
  - After: Roostack will stop the service, turn its addresses off, and say so on its page, under Needs attention, and by email.
- `internal/editor/help.yaml:143` (Package editor). Field help: what will happen if you choose this. See question 2.
  - Before: For a secret: Roostack creates a random value, so nobody has to think of one.
  - After: For a secret: Roostack will create a random value, so nobody has to think of one.
- `cloud:internal/web/notifications.go:192` (Cloud's console and emails). The "lab not heard from" email. Also joined.
  - Before: If you meant to turn it off, there's nothing to do. Once it's back, it checks in by itself.
  - After: If you meant to turn it off, there's nothing to do: once it's back, it will check in by itself.
- `cloud:internal/web/continuity.go:750` (Cloud's console and emails).
  - Before: Subject: In 3 days, your Roostack Cloud account goes to your continuity contact. Body: …Unless you stop it, they become its billing user on 2 December 2026.
  - After: Subject: In 3 days, your Roostack Cloud account will go to your continuity contact. Body: …Unless you stop it, they'll become its billing user on 2 December 2026.
- `cloud:internal/web/portal.go:241` (Cloud's console and emails). This name is waiting, so it's what will happen to it.
  - Before: (name) is waiting for our approval. It looks like a bank's, a government service's or a well-known brand's name: a person checks names like that first, so nobody can pass as one.
  - After: (name) is waiting for our approval. It looks like a bank's, a government service's, or a well-known brand's name: a person will check it first so that nobody can pass as one.
- `cloud:site/src/pages/cloud.astro:141` (Cloud's site and status page). After something a person might do.
  - Before: Unlink whenever you like, from either side: your lab keeps running exactly as before.
  - After: Unlink from either side whenever you like: your lab will keep running exactly as before.

## The present continuous for what's happening now

- `internal/web/templates/parts.html:427` (Roostack's pages). Both tenses in one notice. Also the future.
  - Before: A direct link to server-2 didn't connect, so these backups go through server-1, using its connection twice. Check server-2 can be reached at its address for direct links (UDP port 51820, or the port you entered). Roostack tries again tomorrow, or as soon as you change the address.
  - After: A direct link to server-2 didn't connect, so these backups are going through server-1, which uses its connection twice. Check server-2 can be reached at its address for direct links (UDP port 51820 or the port you entered): Roostack will try again tomorrow or as soon as you change the address.
- `internal/attention/cloud.go:105` (Roostack's attention items). Same at cloud.go:109 and settings-public.html:47.
  - Before: Roostack Cloud has suspended this lab's relay: (reason). Public services don't come through it.
  - After: Roostack Cloud has suspended this lab's relay: (reason). Public services aren't coming through it.
- `internal/manager/restore.go:277` (Activity logs). "It's restored" reads as if it's already done.
  - Before: Roostack can't reach server-2, where Immich ran: it's restored without stopping it there
  - After: Roostack can't reach server-2, where Immich ran, so it's restoring Immich without stopping it there
- `cloud:internal/admin/abuse.go:579` (Cloud's console and emails). The "name suspended" email; abuse.go:576 too. Also joined.
  - Before: Visitors can't reach it: its address is out of DNS, and our relay doesn't route it. The rest of your lab carries on.
  - After: Visitors can't reach it because its address is out of DNS and our relay isn't routing it, but the rest of your lab is still working.

## Clipped statements, joined

Join short statements where a person would, often with "so", "and" or a colon.

- `internal/attention/attention.go:344` (Roostack's attention items). "Tell Roostack," doesn't say what to tell; the link is already "Where it is".
  - Before: This server looks like a cloud server: it has a public address of its own. Tell Roostack, so private services and the public access checklist fit.
  - After: This server looks like a cloud server, as it has a public address of its own. Tell Roostack where it is so that private services and the public access checklist fit it.
- `internal/attention/notify.go:47` (Roostack's attention items). The description of the "Problems" emails. Also the future.
  - Before: Backups failing, a disk running low, a service or server that stopped working, updates that failed. Sent once a problem has lasted a few minutes, then once a day while it lasts, and when it's fixed.
  - After: Backups failing, a disk running low, a service or server that stopped working, or updates that failed. Roostack will email you once a problem has lasted a few minutes, then once a day while it lasts, and again when it's fixed.
- `internal/editor/help.yaml:316` (Package editor). "The package says" is what the file holds, a state. Also the future.
  - Before: Where people reach the app. Every address starts private (home only); the Owner makes it public, and the package says what's safe then.
  - After: Where people reach the app. Every address will start private (home only) until the Owner makes it public, and the package says what's safe once it is.
- `cloud:internal/web/notifications.go:47` (Cloud's console and emails). "Labs check in every day" is a habit and stays. Also the future.
  - Before: Labs check in every day while they're running: is it on, and online? Once each time it goes quiet.
  - After: Labs check in every day while they're running so that Roostack Cloud knows whether each one is on and online. It will email you once each time a lab goes quiet.
- `cloud:internal/admin/auth.go:368` (Cloud's console and emails). An error after a too-short password, not a hint beside the field.
  - Before: At least 12 characters. A password manager's suggestion is best.
  - After: Choose a password of at least 12 characters: a password manager's suggestion is best.

## Long-winded, tightened

- `internal/web/templates/settings-domain.html:29` (Roostack's pages). One "until" instead of two "until then"s, four sentences to three. Also the present continuous and the future.
  - Before: Caddy serves both addresses, each with its own certificate: the old one's still renews through Cloudflare, whose login Roostack keeps until then (Podman secrets roostack-caddy-dns-old-…). Until then, the DNS records Roostack set for the old address follow your home's address, as the new ones do. When the old address stops, Roostack deletes them with that login, then deletes the login. It deletes only its own records, and leaves any you've changed since.
  - After: Caddy is serving both addresses, each with its own certificate, and the old one's is still renewing through Cloudflare. Until the old address stops, Roostack will keep that provider's login (Podman secrets roostack-caddy-dns-old-…), and the DNS records it set for the old address will follow your home's address as the new ones do. Then Roostack will delete its own records with that login, leaving any you've changed since, and delete the login after them.
- `help/docs/services/single-sign-on.md:124` (Help site). "So it is while" is hard to follow. Also the future.
  - Before: …Roostack's own login page is the way in, as before. So it is while the sign-in page can't be reached yet, such as while the first service to use it is still downloading: Roostack checks every few seconds.
  - After: …Roostack's own login page is the way in, as before. It's also the way in while the sign-in page can't be reached yet, such as while the first service to use it is still downloading, and Roostack will check for the sign-in page every few seconds.

## Fine as they are

States with "is", past results, instructions, and the future already in place.

- `internal/labrestore/restore.go:135` (Activity logs). Past result, then the future.
  - Before: Couldn't keep which key opens each backup (error): Roostack will ask restic again
- `internal/manager/restore.go:228` (Errors people can fix). A state, then an instruction: short, but not clipped.
  - Before: That backup isn't there any more. Choose another.
- `internal/editor/static/editor.js:1124` (Package editor). States, not actions.
  - Before: Roostack can set it up: it's valid, and its templates render, and it follows the package policy.
- `cloud:status/worker/page.js:29` (Cloud's site and status page). Past result.
  - Before: This page last checked at 14:05 (3 minutes ago): what it shows may be out of date.

## Habits and standing facts

These stay in the present simple: a routine that always runs, what Roostack is, a fact that always holds, and a check's name.

- `internal/web/templates/monitoring.html:23` (Roostack's pages). A routine that always runs, not one action.
  - Before: Every minute, Roostack checks that each service answers at its address, and tells you when one stops.
- `help/docs/index.md:8` (Help site). Says what Roostack is, like the website's front page.
  - Before: Roostack runs useful services on a computer you own: your photos, your passwords, your own git server, and more. It installs them with a click, gives each one a secure web address, backs them up every night…
- `cloud:internal/web/accounts.go:333` (Cloud's console and emails). A state, a standing fact, an instruction. Also fine as it is.
  - Before: That code isn't right. Codes change every 30 seconds; use the newest one.
- `cloud:status/worker/model.js:47` (Cloud's site and status page). The name of a check (what's being checked), shown with a green or red dot.
  - Before: New names and certificate records reach DNS
