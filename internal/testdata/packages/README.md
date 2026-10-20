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
