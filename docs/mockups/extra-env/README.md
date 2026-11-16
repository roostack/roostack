# Extra environment variables: mock-ups (2026-11-16)

Layer 2 of "Configuring a service beyond its settings" (SPEC §8.1, Locked 2026-10-18): extra environment variables per service, plain or secret, for apps configured with dozens of them (HedgeDoc's `CMD_*`, Forgejo's `FORGEJO__section__KEY`, Immich, Outline), without each package listing them all as settings.

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/extra-env/forgejo.html#advanced>. The bar at the bottom right switches the page, and light or dark. The pages are the real service settings page (rendered from the templates) with the new card added below Name.

## The pages

- **One container** (`forgejo.html`): Forgejo with four variables for sending email, one of them a secret (shown as Hidden, with Replace), and the form to add one.
- **Refused** (`refused.html`): someone tried to add a variable the package sets itself (`FORGEJO__database__PASSWD`): the card says why and keeps the name, never the value.
- **Two containers** (`immich.html`): a Container column and a Container choice, the main container first.

## How it would work

- **Who:** Owners only (it can break a service, and a secret given here goes into it), on the service's Settings page, below its settings and name.
- **What's kept:** the service's instance file, so it survives updates, backups, restores, and moving the service to another server; a secret's value as a Podman secret (`roostack-<service>-env-<name>`), never in the instance file, given to the container as that variable, as the package's own secrets are.
- **What's refused:** a name that isn't letters, digits, and underscores (not starting with a digit), a value with a line break or NUL, a name the package sets for that container (its `env`, its secrets, single sign-on's variables), and more than 50 per service.
- **Where it shows:** the container file on the service's technical side, after the package's own variables; `roostack render` with the instance shows them too, secrets as their Podman secret.
- **Saving** restarts the containers that read the change, as a setting does; nothing else changes.
- **After a wrong value:** the service's page says it isn't starting as usual; removing the variable starts it as it was.

## Decided (2026-11-16), the maintainer

1. **Where:** a card on the service's Settings page, as mocked.
2. **Editing:** in the row itself: Edit turns the row's value into a text box with Save and Cancel (Replace, for a secret).
3. **A secret's value:** hidden after saving, and shown to the Owner on request (Show), each time audited.
4. **Names the package sets:** refused, as mocked.
5. and 6. Not answered yet: the recommendations below until the maintainer says otherwise.

## The questions as asked

1. **Where:** a card on Settings, as mocked (recommended: it's where people change a service), or a page of its own under the service (Settings → Advanced), which keeps Settings short.
2. **Editing:** Edit puts the row's value back in the form above (recommended: one form), or edits in the row itself.
3. **A secret's value:** never shown again after saving, only replaced (recommended, as the package's secrets), or shown on request to the Owner.
4. **Names the package sets:** refused (recommended, as SPEC says: the package's own variables, such as database passwords and addresses, would break the service), or allowed with a warning for the Owner who knows better.
5. **The app's list of options:** a link from the package (a new optional `docs.env` URL in the package file, shown only when it's there; recommended), or no link.
6. **The limit:** 50 variables per service (recommended), or none.
