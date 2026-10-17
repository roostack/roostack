#!/usr/bin/env bash
# Builds the Roostack package (TASKS 1.12): dist/roostack_<version>_<arch>.deb,
# for the architecture the binaries in bin/ were built for (amd64, or arm64
# after `GOARCH=arm64 make build`; ARM64-PLAN.md 1.2). Run after
# `make build`. Uses dpkg-deb when it's there (Debian, CI);
# otherwise assembles the archive with ar and tar, which is all a .deb is.
#
# Reproducible (docs/RELEASING.md): the same binaries and commit make the
# same .deb, byte for byte. Every file's time is SOURCE_DATE_EPOCH (the
# commit's time unless it's set), owners are root, lists are sorted, and
# xz runs on one thread (its output differs with threads).
set -euo pipefail
umask 022

REPO=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO"
# Debian versions must start with a digit: v0.1.0-3-gabc → 0.1.0-3-gabc,
# and a bare commit becomes 0~git<commits>.<commit>: the number of commits
# keeps later builds sorting after earlier ones (commit hashes don't).
# VERSION, if set, is used as it is (dev-vm.sh passes the dev machine's,
# since the VM's copy has no git history). --version prints it and stops.
if [[ -z ${VERSION:-} ]]; then
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
    VERSION=${VERSION#v}
    [[ $VERSION =~ ^[0-9]+\.[0-9] ]] || VERSION="0~git$(git rev-list --count HEAD 2>/dev/null || echo 0).${VERSION}"
    # Uncommitted changes: a time, so each build is newer than the last.
    [[ $VERSION == *-dirty ]] && VERSION="${VERSION}.$(date +%Y%m%d%H%M%S)"
fi
[[ ${1:-} == --version ]] && { echo "$VERSION"; exit 0; }
# The time every file in the package gets: the commit's, or now for a
# copy with no git history (a dev VM's).
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}
export SOURCE_DATE_EPOCH
[[ -x bin/roostack && -x bin/roostack-agent && -s bin/copyright ]] || { echo "build first: make build" >&2; exit 1; }
# A package trusts the registry's real root key, and no development key
# (SPEC.md §8.3.3, docs/REGISTRY-KEYS.md), whatever its version, unless
# it says it's a development build (ROOSTACK_DEV_BUILD=1, as
# dev-install.sh does): those can't fetch the real registry until the key
# ceremony. The keys are the ones make build compiled in, which it
# records in bin/registry-keys, not a search of the source.
if [[ ${ROOSTACK_DEV_BUILD:-} != 1 ]]; then
    [[ -s bin/registry-keys && ! bin/roostack -nt bin/registry-keys ]] ||
        { echo "bin/registry-keys is missing or older than bin/roostack: make build again" >&2; exit 1; }
    grep -qx 'root_keys=[1-9][0-9]*' bin/registry-keys ||
        { echo "a release needs the registry's root key in internal/registry/rootkeys.go (docs/REGISTRY-KEYS.md); for a development build, set ROOSTACK_DEV_BUILD=1" >&2; exit 1; }
    grep -qx 'dev_key=false' bin/registry-keys ||
        { echo "bin/roostack was built with REGISTRY_DEV_KEY: make build without it for a release" >&2; exit 1; }
fi
# A released package always has its help (make build skips it without uv).
[[ -s internal/web/helpsite/site/index.html ]] || { echo "the help site isn't built: install uv, then make build" >&2; exit 1; }
# The architecture, from the ELF header's machine field (byte 18), so this
# runs without Go (dev-install.sh runs it in a VM). Both binaries must agree.
elf_arch() {
    case $(od -An -tx1 -j18 -N1 "$1" | tr -d ' ') in
    3e) echo amd64 ;;
    b7) echo arm64 ;;
    *) echo "$1: not a Linux amd64 or arm64 binary" >&2; exit 1 ;;
    esac
}
ARCH=$(elf_arch bin/roostack)
[[ $(elf_arch bin/roostack-agent) == "$ARCH" ]] || { echo "bin/roostack and bin/roostack-agent are for different processors: make build again" >&2; exit 1; }
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
ROOT=$STAGE/root

install -D -m 0755 bin/roostack "$ROOT/usr/bin/roostack"
install -D -m 0755 bin/roostack-agent "$ROOT/usr/bin/roostack-agent"
install -D -m 0644 packaging/systemd/roostack.service "$ROOT/usr/lib/systemd/system/roostack.service"
install -D -m 0644 packaging/systemd/roostack-agent.service "$ROOT/usr/lib/systemd/system/roostack-agent.service"
install -D -m 0644 packaging/apt/52roostack-unattended-upgrades "$ROOT/etc/apt/apt.conf.d/52roostack-unattended-upgrades"
install -D -m 0644 build/caddy/Containerfile "$ROOT/usr/share/roostack/caddy/Containerfile"
install -D -m 0755 packaging/deb/installed-message "$ROOT/usr/share/roostack/installed-message"
# The starting lab.yaml: no address yet (SPEC.md §6.6).
install -D -m 0644 packaging/deb/lab.yaml "$ROOT/usr/share/roostack/lab.yaml"
# Licences of everything compiled into or served by Roostack (scripts/copyright.sh).
install -D -m 0644 bin/copyright "$ROOT/usr/share/doc/roostack/copyright"
for doc in README.md ARCHITECTURE.md SPEC.md THIRD_PARTY.md; do
    install -D -m 0644 "$doc" "$ROOT/usr/share/doc/roostack/$doc"
done

CONTROL=$STAGE/control
install -d "$CONTROL"
sed -e "s/@VERSION@/$VERSION/" -e "s/@ARCH@/$ARCH/" packaging/deb/control > "$CONTROL/control"
# In KiB, counted as dpkg-gencontrol does (each file rounded up, 1 for
# anything else), not with du, whose answer depends on the filesystem.
find "$ROOT" -mindepth 1 \( -type f -printf '%s\n' -o -printf '1024\n' \) |
    awk '{ k += int(($1 + 1023) / 1024) } END { print "Installed-Size: " k }' >> "$CONTROL/control"
install -m 0644 packaging/deb/conffiles packaging/deb/triggers "$CONTROL/"
install -m 0755 packaging/deb/postinst packaging/deb/prerm packaging/deb/postrm "$CONTROL/"
(cd "$ROOT" && find . -type f ! -path ./etc/\* -printf '%P\0' | LC_ALL=C sort -z | xargs -0 md5sum) > "$CONTROL/md5sums"

mkdir -p dist
OUT=dist/roostack_${VERSION}_${ARCH}.deb
rm -f "$OUT"
stamp() { find "$@" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +; }
if command -v dpkg-deb >/dev/null; then
    cp -r "$CONTROL" "$ROOT/DEBIAN"
    stamp "$ROOT"
    # dpkg-deb sorts the files and, with SOURCE_DATE_EPOCH, dates the ar
    # members with it.
    dpkg-deb --root-owner-group -Zxz -z6 --threads-max=1 --build "$ROOT" "$OUT" >/dev/null
else
    stamp "$ROOT" "$CONTROL"
    tarx() { tar --format=gnu --owner=0 --group=0 --numeric-owner --sort=name -C "$1" -c . | xz -6 -T1 > "$2"; }
    tarx "$CONTROL" "$STAGE/control.tar.xz"
    tarx "$ROOT" "$STAGE/data.tar.xz"
    echo "2.0" > "$STAGE/debian-binary"
    # D: zero times and owners in the ar headers.
    (cd "$STAGE" && ar rcD "$REPO/$OUT" debian-binary control.tar.xz data.tar.xz)
fi
echo "$OUT"
