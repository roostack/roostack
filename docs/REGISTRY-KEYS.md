# The service registry's keys

How the keys that sign the service registry are made, kept, used and replaced
(SPEC.md §8.3.3). Written 2026-10-14, before the ceremony; follow it when
you're ready, and record the date and the public keys at the end.

## What there is

| Key | Where it lives | Signs | Replaced by |
|---|---|---|---|
| **Root key** | Offline, with the lab's other offline keys (two copies) | Only `keys.json`: the statement naming the signing keys | A Roostack release with a new root key built in |
| **Signing key** | A GitHub Actions secret on `roostack/services`, environment `publish` | Every `index.json`, daily and on each merge | A new `keys.json`, signed by the root key: no release |

The root key's public half is built into Roostack
(`internal/registry/rootkeys.go`). A lab trusts a signing key only because a
`keys.json` signed by the root key names it, and trusts a package file only
because an index signed by a signing key holds its SHA-256. So a leaked
signing key is fixed with a new `keys.json`; only a lost or leaked root key
needs a Roostack release.

Every `keys.json` has a serial number. A lab remembers the highest it has
seen and refuses anything lower, so once labs have fetched a new statement,
the old signing key is useless to anyone holding it.

## Before you start

- A Linux computer you trust that can go offline (a Roostack server before
  it's set up, or a Debian live USB), with a `roostack` built from a commit
  you've reviewed: `make build` puts one in `bin/roostack` (`GOARCH=arm64` for
  a Raspberry Pi). The command line only builds for Linux.
- Somewhere to keep the root key offline: the same place as the lab's other
  offline keys, in two copies (e.g. two encrypted USB drives kept apart, or a
  password manager's secure note plus one drive).
- Owner access to the `roostack/services` repository (Settings → Environments
  and Secrets).
- About half an hour.

## The ceremony

1. **Go offline.** Turn off Wi-Fi and unplug the network on the computer
   you're using. Work in a folder that isn't synced anywhere.

2. **Make the root key.**

   ```sh
   roostack registry keygen -out root.key
   ```

   It prints the public key (`ed25519:…`): write it down with today's date.
   `root.key` is the private half (it refuses to overwrite a file). Copy it to
   both offline places now.

3. **Make the signing key.**

   ```sh
   roostack registry keygen -out signing.key
   ```

   Note its public key too.

4. **Sign the statement naming the signing key.**

   ```sh
   roostack registry delegate -root root.key -serial 1 -out keys ed25519:<the signing public key>
   ```

   This writes `keys/keys.json` and `keys/keys.json.sig`. Check `keys.json`
   names the key you noted, with serial 1.

5. **Put the root key away.** Delete `root.key` from this computer once
   both offline copies are checked (open each and compare the first and last
   few characters).

6. **Go online, and give CI the signing key.** On GitHub,
   `roostack/services` → Settings → Environments → New environment
   `publish`: deployment branches "Selected branches", only `main`; add the
   secret `REGISTRY_SIGNING_KEY` with the whole contents of `signing.key`
   (the `ed25519-private:…` line). Add an environment `check` with no
   secrets, for pull requests. Then delete `signing.key`: CI holds the only
   copy, and losing it just means steps 3, 4 and 6 again.

7. **Commit the statement.** Copy `keys/keys.json` and `keys/keys.json.sig`
   into the registry repository's `keys/` folder, and commit them.

8. **Build the root key into Roostack.** In this repository, put the root
   public key into `rootKeys` in `internal/registry/rootkeys.go`, with a
   comment giving the date, and commit it. Release builds refuse to be made
   without it (`scripts/build-deb.sh` checks what `make build` compiled in,
   recorded in `bin/registry-keys`).

9. **Set up publishing**, once: in `roostack/services` → Settings → Pages,
   source "GitHub Actions" and custom domain `registry.roostack.cloud`; add
   the DNS record `registry.roostack.cloud CNAME roostack.github.io`; set the
   repository variables `ROOSTACK_BINARY_URL` (a Roostack release's
   linux/amd64 `roostack` binary, which builds the index; the address is
   in [RELEASING.md](RELEASING.md)) and `ROOSTACK_BINARY_SHA256` (its
   SHA-256, from the release's `SHA256SUMS`). Change both
   when a newer release should check the registry.

10. **Check it.** Run the Publish workflow (Actions → Publish → Run
    workflow), then, with the Roostack from step 8:

    ```sh
    roostack registry check
    ```

    It fetches the index as a lab does and prints its serial, the key
    statement's serial and the packages it checked.

11. **Record it** at the end of this file: the date, both public keys, and
    the key statement's serial.

## Replacing the signing key

When it may have leaked, someone who had access leaves, or CI was
compromised:

1. Offline, with a root key copy: `roostack registry keygen -out signing.key`.
2. `roostack registry delegate -root root.key -serial <last serial + 1> -out keys <new signing public key>`.
   Naming only the new key retires the old one. (For a planned change with
   no leak, name both for a day, then sign again with only the new one.)
3. Online: replace the `REGISTRY_SIGNING_KEY` secret, delete `signing.key`,
   commit the new `keys/` files, and let the Publish workflow run.
4. Labs pick up the new statement within a few hours (they check every 4
   hours); from then on, they refuse the old key.

## Replacing the root key

When it's lost or may have leaked: make a new root key (steps 1–2), sign a
new statement with it (step 4, the next serial), and release a Roostack with
the new public key in `rootKeys`. Labs on releases without the new key can't
fetch from the registry until they update.

For a planned change, sign statements with both keys for a while
(`-root old.key -root new.key`; a lab accepts either) and ship a release with
both in `rootKeys`; once labs have moved to it, drop the old one.

## Development keys

Tests and dev VMs use throwaway keys: `make build REGISTRY_DEV_KEY=ed25519:…`
builds a Roostack that also trusts that root key. `scripts/build-deb.sh`
refuses to make a release from such a build, or from any build until the
real root key is in `rootKeys`; a development package is made with
`ROOSTACK_DEV_BUILD=1 scripts/build-deb.sh` (`scripts/dev-install.sh` does).

## Record

| Date | What | Public key | Statement serial |
|---|---|---|---|
| | | | |
