# Releasing Roostack

How a Roostack release is built, signed and checked, and the ceremony for
the key that signs it (TASKS 1.12, "Encryption anyone can check"). Written
2026-10-18, before the first release and before the ceremony. The service
registry's keys are separate: [REGISTRY-KEYS.md](REGISTRY-KEYS.md).

The names and addresses below (file names, `dl.roostack.cloud`,
`apt.roostack.cloud`, the key's user ID) are placeholders until the Owner
confirms them.

## What a release is

A tag like `v1.2.3` on `main`. Its version is the tag without the `v`
(`1.2.3`), and it has these files:

| File | What |
|---|---|
| `roostack_<version>_amd64.deb`, `roostack_<version>_arm64.deb` | The package, as apt installs it |
| `roostack-<version>-linux-amd64`, `roostack-<version>-linux-arm64` | The `roostack` binary alone, for tools that run it outside a server (the registry's Publish workflow) |
| `SHA256SUMS` | The SHA-256 of each file above |
| `SHA256SUMS.asc` | An OpenPGP signature of `SHA256SUMS`, by the release key |

They're published at `https://dl.roostack.cloud/releases/<version>/<file>`,
and the packages in the apt repository at `apt.roostack.cloud`, by
`apt-publish.yml`, which takes them from the release workflow's
`release-files` artifact. GitHub's releases can't serve them while the
repository is private.

For the registry's Publish workflow (REGISTRY-KEYS.md, step 9):

- `ROOSTACK_BINARY_URL`: `https://dl.roostack.cloud/releases/<version>/roostack-<version>-linux-amd64`
- `ROOSTACK_BINARY_SHA256`: that file's line in the release's `SHA256SUMS`

## Making a release

1. Check `main`'s last CI run passed, and that TASKS, SPEC and the help are
   up to date. For a new series (a `.0` release), write its section in
   [help/docs/whats-new.md](../help/docs/whats-new.md), with the id
   `roostack-<series>`, and publish the help: servers on an older series
   link to it when they offer the new one.
2. Tag the commit and push the tag:

   ```sh
   git tag -a v0.1.0 -m "Roostack 0.1.0" origin/main
   git push origin v0.1.0
   ```

3. The release workflow ([release.yml](../.github/workflows/release.yml))
   runs:
   - **check:** the tag is `vMAJOR.MINOR.PATCH`, `git describe` gives
     exactly it (so one release tag per commit), its commit is on `main`,
     the release key in `packaging/keys` isn't the placeholder, and
     govulncheck finds nothing the code reaches;
   - **build:** `scripts/build-release.sh` twice, on two runners, checked
     out in two different folders;
   - **compare:** the two builds must be identical, byte for byte;
   - **sign:** waits for a reviewer to approve the `release` environment
     (Actions → the run → Review deployments), then signs `SHA256SUMS` and
     checks the signature with `gpgv` against
     `packaging/keys/roostack-release.asc`;
   - and hands the files on as the artifact `release-files`;
   - **publish:** `apt-publish.yml`, called with the version, publishes them
     at dl.roostack.cloud and in the apt repository (in the `release`
     environment too, so a reviewer approves it as well).

A release refuses to be made:

- without the registry's real root key in `internal/registry/rootkeys.go`,
  or with a development one (`scripts/build-deb.sh` checks what was
  compiled in; the release never sets `ROOSTACK_DEV_BUILD` or
  `REGISTRY_DEV_KEY`);
- from a checkout with changes, or from a commit the tag doesn't name;
- while `packaging/keys/roostack-release.asc` is the placeholder;
- if the two builds differ, or the signature doesn't check.

A tag that fails is deleted and made again once it's fixed
(`git push origin :refs/tags/v0.1.0`), as long as nothing was published.

Patch releases (`1.2.3` → `1.2.4`) install on labs unattended; a new minor
release (`1.3.0`) only tells the Owner (SPEC.md §3.2, §7). The apt
repository has a suite for each minor series to make that work
(apt-publish.yml).

## Reproducible builds

Anyone can rebuild a release from its tag and get the same files, byte for
byte, so the published binaries can be checked against the published
source rather than taken on trust. `scripts/build-release.sh` (`make
release`) fixes everything that could otherwise differ:

- **Go:** the release's Go (`GO_RELEASE`, fetched by the go tool if it's
  another), `-trimpath` (no folder names) and an empty build ID (`make
  build` does both for every build), no cgo, no `go.work`, no settings from
  the builder's go env file, the baseline processor level (`GOAMD64=v1`,
  `GOARM64=v8.0`).
- **The help site:** Zensical exactly as `help/uv.lock` pins it, refusing a
  lock that's out of date. Its output has no dates or paths in it (checked
  2026-10-18: two builds in different folders and on Python 3.12 and 3.15
  gave the same files), and it's built fresh each time.
- **The packages:** every file's time is `SOURCE_DATE_EPOCH`, the tag's
  commit time; owners are root; files and `md5sums` are sorted;
  `Installed-Size` is counted from file sizes (not `du`, which depends on
  the filesystem); xz compresses on one thread (its output differs with
  more).
- **The machine:** the release workflow builds in a Debian 13 image pinned
  by digest (the `container:` in release.yml), so the `dpkg-deb` and xz that
  make the packages are fixed too.

The binaries reproduce on any system (Go cross-compiles); the packages need
the same `dpkg-deb` and xz, so rebuild them in the same image.

**Dry run (2026-10-18),** with a throwaway release key and registry root
key, on an arm64 dev VM: two builds in the pinned image, in different
folders and time zones, gave identical files; the plain binaries also
matched ones built on macOS with the same settings; the signature, made
with GnuPG 2.4.7, checked with gpgv; the package installed and passed
`dpkg --verify`; and each refusal above was tried. The workflow itself
hasn't run on GitHub yet (its runners are amd64).

### Checking a release yourself

On any Linux machine with Podman (a Roostack server has it) or Docker, from
a checkout of the release's tag:

```sh
git clone <the repository> roostack && cd roostack
git checkout v0.1.0
# The image the release was built in: release.yml's container image, at the tag.
image=$(sed -n 's/^ *image: //p' .github/workflows/release.yml)
podman run --rm -it -v "$PWD:/src" -w /src "$image" bash
```

Then, inside the container (uv's version and SHA-256 are in release.yml
too):

```sh
apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git make
git config --global --add safe.directory /src
curl -fsSL https://go.dev/dl/go1.27.2.linux-amd64.tar.gz | tar -xz -C /usr/local
curl -fsSL https://github.com/astral-sh/uv/releases/download/0.12.22/uv-x86_64-unknown-linux-gnu.tar.gz | tar -xz -C /usr/local
export PATH=/usr/local/go/bin:/usr/local/uv-x86_64-unknown-linux-gnu:$PATH
make release
```

(on an arm64 machine, `linux-arm64` and `aarch64` in those two downloads).
Compare `dist/release/SHA256SUMS` with the published one: they should be
the same. If they aren't, [diffoscope](https://diffoscope.org) shows what
differs (`diffoscope roostack_0.1.0_amd64.deb dist/release/roostack_0.1.0_amd64.deb`).

### Checking a download

```sh
gpg --dearmor < roostack-release.asc > roostack-release.gpg
gpgv --keyring ./roostack-release.gpg SHA256SUMS.asc SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
```

`roostack-release.asc` is `packaging/keys/roostack-release.asc`; check its
fingerprint (`gpg --show-keys roostack-release.asc`) against the one
recorded below and on roostack.cloud. Servers don't need this: apt checks
every package against the keyring the package installs
(`/usr/share/keyrings/roostack-archive-keyring.gpg`).

## The release key

One OpenPGP key, Ed25519, signs releases (`SHA256SUMS.asc`) and the apt
repository:

| Part | Used for | Where it lives | Expires |
|---|---|---|---|
| **Primary key** | Only certifying: adding, extending and revoking subkeys | Offline, in two copies, with a passphrase | Never |
| **Signing subkey** | Signing | The `release` environment's secret `ROOSTACK_RELEASE_SIGNING_KEY` on GitHub, without a passphrase | After 3 years; renewed a year before |
| **Public key** | Checking | `packaging/keys/roostack-release.asc`; the package's keyring; roostack.cloud | |

Labs trust the public key because their package installed it, and they get
its updates (a new subkey, a longer expiry, a revocation) with Roostack's
updates. So a change to the key reaches labs only once a release carrying
it is installed: add a new subkey well before it's needed.

**No passphrase on the CI copy (decided 2026-10-18):** a passphrase would be
a second secret in the same environment, read by the same job, so anyone
who could take the key could take it too; it would protect nothing that
GitHub's encrypted secrets and the environment's reviewer don't. The
primary key, which can make new subkeys, does have one, and never leaves
the offline copies. Considered: a passphrase secret
(`ROOSTACK_RELEASE_SIGNING_PASSPHRASE`), for the export file's trip from
the offline computer to GitHub; deleting the file once it's in the secret
does as much.

**Why a subkey on GitHub, not the primary key:** a leaked subkey is revoked
and replaced with the primary key, and labs keep the same trusted key; a
leaked primary key means every lab must be given a new key by hand or
through a release signed with the old one.

### The ceremony

You need:

- a Linux computer you trust that can go offline (a Debian live USB is
  good), with GnuPG 2.2 or newer;
- the two offline places that hold the registry's root key
  (REGISTRY-KEYS.md);
- admin access to the Roostack repository on GitHub;
- a password manager for the primary key's passphrase;
- about half an hour.

1. **Go offline**, and keep the keys in memory only:

   ```sh
   export GNUPGHOME=$(mktemp -d -p /dev/shm)
   ```

2. **Make the primary key**, for certifying only, without a passphrase for
   now (it's added in step 5, once the subkey for CI is exported without
   one):

   ```sh
   gpg --batch --passphrase '' --quick-generate-key 'Roostack releases <releases@roostack.cloud>' ed25519 cert never
   gpg --fingerprint
   ```

   Write down the fingerprint (40 hex characters) with today's date. GnuPG
   also wrote a revocation certificate to
   `$GNUPGHOME/openpgp-revocs.d/<fingerprint>.rev`.

3. **Add the signing subkey**, valid for 3 years:

   ```sh
   gpg --batch --passphrase '' --quick-add-key <fingerprint> ed25519 sign 3y
   gpg --list-keys --with-subkey-fingerprints
   ```

   Note the subkey's fingerprint (the second one).

4. **Export the public key and the subkey for CI.** The `!` exports only
   that subkey; the primary key's secret stays out:

   ```sh
   gpg --armor --export <fingerprint> > roostack-release.asc
   gpg --armor --export-secret-subkeys '<subkey fingerprint>!' > signing-subkey.asc
   ```

5. **Give the primary key a passphrase** (from the password manager), and
   make the offline copies:

   ```sh
   gpg --passwd <fingerprint>
   gpg --armor --export-secret-keys <fingerprint> > roostack-release-secret.asc
   ```

   Copy `roostack-release-secret.asc`, the revocation certificate and
   `roostack-release.asc` to both offline places. The revocation
   certificate revokes the whole key without the passphrase: keep it as
   carefully as the key.

6. **Go online, and give CI the subkey.** On GitHub, the repository →
   Settings → Environments → New environment `release`:
   - Required reviewers: the Owner (and anyone else who may release).
     Leave "Prevent self-review" off while the Owner releases alone.
   - Deployment branches and tags: Selected; add the tag rule `v*`.
   - Environment secret `ROOSTACK_RELEASE_SIGNING_KEY`: the whole of
     `signing-subkey.asc`.

   Then delete `signing-subkey.asc` and the `$GNUPGHOME` folder: CI holds
   the only online copy, and the offline copies can make another.

7. **Commit the public key:** replace `packaging/keys/roostack-release.asc`
   with `roostack-release.asc`, add the fingerprint to the record below,
   and commit. Until then every release refuses to run.

8. **Publish the fingerprint** where people find the install command: the
   help site's install page, roostack.cloud, and the record below. (Uploading
   the key to keys.openpgp.org is optional; it checks the email address.)

9. **Check it** with the first release: the sign job's log ends with gpgv's
   "Good signature", and the check under "Checking a download" passes on
   the published files.

### Renewing the signing subkey

Every two years, a year before the subkey expires, offline with an offline
copy of the primary key:

```sh
export GNUPGHOME=$(mktemp -d -p /dev/shm)
gpg --import roostack-release-secret.asc
gpg --quick-add-key <fingerprint> ed25519 sign 3y
gpg --armor --export <fingerprint> > roostack-release.asc
gpg --armor --export-secret-keys <fingerprint> > roostack-release-secret.asc
```

1. Commit the new `roostack-release.asc` (it now has both subkeys) and make
   a release with it, signed by the old subkey as usual. Update the
   offline copies.
2. Once that release has reached labs (a few weeks: patch releases install
   by themselves), export the new subkey (step 4 of the ceremony, with its
   fingerprint) and replace `ROOSTACK_RELEASE_SIGNING_KEY`.

GnuPG signs with the newest signing subkey it has, so the secret holds
only the one in use.

Extending a subkey instead (`gpg --quick-set-expire <fingerprint> 2y
'<subkey fingerprint>'`) also works, through the same commit and release.

### If the signing subkey leaks

1. Offline, revoke it and add a new one:

   ```sh
   gpg --import roostack-release-secret.asc
   gpg --edit-key <fingerprint>     # key N (the leaked subkey), revkey, save
   gpg --quick-add-key <fingerprint> ed25519 sign 3y
   ```

2. Commit the new `roostack-release.asc` (the old subkey revoked, the new
   one added) and release at once, with the secret still holding the old
   subkey: labs know only that one, so it's the only signature they can
   check, and the keyring that release installs revokes it.
3. Then replace `ROOSTACK_RELEASE_SIGNING_KEY` with the new subkey, update
   the offline copies, and say what happened on roostack.cloud. Labs trust
   the leaked subkey until they install that release.

### If the primary key is lost or leaks

- **Lost** (both copies): the signing subkey keeps working until it
  expires, but nothing can renew or revoke it. Make a new key (the
  ceremony), and ship its public key alongside the old in a release signed
  by the old subkey; then sign with the new one.
- **Leaked:** the same, and publish the old key's revocation certificate in
  the release that ships the new key, so labs stop trusting it.

## Record

| Date | What | Fingerprint |
|---|---|---|
| | | |
