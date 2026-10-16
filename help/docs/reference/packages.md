# Package format

A **package** describes a service for Roostack: its containers, where its
data lives, its databases, its web address and its settings. Roostack
generates everything else from it (the containers' units, the web server
config, secrets, folders and the backup plan). To write one, start with
[Write a package](../packages/write.md); this page is the reference.

Packages are kept in Roostack's public registry,
[github.com/roostack/services](https://github.com/roostack/services), a
folder per package with a file per version. Roostack fetches the registry's
signed list of packages and keeps the version of each it would install in
`/var/lib/roostack/catalog/<name>/<version>.yaml`, checked against the list
([Services you can install](../settings/updates.md#services-you-can-install)).
When you install one,
Roostack copies its file into the lab, as
`/var/lib/roostack/packages/<name>/<version>.yaml`, and the service runs
from that copy: a newer Roostack with a newer package doesn't change it
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
  icon: icon.png                    # the logo, beside this file
  categories: [passwords]
  keywords: [password manager, bitwarden, two-factor, passkeys]
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
    letters, digits, dots, dashes, underscores and plus signs.

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
    - `icon`: the project's logo, a PNG file beside the package file
      (`icon.png` in the registry): square, at least 256×256 pixels and at most 100 KB.
    - `categories`: one to three of `photos` (Photos and videos), `media`
      (Films, TV and music), `documents` (Documents and notes),
      `productivity` (Tasks and planning), `home` (Smart home and
      cameras), `passwords` (Passwords and security), `development`
      (Developer tools), `publishing` (Websites and blogs) and `network`
      (Network).
    - `keywords`: up to ten words or short phrases people might search
      for.

`requires`
:   `roostack`: the oldest Roostack release the package works with, such as
    `"0.6"` or `"0.6.1"`. A lab running an older one doesn't offer it, and
    says it needs a newer Roostack. Releases are compared number by number
    (`0.10` is newer than `0.9`).

`upgrade_notes`
:   What to know before updating, shown before a service updates. Each
    note has the package `version` it arrived in and the `note` itself; a
    package keeps the notes it still needs from earlier versions, so
    someone updating past several versions at once sees all of them:

    ```yaml
    upgrade_notes:
      - version: 2.0.0-roostack.1
        note: Version 2 moves sharing links to a new address; send new ones to people you shared with.
    ```

`settings`
:   What people choose, at install and later. `type` is `bool`, `string`,
    `int` or `secret`. A secret can be `generate`d by Roostack, `reveal`ed
    to Admins, or `install_only` (deleted once setup has run). Secrets are
    Podman secrets, never in files.

`containers`
:   Each container: an `image` (pinned by tag and digest), `command`,
    `env` (templates), `secrets` (as environment variables), `mounts` of
    storage, `devices` (such as a GPU), a `health` check, `depends_on`,
    and `when` (a template: the container is left out unless it renders
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
    the service's storage), `postgres` or `redis` (a container of their
    own). Roostack makes their passwords. `postgres` and `redis` take a
    `memory` like a container's; otherwise Roostack expects 200 MB and
    50 MB.

`routes`
:   Web addresses: which container and port, the default `subdomain`,
    `exposure` (`private` or `public`) and `max_body_size`. `public` says
    what's safe once the Owner makes the address public: `unsafe` (why the
    whole address shouldn't be), `private_paths` (paths, and everything
    under them, that stay home-only, each with its reason) and
    `login_paths` (where people sign in, which get a strict rate limit for
    outside visitors). The Owner can still open an unsafe address or path,
    after a warning that gives the package's reason. Paths are plain: they
    start with `/`, with no `*` or trailing `/`.

`ports`
:   [Extra connections](../services/settings.md#extra-connections) that
    aren't web: a container's port published on the server, off unless
    `enabled`.

`hooks`
:   Commands run inside a container: `on: install` once after the first
    start (such as creating an admin account), or `on: action`, a button on
    the service page with `inputs`. Values people type reach the command
    only as environment variables.

`links`
:   Links to the app's own settings pages, shown on the service page.

## What a package may not do

Every package in the registry follows a **package policy**, which Roostack
checks itself rather than leaving it to reviewers. It stops a package
reaching beyond its own service: other services' data and secrets, the
server's files, or the internet before the Owner says so. `roostack
validate` reports each problem with what to use instead.

| Not allowed | Use instead |
|---|---|
| An image without a digest (`app:1.2`) | `app:1.2@sha256:…`, so every lab runs the image that was reviewed |
| A route or port with `exposure: public` | `private`: only the Owner makes things public |
| A route with neither `public.login_paths` nor `public.unsafe` | The pages where people sign in, or why the address shouldn't be public |
| No `about.maker`, `about.licence`, `about.icon` or `about.categories`; no `about.source` (or, for `licence: closed`, no `about.website`) | The facts, so the project is credited and reviewers can check them |
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
| `User=` or `Group=` as root (`root`, `0`, `00`), `UIDMap=`, `GIDMap=`, `SubUIDMap=`, `SubGIDMap=`, `UserNS=` other than `keep-id…`, `auto`, `nomap` or `private` | The image's own user, and `UserNS=keep-id:uid=…,gid=…` if its files need that owner |
| `ContainerName=`, `ServiceName=`, `Label=io.roostack.…` | Nothing: Roostack names and labels its containers |
| `ExecStart…`, `ExecStop…` or any other `Exec…` in `[Service]` or `[Unit]` | `command:` or `hooks:`, which run inside the container |
| In `[Service]`: `Environment…`, `PassEnvironment`, `UnsetEnvironment`, `Standard…`, `TTY…`, `User`, `Group`, `SupplementaryGroups`, `DynamicUser`, `Root…`, `PIDFile`, `TemporaryFileSystem`, any key naming a `Path`, `Directory`, `Credential` or `Image`, and `Type`, `NotifyAccess`, `Delegate`, `Slice`, `Sockets`, `BusName`, `LogNamespace`, `PAMName`, `KeyringMode` and security contexts | `env:` for the container's environment; the rest is how the server runs Podman, which Roostack sets |
| In `[Unit]`: dependencies and relationships (`Requires`, `Wants`, `BindsTo`, `PartOf`, `Upholds`, `Conflicts`, `Before`, `After`, `OnFailure`, `OnSuccess` and the like), `SourcePath`, and the `…Action` keys | `depends_on:` |
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

Values in `env`, `when` and hooks are Go templates:

| Value | Example |
|---|---|
| `.Routes.<route>.Host` / `.URL` | `photos.smith.family` / `https://photos.smith.family`. Use `.URL` for an app's own address: until the lab has one, it's the local `http://localhost:<port>` (and `.Host` is `localhost`), so the app still starts |
| `.Settings.<key>` | Settings that aren't secrets |
| `.Databases.<name>.Host` / `.Port` / `.User` / `.Name` | Connection details for a database |
| `(index .Containers "<name>").Host` | Another container of the service |
| `.Lab.Domain`, `.Service.Name` | The lab's domain, and the service's name |

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
