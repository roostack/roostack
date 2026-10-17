#!/usr/bin/env bash
# Adds Roostack packages to its apt repository and signs it (SPEC.md §7,
# "Roostack's own updates"):
#
#   scripts/build-apt-repo.sh --key FINGERPRINT --keyring KEY.asc REPO DEB...
#
# REPO is the repository as published (apt.roostack.cloud): its dists/ and
# series file are enough, since pool/ is never rewritten. New packages are
# copied into REPO/pool/main/r/roostack/, and the suites they belong to get
# new indexes, a new Release file and new signatures (InRelease and
# Release.gpg, by gpg with FINGERPRINT's key). Other suites aren't touched.
#
# One suite per release series: 0.4.0 and 0.4.1 go to suite "0.4", 0.5.0 to
# "0.5". A server's source names its own series, so unattended-upgrades
# installs patch releases by itself, and a new series waits until the Owner
# accepts it. REPO/series lists every suite and its newest version, for
# Roostack to notice a new one.
#
# The repository is rebuilt from what's already published, so a run never
# drops an older version. That state is trusted only after checking it:
# each suite's InRelease against KEY.asc (the release key's public part,
# packaging/keys/roostack-release.asc), and its Packages files against the
# hashes in it. A version already published can't be published again with
# different contents.
#
# Needs apt-ftparchive (apt-utils), dpkg, gpg and gpgv: the Debian tools
# Debian's own archive uses. apt-publish.yml runs this in a Debian
# container pinned by digest.
set -euo pipefail

usage() { echo "usage: $0 --key FINGERPRINT --keyring KEY.asc REPO DEB..." >&2; exit 2; }
KEY='' KEYRING=''
while [[ $# -gt 0 ]]; do
    case $1 in
    --key) KEY=${2:-}; shift 2 ;;
    --keyring) KEYRING=${2:-}; shift 2 ;;
    --) shift; break ;;
    -*) usage ;;
    *) break ;;
    esac
done
[[ -n $KEY && -n $KEYRING && $# -ge 2 ]] || usage
REPO=$1
shift
for tool in apt-ftparchive dpkg dpkg-deb gpg gpgv gzip sha256sum; do
    command -v "$tool" >/dev/null || { echo "$tool isn't installed" >&2; exit 1; }
done

# What the Release files say. Origin and Label are what unattended-upgrades
# matches (packaging/apt/52roostack-unattended-upgrades).
ORIGIN=Roostack
LABEL=Roostack
ARCHES=(amd64 arm64)
POOL=pool/main/r/roostack

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$REPO"
REPO=$(cd "$REPO" && pwd)

# gpgv wants a binary keyring; the key is committed armoured.
if grep -q -- '-----BEGIN PGP PUBLIC KEY BLOCK-----' "$KEYRING"; then
    gpg --batch --quiet --dearmor < "$KEYRING" > "$WORK/keyring.gpg"
else
    cp "$KEYRING" "$WORK/keyring.gpg"
fi

# series_of VERSION: the release series (the suite), major.minor.
# Only plain release versions are published: a pre-release (0.5.0~rc1) in
# its series' suite would install itself on every server in it.
series_of() {
    [[ $1 =~ ^([0-9]+\.[0-9]+)\.[0-9]+$ ]] ||
        { echo "$1: only release versions (major.minor.patch) go into the repository" >&2; exit 1; }
    echo "${BASH_REMATCH[1]}"
}

# field FILE STANZA_VERSION ARCH NAME: a field of the stanza for that
# version and architecture in a Packages file.
field() {
    awk -v RS= -v FS='\n' -v ver="$2" -v arch="$3" -v name="$4" '{
        delete f
        for (i = 1; i <= NF; i++) if ((c = index($i, ": ")) > 0) f[substr($i, 1, c - 1)] = substr($i, c + 2)
        if (f["Version"] == ver && f["Architecture"] == arch) { print f[name]; exit }
    }' "$1"
}

# 1. Check what's published. Every suite must be signed by the release
# key, and every Packages file must match its InRelease: the indexes
# rebuilt below keep their stanzas.
for dir in "$REPO"/dists/*/; do
    [[ -d $dir ]] || continue
    suite=$(basename "$dir")
    [[ -f $dir/InRelease ]] || { echo "dists/$suite has no InRelease: fix the published repository first" >&2; exit 1; }
    if ! gpgv --quiet --keyring "$WORK/keyring.gpg" --output "$WORK/Release.$suite" "$dir/InRelease" 2>"$WORK/gpgv.log"; then
        cat "$WORK/gpgv.log" >&2
        echo "dists/$suite/InRelease isn't signed by the release key: refusing to build on it" >&2
        exit 1
    fi
    for arch in "${ARCHES[@]}"; do
        f=main/binary-$arch/Packages
        want=$(awk -v f="$f" '/^SHA256:/ {s = 1; next} /^[^ ]/ {s = 0} s && $3 == f {print $1}' "$WORK/Release.$suite")
        [[ -n $want ]] || { echo "dists/$suite/InRelease doesn't list $f" >&2; exit 1; }
        [[ -f $dir/$f ]] || { echo "dists/$suite/$f is missing" >&2; exit 1; }
        got=$(sha256sum < "$dir/$f" | cut -d' ' -f1)
        [[ $got == "$want" ]] || { echo "dists/$suite/$f doesn't match its InRelease" >&2; exit 1; }
    done
done

# 2. The new packages: into the pool, unless that version is published
# already (the same file: nothing to do; a different one: refused).
declare -A TOUCHED=()
mkdir -p "$REPO/$POOL"
for deb in "$@"; do
    pkg=$(dpkg-deb -f "$deb" Package)
    ver=$(dpkg-deb -f "$deb" Version)
    arch=$(dpkg-deb -f "$deb" Architecture)
    [[ $pkg == roostack ]] || { echo "$deb: not the roostack package ($pkg)" >&2; exit 1; }
    [[ " ${ARCHES[*]} " == *" $arch "* ]] || { echo "$deb: architecture $arch isn't published" >&2; exit 1; }
    suite=$(series_of "$ver")
    name=roostack_${ver}_$arch.deb
    sum=$(sha256sum < "$deb" | cut -d' ' -f1)
    old=$REPO/dists/$suite/main/binary-$arch/Packages
    if [[ -f $old ]] && published=$(field "$old" "$ver" "$arch" SHA256) && [[ -n $published ]]; then
        [[ $published == "$sum" ]] || { echo "$name is already published with different contents: give the release a new version" >&2; exit 1; }
        echo "$name: already published"
        continue
    fi
    if [[ -e $REPO/$POOL/$name ]] && ! cmp -s "$deb" "$REPO/$POOL/$name"; then
        echo "$POOL/$name is already there with different contents" >&2
        exit 1
    fi
    install -m 0644 "$deb" "$REPO/$POOL/$name"
    mkdir -p "$WORK/new/$suite/$POOL"
    ln -sf "$REPO/$POOL/$name" "$WORK/new/$suite/$POOL/$name"
    TOUCHED[$suite]=1
    echo "$name: added to suite $suite"
done

# 3. Each changed suite: its indexes (the published stanzas, then the new
# ones), each also under by-hash/ so a client never gets an index that
# doesn't match the InRelease it has (the CDN caches InRelease briefly and
# by-hash files for good), then the Release file, signed.
for suite in "${!TOUCHED[@]}"; do
    d=$REPO/dists/$suite
    for arch in "${ARCHES[@]}"; do
        bin=$d/main/binary-$arch
        mkdir -p "$bin"
        touch "$bin/Packages"
        (cd "$WORK/new/$suite" && apt-ftparchive --arch "$arch" packages "$POOL") > "$WORK/new.Packages"
        {
            # Stanzas end with a blank line; awk's paragraph mode tidies them.
            awk -v RS= '{print $0 "\n"}' "$bin/Packages"
            awk -v RS= '{print $0 "\n"}' "$WORK/new.Packages"
        } > "$WORK/Packages"
        mv "$WORK/Packages" "$bin/Packages"
        gzip -9 -n -c "$bin/Packages" > "$bin/Packages.gz"
        for f in Packages Packages.gz; do
            h=$(sha256sum < "$bin/$f" | cut -d' ' -f1)
            install -D -m 0644 "$bin/$f" "$bin/by-hash/SHA256/$h"
        done
    done
    rm -f "$d/Release" "$d/InRelease" "$d/Release.gpg"
    apt-ftparchive \
        -o APT::FTPArchive::Release::Origin="$ORIGIN" \
        -o APT::FTPArchive::Release::Label="$LABEL" \
        -o APT::FTPArchive::Release::Suite="$suite" \
        -o APT::FTPArchive::Release::Codename="$suite" \
        -o APT::FTPArchive::Release::Architectures="${ARCHES[*]}" \
        -o APT::FTPArchive::Release::Components=main \
        -o APT::FTPArchive::Release::Description="Roostack $suite" \
        -o APT::FTPArchive::Release::Acquire-By-Hash=yes \
        -o APT::FTPArchive::Release::MD5=false \
        -o APT::FTPArchive::Release::SHA1=false \
        -o APT::FTPArchive::Release::SHA512=false \
        release "$d" > "$WORK/Release"
    mv "$WORK/Release" "$d/Release"
    sign=(gpg --batch --yes --quiet --local-user "$KEY" --digest-algo SHA512)
    "${sign[@]}" --clearsign --output "$d/InRelease" "$d/Release"
    "${sign[@]}" --armor --detach-sign --output "$d/Release.gpg" "$d/Release"
    # What was just signed must check out as the published suites did.
    gpgv --quiet --keyring "$WORK/keyring.gpg" "$d/InRelease" 2>/dev/null ||
        { echo "the new InRelease for $suite doesn't check against $KEYRING: is --key the release key's signing subkey?" >&2; exit 1; }
    echo "dists/$suite: signed"
done

# 4. The list of series, newest version in each: Roostack reads it to tell
# the Owner a new series is out (SPEC.md §7). Only a hint: switching
# runs apt, which checks the new suite's signature.
{
    echo "# Roostack's release series, one apt suite each, and the newest version in each (SPEC.md §7)."
    for dir in "$REPO"/dists/*/; do
        [[ -d $dir ]] || continue
        suite=$(basename "$dir")
        newest=
        while read -r v; do
            if [[ -z $newest ]] || dpkg --compare-versions "$v" gt "$newest"; then newest=$v; fi
        done < <(sed -n 's/^Version: //p' "$dir"/main/binary-*/Packages | sort -u)
        [[ -n $newest ]] && echo "$suite $newest"
    done | sort -V
} > "$REPO/series"
echo "series:"
grep -v '^#' "$REPO/series"
