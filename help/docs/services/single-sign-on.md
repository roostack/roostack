# Single sign-on

**Single sign-on** gives everyone one account for the lab's services: the
one they have for Roostack. They sign in once, on your lab's own sign-in
page, and each service knows who they are. You choose who can use each
service, and the sign-in page wears your lab's name, logo, and colours.

It's for services whose package says how to take it (the service's page
says so). Your lab needs its address first: the sign-in page is at
`login.` and your lab's address.

## Turning it on

On the service's page, under **Single sign-on**, choose who can sign in,
then **Turn on single sign-on**. Owners and Admins can turn it on; only an
Owner chooses who can sign in. Roostack will then:

- for an app that signs people in itself (such as Mealie), register it with
  the sign-in page, give it its settings and a secret, and restart it,
  which will take a few seconds. The app will make each person an account
  the first time they sign in. What it will do with an account someone
  already has in it depends on the app, and the card will say: some will
  find it by the person's **email address** or **username**, so check
  them on **Settings → People** first, and some will keep it separate;
- for an app with no accounts of its own (such as Stirling-PDF), put it
  **behind the sign-in page**: everyone will sign in before they reach it.

Some apps keep their sign-in settings in their own database (Forgejo,
Immich): once the app has restarted, Roostack will run its
**single sign-on step** inside it, which will set them. The card will say
while it runs, and if it fails, why, with **Try again**; the app will keep
working with its own passwords meanwhile. Roostack will run the step again
by itself when your lab's address or name changes.

The card says what still uses the app's own sign-in, such as its phone
apps or API tokens, and anything else to know: Paperless-ngx, for one,
doesn't find an account it already has by its email, so its administrator
signs in with their password once and connects single sign-on on their
profile page. Wiki.js gives everyone who signs in an account of their own
that can read and edit pages (its administration stays with the account
made at install), and Papra, while single sign-on is on, is reached only
through the lab's sign-in page. Where the app had an administrator before
(made when you installed it), some apps find it by your username or email
and some can't: the card's note says which, and what to do.

Some apps can't take single sign-on: Jellyfin, Plex, ntfy, and Radicale,
whose phone, TV, and desktop apps sign in by themselves, and, for now,
Uptime Kuma, PhotoPrism, Lychee, and Ghost.

### Apps set through their own settings pages

Immich takes its sign-in settings only from its administrator, so Roostack
keeps a **key to Immich's settings**, which Immich makes when it's
installed. The key can read and change Immich's settings and nothing else
(no photos, no accounts); Roostack never shows it, and it's in Immich's
backup like its other secrets.

An Immich installed before Roostack kept this key will ask you to
**connect Roostack to Immich** first: the card's link will open the action,
which will ask for Immich's administrator email and password once. Roostack
will use them to make the key and keep only the key. Use it again if the
key is lost, for example after Immich's administrator account was deleted.

## Turning it off

**Turn off single sign-on…** on the card. People will then sign in with the
app's own passwords again. Accounts the app made when someone first
signed in with single sign-on have no password of their own: give each
one a password in the app (Forgejo's **Reset a password** action, say).

For an app set by its single sign-on step, Roostack will run the step once
more to undo it, then delete the app's client secret. Forgejo will keep its
"Sign in with …" button if accounts were made through it (it won't delete
a sign-in method that's in use); the button will then lead to a page saying
single sign-on is off.

## Who can sign in

**Everyone in the lab**, or the **groups and people** you choose. Make
groups, such as Family or Friends, on **Settings → People**; a group's
**Change…** also lists the services it can use. Someone without access who
tries will be told who to ask.

Members see only the services they can use on Roostack's home page.

The lab's **Owners and Admins** are admins in apps that can make someone
an admin from single sign-on (Mealie, for one), and the card's **Admins in**
line will say so. In other apps, admins are chosen in the app itself.

## Signing in

The sign-in page asks for a username (or email address) and the person's
Roostack password, then their two-factor code if they use one. **Keep me
signed in** will keep them signed in on that device for 30 days; otherwise
until the browser closes, and at most 12 hours. Under **Settings → People
→ Signing in** you can untick it by default, or ask everyone for
two-factor (Owners always need it, and Admins whenever Roostack's pages
can be reached from the internet).

Someone with a temporary password will choose their own on Roostack's pages
first.

**Signing out** of the sign-in page will end access to services behind it
at once, and to Roostack's own pages. Apps that keep their own sign-in
(Mealie, say) will stay signed in until you sign out there too.

## Roostack's own pages

Once a service uses single sign-on, Roostack's own pages will sign in
through the same page: open them at their address (`roost.` and your lab's
address) and you'll be sent to the sign-in page, then straight back to the
page you asked for. Someone already signed in there for a service won't
have to sign in again. The sign-in page lists Roostack's pages with the
services.

- **Members** stay signed in to Roostack's pages for as long as their
  sign-in lasts.
- **Owners and Admins** will still be signed out of Roostack's pages after
  the time chosen under **Settings → People → Signing in** without using
  them (30 minutes unless you change it), since those pages can change the
  whole lab. They'll then enter their **two-factor code** on the sign-in
  page (an Admin without two-factor, their password) and carry on; their
  services will stay signed in meanwhile.
- **Signing out** of Roostack's pages will sign you out of the sign-in page
  too, and the other way round.

Through the SSH tunnel, at a local address, or before any service uses
single sign-on, Roostack's own login page is the way in, as before. It's
also the way in while the sign-in page can't be reached yet, such as while
the first service to use it is still downloading, and Roostack will check
for the sign-in page every few seconds. It's also at `/login?here` on
Roostack's address, should the sign-in page ever be out of reach.

## Who can reach the sign-in page

The same people who can reach the services using it: home and VPN only
while they're all private, and anyone once one of them is
[public](public.md), or once Roostack's own pages are (a lab on the
internet), with the same protection as a public service (CrowdSec, known
exploits blocked, a strict limit on password tries).

## Its look

On **Settings → People → Sign-in page**:

- **Name:** your lab's name (**Settings → Lab name**).
- **Logo:** a square **mark** shown beside the name (also the browser tab's
  icon), or a **wide logo** with the name in it, shown in its place. A
  wide logo can have a version for dark mode; without one, it will sit on a
  light panel when the device is in dark mode so that dark lettering stays
  readable. Or none: Roostack's hen.
- **Colours:** one of Roostack's themes. Light or dark follows each
  visitor's device.

PNG or SVG, up to 200 KB. SVGs with scripts are refused.

??? note "For technical users"

    - The sign-in page is Roostack itself, on its own listener
      (`127.0.0.1:8482`), separate from Roostack's own pages. It's an
      **OpenID Connect provider**: discovery at
      `/.well-known/openid-configuration`, the authorisation-code flow
      (with PKCE when the app sends it), refresh tokens (each swapped once,
      then working again for a minute for an app retrying after a lost
      answer), and ID tokens
      signed RS256 with the key in `/var/lib/roostack/sso/signing-key.pem`.
    - Apps are told `sub` (a fixed random ID per person), and what their
      scopes ask for: `name` and `preferred_username` (`profile`), `email`
      and `email_verified` (`email`), and `groups` (`groups`): `users`,
      plus `admins` for the lab's Owners and Admins. ID tokens also carry
      `auth_time`; `prompt=login` and `max_age` ask for the password
      again.
    - Checked with the OpenID Foundation's conformance suite (Basic OP and
      Config OP plans).
    - An app's client ID is the service's name; its secret is the Podman
      secret `roostack-<service>-sso-client-secret`, deleted once single
      sign-on is off. Its container finds `login.` your address through an
      `AddHost` line pointing at the server's Caddy.
    - An app set by its single sign-on step (an `on: sso` hook) gets
      `ROOSTACK_SSO=on` or `off`; Roostack keeps a fingerprint of what it
      was last given (on or off, its command and settings, its secrets'
      names but not their values) and runs it whenever that changes. Its
      output is on the card's **Details**, with any secret replaced by
      `[secret]`.
    - Immich's key is the Podman secret `roostack-immich-settings_key`,
      an Immich API key named Roostack with only the `adminConfig.read`
      and `adminConfig.update` permissions.
    - Paperless-ngx's provider list, which holds its client secret, is a
      Podman secret made from a template
      (`roostack-<service>-sso-env-…`), passed as an environment variable:
      never in a file Roostack writes.
    - Walled services use Caddy's `forward_auth`. Each service's address
      gets its own cookie, which Caddy removes before the request reaches
      the service; `Remote-*` headers from visitors are removed on every
      address.
    - Groups, who can use each service, and sign-ins are in Roostack's
      database, backed up with the server.
    - Roostack's own pages sign in through `/login/sign-in`, which sets a
      short-lived cookie and goes to `login.…/roostack`; the sign-in page
      sends the browser back to `/login/sign-in/callback` with a code that
      works once, within a minute, and only in the browser holding that
      cookie. The session it makes there is linked to the sign-in in the
      `sessions` table (`sign_in`) and is deleted with it.
