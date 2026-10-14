#!/usr/bin/env bash
# Builds the Roostack package (TASKS 1.12): dist/roostack_<version>_<arch>.deb,
# for the architecture the binaries in bin/ were built for (amd64, or arm64
# after `GOARCH=arm64 make build`; ARM64-PLAN.md 1.2). Run after
# `make build`. Uses dpkg-deb when it's there (Debian, CI);
# otherwise assembles the archive with ar and tar, which is all a .deb is.
set -euo pipefail

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
[[ -x bin/roostack && -x bin/roostack-agent && -s bin/copyright ]] || { echo "build first: make build" >&2; exit 1; }
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
for pkg in docs/examples/*.yaml; do
    name=$(basename "$pkg" .yaml)
    [[ $name == lab ]] && continue
    install -D -m 0644 "$pkg" "$ROOT/usr/share/roostack/catalog/$name/package.yaml"
    # Its logo (about.icon), beside it as in the package's folder.
    [[ -f docs/examples/$name.png ]] && install -D -m 0644 "docs/examples/$name.png" "$ROOT/usr/share/roostack/catalog/$name/$name.png"
done
# Licences of everything compiled into or served by Roostack (scripts/copyright.sh).
install -D -m 0644 bin/copyright "$ROOT/usr/share/doc/roostack/copyright"
for doc in README.md ARCHITECTURE.md SPEC.md THIRD_PARTY.md; do
    install -D -m 0644 "$doc" "$ROOT/usr/share/doc/roostack/$doc"
done

CONTROL=$STAGE/control
install -d "$CONTROL"
sed -e "s/@VERSION@/$VERSION/" -e "s/@ARCH@/$ARCH/" packaging/deb/control > "$CONTROL/control"
echo "Installed-Size: $(du -sk "$ROOT" | cut -f1)" >> "$CONTROL/control"
install -m 0644 packaging/deb/conffiles packaging/deb/triggers "$CONTROL/"
install -m 0755 packaging/deb/postinst packaging/deb/prerm packaging/deb/postrm "$CONTROL/"
(cd "$ROOT" && find . -type f ! -path ./etc/\* -printf '%P\0' | xargs -0 md5sum) > "$CONTROL/md5sums"

mkdir -p dist
OUT=dist/roostack_${VERSION}_${ARCH}.deb
rm -f "$OUT"
if command -v dpkg-deb >/dev/null; then
    cp -r "$CONTROL" "$ROOT/DEBIAN"
    dpkg-deb --root-owner-group -Zxz --build "$ROOT" "$OUT" >/dev/null
else
    tarx() { tar --owner=0 --group=0 --numeric-owner --sort=name -C "$1" -cJf "$2" .; }
    tarx "$CONTROL" "$STAGE/control.tar.xz"
    tarx "$ROOT" "$STAGE/data.tar.xz"
    echo "2.0" > "$STAGE/debian-binary"
    (cd "$STAGE" && ar rc "$REPO/$OUT" debian-binary control.tar.xz data.tar.xz)
fi
echo "$OUT"
