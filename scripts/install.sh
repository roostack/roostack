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
#   4. runs apt update and apt install --no-install-recommends roostack;
#   5. on a server with no swap, offers to make a swap file (/swapfile), so
#      a service needing more memory for a moment slows down rather than
#      being killed (SPEC.md §6.1, "Memory").
# Safe to run again: each step writes the same files.
#
# For testing, ROOSTACK_REPO is another repository to use (plain HTTP is
# allowed for it), with ROOSTACK_KEY_SHA256 its key's SHA-256 and
# ROOSTACK_SUITE its suite. ROOSTACK_SWAP=yes or no answers step 5's
# question, which is otherwise asked on the terminal (and skipped
# without one).
set -eu

# Set at release (SPEC.md §6.3.2): the suite is the current minor series,
# and the SHA-256 is of the key file published in the repository.
REPO=${ROOSTACK_REPO:-https://apt.roostack.cloud}
SUITE=${ROOSTACK_SUITE:-0.1}
KEY_SHA256=${ROOSTACK_KEY_SHA256:-5f5dd315ecd363938ec2da9175286f852001aeef1a0ff9b2aee436f9322f27bc}
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
    # shellcheck source=/dev/null # the system's file, read on the server
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

    # 5. A swap file, if there's no swap at all.
    offer_swap
}

# offer_swap makes /swapfile, as big as the server's memory between 1 and
# 4 GB, if the server has no swap, there's room for it on the disk, and
# whoever is installing says yes.
offer_swap() {
    [ -z "$(swapon --show --noheadings 2>/dev/null)" ] || return 0
    [ ! -e /swapfile ] || return 0
    size=$(awk '/^MemTotal:/ { print int($2 / 1024) }' /proc/meminfo)
    [ "$size" -ge 1024 ] || size=1024
    [ "$size" -le 4096 ] || size=4096
    free=$(df -Pm / | awk 'NR == 2 { print $4 }')
    if [ "$free" -lt $((size + 4096)) ]; then
        echo "This server has no swap, and there isn't enough free disk space for a swap file of $size MB, so the installer will leave it."
        return 0
    fi
    if [ "$(stat -f -c %T /)" = btrfs ]; then
        echo "This server has no swap. Its disk uses Btrfs, which needs a swap file made its own way, so the installer will leave it."
        return 0
    fi
    answer=${ROOSTACK_SWAP:-}
    if [ -z "$answer" ]; then
        # In a subshell: a redirection that fails on ":" (a special
        # built-in) would end the whole script.
        if ! (: </dev/tty) 2>/dev/null; then
            echo "This server has no swap. To have the installer make a swap file of $size MB, run it again with ROOSTACK_SWAP=yes."
            return 0
        fi
        printf 'This server has no swap, so a service needing more memory than it has for a moment would be stopped rather than slowed down.\nMake a swap file of %s MB at /swapfile? [Y/n] ' "$size"
        read -r answer </dev/tty || answer=n
    fi
    case "$answer" in
    "" | [Yy]*) ;;
    *)
        echo "No swap file made."
        return 0
        ;;
    esac
    echo "Making a swap file of $size MB at /swapfile"
    fallocate -l "${size}M" /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count="$size" status=none
    chmod 600 /swapfile
    mkswap /swapfile >/dev/null
    swapon /swapfile
    grep -q '^/swapfile[[:space:]]' /etc/fstab || echo '/swapfile none swap sw 0 0' >>/etc/fstab
}

main
