# Write a package

The quickest way to write a package is to copy the one in the
[service registry](https://github.com/roostack/services/tree/main/packages)
closest to your app, and change it:

Vaultwarden
:   One container, its data in one folder, a SQLite database.

Forgejo
:   One container with a Postgres database Roostack runs for it, an extra
    connection (Git over SSH), and a setup step that creates the first
    administrator.

Immich
:   Several containers, one of them optional (a setting turns it on),
    Postgres and Redis, a GPU, and large data on the media folder.

Every field is described in the
[package format reference](../reference/packages.md). This page goes
through them in the order you'll need them.

## Name and version

```yaml
schema: roostack/v1alpha1
name: myapp
title: My App
description: What it does, in one line, for the list of services
version: 2.4.1-roostack.1
```

`name` is lowercase letters, digits and dashes. It names the service's
folders, containers and backups, so it can't change once people use it.

`version` is the app's own version, then `-roostack.` and the package's
revision, starting at 1. A better package for the same app version is a
new revision (`2.4.1-roostack.2`), in a new file; a published version is
never changed. In the registry, the file is named after its version:
`packages/myapp/2.4.1-roostack.1.yaml`.

## The project

```yaml
about:
  maker: a volunteer community
  website: https://myapp.example
  source: https://github.com/example/myapp
  licence: AGPL-3.0-only
  support: https://opencollective.com/myapp
  releases: https://github.com/example/myapp/releases/tag/v{version}
  icon: icon.png
  categories: [documents]
  keywords: [notes, wiki, markdown]
requires:
  roostack: "0.1"
```

Take the licence from the project's own licence file or notice, as an
[SPDX identifier](https://spdx.org/licenses/). For the GNU licences, say
`-only` or `-or-later` as the notice does. The logo is the project's own,
as a square PNG beside the package file, at least 256×256 pixels and at
most 100 KB. `releases` is where the project describes each version, with
`{version}` where the app's version goes (here, `2.4.1`): when a service
updates, Roostack links to what's new in the version it updates to.
`requires.roostack` is the oldest Roostack release your package works
with: the one you tried it on, unless you know better.

## Containers and images

Use the image the project publishes itself, never someone else's copy.
Pin it to the exact image by its **digest**, after the tag, so every lab
runs the image you tried:

```yaml
containers:
  app:
    image: docker.io/example/myapp:2.4.1@sha256:…
```

The digest must be the image's **multi-architecture** digest (its index),
which covers every processor it's built for. Either of these prints it:

```bash
skopeo inspect --raw docker://docker.io/example/myapp:2.4.1 | sha256sum
docker buildx imagetools inspect docker.io/example/myapp:2.4.1   # the Digest line
```

`skopeo` is in Debian (`apt install skopeo`) and Homebrew. Don't use
`podman image inspect`: it gives the digest of the build for your own
processor only. `skopeo inspect --raw` also lists the architectures:
Roostack runs on `amd64` and `arm64`, and a package should work on both
when the project builds for both.

Give each container what the app's own documentation asks for:

- `env`: settings as environment variables. Values can be
  [templates](../reference/packages.md#templates), such as
  `"{{ .Routes.web.URL }}"` for the app's own address.
- `secrets`: passwords and keys, as environment variables. Point one at a
  `secret` setting, at `databases.<name>.password`, or at
  `databases.<name>.url` for an app that takes its database only as one
  URL with the password in it (`postgresql://user:password@host:5432/name`,
  as apps using Prisma do). Roostack keeps them as Podman secrets, never
  in a file.
- `mounts`: the app's data folders, each one a `storage` entry (below).
- `health`: how Roostack tells it's working, such as
  `http: { port: 8080, path: /health }`.
- `depends_on`: what has to start first, such as `[databases.main]`.
- `memory`: what it typically uses, such as `300 MB`. Measure it once
  it's running ([Try it on a lab](try.md#measure-its-memory)).

If the app runs as a fixed user and its files must belong to that user,
add `UserNS: keep-id:uid=1000,gid=1000` under `quadlet: Container:`, as
Forgejo's package does. Only some Quadlet lines are allowed:
[what a package may not do](../reference/packages.md#what-a-package-may-not-do)
lists them, and what to use instead of the others.

## Data

Every folder the app writes to that should survive a restart is a
`storage` entry, mounted into the container:

```yaml
storage:
  data:
    title: Your notes and attachments
    class: critical
  cache:
    title: Thumbnails
    class: replaceable
```

`critical` is backed up; `replaceable` isn't, because the app makes it
again (caches, thumbnails, converted files). Getting this right is the most
important part of a package: anything people would miss must be
`critical`. `title` says what it holds, in a few words for people: the
summary before installing reads "Your notes and attachments, and its
database: backed up". Large media goes on `location: media`.

For a database, don't run your own container: declare it, and Roostack
runs it, makes its password, and backs it up consistently.

```yaml
databases:
  main:
    type: postgres
```

`postgres` and `redis` run in containers of their own; the app finds them
with `{{ .Databases.main.Host }}` and the rest. For SQLite, give the file's
place in a storage folder (`path: data/db.sqlite3`), and Roostack copies it
safely before each backup. A Redis used only as a queue or cache can say
`backup: false`.

## Its web address

```yaml
routes:
  web:
    container: app
    port: 8080
    subdomain: notes
    exposure: private
    public:
      login_paths:
        - /api/login
      private_paths:
        - path: /admin
          reason: The app's admin pages, which change every account.
```

`subdomain` is the default; people can change it. Every route starts
`private`: only the Owner makes things public. `public` says what's safe
once they do: `login_paths` are where people sign in (outside visitors get
a strict limit on tries there), and `private_paths` stay home-only, each
with the reason shown to the Owner. An address that shouldn't be public at
all (an API with no login) says why, with `unsafe`.

## Settings, setup and links

`settings` are what people choose when they install, and later on the
service's page: booleans, strings, numbers and secrets. A setting can turn
a container on or off with `when:`, as Immich's smart search does. A
`secret` with `generate: true` is made by Roostack; one with
`install_only: true` is asked for at install and deleted once setup has
run (the first administrator's password, say). A setting with an
`install_value` takes that value until the install hooks succeed, then the
one chosen: for an app that makes its first account only by signing up,
sign-ups can be on while the hook signs the Owner up, and off afterwards
(`default: false`, `install_value: true`).

`hooks` run commands inside a container: `on: install` once after the
first start, and `on: action` as a button on the service's page. Whatever
people type reaches the command only as an environment variable, and an
install hook must be safe to run twice. Installed again over its data from
before, a service may be installed with its `install_only` settings left
empty, and its install hooks don't run then. `links` point to the app's own
settings pages.

## Check it

On a Roostack server, or any Linux computer with the `roostack` command:

```bash
roostack validate myapp/2.4.1-roostack.1.yaml
```

`validate` checks every field, renders every template with the settings'
defaults and their opposites, checks the logo beside the file, and checks
the [package policy](../reference/packages.md#what-a-package-may-not-do)
every registry package follows. For a package only for your own lab, add
`-local`: what breaks the policy is then a warning, the same list Roostack
shows before installing it.

To see exactly what Roostack would generate:

```bash
roostack render -package myapp/2.4.1-roostack.1.yaml
```

It prints the Quadlet files, the web server config, the secrets, folders
and backup plan for the package with its default settings, in an example
lab at `lab.example.com`, and changes nothing. The registry shows the same
for every pull request. To try other settings, describe the service in a
small file, `service.yaml`, and add `-instance service.yaml`:

```yaml
package: myapp
version: 2.4.1-roostack.1
updates: notify
routes:
  web: { port: 20001 }   # Roostack picks one when it installs
settings:
  some_setting: true     # any you want to try other than the default
```

Once you've added the package to your lab, the same files, for your lab
and what you chose, are under **Details** on the install page.

Next: [try it on a lab](try.md).
