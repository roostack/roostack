# Extra environment variables: mock-ups (2026-11-16)

Layer 2 of "Configuring a service beyond its settings" (SPEC §8.1, Locked 2026-10-18): extra environment variables per service, plain or secret, for apps configured with dozens of them (HedgeDoc's `CMD_*`, Forgejo's `FORGEJO__section__KEY`, Immich, Outline), without each package listing them all as settings.

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/extra-env/forgejo.html#advanced>. The bar at the bottom right switches the page, and light or dark. The pages are the real service settings page (rendered from the templates) with the new card below Name; `mkmock.py` isn't kept, the HTML is.

## The pages

- **Editing a row** (`forgejo.html`): Forgejo with four variables for sending email, one of them a secret; `SMTP_ADDR` being edited in its row (Save, Cancel); a row at the end to add one, with a Secret tick box.
- **A secret shown** (`shown.html`): the Owner chose Show on the secret: its value, with Hide, and a note that it was recorded in the audit log.
- **Refused** (`refused.html`): someone tried to add a variable the package sets itself: the card says why above the add row, and keeps the name, never the value.
- **As text** (`text.html`): the same variables as one text box (the Rows / As text toggle is the service page's Friendly / Technical switch), in `.env` form, the secret as `‹hidden›`, with a few lines pasted in.
- **Check** (`check.html`): after Continue: each added or changed variable with a Secret tick box (ticked by Roostack for a name that looks like a secret, and saying so), the problems with their line numbers (here a variable the package sets, which stops the save until it's removed), and what's unchanged.
- **Two containers** (`immich.html`): a Container column and a container choice in the add row, the main container first.

## How it would work

- **Who:** Owners only (it can break a service, and a secret given here goes into it), on the service's Settings page, below its settings and name.
- **What's kept:** the service's instance file, so it survives updates, backups, restores, and moving the service to another server; a secret's value as a Podman secret (`roostack-<service>-env-<name>`), never in the instance file, given to the container as that variable, as the package's own secrets are.
- **What's refused:** a name that isn't letters, digits, and underscores (not starting with a digit), a value with a line break or NUL, a name the package sets for that container (its `env`, its secrets, single sign-on's variables), more than 100 per service, a value over 4 KB, and a text box over 64 KB.
- **The text box:** one `NAME=value` a line; quotes around a value and `export` in front are accepted, blank lines and `#` comments left out, a name given twice refused with both line numbers. Saving it replaces the service's extra variables with what's in it: a line taken out removes its variable, and `‹hidden›` keeps a secret's value. With two containers, there's a box for each, under its name.
- **Guessing secrets:** a name with PASSWORD, PASSWD, SECRET, TOKEN, or KEY in it (any case) is ticked on the check; the person can untick it, or tick another.
- **Where it shows:** the container file on the service's technical side, after the package's own variables; `roostack render` with the instance shows them too, secrets as their Podman secret.
- **Saving** restarts the containers that read the change, as a setting does; nothing else changes.
- **After a wrong value:** the service's page says it isn't starting as usual; removing the variable starts it as it was.

## Decided (2026-11-16), the maintainer

1. **Where:** a card on the service's Settings page.
2. **Editing:** in the row itself (Save and Cancel; Replace for a secret).
3. **A secret's value:** hidden after saving, shown to the Owner on request (Show), each time recorded in the audit log.
4. **Names the package sets:** refused.
5. **The app's list of options:** a link from the package, a new optional `docs.env` address in the package file, shown only when it's there.
6. **The limit:** 100 variables per service (50 was low: HedgeDoc alone documents about a hundred); 4 KB a value, 64 KB for the text box.
7. **Pasting:** a text box beside the rows, in `.env` form, with a check step that asks which new values are secrets before anything is saved.
