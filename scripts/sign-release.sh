#!/usr/bin/env bash
# Signs a release (docs/RELEASING.md): an armored, detached OpenPGP
# signature of SHA256SUMS, SHA256SUMS.asc, made with the release key's
# signing subkey, then checked with gpgv against the public key in this
# repository (packaging/keys/roostack-release.asc), as people downloading
# a release check it.
#
#   scripts/sign-release.sh --check-key   the committed key is a real one
#   scripts/sign-release.sh DIR           signs DIR/SHA256SUMS
#
# The subkey comes from ROOSTACK_RELEASE_SIGNING_KEY: the armored export
# of the signing subkey alone, without a passphrase (the release
# workflow's `release` environment holds it). It's never printed.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
PUBLIC=$REPO/packaging/keys/roostack-release.asc

fail() { echo "sign-release.sh: $*" >&2; exit 1; }

# On Debian 13, gpg can be there without gpgv (apt checks signatures
# with sqv now, and gnupg without its recommended packages lacks it);
# without gpgv the check below failed as if the key were wrong.
for tool in gpg gpgv; do
    command -v "$tool" >/dev/null || fail "$tool isn't installed (Debian: apt install gnupg gpgv)"
done

GNUPGHOME=$(mktemp -d)
export GNUPGHOME
trap 'rm -rf "$GNUPGHOME"' EXIT

# check_key refuses the placeholder, and anything but one public key.
check_key() {
    [[ -s $PUBLIC ]] || fail "$PUBLIC is missing"
    ! grep -q ROOSTACK-RELEASE-KEY-PLACEHOLDER "$PUBLIC" ||
        fail "packaging/keys/roostack-release.asc is still the placeholder: the release key's ceremony comes first (docs/RELEASING.md)"
    local fprs
    fprs=$(gpg --batch --with-colons --show-keys "$PUBLIC" 2>/dev/null | awk -F: '$1 == "pub" { p = 1; next } p && $1 == "fpr" { print $10; p = 0 }') ||
        fail "packaging/keys/roostack-release.asc isn't an OpenPGP public key"
    [[ $(wc -l <<<"$fprs") -eq 1 && -n $fprs ]] || fail "packaging/keys/roostack-release.asc must hold exactly one key"
    grep -q 'PRIVATE KEY' "$PUBLIC" && fail "packaging/keys/roostack-release.asc holds a private key: replace it with the public key alone"
    echo "Release key: $fprs"
}

check_key
[[ ${1:-} == --check-key ]] && exit 0

DIR=${1:?usage: sign-release.sh --check-key | DIR}
[[ -s $DIR/SHA256SUMS ]] || fail "$DIR/SHA256SUMS is missing"
[[ -w $DIR ]] || fail "can't write to $DIR"
[[ -n ${ROOSTACK_RELEASE_SIGNING_KEY:-} ]] || fail "ROOSTACK_RELEASE_SIGNING_KEY isn't set"

printf '%s\n' "$ROOSTACK_RELEASE_SIGNING_KEY" | gpg --batch --quiet --import 2>/dev/null ||
    fail "ROOSTACK_RELEASE_SIGNING_KEY isn't an OpenPGP secret key export"
# The primary key stays offline: the secret must hold only the subkey,
# its primary key a stub ("#" in the 15th field).
gpg --batch --with-colons --list-secret-keys | awk -F: '$1 == "sec" && $15 != "#" { bad = 1 } $1 == "sec" { n++ } END { exit !(n == 1 && !bad) }' ||
    fail "ROOSTACK_RELEASE_SIGNING_KEY must be the signing subkey alone (gpg --export-secret-subkeys), without the primary key"

rm -f "$DIR/SHA256SUMS.asc"
gpg --batch --yes --armor --detach-sign --digest-algo SHA512 --output "$DIR/SHA256SUMS.asc" "$DIR/SHA256SUMS" ||
    fail "signing failed, as gpg says above (a subkey with a passphrase can't be used here: export it without one)"

# Checked with the committed public key alone, not the imported secret.
gpg --dearmor < "$PUBLIC" > "$GNUPGHOME/release.gpg"
gpgv --keyring "$GNUPGHOME/release.gpg" "$DIR/SHA256SUMS.asc" "$DIR/SHA256SUMS" ||
    fail "the signature doesn't check against packaging/keys/roostack-release.asc: the secret holds another key's subkey"
echo "Signed $DIR/SHA256SUMS, and gpgv checks it against packaging/keys/roostack-release.asc"
