#!/usr/bin/env bash
# Checks a published Roostack release against its source
# (docs/RELEASING.md, "Checking a release yourself"):
#
#   scripts/verify-release.sh 1.2.3        (or make verify-release VERSION=1.2.3)
#
# 1. Takes the release's SHA256SUMS and SHA256SUMS.asc from
#    dl.roostack.cloud (or a folder: FROM=dir).
# 2. Checks SHA256SUMS.asc against the release key (the one committed at
#    the tag, or KEY=file), and shows the key's fingerprint to compare
#    with the one published on roostack.cloud.
# 3. Builds the release from its tag, in the Debian 13 image the release
#    workflow builds in (pinned by digest in the tag's release.yml), with
#    the same Go, uv and Zensical.
# 4. Compares every rebuilt file's SHA-256 with the published SHA256SUMS,
#    and says plainly whether the release matches.
#
# Needs git, curl, and Podman or Docker (ENGINE=podman|docker picks one);
# everything else runs in the container. Runs on Linux, or on a Mac with
# Podman's or Docker's Linux machine. The tag must be in this checkout
# (git fetch --tags). The rebuilt files are left in dist/verify-<version>
# (OUT=dir), to compare with diffoscope if anything differs. Takes about
# 10 minutes: Go, Python and the modules are downloaded afresh, as the
# release workflow does.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO"

fail() { echo "verify-release.sh: $*" >&2; exit 1; }

VER=${1:-}
VER=${VER#v}
[[ $VER =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] ||
    fail "say which release to check, like 1.2.3 (make verify-release VERSION=1.2.3)"
TAG=v$VER
URL=${RELEASE_URL:-https://dl.roostack.cloud/releases/$VER}

if [[ -n ${ENGINE:-} ]]; then
    command -v "$ENGINE" >/dev/null || fail "$ENGINE isn't installed"
elif command -v podman >/dev/null; then
    ENGINE=podman
elif command -v docker >/dev/null; then
    ENGINE=docker
else
    fail "needs Podman or Docker, to build in the release's own image"
fi

git rev-parse -q --verify "refs/tags/$TAG^{commit}" >/dev/null ||
    fail "there's no tag $TAG in this checkout: get the tags first (git fetch --tags)"
COMMIT=$(git rev-parse "$TAG^{commit}")

# What the release workflow built with, as the tag has it: the image (by
# digest) and uv. Go comes from the tag's build-release.sh, which makes
# the go tool fetch the release's own Go.
WORKFLOW=$(git show "$TAG:.github/workflows/release.yml") || fail "$TAG has no release workflow"
wf() { sed -n "s/^ *$1: *\"\{0,1\}\([^\"]*\)\"\{0,1\} *$/\1/p" <<<"$WORKFLOW" | head -1; }
IMAGE=$(wf image)
UV_VERSION=$(wf UV_VERSION)
UV_SHA256=$(wf UV_SHA256)
UV_SHA256_AARCH64=$(wf UV_SHA256_AARCH64)
[[ $IMAGE == *@sha256:* ]] || fail "$TAG's release.yml doesn't pin its image by digest"
[[ -n $UV_VERSION && -n $UV_SHA256 ]] || fail "$TAG's release.yml doesn't say which uv it uses"

# Only its own two folders in OUT are replaced, whatever OUT is.
OUT=${OUT:-dist/verify-$VER}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
IN=$OUT/in
rm -rf "$IN" "$OUT/files"
mkdir "$IN"
trap 'rm -rf "$IN"' EXIT

echo "== The published release"
if [[ -n ${FROM:-} ]]; then
    for f in SHA256SUMS SHA256SUMS.asc; do
        [[ -s $FROM/$f ]] || fail "$FROM/$f is missing"
        cp "$FROM/$f" "$IN/$f"
    done
    echo "From $FROM"
else
    for f in SHA256SUMS SHA256SUMS.asc; do
        curl -fsSL --retry 3 -o "$IN/$f" "$URL/$f" || fail "couldn't download $URL/$f"
    done
    echo "From $URL"
fi

# The release key: the one the tag has (the release workflow checks its
# signature against exactly that), unless another is given, such as one
# downloaded from roostack.cloud.
if [[ -n ${KEY:-} ]]; then
    cp "$KEY" "$IN/release-key.asc"
else
    git show "$TAG:packaging/keys/roostack-release.asc" > "$IN/release-key.asc"
fi
git bundle create -q "$IN/source.bundle" "refs/tags/$TAG"

# Everything below runs in the release's image, as root there.
cat > "$IN/verify.sh" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
fail() { echo "verify-release.sh: $*" >&2; exit 1; }

echo "== Getting the build tools"
export DEBIAN_FRONTEND=noninteractive
apt-get update -q >/dev/null
# As release.yml, plus gpg to check the signature and Debian's Go, which
# fetches the release's own Go (checked against Go's checksum database).
apt-get install -y -q --no-install-recommends ca-certificates curl git make gnupg gpgv golang-go >/dev/null

echo "== The signature"
export GNUPGHOME=$(mktemp -d)
gpg --batch --quiet --dearmor < /in/release-key.asc > "$GNUPGHOME/release.gpg" ||
    fail "the release key isn't an OpenPGP public key"
fpr=$(gpg --batch --with-colons --show-keys /in/release-key.asc 2>/dev/null | awk -F: '$1 == "pub" { p = 1; next } p && $1 == "fpr" { print $10; p = 0 }')
echo "Release key: $fpr"
gpgv --keyring "$GNUPGHOME/release.gpg" /in/SHA256SUMS.asc /in/SHA256SUMS ||
    fail "SHA256SUMS.asc isn't a good signature of SHA256SUMS by the release key: don't trust this release"
echo "SHA256SUMS is signed by the release key."

echo "== uv $UV_VERSION"
case $(uname -m) in
    x86_64) uvarch=x86_64 sum=$UV_SHA256 ;;
    aarch64) uvarch=aarch64 sum=$UV_SHA256_AARCH64 ;;
    *) fail "the release is built on x86_64 or aarch64, not $(uname -m)" ;;
esac
[[ -n $sum ]] || fail "the tag's release.yml has no SHA-256 for uv on $uvarch"
curl -fsSL --retry 3 -o /tmp/uv.tar.gz "https://github.com/astral-sh/uv/releases/download/$UV_VERSION/uv-$uvarch-unknown-linux-gnu.tar.gz"
echo "$sum  /tmp/uv.tar.gz" | sha256sum -c --quiet - || fail "uv's download doesn't match the SHA-256 in release.yml"
tar -xzf /tmp/uv.tar.gz -C /usr/local
export PATH=/usr/local/uv-$uvarch-unknown-linux-gnu:$PATH

echo "== Building $TAG"
git init -q /build
cd /build
git fetch -q /in/source.bundle "refs/tags/$TAG:refs/tags/$TAG"
git -c advice.detachedHead=false checkout -q "$TAG"
make release
(cd dist/release && sha256sum -c --strict --quiet SHA256SUMS) || fail "the rebuilt files don't match their own SHA256SUMS"

rm -rf /out/files
mkdir /out/files
cp dist/release/* /out/files/
[[ -z ${CHOWN:-} ]] || chown -R "$CHOWN" /out/files
SCRIPT

echo "== Checking and rebuilding $TAG ($COMMIT) in $IMAGE, with $ENGINE"
# Docker runs as root, so the rebuilt files are given back to whoever ran
# this; rootless Podman's root is already them.
chown=""
if [[ $ENGINE == docker ]] || [[ $($ENGINE info --format '{{.Host.Security.Rootless}}' 2>/dev/null) == false ]]; then
    chown="$(id -u):$(id -g)"
fi
"$ENGINE" run --rm --pull=missing \
    -e TAG="$TAG" -e UV_VERSION="$UV_VERSION" -e UV_SHA256="$UV_SHA256" \
    -e UV_SHA256_AARCH64="$UV_SHA256_AARCH64" -e CHOWN="$chown" \
    -v "$IN:/in:ro,z" -v "$OUT:/out:z" \
    "$IMAGE" bash /in/verify.sh

echo
echo "== Comparing"
published=$IN/SHA256SUMS
bad=0
while read -r sum name; do
    mine=$(awk -v n="$name" '$2 == n { print $1 }' "$OUT/files/SHA256SUMS")
    if [[ -z $mine ]]; then
        echo "  MISSING  $name: published, but the source doesn't build it"
        bad=1
    elif [[ $mine == "$sum" ]]; then
        echo "  same     $name"
    else
        echo "  DIFFERS  $name"
        bad=1
    fi
done < "$published"
while read -r _ name; do
    awk -v n="$name" '$2 == n { found = 1 } END { exit !found }' "$published" ||
        { echo "  EXTRA    $name: built from the source, but not published"; bad=1; }
done < "$OUT/files/SHA256SUMS"

echo
if ((bad)); then
    echo "Roostack $VER DOES NOT MATCH its source: the files marked above aren't what tag $TAG builds."
    echo "Your rebuild is in $OUT/files; diffoscope shows what differs (docs/RELEASING.md)."
    exit 1
fi
echo "Roostack $VER matches its source: every published file is exactly what tag $TAG ($COMMIT) builds,"
echo "and SHA256SUMS is signed by the release key above. Check that key's fingerprint against the one on roostack.cloud."
