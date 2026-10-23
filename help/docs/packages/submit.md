# Submit it to the registry

!!! note "Not open yet"

    For now, the registry holds the Roostack project's own packages only.
    It will take packages from anyone soon. Until then, you can still
    write and use packages for your own lab, and open an issue on the
    [registry](https://github.com/roostack/services/issues) for an app
    you'd like to see.

Packages are submitted as a pull request to the registry,
[github.com/roostack/services](https://github.com/roostack/services):

1. Fork the repository and add your package as
   `packages/<name>/<version>.yaml`, with the project's logo beside it as
   `icon.png`. Don't add `registry.yaml`: the registry's maintainers write
   it (the package's tier and who maintains it).
2. Run `roostack validate` on it one last time.
3. Open a pull request. Its description is a checklist: fill it in, with
   what you noted while [trying it on a lab](try.md).

A new version of a package is a new file beside the old ones. Don't change
a version that's already published.

## What to include

The pull request asks for:

- **The Roostack version** you tried it with (at the foot of every page)
  and the **processor** of the server (`dpkg --print-architecture`:
  `amd64` or `arm64`). Say if you tried both.
- **`roostack validate`**: the line it printed (`…: ok`).
- **Install**: that it installed and started, what you added to it, and
  anything the summary before installing got wrong.
- **Public access**: if it has a web address, that the sign-in and
  private pages behaved as the package says once it was public.
- **Memory**: what `podman stats` showed for each container.
- **Backup and restore**: the line `roostack backup snapshots` printed for
  the service, and that everything you added was there after restoring
  the lab onto a fresh server.
- **Remove**: that it stopped and its data was kept.

## What happens next

The registry checks every pull request: each package is validated,
against the package policy too, and each image's registry is asked which
processors it's built for. A maintainer will then review the package
against the same checklist. Once it's merged, the registry's signed list of
packages will be rebuilt, and every lab will see the package within a few
hours.

You'll become the package's maintainer: when the app has a new release,
especially a security fix, a new version of the package is needed. A
package that falls behind on security releases will be marked as
unmaintained and withdrawn if it becomes unsafe to run.
