#!/usr/bin/env bash
# Builds the MyLab package (TASKS 1.12): dist/mylab_<version>_amd64.deb.
# Run after `make build`. Uses dpkg-deb when it's there (Debian, CI);
# otherwise assembles the archive with ar and tar, which is all a .deb is.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO"
[[ -x bin/mylab && -x bin/mylab-agent ]] || { echo "build first: make build" >&2; exit 1; }
# Debian versions must start with a digit: v0.1.0-3-gabc → 0.1.0-3-gabc,
# and a bare commit becomes 0~git<commit>.
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
VERSION=${VERSION#v}
[[ $VERSION =~ ^[0-9]+\.[0-9] ]] || VERSION="0~git${VERSION}"

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
ROOT=$STAGE/root

install -D -m 0755 bin/mylab "$ROOT/usr/bin/mylab"
install -D -m 0755 bin/mylab-agent "$ROOT/usr/bin/mylab-agent"
install -D -m 0644 packaging/systemd/mylab.service "$ROOT/usr/lib/systemd/system/mylab.service"
install -D -m 0644 packaging/systemd/mylab-agent.service "$ROOT/usr/lib/systemd/system/mylab-agent.service"
install -D -m 0644 packaging/apt/52mylab-unattended-upgrades "$ROOT/etc/apt/apt.conf.d/52mylab-unattended-upgrades"
install -D -m 0644 build/caddy/Containerfile "$ROOT/usr/share/mylab/caddy/Containerfile"
install -D -m 0644 docs/examples/lab.yaml "$ROOT/usr/share/mylab/lab.yaml"
for pkg in docs/examples/*.yaml; do
    name=$(basename "$pkg" .yaml)
    [[ $name == lab ]] && continue
    install -D -m 0644 "$pkg" "$ROOT/usr/share/mylab/catalog/$name/package.yaml"
done
for doc in README.md ARCHITECTURE.md SPEC.md THIRD_PARTY.md; do
    install -D -m 0644 "$doc" "$ROOT/usr/share/doc/mylab/$doc"
done

CONTROL=$STAGE/control
install -d "$CONTROL"
sed "s/@VERSION@/$VERSION/" packaging/deb/control > "$CONTROL/control"
echo "Installed-Size: $(du -sk "$ROOT" | cut -f1)" >> "$CONTROL/control"
install -m 0644 packaging/deb/conffiles "$CONTROL/conffiles"
install -m 0755 packaging/deb/postinst packaging/deb/prerm packaging/deb/postrm "$CONTROL/"
(cd "$ROOT" && find . -type f ! -path ./etc/\* -printf '%P\0' | xargs -0 md5sum) > "$CONTROL/md5sums"

mkdir -p dist
OUT=dist/mylab_${VERSION}_amd64.deb
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
