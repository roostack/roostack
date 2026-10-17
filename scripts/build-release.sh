#!/usr/bin/env bash
# Builds a Roostack release from a checkout of its tag (docs/RELEASING.md):
#
#   dist/release/roostack_<version>_amd64.deb
#   dist/release/roostack_<version>_arm64.deb
#   dist/release/roostack-<version>-linux-amd64   (the plain binary)
#   dist/release/roostack-<version>-linux-arm64
#   dist/release/SHA256SUMS
#
# The release workflow (.github/workflows/release.yml) runs this twice and
# signs SHA256SUMS only if both builds agree. It's reproducible: anyone can
# run it on Linux (Debian 13 or Ubuntu 24.04) from the same tag and get
# the same SHA256SUMS. It needs git, uv, dpkg-deb and Go 1.21 or newer,
# which fetches the Go below by itself.
#
# ROOSTACK_RELEASE_TAG, if set, is the tag this must be (the workflow sets
# the one that was pushed).
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO"

# The Go a release is built with: the release workflow's GO_VERSION, and
# CI's. Update all three together.
GO_RELEASE=go1.27.2

fail() { echo "build-release.sh: $*" >&2; exit 1; }
# make build and build-deb.sh take a VERSION from the environment; a
# release's comes from its tag alone.
unset VERSION

# A release trusts only the registry's real root key (SPEC.md §8.3.3):
# never a development build.
[[ -z ${ROOSTACK_DEV_BUILD:-} ]] || fail "ROOSTACK_DEV_BUILD is set: a release is never a development build"
[[ -z ${REGISTRY_DEV_KEY:-} ]] || fail "REGISTRY_DEV_KEY is set: a release trusts no development key"
command -v uv >/dev/null || fail "a release includes the help site, which needs uv (https://docs.astral.sh/uv/)"
command -v dpkg-deb >/dev/null || fail "needs dpkg-deb (Debian or Ubuntu)"

# The tag: exactly vMAJOR.MINOR.PATCH, on this commit, with nothing
# changed or added. Untracked files count, as they could be built in.
[[ -z $(git status --porcelain) ]] || { git status --short >&2; fail "the checkout has changes: a release is built from its tag alone"; }
TAG=$(git describe --tags --dirty)
[[ $TAG =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] ||
    fail "git describe says $TAG: a release is built from a tag like v1.2.3 on this very commit"
[[ -z ${ROOSTACK_RELEASE_TAG:-} || $ROOSTACK_RELEASE_TAG == "$TAG" ]] ||
    fail "this is $TAG, not $ROOSTACK_RELEASE_TAG"
REL=${TAG#v}

# Everything the output depends on is fixed here, not left to the machine.
# The go tool: the release's Go, no go.work from a folder above, no
# settings from the builder's go env file, the baseline processor level.
export GOTOOLCHAIN=$GO_RELEASE GOWORK=off GOENV=off GOFLAGS='' GOEXPERIMENT='' GOAMD64=v1 GOARM64=v8.0
# Files in the packages get the commit's time.
SOURCE_DATE_EPOCH=$(git log -1 --format=%ct HEAD)
export SOURCE_DATE_EPOCH
# The help site: Zensical exactly as help/uv.lock says (refusing a lock
# that's out of date), on a Python uv provides. The patch release of
# Python doesn't change the site (checked: the same pages byte for byte).
export UV_LOCKED=1 UV_PYTHON=3.13 UV_PYTHON_PREFERENCE=only-managed UV_LINK_MODE=copy
export LC_ALL=C TZ=UTC

echo "Building Roostack $REL ($(git rev-parse HEAD)) with $(go version | cut -d' ' -f3), $(uv --version), $(dpkg-deb --version | head -1)"

OUT=dist/release
rm -rf "$OUT" internal/web/helpsite/site help/site
mkdir -p "$OUT"
for arch in amd64 arm64; do
    echo "== $arch"
    rm -rf bin
    GOARCH=$arch make build
    deb=$(scripts/build-deb.sh)
    [[ $deb == "dist/roostack_${REL}_${arch}.deb" ]] || fail "build-deb.sh made $deb, not a $REL package for $arch"
    mv "$deb" "$OUT/"
    install -m 0755 bin/roostack "$OUT/roostack-$REL-linux-$arch"
done

(cd "$OUT" && sha256sum roostack_* roostack-* > SHA256SUMS)
cat "$OUT/SHA256SUMS"
