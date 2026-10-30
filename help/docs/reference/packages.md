# Package format

A **package** describes a service for Roostack: its containers, where its
data lives, its databases, its web address, and its settings. Roostack
generates everything else from it (the containers' units, the web server
config, secrets, folders, and the backup plan). To write one, start with
[Write a package](../packages/write.md); this page is the reference.

Packages are kept in Roostack's public registry,
[github.com/roostack/services](https://github.com/roostack/services), a
folder per package with a file per version. Roostack fetches the registry's
signed list of packages and keeps the version of each it would install in
`/var/lib/roostack/catalog/<name>/<version>.yaml`, checked against the list
([Services you can install](../settings/updates.md#services-you-can-install)).
When you install one, Roostack will copy its file into the lab, as
`/var/lib/roostack/packages/<name>/<version>.yaml`, and the service will
run from that copy: a newer Roostack with a newer package won't change it
behind your back, and it's in the lab's backups, so a restore needs nothing
else. The format is still settling (`schema: roostack/v1alpha1`).

## An example

Vaultwarden's package, slightly shortened:

```yaml
schema: roostack/v1alpha1
name: vaultwarden
title: Vaultwarden
description: Password manager compatible with the Bitwarden apps
version: 1.34.3-roostack.1          # upstream version + package revision

about:                              # the project, credited in the catalog
  maker: Daniel García and volunteer maintainers
  source: https://github.com/dani-garcia/vaultwarden
  licence: AGPL-3.0-only
  support: https://github.com/sponsors/dani-garcia
  releases: https://github.com/dani-garcia/vaultwarden/releases/tag/{version}
  icon: icon.png                    # the logo, beside this file
  categories: [passwords]
  keywords: [password manager, bitwarden, two-factor, passkeys]
built_with: [rust]                  # what the app is built on
requires:
  roostack: "0.1"                   # the oldest Roostack it works with

settings:
  - key: signups_allowed
    title: Allow anyone to create an account
    type: bool
    default: false
  - key: admin_token
    title: Admin token
    type: secret
    generate: true                  # Roostack creates it
    reveal: true                    # Admins can see it on the service page

containers:
  app:
    image: docker.io/vaultwarden/server:1.34.3@sha256:84fd8a47…
    env:
      DOMAIN: "{{ .Routes.web.URL }}"
      SIGNUPS_ALLOWED: "{{ .Settings.signups_allowed }}"
    secrets:
      ADMIN_TOKEN: admin_token
    mounts:
      - { storage: data, path: /data }
    health:
      http: { port: 80, path: /alive }
    memory: 50 MB                   # what it typically uses

storage:
  data:
    title: Passwords, accounts and settings
    class: critical                 # backed up

databases:
  main:
    type: sqlite
    path: data/db.sqlite3

routes:
  web:
    container: app
    port: 80
    subdomain: vault                # the default; people can change it
    exposure: private
    public:                         # when the Owner makes it public
      private_paths:
        - path: /admin
          reason: Vaultwarden's admin panel, which changes every account.
      login_paths:
        - /identity/connect/token
```

## The parts

`version`
:   The app's own version and the package's revision, such as
    `1.34.3-roostack.1`. It names the lab's copy of the file, so it's
    letters, digits, dots, dashes, underscores, and plus signs.

`about`
:   Facts about the project the package runs, shown in the catalog and on
    roostack.cloud/services, and checked when the package is reviewed:

    - `maker`: who makes the app, in a few words.
    - `website` and `source`: the project's site and where its code is
      published, both `https://` addresses.
    - `licence`: the app's licence as an
      [SPDX expression](https://spdx.org/licenses/), such as
      `AGPL-3.0-only` or `MIT OR Apache-2.0`. Say `-only` or `-or-later`
      for the GNU licences, as the project's licence notice does. For an
      app whose code isn't published, `closed`: it then has a `website`
      and no `source`.
    - `support`: the project's own page for donating or sponsoring.
    - `releases`: where the project says what's new in each version, an
      `https://` address with `{version}` where the app's version goes
      (the version without `-roostack.N`), or one page for every version.
      A service's update links to it as **What's new in** *version*.
    - `icon`: the project's logo, a PNG file beside the package file
      (`icon.png` in the registry): square, at least 256×256 pixels and at most 100 KB.
    - `categories`: one to three of `photos` (Photos and videos), `media`
      (Films, TV and music), `documents` (Documents and notes),
      `productivity` (Tasks and planning), `home` (Smart home and
      cameras), `passwords` (Passwords and security), `development`
      (Developer tools), `publishing` (Websites and blogs), and `network`
      (Network).
    - `keywords`: up to ten words or short phrases people might search
      for.

`built_with`
:   What the app is built on, as short names: its language (`php`,
    `node`, `python`, `java`, `go`, `rust`), its web server if it has
    its own (`nginx`, `frankenphp`), and frameworks or libraries known
    for security problems (`laravel`, `spring`, `log4j`, `imagemagick`).
    Not every library it uses: up to twenty names, each lowercase
    letters, digits and dashes. If one of them has a security problem,
    the Owner will be able to pause public access to every service built
    with it at once. Roostack will add the databases it runs for the app
    by itself (`postgres`, `mysql`, `mariadb`, `sqlite`, and `valkey` for
    a `redis` database on Roostack's own image). In the service registry,
    each name must be on its list of names (`built-with.yaml`), which
    gives it the title Roostack shows (PHP for `php`); a name that isn't
    there yet is added to the list in the same change.

`requires`
:   `roostack`: the oldest Roostack release the package works with, such as
    `"0.6"` or `"0.6.1"`. A lab running an older one won't offer it and
    will say it needs a newer Roostack. Releases are compared number by
    number (`0.10` is newer than `0.9`).

`upgrade_notes`
:   What to know before updating, shown before a service updates. Each
    note has the package `version` it arrived in and the `note` itself; a
    package keeps the notes it still needs from earlier versions, so
    someone updating past several versions at once will see all of them:

    ```yaml
    upgrade_notes:
      - version: 2.0.0-roostack.1
        note: Version 2 moves sharing links to a new address; send new ones to people you shared with.
    ```

`settings`
:   What people choose, at install and later. `type` is `bool`, `string`,
    `int`, or `secret`. A secret can be `generate`d by Roostack, `reveal`ed
    to Admins, or `install_only` (deleted once setup has run). Secrets are
    Podman secrets, never in files. A setting that isn't a secret can have
    an `install_value`, used until the install hooks succeed (or while
    they've failed and are waiting for Try again); the package needs an
    install hook for it. A secret with `from_hook: true` is made by the app
    itself and handed to Roostack by a hook that `saves` it (below): a key
    Immich makes for its own settings, say. It's never shown, only hooks
    may use it, and the package needs an action that saves it too, for
    services installed before it or whose key was lost.

`containers`
:   Each container: an `image` (pinned by tag and digest), `command`,
    `env` (templates), `secrets` (as environment variables), `mounts` of
    storage or, with `{ libraries: true, path: /media }`, of the media
    libraries the Owner chose for the service (each in a folder of its
    name under the path, read-only, never backed up), `devices` (such as a GPU), a `health` check, `depends_on`,
    and `when` (a template: the container will be left out unless it renders
    `true`). `memory` is what it typically uses, such as `300 MB` or
    `1.5 GB` (1 GB is 1000 MB): an estimate for the summary shown before
    installing, which adds up the containers that would run with the
    settings chosen. It isn't a limit. `quadlet` passes extra Quadlet keys
    straight through: only the ones
    [the package policy allows](#what-a-package-may-not-do).

`storage`
:   Folders the service owns. `title` says what one holds, in a few words
    for people ("Your photos and videos"), for the summary shown before
    installing. `class` is `critical` (backed up) or `replaceable` (not,
    such as caches and thumbnails); `location` is `service` (under
    `/srv/roostack/services`) or `media` (under `/srv/roostack/media`, for
    large data).

`databases`
:   Databases Roostack runs and backs up consistently: `sqlite` (a file in
    the service's storage), `postgres`, `mysql`, `mariadb`, or `redis` (a
    container of their own). Roostack will make their passwords. A
    container's `secrets` can point at a Postgres, MySQL, or MariaDB
    database's `databases.<name>.password`, or at a Postgres database's
    `databases.<name>.url`: the whole URL, password included, which
    Roostack will make from the password. Roostack's images are Postgres
    17, MySQL 8.4, and MariaDB 11.8 (their long-term-support lines), and
    Valkey for `redis`; `image` picks another. The containers take a
    `memory` like a container's; otherwise Roostack expects 200 MB for
    Postgres, 500 MB for MySQL, 150 MB for MariaDB, and 50 MB for Redis.

    A `mysql` or `mariadb` database can have more `schemas` on the same
    server, for an app that wants several for one user (Seafile wants
    three): `schemas: [ccnet, seahub]`. Roostack will name each after the
    database, `<database>_<schema>` (`seafile_ccnet`), make the ones that
    aren't there each time the database starts, so a later version of
    the package can add one, and back them up and restore them with the
    database, all at the same moment. Templates find their names with
    `{{ .Databases.main.Schemas.ccnet }}`. A schema's name has lower-case
    letters, digits, and `_`, at most 31 characters. A package that no
    longer lists one leaves it on the server, but it isn't backed up any
    more. Postgres has none: an app that wants several Postgres databases
    gets one `databases` entry for each.

`routes`
:   Web addresses: which container and port, the default `subdomain`,
    `exposure` (`private` or `public`), `max_body_size`, and
    `client_ip_header`: Roostack's proxy sends the visitor's address in
    `X-Forwarded-For`; an app that reads another header (Vaultwarden reads
    `X-Real-IP`) names it here, and the proxy will set it too, replacing any
    the visitor sent. It must be an `X-` header other than `X-Forwarded-*`.
    `public` says what's safe once the Owner makes the address public:
    `unsafe` (why the whole address shouldn't be), `private_paths` (paths,
    and everything under them, that will stay home-only, each with its
    reason), and `login_paths` (where people sign in, which get a strict
    rate limit for outside visitors). The Owner can still open an unsafe
    address or path, after a warning that gives the package's reason. Paths
    are plain: they start with `/` and have letters, digits, and `- _ . :`
    between the slashes, with no `*` or trailing `/`.
    [Monitoring](../servers/monitoring.md) asks each route every minute: the
    container's `health.http` path when the route leads to that port, or
    `/`, and any answer below 500 is up. `monitor` changes that for an app
    whose usual answer doesn't say it works: a `path` to ask instead, and
    the `statuses` that count as up, e.g.
    `monitor: { path: /api/health, statuses: [200] }`.

`ports`
:   [Extra connections](../services/settings.md#extra-connections) that
    aren't web: a container's port published on the server, off unless
    `enabled`.

`hooks`
:   Commands run inside a container: `on: install` once after the first
    start (such as creating an admin account), `on: action`, a button on
    the service page with `inputs`, `on: sso`, run when single sign-on
    is turned on, changed (the lab's address or name), or off, for an app
    set by a command or its own API (below), or `on: schedule`, a
    [scheduled task](#scheduled-tasks). Values people type reach the
    command only as environment variables, and any secret it's given is
    replaced by `[secret]` in what it prints before that's shown.

    An install hook or action with `saves: <setting>` hands Roostack a key
    the app made: it prints the key, and only the key, on standard output,
    which becomes that `from_hook` secret and is never shown or logged; it
    prints everything else on standard error, its log.

    An `on: sso` hook gets `ROOSTACK_SSO=on` or `off`, and must be safe to
    run again: it runs again whenever what it's given changes, and once
    more, with `off`, when single sign-on is turned off. Its `env` may use
    `.SSO`; an OIDC app's may have `sso.client_secret` in its `secrets`,
    which is empty while it's off. It runs once the service has started
    and its install hooks are done, retried for up to 10 minutes; the
    service's Single sign-on card shows its last run.

`links`
:   Links to the app's own settings pages, shown on the service page.

`sso`
:   How the app takes [single sign-on](../services/single-sign-on.md),
    which the Owner turns on per service. One of:

    - `oidc`: the app signs people in through OIDC itself. `route` is the
      route whose address `redirect_paths` are on (exact paths, such as
      `/login`); `redirect_uris` are its phone apps' own addresses (such as
      `app.immich:///oauth-callback`), never `http` or `https`. The app is
      told only what its scopes ask for: `profile` for `name` and
      `preferred_username`, `email` for `email`, `groups` for `groups`. If
      it maps groups to its admins, check it asks for `groups` (Mealie does
      once `OIDC_ADMIN_GROUP` is set).
    - `forward_auth`: the app will go behind the wall on `route`: everyone
      will sign in on the lab's sign-in page before a request reaches it.
      `bypass_paths` will stay outside it, each with the `reason` it's
      safe. Its containers may read who someone is from the `Remote-User`,
      `Remote-Email`, `Remote-Name`, and `Remote-Groups` headers; Roostack
      removes any a visitor sends.

    `env` (container, then variable) is added to containers only while
    single sign-on is on, replacing their own value of the same name; its
    templates have `.SSO` (below). `secrets` gives a container the app's
    client secret: `OIDC_CLIENT_SECRET: sso.client_secret`. `secret_env`
    is for an app that takes the secret inside a bigger value, such as
    Paperless-ngx's JSON list of providers: a template using
    `{{ json .SSO.ClientSecret }}`, which Roostack will make as a Podman
    secret and pass as that variable, never written into a file. `note`
    says what still uses the app's own sign-in, shown beside the switch.

    A redirect path may use `.SSO` (Forgejo's names its sign-in source:
    `/user/oauth2/{{ pathescape .SSO.ProviderName }}/callback`).
    `oidc.groups` renames the two groups the app is told about, for an app
    that expects its own words: `{ users: user, admins: admin }` for
    Immich, which makes someone its admin when the claim holds `admin`.

    Two fields say what the app does, for the service's Single sign-on
    card, which claims neither unless the package says so:
    `oidc.admins: true` if the app will make the lab's Owners and Admins
    its admins (from `groups`), and `oidc.existing_accounts` for what it
    will do when someone who signs in already has an account in it:
    `email` (use the one with their email address), `username`,
    `username_or_email` (their username, or else their email address), or
    `separate` (make a new one beside it). Check both on a lab before
    saying so, since the card will tell people to check their details on
    People before they turn it on.

    How each kind of app is connected:

    | The app takes its settings | The package has |
    |---|---|
    | from env (Mealie, Vaultwarden) | `env` and `secrets` |
    | from its admin command (Forgejo, Gitea) | an `on: sso` hook running the command |
    | only through its API (Immich, Audiobookshelf, Wiki.js) | an `on: sso` hook calling the API with a `from_hook` key the app made at install (an install hook that `saves` it, and an action that makes it again) |
    | as a value holding the secret (Paperless-ngx, Papra) | `secret_env` |

    ```yaml
    sso:
      oidc:
        route: web
        redirect_paths: [/login]
        admins: true                # from OIDC_ADMIN_GROUP below
        existing_accounts: email
      env:
        app:
          OIDC_AUTH_ENABLED: "true"
          OIDC_CONFIGURATION_URL: "{{ .SSO.DiscoveryURL }}"
          OIDC_CLIENT_ID: "{{ .SSO.ClientID }}"
          OIDC_ADMIN_GROUP: "{{ .SSO.AdminGroup }}"
      secrets:
        app:
          OIDC_CLIENT_SECRET: sso.client_secret
      note: Phone apps still sign in with the app's own password.
    ```

`config_files`
:   Files the app reads its settings from, for an app that takes them
    from a file rather than its environment. Roostack will write each one
    from its template and the service's settings, inside one of the
    service's storage folders, and write it again whenever they change
    (a setting, single sign-on turned on or off, a new address), then
    restart the containers that mount that storage. Each has a name of
    its own and:

    - `title`: what it is, in a few words ("Seafile's web settings").
    - `storage` and `path`: the storage entry it's in, which a container
      must mount, and where inside it, such as `conf/settings.yaml`.
    - `format`: `python`, `yaml`, `json`, or `text`. It decides how a
      secret is written: quoted as JSON writes a string, which Python,
      YAML, and JSON all read, or as it is in `text`. A `python` or `yaml`
      file starts with a comment saying Roostack writes it. `roostack
      validate` checks that a `yaml` or `json` file is valid once
      rendered.
    - `block: true`, for an app that makes the file itself and keeps its
      own values in it, such as Seafile's `seahub_settings.py` with its
      `SECRET_KEY`: the template is then Roostack's lines at the end of
      the app's file, between `# BEGIN Roostack` and `# END Roostack`, and
      Roostack leaves the rest alone. Roostack will add them once the app
      has made the file on its first start, then restart it, and the
      service's install hooks wait for that. Only `python` for now, where
      a later line wins over an earlier one.
    - `content`: the template. Besides what other templates use, it can
      use the service's secrets: `{{ .Secrets.<key> }}` for a secret
      setting, and `{{ .SSO.ClientSecret }}` while single sign-on is on.
      Roostack will fill them in on the server as it writes the file, so
      they're never in what it shows (the service's page, the summary
      before installing, `roostack render`), which has `‹secret: …›` in
      their place. Don't quote them yourself. An `install_only` secret
      will be in the file only until setup has finished: Roostack will
      then delete it and write the file again with `""` in its place.

    The file is written as Roostack's user, which is the container's root
    (or its user with `UserNS=keep-id`), and only that user can read it.
    So the app must read it as root, or as the user `keep-id` maps. Give
    an app that changes its data folder's owner at start a storage entry
    of its own for the file, mounted `readonly`. Use it for what the app
    can't take from its environment.

    ```yaml
    config_files:
      settings:
        title: Pingvin Share X's settings
        storage: config          # mounted read-only at /opt/app/config
        path: config.yaml
        format: yaml
        content: |
          general:
            appUrl: {{ json .Routes.web.URL }}
          security:
            allowRegistration: {{ if .Settings.signups_allowed }}"true"{{ else }}"false"{{ end }}
          initUser:
            enabled: true
            email: {{ json .Settings.admin_email }}
            password: {{ .Secrets.admin_password }}
    ```

## Scheduled tasks

A hook with `on: schedule` is upkeep the app's image doesn't schedule
itself, such as Seafile's garbage collection:

```yaml
hooks:
  - on: schedule
    name: free-space            # in its address and the service's history
    title: Free up space
    description: Removes blocks that no file or version uses any more.
    container: app
    every: week                 # day, week, or month
    timeout: 2h                 # optional: an hour without it, 6h at most
    command: [/scripts/gc.sh]
```

`every` says how often, and Roostack picks the time: at night, after the
server's backup and OS updates and before its restart window, one task at
a time. A weekly task gets its own weekday and a monthly one its own day
(from 1 to 28), so a lab's tasks don't all run on the same night. A task
runs only while the service is running and has finished setting up; one
that didn't run on its night (the server was off, or the service stopped)
will run the next night. It's never run twice at once.

Like other hooks, it's a command in one of the service's containers, given
settings only as `env` and `secrets`, with no `inputs`; it doesn't `saves`
a secret. A task still running after its `timeout` will be
interrupted, then Roostack will restart the container it ran in, so that
nothing it started keeps running; the run counts as failed. It must be safe to run at any time the app is running,
including straight after a restore. The service page shows each task's
last run with **Run now**, and two failures in a row go under **Needs
attention**. A Roostack from before scheduled tasks won't accept the
package, so its `requires.roostack` must name a release that has them.

## What a package may not do

Every package in the registry follows a **package policy**, which Roostack
checks itself rather than leaving it to reviewers. It stops a package
reaching beyond its own service: other services' data and secrets, the
server's files, or the internet before the Owner says so. `roostack
validate` reports each problem with what to use instead.

| Not allowed | Use instead |
|---|---|
| An image without a digest (`app:1.2`) | `app:1.2@sha256:…`, so every lab runs the image that was reviewed |
| A route or port with `exposure: public` | `private`: only the Owner can make things public |
| A route with neither `public.login_paths` nor `public.unsafe` | The pages where people sign in, or why the address shouldn't be public |
| No `about.maker`, `about.licence`, `about.icon`, or `about.categories`; no `about.source` (or, for `licence: closed`, no `about.website`) | The facts, so the project is credited and reviewers can check them |
| A licence such as `GPL-3.0` that doesn't say `-only` or `-or-later` | The identifier the project's licence notice gives |
| No `requires.roostack` | The oldest Roostack release the package works with |
| A container without `memory` | What it typically uses, for the summary shown before installing |

In `quadlet:`, a key is letters and digits only, starting with a letter,
and a value can't contain a line break or end in a backslash: anything
else could add a line of its own to the file Roostack writes. These lines
(in any case) are refused:

| Quadlet line | Use instead |
|---|---|
| `Volume=` with a host folder or a named volume, `Mount=` other than a single `type=tmpfs`, `Rootfs=` | `storage:` and `mounts:`; an anonymous `Volume=/path` or a tmpfs is fine |
| `EnvironmentFile=`, `EnvironmentHost=`, `ContainersConfModule=`, `SeccompProfile=`, a `LogOpt=` with a path, `HealthLogDestination=` a folder | `env:` and `secrets:`: these read or write files on the server |
| `Secret=` | `secrets:` |
| `Image=`, `AutoUpdate=` | the container's `image:` |
| `PodmanArgs=`, `GlobalArgs=` | A field in the package format: ask for one |
| `AddCapability=`, `SecurityLabel…=`, `Unmask=`, `Annotation=` | Nothing: containers keep Podman's protections |
| `Network=`, `Pod=` | Nothing: each service has its own network |
| `PublishPort=` | `routes:` for web addresses, `ports:` for other connections |
| `AddDevice=`, `GroupAdd=keep-groups` | `devices:` (`gpu: intel`) |
| `User=` or `Group=` as root (`root`, `0`, `00`), `UIDMap=`, `GIDMap=`, `SubUIDMap=`, `SubGIDMap=`, `UserNS=` other than `keep-id…`, `auto`, `nomap`, or `private` | The image's own user, and `UserNS=keep-id:uid=…,gid=…` if its files need that owner |
| `ContainerName=`, `ServiceName=`, `Label=io.roostack.…` | Nothing: Roostack names and labels its containers |
| `ExecStart…`, `ExecStop…` or any other `Exec…` in `[Service]` or `[Unit]` | `command:` or `hooks:`, which run inside the container |
| In `[Service]`: `Environment…`, `PassEnvironment`, `UnsetEnvironment`, `Standard…`, `TTY…`, `User`, `Group`, `SupplementaryGroups`, `DynamicUser`, `Root…`, `PIDFile`, `TemporaryFileSystem`, any key naming a `Path`, `Directory`, `Credential`, or `Image`, and `Type`, `NotifyAccess`, `Delegate`, `Slice`, `Sockets`, `BusName`, `LogNamespace`, `PAMName`, `KeyringMode`, and security contexts | `env:` for the container's environment; the rest is how the server runs Podman, which Roostack sets |
| In `[Unit]`: dependencies and relationships (`Requires`, `Wants`, `BindsTo`, `PartOf`, `Upholds`, `Conflicts`, `Before`, `After`, `OnFailure`, `OnSuccess`, and the like), `SourcePath`, and the `…Action` keys | `depends_on:` |
| Anything in `[Install]` | Nothing: Roostack starts every service with the server |

Quadlet reads a `Label=` or `Mount=` line as several values, so each one
is checked; write them without backslashes.

Beyond those, a package may use only the keys Roostack has checked stay
inside the service, written exactly as here:

| Section | Keys |
|---|---|
| `Container` | `AddHost`, `DNS`, `DNSOption`, `DNSSearch`, `DropCapability`, `Entrypoint`, `Environment`, `Exec`, `ExposeHostPort`, `HostName`, `IP`, `IP6`, `Mask`, `NetworkAlias`, `NoNewPrivileges`, `Notify`, `PidsLimit`, `Pull`, `ReadOnly`, `ReadOnlyTmpfs`, `RunInit`, `ShmSize`, `StopSignal`, `StopTimeout`, `Sysctl`, `Timezone`, `Tmpfs`, `Ulimit`, `WorkingDir`, every `Health…` key, and, with the values above, `Volume`, `Mount`, `User`, `Group`, `UserNS`, `Label`, `LogOpt`, `GroupAdd`, `HealthLogDestination` |
| `Service` | `Restart`, `RestartSec`, `RestartSteps`, `RestartMaxDelaySec`, `RestartPreventExitStatus`, `RestartForceExitStatus`, `SuccessExitStatus`, `TimeoutSec`, `TimeoutStartSec`, `TimeoutStopSec`, `TimeoutAbortSec`, `RuntimeMaxSec`, `WatchdogSec`, `CPUQuota`, `CPUWeight`, `IOWeight`, `MemoryHigh`, `MemoryMax`, `MemorySwapMax`, `TasksMax`, `LimitNOFILE`, `LimitNPROC`, `Nice`, `OOMPolicy`, `OOMScoreAdjust` |
| `Unit` | `Documentation`, `StartLimitIntervalSec`, `StartLimitBurst` |
| `Install` | None |

Forgejo's `UserNS=keep-id:uid=1000,gid=1000` and Immich's database
`ShmSize=128m` are examples. If a package needs another key, ask for it
to be added to the policy, saying what it's for. A package of your own
may break the policy: check it with `roostack validate -local`, and see
[Add your own](../services/add.md#your-own) for adding it to your lab.

## Templates

Values in `env`, `when`, hooks, and config files are Go templates:

| Value | Example |
|---|---|
| `.Routes.<route>.Host` / `.URL` | `photos.smith.family` / `https://photos.smith.family`. Use `.URL` for an app's own address: until the lab has one, it's the local `http://localhost:<port>` (and `.Host` is `localhost`), so the app still starts |
| `.Settings.<key>` | Settings that aren't secrets |
| `.Databases.<name>.Host` / `.Port` / `.User` / `.Name` | Connection details for a database |
| `.Databases.<name>.Schemas.<schema>` | The name of one of a MySQL or MariaDB database's more schemas, e.g. `seafile_ccnet` |
| `(index .Containers "<name>").Host` | Another container of the service |
| `.Lab.Domain`, `.Service.Name` | The lab's domain, and the service's name |
| `.Lab.TimeZone` | The server's time zone, e.g. `Europe/Berlin` (`UTC` if it isn't known). Containers already run in it; this is for apps with their own time-zone setting |
| `.SSO.Enabled` | Whether single sign-on is on for the service |
| `.SSO.Issuer` / `.DiscoveryURL` | `https://login.smith.family` / its `/.well-known/openid-configuration` |
| `.SSO.AuthorizeURL` / `.TokenURL` / `.UserinfoURL` | The sign-in page's endpoints, for apps that don't read the configuration |
| `.SSO.ClientID` | The app's ID at the sign-in page: the service's name |
| `.SSO.ProviderName` / `.ButtonText` | The lab's name / `Sign in with` and the lab's name |
| `.SSO.UserGroup` / `.AdminGroup` | `users` and `admins` (or the names `sso.oidc.groups` gives): the `groups` claim (with the `groups` scope) has `users` for everyone with access, and `admins` too for the lab's Owners and Admins |
| `.SSO.ClientSecret` | The app's client secret, in `sso.secret_env` and config files only: anywhere else it would land in a file Roostack writes |
| `.Secrets.<key>` | A secret setting, in config files only, filled in on the server as Roostack writes the file |

Two functions help: `json` writes a value as a JSON string, quoted and
escaped (`{{ json .SSO.ProviderName }}`), and `pathescape` escapes it for
an address's path.

## Checking a package

```bash
roostack validate package.yaml
roostack render -package package.yaml
```

`validate` checks the package and the package policy, checks the logo
beside it, and renders every template in it. `render` shows exactly what Roostack would generate. See
[Command line](cli.md#looking-at-packages), and
[Check it](../packages/write.md#check-it) for a `service.yaml` to render
with.
