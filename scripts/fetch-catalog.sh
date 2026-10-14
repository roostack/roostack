#!/usr/bin/env bash
# Fetches the registry at the commit packaging/registry names into
# bin/catalog, which build-deb.sh ships as /usr/share/roostack/catalog:
# the packages a lab can install, until labs fetch the registry's signed
# index themselves (SPEC.md §8.3.3). make build runs it; it does nothing
# when bin/catalog already holds that commit. Needs git and, the first
# time for each commit, the network.
#
#   ROOSTACK_REGISTRY=~/dev/roostack-services scripts/fetch-catalog.sh
#
# fetches from a local clone instead (any URL git takes). The commit is
# checked either way: a commit ID names its files exactly.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO"
url=$(sed -n 's/^url=//p' packaging/registry)
commit=$(sed -n 's/^commit=//p' packaging/registry)
url=${ROOSTACK_REGISTRY:-$url}
[[ $commit =~ ^[0-9a-f]{40}$ ]] || { echo "packaging/registry: commit must be a full commit ID" >&2; exit 1; }

out=bin/catalog
[[ $(cat "$out/registry-commit" 2>/dev/null) == "$commit" ]] && exit 0

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Fetching the registry's packages at ${commit:0:12} from $url" >&2
git clone --quiet --no-checkout "$url" "$tmp/registry"
git -C "$tmp/registry" -c advice.detachedHead=false checkout --quiet "$commit" -- 2>/dev/null ||
    { echo "$url has no commit $commit: is it pushed?" >&2; exit 1; }
[[ $(git -C "$tmp/registry" rev-parse HEAD) == "$commit" ]] || { echo "checked out the wrong commit" >&2; exit 1; }
[[ -d $tmp/registry/packages ]] || { echo "the registry at $commit has no packages folder" >&2; exit 1; }

mkdir -p bin
rm -rf "$out.new" "$out"
cp -R "$tmp/registry/packages" "$out.new"
# Which commit it is, shipped beside the packages so a technical user can
# see where they came from.
echo "$commit" > "$out.new/registry-commit"
mv "$out.new" "$out"
