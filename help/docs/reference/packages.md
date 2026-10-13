# Package format

A **package** describes a service for Roostack: its containers, where its
data lives, its databases, its web address and its settings. Roostack
generates everything else from it (the containers' units, the web server
config, secrets, folders and the backup plan).

Packages live in `/usr/share/roostack/catalog/<name>/package.yaml`. The
format is still settling (`schema: roostack/v1alpha1`).

## An example

Vaultwarden's package, slightly shortened:

```yaml
schema: roostack/v1alpha1
name: vaultwarden
title: Vaultwarden
description: Password manager compatible with the Bitwarden apps
version: 1.34.3-roostack.1          # upstream version + package revision

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

storage:
  data:
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
    `true`). `quadlet` passes extra Quadlet keys straight through.

`storage`
:   Folders the service owns. `class` is `critical` (backed up) or
    `replaceable` (not, such as caches and thumbnails); `location` is
    `service` (under `/srv/roostack/services`) or `media` (under
    `/srv/roostack/media`, for large data).

`databases`
:   Databases Roostack runs and backs up consistently: `sqlite` (a file in
    the service's storage), `postgres` or `redis` (a container of their
    own). Roostack makes their passwords.

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
roostack render -lab lab.yaml -package package.yaml -instance service.yaml
```

`validate` checks the package and renders every template in it. `render`
shows exactly what Roostack would generate. See
[Command line](cli.md#looking-at-packages).
