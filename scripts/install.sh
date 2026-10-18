#!/bin/sh
# Installs Roostack on Debian 13 (https://roostack.cloud). Run it as root:
#
#   curl -fsSL https://dl.roostack.cloud/install.sh | sudo sh
#
# It does only what the manual steps in Roostack's help do ("Install
# Roostack"), and downloads nothing else:
#   1. checks this is Debian 13 on amd64 or arm64;
#   2. fetches Roostack's release key and checks it against the SHA-256
#      written below, before anything trusts it;
#   3. adds Roostack's package source, signed by that key only;
#   4. runs apt update and apt install --no-install-recommends roostack.
# Safe to run again: each step writes the same files.
#
# For testing, ROOSTACK_REPO is another repository to use (plain HTTP is
# allowed for it), with ROOSTACK_KEY_SHA256 its key's SHA-256 and
# ROOSTACK_SUITE its suite.
set -eu

# Set at release (SPEC.md §6.3.2): the suite is the current minor series,
# and the SHA-256 is of the key file published in the repository.
REPO=${ROOSTACK_REPO:-https://apt.roostack.cloud}
SUITE=${ROOSTACK_SUITE:-@SUITE@}
KEY_SHA256=${ROOSTACK_KEY_SHA256:-@KEY_SHA256@}
PROTO='=https'
[ -z "${ROOSTACK_REPO:-}" ] || PROTO='=https,http'

KEYRING=/usr/share/keyrings/roostack-archive-keyring.gpg
SOURCE=/etc/apt/sources.list.d/roostack.sources

fail() {
    echo "Roostack can't be installed here: $*" >&2
    exit 1
}

# Everything runs from main, once sh has read the whole script: piped into
# sh, a command that reads its input (apt, dpkg) would otherwise swallow
# the rest of it.
main() {
    case "$SUITE$KEY_SHA256" in
    *@*) fail "this copy of the installer has no release in it. Use the one at https://roostack.cloud." ;;
    esac
    [ "$(id -u)" = 0 ] || fail "run it as root (with sudo)."

    # 1. Debian 13 ("trixie") on a 64-bit PC or ARM computer.
    . /etc/os-release
    [ "${ID:-}" = debian ] && [ "${VERSION_ID:-}" = 13 ] ||
        fail "it needs Debian 13, and this is ${PRETTY_NAME:-something else}."
    arch=$(dpkg --print-architecture)
    case "$arch" in
    amd64 | arm64) ;;
    *) fail "it runs on amd64 and arm64 computers, and this one is $arch." ;;
    esac
    command -v curl >/dev/null || fail "curl is missing. Install it with: apt install curl"

    # 2. The release key, checked before it goes where apt trusts it. The
    # whole file is checked, so nothing can be added to it either.
    tmp=$(mktemp)
    trap 'rm -f "$tmp"' EXIT
    echo "Fetching Roostack's release key from $REPO"
    curl -fsSL --proto "$PROTO" --tlsv1.2 -o "$tmp" "$REPO/roostack-archive-keyring.gpg" ||
        fail "the release key couldn't be downloaded from $REPO."
    got=$(sha256sum "$tmp" | cut -d' ' -f1)
    [ "$got" = "$KEY_SHA256" ] ||
        fail "the release key isn't the one this installer expects (its SHA-256 is $got). Nothing was changed."
    install -m 0644 "$tmp" "$KEYRING"

    # 3. Roostack's package source, which only that key signs. The package
    # brings the same file (packaging/apt/roostack.sources, byte for byte,
    # so dpkg takes it over without asking) and keeps it from then on.
    cat >"$tmp" <<SOURCES
# Roostack's own packages (SPEC.md §7), installed by the roostack package.
# Suites is Roostack's release series: patch releases in it install by
# themselves each night (52roostack-unattended-upgrades). When the Owner
# accepts a new series on the Updates page, Roostack changes Suites to it.
# You may point URIs at a mirror; keep Signed-By, which checks that the
# packages are Roostack's.
# scripts/install.sh writes these same bytes before installing Roostack:
# change both together, or dpkg asks about this file on a fresh install.
Types: deb
URIs: $REPO
Suites: $SUITE
Components: main
Architectures: amd64 arm64
Signed-By: $KEYRING
SOURCES
    install -m 0644 "$tmp" "$SOURCE"

    # 4. Roostack, and what it needs from Debian (Podman, restic, WireGuard...).
    # Should the source differ from the package's (a test repository), the
    # one written here is kept, without dpkg stopping to ask. Nothing asks
    # questions (debconf's noninteractive frontend): with its input closed,
    # debconf otherwise tries three other frontends first and prints why
    # each failed. Without Podman's recommended extras (buildah, criu, a
    # compiler...): Roostack depends on the ones it uses itself
    # (packaging/deb/control), so this saves about 300 MB.
    echo "Installing Roostack"
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold roostack </dev/null
}

main
