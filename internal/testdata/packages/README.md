# Test fixtures: packages

Copies of five packages from the registry
([`roostack/services`](https://github.com/roostack/services), SPEC.md
§8.3.2), in its layout: `<name>/<version>.yaml`, `icon.png` and
`registry.yaml`. The tests own them: the generator's golden files are
rendered from them, and other tests build a catalog from them
(`internal/pkgtest`).

They don't follow the registry by themselves. Changing one here is a test
change; run `make golden` and read the diff. A package format change updates
them along with the registry.

Mealie and Stirling-PDF are a revision ahead of the registry: each has the
`sso` part single sign-on added to the package format (SPEC.md §5.2, an OIDC
app and a walled one), which the registry's copies gain once it's released.
Forgejo has its `sso` part and `on: sso` hook too (an app set by a command,
as the registry's 15.0.10-roostack.2 has them), under the version the tests
name. Immich is an older version, without single sign-on: the key-saving
path is tested with a small package of its own (`internal/node`).

`scheduled-test` isn't in the registry: BusyBox's web server with two
scheduled tasks (`on: schedule`, SPEC.md §8.1), a daily one whose settings
make it take a while or fail, and a weekly one. Install it as a package of
your own on a dev VM to try scheduled tasks there.
