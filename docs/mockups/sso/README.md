# Single sign-on: mock-ups (2026-10-20)

For SPEC §5.2, before building: the lab's sign-in page, the Single sign-on card on a service's page (an OIDC app and a walled app), and groups and the sign-in page's look on Settings → People.

They're static pages on the real `tokens.css`, themes and `app.css`, with the sidebar, brand mark and icons copied from today's UI. `mock.css` holds only what the real stylesheet lacks. Content is a made-up lab, Smiths' lab at `smiths.roost.ac`, with six people (sam the Owner, robin an Admin, mum, dad, ella, and jo, a friend) in two groups (Family, Friends) and four services. The service logos are the registry's.

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

Then open <http://127.0.0.1:8791/docs/mockups/sso/signin.html>. The bar at the bottom right switches:

- the page;
- that page's states;
- light or dark;
- on the sign-in page, the lab's theme (all nine) and its logo: a mark with the name, a wide logo with the name in it, or none.

Choices go into the address (`?s=code&m=dark&t=nord&logo=wide`; `logo=0` is none).

## The pages

- **`signin.html`: the sign-in page at `login.<domain>`.**
  - **The look:** the lab's logo and name in the lab's theme, with "to continue to" naming the app. The logo is a square mark beside the name, or a wide logo with the name in it (added 2026-10-20, the Owner's idea), which sits on a light panel in dark mode when there's no dark version.
  - **Its states:** signing in, the second step, someone without access ("You can't use Immich yet", saying who to ask), signed in (the page itself lists the services you can use, and signs you out), and signed out (saying which apps keep their own sign-in).
  - **Keep me signed in** is ticked by default here, with a line about shared computers.
- **`service.html`: Mealie's page, an OIDC app.**
  - **On:**
    - who can sign in, the app's admins, the sign-in page's address, and what still uses Mealie's own password;
    - "Change who can sign in" (everyone, or chosen groups and people);
    - **Details**, with the client ID, the redirect address and the env Mealie is given;
    - turning it off.
  - **Off:** what turning it on does, in three lines, and who can sign in.
  - **Turning on:** the activity's steps and log.
- **`wall.html`: Stirling-PDF's page, a walled app (forward auth).** The same card says what the wall does and what stays outside it (`/api`, with the package's reason), and **Details** shows the Caddy snippet.
- **`people.html`: Settings → People.**
  - **The people table** gains each person's email, which single sign-on needs (dad has none, and the table says so), and their groups.
  - **Groups:** a group's members, and the services they can use.
  - **The sign-in page's look:** a live preview, the name (the lab name), the logo (a mark, a wide logo with optional dark-mode and browser-tab versions, or none), and the colours (the built-in themes).
  - **Signing in** gains the sign-in page's two choices.
  - **Add someone** gains an email and groups.

## To decide from these

- **Where the sign-in page's look lives:** on People (as here, beside who signs in), or on Settings → Lab name with the name.
- **"Keep me signed in" ticked by default on the sign-in page:** shown here. Roostack's own login page leaves it unticked.
- **Members on the dashboard:** they see only the services they can use. That isn't mocked here; it's a filter on today's Home.
- **A new icon,** `sign-in` (a padlock), for the service page's steps and later the sidebar: drawn for these pages only, to be added to `icons.go` with the feature.
