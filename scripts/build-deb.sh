#!/usr/bin/env bash
# Builds the Roostack package (TASKS 1.12): dist/roostack_<version>_amd64.deb.
# Run after `make build`. Uses dpkg-deb when it's there (Debian, CI);
# otherwise assembles the archive with ar and tar, which is all a .deb is.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO"
[[ -x bin/roostack && -x bin/roostack-agent && -s bin/copyright ]] || { echo "build first: make build" >&2; exit 1; }
# Debian versions must start with a digit: v0.1.0-3-gabc → 0.1.0-3-gabc,
# and a bare commit becomes 0~git<commit>.
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
VERSION=${VERSION#v}
[[ $VERSION =~ ^[0-9]+\.[0-9] ]] || VERSION="0~git${VERSION}"

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
ROOT=$STAGE/root

install -D -m 0755 bin/roostack "$ROOT/usr/bin/roostack"
install -D -m 0755 bin/roostack-agent "$ROOT/usr/bin/roostack-agent"
install -D -m 0644 packaging/systemd/roostack.service "$ROOT/usr/lib/systemd/system/roostack.service"
install -D -m 0644 packaging/systemd/roostack-agent.service "$ROOT/usr/lib/systemd/system/roostack-agent.service"
install -D -m 0644 packaging/apt/52roostack-unattended-upgrades "$ROOT/etc/apt/apt.conf.d/52roostack-unattended-upgrades"
install -D -m 0644 build/caddy/Containerfile "$ROOT/usr/share/roostack/caddy/Containerfile"
install -D -m 0644 docs/examples/lab.yaml "$ROOT/usr/share/roostack/lab.yaml"
for pkg in docs/examples/*.yaml; do
    name=$(basename "$pkg" .yaml)
    [[ $name == lab ]] && continue
    install -D -m 0644 "$pkg" "$ROOT/usr/share/roostack/catalog/$name/package.yaml"
done
# Licences of everything compiled into or served by Roostack (scripts/copyright.sh).
install -D -m 0644 bin/copyright "$ROOT/usr/share/doc/roostack/copyright"
for doc in README.md ARCHITECTURE.md SPEC.md THIRD_PARTY.md; do
    install -D -m 0644 "$doc" "$ROOT/usr/share/doc/roostack/$doc"
done

CONTROL=$STAGE/control
install -d "$CONTROL"
sed "s/@VERSION@/$VERSION/" packaging/deb/control > "$CONTROL/control"
echo "Installed-Size: $(du -sk "$ROOT" | cut -f1)" >> "$CONTROL/control"
install -m 0644 packaging/deb/conffiles "$CONTROL/conffiles"
install -m 0755 packaging/deb/postinst packaging/deb/prerm packaging/deb/postrm "$CONTROL/"
(cd "$ROOT" && find . -type f ! -path ./etc/\* -printf '%P\0' | xargs -0 md5sum) > "$CONTROL/md5sums"

mkdir -p dist
OUT=dist/roostack_${VERSION}_amd64.deb
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
