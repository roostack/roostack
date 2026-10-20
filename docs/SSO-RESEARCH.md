# Single sign-on: research (2026-10-20)

The background to the single sign-on proposal in SPEC.md §5.2. Two
surveys: the identity providers a home lab could run, and how each package
in the registry can take single sign-on. Facts were checked on
2026-10-20, from each project's releases, licence files and documentation
(sources at the end of each part). **Unverified** marks what needs a
check on a VM.

## Words

- **Identity provider:** the one place people sign in. It tells each app
  who they are.
- **OIDC** (OpenID Connect): the standard way an app asks an identity
  provider "who is this?". The app sends the browser to the provider's
  sign-in page, which sends it back to the app's *redirect* (or
  *callback*) address with a one-time code; the app swaps the code for a
  signed token, server to server. The app keeps its own accounts and
  sessions, made or matched from the token.
- **Client:** an app registered with the provider: an ID, a secret, and
  the exact addresses it may be sent back to.
- **Forward auth:** for apps with no OIDC. The proxy (Caddy) asks the
  provider about every request before passing it on; someone not signed in
  is sent to the sign-in page. The app sees only requests from signed-in
  people, and may read who they are from headers (`Remote-User`), which is
  **header login**.

## 1. Identity providers

| Provider | Release (date) | Licence | Maintainers | Idle memory | Database | OIDC | Forward auth | Passkeys / two-factor / passwords | Configured by |
|---|---|---|---|---|---|---|---|---|---|
| **Authelia** | 4.39.28 (2026-09-17) | Apache-2.0 | small core team, one main developer | ~30 MB (unverified) | SQLite; sessions in memory unless Redis | yes, "beta" on its roadmap, OpenID Certified (2025) | **yes**, with `Remote-User/-Groups/-Email/-Name` and rules by group | yes / yes / yes | **YAML files only**; users from a file (watched) or LDAP; no admin API |
| **Pocket ID** | 2.18.0 (2026-10-04) | BSD-2 | one main developer | small (unverified) | SQLite | yes, mature | no (pair with tinyauth) | passkeys **only**, no passwords | full REST API with a static API key |
| **Kanidm** | 1.11.2 (2026-09-11) | MPL-2.0 | two main developers | unverified | its own SQLite | yes, strict (PKCE and ES256 enforced) | no | yes / yes / yes | HJSON migrations (1.9+), CLI, REST |
| **VoidAuth** | 1.16.0 (2026-09-26) | AGPL-3.0 | one developer, "not audited" | Node.js (unverified) | SQLite | yes | yes | yes / yes / yes | clients by env; users and groups in its UI only |
| **Rauthy** | 0.37.2 (2026-10-19) | Apache-2.0 | one developer; independently audited | ~40 MB | embedded SQLite | yes | awkward (a callback injected per app) | yes / **no TOTP** / yes | TOML, bootstrap JSON, admin API |
| **tinyauth** | 5.2.0 (2026-09-07) | AGPL-3.0 | one developer | tiny | SQLite | basic | **yes** (its purpose) | no / yes / yes | env only |
| **Dex** | 2.46.0 (2026-10-07) | Apache-2.0 | CNCF project | small | SQLite | yes | no | WebAuthn and TOTP new in 2.46 | YAML, gRPC; "connectors" to other sources |
| **Ory Hydra** | 26.2.0 (2026-03-20) | Apache-2.0 | company; no commits since 2026-07 | small | SQLite or Postgres | yes, certified, **headless** (you write the sign-in pages) | no | none of its own | admin API |
| Zitadel | 4.19.5 (2026-10-15) | AGPL-3.0 | company | ~512 MB | **Postgres** | yes | no | yes | API |
| Authentik | 2026.8.4 (2026-10-14) | MIT (+ enterprise) | company | **2 GB minimum** | Postgres | yes | yes (outposts) | yes | blueprints, API |
| Keycloak | 26.8.1 (2026-10-13) | Apache-2.0 | Red Hat | **~1.25 GB** | Postgres | yes | no | yes | realm import, API |
| Logto | 1.45.0 (2026-10-13) | MPL-2.0 | company | Node.js, 327 MB image | Postgres | yes | no | yes | API |
| Casdoor | 4.22.0 (2026-10-19) | Apache-2.0 | company | Go | several | yes | unverified | yes | API; a critical sign-in bypass in 2026 (CVE-2026-9090) |
| lldap | 0.6.3 (2026-04-30) | GPL-3.0 | one developer | ~10–20 MB (unverified) | SQLite | an LDAP directory, not a provider | – | – | GraphQL; passwords only through its own tool (OPAQUE) |

**Too heavy for 4–8 GB machines:** Zitadel, Authentik, Keycloak, Logto (each
wants Postgres, and most several hundred MB to 2 GB). **Rejected:** Casdoor
(security record), Ory (Hydra, Kratos and our own pages: three pieces, and
its open-source releases have slowed).

### The serious candidates

**Authelia.** One small Go container, forward auth and OIDC, passwords,
TOTP and passkeys. Every part of it is a file Roostack could write, which
suits Roostack's desired-state model (SPEC §1 principle 2). Its users can
come from a file whose password hashes are argon2id PHC strings, the format
`authkit` already writes, so **Roostack's own accounts can be the source**:
Roostack writes `users_database.yml` from its people. Its costs:

- Adding or changing a client needs a restart (no documented reload).
  Sessions are kept in memory unless a Redis/Valkey container is added, so a
  restart signs everyone out of forward-auth apps.
- People enrol their second factor **in Authelia**, separately from
  Roostack's, and enrolling needs a one-time code sent by email (or written
  to a file Roostack would have to show).
- **Theming is weak:** a logo, a favicon and light, dark or grey. No colours,
  no CSS.
- Its OIDC provider is still labelled beta; 4.40 (in progress) changes the
  consent settings.

**Pocket ID.** The best API of the small ones (clients, people, groups and
allowed groups, all with IDs we choose) and a pleasant look. But it signs in
**with passkeys only**, by design: no passwords. That's a hard sell for a
family member on an old phone. It has no forward auth (a second component,
tinyauth, is needed) and one main developer.

**Kanidm.** Rigorous and declarative, but it wants TLS on its own listener,
has no forward auth, and its strict OAuth rules break several apps until
per-client exceptions are set.

**VoidAuth.** On paper the closest match (OIDC, forward auth, passkeys,
invitations, theming by env), but people and groups are managed only in its
own UI, and it is one unaudited developer's project.

**Building it into Roostack.** Roostack already has the hard, personal parts
of an identity provider: accounts, argon2id passwords, TOTP, sessions with
idle limits, rate limits, the audit log. What it lacks is the protocol:

- **Forward auth** is small: a check endpoint Caddy calls, and a short
  hand-off that gives each service's address its own cookie.
- **An OIDC provider** for the code flow is a larger but bounded piece:
  discovery, an authorise endpoint, a token endpoint (code with PKCE, refresh
  tokens), userinfo, the signing keys. It means Roostack owns exact redirect
  matching, single-use codes, refresh-token rotation and key rotation, and
  answers for their bugs.
- **Go libraries:**
  - `zitadel/oidc` v3 (Apache-2.0, very active, 3.51.16 on 2026-10-15): its
    `op` package is what Zitadel itself serves. It brings about fifteen
    modules (chi, go-jose, OpenTelemetry, gorilla/securecookie, …).
  - `ory/fosite` has had no commits since mid-2025; Authelia keeps a fork.
  - The subset Roostack needs can also be written with the standard
    library, as `awssig` and `authkit` were: RS256 signing is `crypto/rsa`.

## 2. The registry's packages

The newest version of each package in `roostack-services`, checked against
each app's documentation and, where that was thin, its source at the pinned
tag.

| Package | OIDC | How it's set | Redirect path | Accounts and admins | Forward auth / headers | Must stay outside a wall |
|---|---|---|---|---|---|---|
| actual-budget 26.10.0 | yes | env (`ACTUAL_OPENID_*`) | `/openid/callback` | made at first sign-in; **the first one becomes owner**; matched by username | – | desktop app, API scripts |
| audiobookshelf 2.37.1 | yes | **database**: UI or `PATCH /api/auth-settings` | `/auth/openid/callback` (+ mobile) | made; matched by email; group `admin`/`user` sets the role | – | mobile apps, feeds, shares |
| forgejo 15.0.10 | yes | **command**: `forgejo admin auth add-oauth` (+ env) | `/user/oauth2/<name>/callback` | made; linked by email; `--admin-group` | `X-WEBAUTH-USER` | git over HTTPS, API, packages |
| gitea 28.1.0 | yes | same as Forgejo | same | same | same | same |
| ghost 6.71.0 | **no** (paid Ghost(Pro) only) | – | – | – | – | the whole public site |
| hedgedoc 1.12.0 | OAuth2, no discovery | env (`CMD_OAUTH2_*`, each endpoint named) | `/auth/oauth2/callback` | made; no admins in 1.x | – | published notes |
| immich v3.3.3 | yes | config file (freezes other settings), or `PUT /api/system-config` with an admin key | `/auth/login`, `/user-settings`, mobile | made; linked by email; a role claim grants admin | – | mobile app, API, shares |
| jellyfin 12.2 | **no** (core PR closed 2026-10-02; plugin archived) | – | – | – | – | every native client |
| linkwarden 2.16.3 | yes | env (`OIDC_*`) | `/api/v1/auth/callback/oidc` | made; linked by verified email | – | API, mobile app |
| lychee 7.11.1 | named providers only | env (`<P>_CLIENT_ID`…) + a database setting | `/auth/<provider>/redirect` | creating accounts is a database setting | – | shared albums, API |
| mealie 3.29.0 | yes | env (`OIDC_*`) | `/login` | made; linked by email; **admin group** | header login (3.29) | API tokens, shared recipes |
| miniflux 2.3.3 | yes | env (`OAUTH2_*`) | `/oauth2/oidc/callback` | made; **an existing username fails** | `AUTH_PROXY_HEADER` | reader APIs (`/fever`, `/v1`, …) |
| navidrome 0.64.2 | no | – | – | – | **header login** (`ND_EXTAUTH_*`) | Subsonic apps `/rest`, shares |
| ntfy 2.31.1 | no | – | – | – | – | everything but its web page |
| paperless-ngx 3.3.2 | yes | env, but the secret is **inside a JSON value** | `/accounts/oidc/<id>/login/callback/` | made; group sync, admin group | `REMOTE_USER` | mobile apps, shares |
| papra 26.7.0 | yes | env, secret **inside a JSON value** | `/api/auth/oauth2/callback/<id>` | needs registration on | – | API, shares, mail intake |
| photoprism 261007 | yes | env (`PHOTOPRISM_OIDC_*`) | `/api/v1/oidc/redirect` | new people are **guests** (roles are paid) | – | WebDAV, shares |
| plex 1.43.4 | **no** (plex.tv accounts) | – | – | – | – | everything |
| radicale 3.8.3 | no | – | – | – | header login (anonymous without it) | every CalDAV/CardDAV client |
| stirling-pdf 3.2.0 | yes (free up to 5 people) | env (`SECURITY_OAUTH2_*`) | `/login/oauth2/code/<p>` | made | – (packaged with no accounts: a wall suits it) | API scripts |
| uptime-kuma 2.5.6 | no | – | – | one account | wall + "no sign-in" (a database setting) | status pages, push, badges |
| vaultwarden 1.37.4 | yes (since 1.35) | env (`SSO_*`) | `/identity/connect/oidc-signin` | made; linked by email; **the master password is still needed** | – | every Bitwarden app |
| wiki-js 2.5.316 | yes | **database**: GraphQL with an API key | `/login/<id>/callback` | made; groups mapped by name | – | API, guest reading |

### What that means

- **Set at install from env, easy to opt in (10):** Vaultwarden, Mealie,
  Miniflux, Linkwarden, PhotoPrism, Actual Budget, HedgeDoc, Stirling-PDF;
  Paperless-ngx and Papra once a secret can sit inside a JSON value.
- **Set by a command after the app starts (2):** Forgejo, Gitea. Their
  install hook already runs the app's admin command.
- **Set through the app's API with an admin key (3):** Immich,
  Audiobookshelf, Wiki.js. Today's packages throw the admin password away
  after setup (`install_only`), so these need a kept key first.
- **A wall in front (forward auth), with header login where the app has
  it:** Stirling-PDF as packaged, Uptime Kuma, Navidrome.
- **Not sensible:** Plex, Jellyfin, ntfy, Radicale, Ghost (native clients,
  or no way to take an identity).
- **Never wall an app whose own clients sign in themselves:** Vaultwarden,
  Immich, Forgejo, Jellyfin. They use OIDC or nothing.
- **What still needs its own password, to tell people:** Bitwarden apps
  (the master password), Subsonic apps, git tokens, mobile apps that don't
  do OIDC.
- **Pitfalls an `sso:` block must cover:**
  - an app's first admin, made by the install hook, must be the same person
    the provider names, or Miniflux refuses them and Actual makes someone
    else the owner;
  - Mealie requires `email_verified`;
  - Vaultwarden needs access tokens that last more than 5 minutes, or
    refresh tokens;
  - the app's container must reach the provider by its public name and
    trust its certificate (Gitea blocks private addresses unless allowed).

### Sources

- Identity providers:
  - https://www.authelia.com/roadmap/active/openid-connect-1.0-provider/
  - https://www.authelia.com/configuration/first-factor/file/
  - https://pocket-id.org/docs/api
  - https://github.com/kanidm/kanidm/blob/master/RELEASE_NOTES.md
  - https://github.com/voidauth/voidauth
  - https://github.com/sebadob/rauthy
  - https://tinyauth.app/docs/
  - https://github.com/dexidp/dex/releases/tag/v2.46.0
  - https://zitadel.com/docs/self-hosting/manage/production
  - https://docs.goauthentik.io/install-config/install/docker-compose/
  - https://www.keycloak.org/high-availability/multi-cluster/concepts-memory-and-cpu-sizing
  - https://github.com/lldap/lldap
  - https://github.com/zitadel/oidc
  - https://github.com/ory/fosite
- Apps:
  - https://actualbudget.org/docs/config/oauth-auth
  - https://www.audiobookshelf.org/guides/oidc_authentication/
  - https://forgejo.org/docs/v15.0/admin/command-line/
  - https://docs.gitea.com/administration/command-line
  - https://docs.hedgedoc.org/configuration/
  - https://docs.immich.app/administration/oauth
  - https://github.com/jellyfin/jellyfin/pull/17271
  - https://docs.linkwarden.app/self-hosting/sso-oauth
  - https://lycheeorg.dev/docs/getting-started/configuration/
  - https://mealie.io/documentation/getting-started/authentication/oidc-v2/
  - https://miniflux.app/docs/configuration.html
  - https://www.navidrome.org/docs/usage/integration/authentication/
  - https://docs.ntfy.sh/config/
  - https://docs.paperless-ngx.com/configuration/#authentication
  - https://docs.papra.app/guides/setup-custom-oauth2-providers/
  - https://docs.photoprism.app/getting-started/advanced/openid-connect/
  - https://radicale.org/v3.html
  - https://docs.stirlingpdf.com/Server/Security/OAuth-SSO/
  - https://github.com/dani-garcia/vaultwarden/wiki/Enabling-SSO-support-using-OpenId-Connect
  - https://github.com/requarks/wiki/blob/v2.5.315/server/modules/authentication/oidc/definition.yml
