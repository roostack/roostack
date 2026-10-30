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

## The package editor

The **package editor** writes a package with you, in your browser:

- on [roostack.cloud](https://roostack.cloud/package-editor), or
  [downloaded](https://roostack.cloud/package-editor/roostack-package-editor.html)
  as one file that works with no network;
- in your lab, as the Owner: **Services → Add a service → Add your own →
  Write one in the package editor**.

Start from the app's **Docker Compose file** (and its `.env`, if it has
one), a package file, or one of the three above. From a Compose file, the
editor will convert what fits (images, ports, folders, environment,
databases, health checks, what starts after what) and list what it didn't,
and why: what Roostack doesn't allow (the Docker socket, a privileged
container), what Roostack does itself (restarts, the database's own
password), and what's over to you (the image's digest, memory, where people
sign in).

The form and the YAML sit side by side and follow each other; your
comments stay. Each field has its help, with suggestions you can take
in one click (a licence's `-only` or `-or-later`, sign-in paths, which
folders are backed up). **Problems** lists what `roostack validate`
would refuse (validation errors), what the service registry's package
policy would refuse (registry errors), and the suggestions; each opens its
field and its line. Tick **For my own lab only** for a package you'll only
add to your own lab: registry errors will then be allowed, as with
`roostack validate -local`.

The editor checks a package as `roostack validate` does, with one
exception: it can't render the templates, so it checks only what they
name. `roostack validate`, or **Check with this Roostack** in your lab,
does the rest.

## Name and version

```yaml
schema: roostack/v1alpha1
name: myapp
title: My App
description: What it does, in one line, for the list of services
version: 2.4.1-roostack.1
```

`name` is lowercase letters, digits, and dashes. It names the service's
folders, containers, and backups, so it can't change once people use it.

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
built_with: [php, laravel]
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
`built_with` says what the app is built on: its language, its own web
server if it has one, and any framework or library known for security
problems. The project's source usually shows it (a `composer.json` is
PHP, a `package.json` Node). Name a few things, not every library, and
leave out the databases: Roostack will add the ones it runs by itself.

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
  as apps using Prisma do). Roostack will keep them as Podman secrets,
  never in a file.
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

`critical` will be backed up; `replaceable` won't, because the app makes it
again (caches, thumbnails, converted files). Getting this right is the most
important part of a package: anything people would miss must be
`critical`. `title` says what it holds, in a few words for people: the
summary before installing reads "Your notes and attachments, and its
database: backed up". Large media goes on `location: media`.

For a database, don't run your own container: declare it, and Roostack will
run it, make its password, and back it up consistently.

```yaml
databases:
  main:
    type: postgres
```

`postgres`, `mysql`, `mariadb`, and `redis` run in containers of their own;
the app finds them with `{{ .Databases.main.Host }}` and the rest
(`.Port`, `.User`, `.Name`), and gets the password from
`databases.main.password` in its `secrets`. For SQLite, give the file's
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

`subdomain` is the default; people can change it. Every route will start
`private`: only the Owner can make things public. `public` says what's safe
once they do: `login_paths` are where people sign in (outside visitors get
a strict limit on tries there), and `private_paths` will stay home-only,
each with the reason shown to the Owner. An address that shouldn't be
public at all (an API with no login) says why, with `unsafe`.

## Settings, setup and links

`settings` are what people choose when they install, and later on the
service's page: booleans, strings, numbers, and secrets. A setting can turn
a container on or off with `when:`, as Immich's smart search does. A
`secret` with `generate: true` will be made by Roostack; one with
`install_only: true` will be asked for at install and deleted once setup
has run (the first administrator's password, say). A setting with an
`install_value` will take that value until the install hooks succeed, then
the one chosen: for an app that makes its first account only by signing up,
sign-ups can be on while the hook signs the Owner up, and off afterwards
(`default: false`, `install_value: true`).

`hooks` run commands inside a container: `on: install` once after the
first start, `on: action` as a button on the service's page, and `on:
schedule` every day, week, or month for upkeep the app doesn't schedule
itself (a [scheduled task](../reference/packages.md#scheduled-tasks)). Whatever
people type reaches the command only as an environment variable, and an
install hook must be safe to run twice. Installed again over its data from
before, a service may be installed with its `install_only` settings left
empty, and its install hooks won't run then. `links` point to the app's own
settings pages.

## Single sign-on

If the app can sign people in through OIDC, or has no sign-in of its own,
an `sso` part lets the Owner turn on [single
sign-on](../services/single-sign-on.md) for it with a switch. How the app
takes its OIDC settings decides what the part needs:

- **Environment variables** (Mealie, Vaultwarden): the variables in
  `sso.env`, the client secret in `sso.secrets`.
- **Its admin command** (Forgejo, Gitea): an `on: sso` hook that runs it.
  It's told `ROOSTACK_SSO=on` or `off`, and runs again whenever what it's
  given changes, so it must be safe to run again: find what it made last
  time and update it, rather than adding another. Forgejo's keeps its
  sign-in source's ID in Forgejo's own data.
- **Only its API** (Immich, Audiobookshelf, Wiki.js): an `on: sso` hook
  that calls the API with a key the app makes. Make the key in an install
  hook that `saves` it into a `from_hook` secret setting (it prints only
  the key on standard output, the rest on standard error), and add an
  action that makes one again from the administrator's password, for
  services installed before. Ask the app for the least power it offers:
  Immich's key can only read and change its settings.
- **A value holding the secret** (Paperless-ngx's JSON): `sso.secret_env`,
  with `{{ json .SSO.ClientSecret }}` where the secret goes.
- **No sign-in of its own** (Stirling-PDF): `forward_auth`, a wall in
  front of it.

The registry's Forgejo, Immich, and Paperless-ngx packages show each.
[The package reference](../reference/packages.md) has every field.

## Check it

On a Roostack server, or any Linux computer with the `roostack` command:

```bash
roostack validate myapp/2.4.1-roostack.1.yaml
```

`validate` checks every field, renders every template with the settings'
defaults and their opposites, checks the logo beside the file, and checks
the [package policy](../reference/packages.md#what-a-package-may-not-do)
every registry package follows. For a package only for your own lab, add
`-local`: what breaks the policy will then be a warning, the same list
Roostack shows before installing it.

To see exactly what Roostack would generate:

```bash
roostack render -package myapp/2.4.1-roostack.1.yaml
```

It prints the Quadlet files, the web server config, the secrets, folders,
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
