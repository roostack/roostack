# Single sign-on

**Single sign-on** gives everyone one account for the lab's services: the
one they have for Roostack. They sign in once, on your lab's own sign-in
page, and each service knows who they are. You choose who can use each
service, and the sign-in page wears your lab's name, logo and colours.

It's for services whose package says how to take it (the service's page
says so). Your lab needs its address first: the sign-in page is at
`login.` and your lab's address.

## Turning it on

On the service's page, under **Single sign-on**, choose who can sign in,
then **Turn on single sign-on**. Owners and Admins can turn it on; only an
Owner chooses who can sign in. Roostack then:

- for an app that signs people in itself (such as Mealie), registers it
  with the sign-in page, gives it its settings and a secret, and restarts
  it, which takes a few seconds. The app makes each person's account the
  first time they sign in, or finds the one with their **email address**,
  so check everyone's email on **Settings → People** first;
- for an app with no accounts of its own (such as Stirling-PDF), puts it
  **behind the sign-in page**: everyone signs in before they reach it.

The card says what still uses the app's own sign-in, such as its phone
apps or API tokens.

## Who can sign in

**Everyone in the lab**, or the **groups and people** you choose. Make
groups, such as Family or Friends, on **Settings → People**; a group's
**Change…** also lists the services it can use. Someone without access who
tries is told who to ask.

Members see only the services they can use on Roostack's home page.

The lab's **Owners and Admins** are admins in apps that can make someone
an admin from single sign-on (Mealie, for one).

## Signing in

The sign-in page asks for a username (or email address) and the person's
Roostack password, then their two-factor code if they use one. **Keep me
signed in** keeps them signed in on that device for 30 days; otherwise
until the browser closes, and at most 12 hours. Under **Settings → People
→ Signing in** you can untick it by default, or ask everyone for
two-factor (Owners always need it, and Admins whenever Roostack's pages
can be reached from the internet).

Someone with a temporary password chooses their own on Roostack's pages
first.

**Signing out** of the sign-in page ends access to services behind it at
once. Apps that keep their own sign-in (Mealie, say) stay signed in until
you sign out there too.

## Who can reach the sign-in page

The same people who can reach the services using it: home and VPN only
while they're all private, and anyone once one of them is
[public](public.md), with the same protection as a public service
(CrowdSec, known exploits blocked, a strict limit on password tries).

## Its look

On **Settings → People → Sign-in page**:

- **Name:** your lab's name (**Settings → Lab name**).
- **Logo:** a square **mark** shown beside the name (also the browser tab's
  icon), or a **wide logo** with the name in it, shown in its place. A
  wide logo can have a version for dark mode; without one it sits on a
  light panel when the device is in dark mode, so dark lettering stays
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
      secret `roostack-<service>-sso-client-secret`. Its container finds
      `login.` your address through an `AddHost` line pointing at the
      server's Caddy.
    - Walled services use Caddy's `forward_auth`. Each service's address
      gets its own cookie, which Caddy removes before the request reaches
      the service; `Remote-*` headers from visitors are removed on every
      address.
    - Groups, who can use each service and sign-ins are in Roostack's
      database, backed up with the server.
