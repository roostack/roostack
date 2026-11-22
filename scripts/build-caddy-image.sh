#!/usr/bin/env bash
# Builds Roostack's Caddy image from build/caddy/Containerfile for amd64
# and arm64, pushes it, and prints it named by digest (SPEC.md §6.5,
# docs/RELEASING.md):
#
#   scripts/build-caddy-image.sh [REPOSITORY]
#
# REPOSITORY is ghcr.io/roostack/roostack-caddy unless given: the Caddy
# image workflow (.github/workflows/caddy-image.yml) runs this, logged in
# to GHCR. For a development image, give a registry of your own, such as
# localhost:5000/roostack-caddy, and build Roostack with
# CADDY_DEV_IMAGE=<what this prints> (Makefile). Needs Podman (4.4 or
# later) and git; it builds on one machine without emulation, as the
# Containerfile cross-compiles.
#
# Each image is labelled with the SHA-256 of the Containerfile it was
# built from (io.roostack.containerfile.sha256, which the release
# workflow checks), and the commit. It's also tagged containerfile-<that
# SHA-256>, for people looking at the registry; Roostack names the image
# only by digest.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO"

fail() { echo "build-caddy-image.sh: $*" >&2; exit 1; }

DEST=${1:-ghcr.io/roostack/roostack-caddy}
[[ $DEST =~ ^[a-z0-9.-]+(:[0-9]+)?/[a-z0-9._/-]+$ ]] || fail "$DEST isn't a repository (registry/name, without a tag)"
command -v podman >/dev/null || fail "needs Podman"

# What's built is what's committed: the label says which Containerfile.
[[ -z $(git status --porcelain build/caddy) ]] || fail "build/caddy has changes: commit them first, so the image says what it was built from"
SHA=$(sha256sum build/caddy/Containerfile | cut -d' ' -f1)
COMMIT=$(git rev-parse HEAD)

LIST=localhost/roostack-caddy-build:$SHA
podman manifest rm "$LIST" >/dev/null 2>&1 || true
podman build --platform linux/amd64,linux/arm64 --manifest "$LIST" \
    --label "io.roostack.containerfile.sha256=$SHA" \
    --label "org.opencontainers.image.source=https://github.com/roostack/roostack" \
    --label "org.opencontainers.image.revision=$COMMIT" \
    build/caddy >&2

# A registry on this machine has no certificate.
tls=()
[[ $DEST == localhost:* || $DEST == 127.0.0.1:* ]] && tls=(--tls-verify=false)
DIGESTFILE=$(mktemp)
trap 'rm -f "$DIGESTFILE"' EXIT
podman manifest push --all "${tls[@]}" --digestfile "$DIGESTFILE" "$LIST" "docker://$DEST:containerfile-$SHA" >&2
DIGEST=$(cat "$DIGESTFILE")
[[ $DIGEST =~ ^sha256:[0-9a-f]{64}$ ]] || fail "podman gave no digest for the pushed image ($DIGEST)"
podman manifest rm "$LIST" >/dev/null

echo "Built from build/caddy/Containerfile (SHA-256 $SHA) at $COMMIT; put this in internal/generate/caddyimage.go:" >&2
echo "  PublishedCaddyImage      = \"$DEST@$DIGEST\"" >&2
echo "  CaddyContainerfileSHA256 = \"$SHA\"" >&2
echo "$DEST@$DIGEST"
