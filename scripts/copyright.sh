#!/usr/bin/env bash
# Writes the package's Debian copyright file (machine-readable format) to
# stdout: every Go module compiled into the binaries (from the binary's own
# build information, licence texts from the module cache), the Go standard
# library, and the vendored web libraries and theme palettes
# (packaging/licenses). Run after `make build`.
set -euo pipefail
REPO=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO"

# indent formats a licence text for a copyright file: each line indented,
# blank lines as " .".
indent() { sed -e 's/[[:space:]]*$//' -e 's/^$/./' -e 's/^/ /'; }

# name guesses a licence's short name from its text.
name() {
    local f=$1
    if grep -q "Apache License" "$f" && grep -q "MIT License\|Permission is hereby granted" "$f"; then echo "MIT and Apache-2.0"
    elif grep -q "Apache License" "$f"; then echo "Apache-2.0"
    elif grep -q "Permission is hereby granted, free of charge" "$f"; then echo "MIT"
    elif grep -q "Zero-Clause BSD\|with or without fee is hereby granted" "$f"; then echo "0BSD"
    elif grep -q "Neither the name" "$f"; then echo "BSD-3-Clause"
    elif grep -q "Redistribution and use" "$f"; then echo "BSD-2-Clause"
    else echo "other"; fi
}

cat <<HDR
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: Roostack
Source: https://roostack.cloud
Comment: Roostack itself is under the Apache License 2.0. The other
 paragraphs cover code compiled into or served by Roostack (THIRD_PARTY.md).

Files: *
Copyright: The Roostack authors
License: Apache-2.0
 On Debian systems, the full text of the Apache License 2.0 is in
 /usr/share/common-licenses/Apache-2.0.

HDR

goroot=$(go env GOROOT)
# Some distributions keep Go's licence outside GOROOT (Arch:
# /usr/share/licenses/go; Homebrew: the folder above GOROOT).
golicense=""
for f in "$goroot/LICENSE" /usr/share/licenses/go/LICENSE "$goroot/../LICENSE"; do
    [[ -f $f ]] && { golicense=$f; break; }
done
[[ -n $golicense ]] || { echo "copyright.sh: can't find Go's LICENSE" >&2; exit 1; }
echo "Files: usr/bin/roostack usr/bin/roostack-agent"
echo "Copyright: The Go Authors"
echo "Comment: The Go standard library, compiled in ($(go env GOVERSION))."
echo "License: BSD-3-Clause"
indent < "$golicense"
echo

go version -m bin/roostack bin/roostack-agent | awk '$1 == "dep" { print $2, $3 }' | sort -u | while read -r mod ver; do
    dir=$(go mod download -json "$mod@$ver" | sed -n 's/^[[:space:]]*"Dir": "\(.*\)",$/\1/p')
    lic=$(ls "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* 2>/dev/null | head -1 || true)
    [[ -n $lic ]] || { echo "copyright.sh: no licence file for $mod@$ver" >&2; exit 1; }
    echo "Files: usr/bin/roostack usr/bin/roostack-agent"
    holders=$(grep '^Copyright' "$lic" | sed 's/^Copyright[[:space:]]*//' | paste -sd'\n' - | sed '2,$s/^/ /' || true)
    echo "Copyright: ${holders:-The $mod authors}"
    echo "Comment: Go module $mod $ver, compiled in."
    echo "License: $(name "$lic")"
    indent < "$lic"
    echo
done

vendored() { # file-in-package holder licence-file
    echo "Files: $1"
    echo "Copyright: $2"
    echo "License: $(name "$3")"
    indent < "$3"
    echo
}
vendored "usr/bin/roostack" "The htmx authors (htmx, served by Roostack)" packaging/licenses/htmx
vendored "usr/bin/roostack" "$(grep -m1 -i copyright packaging/licenses/alpine | sed 's/^Copyright //') (Alpine.js, served by Roostack)" packaging/licenses/alpine
for theme in catppuccin gruvbox nord solarized everforest rose-pine; do
    holder=$(grep -m1 -i '^copyright' "packaging/licenses/$theme" | sed 's/^[Cc]opyright //')
    vendored "usr/bin/roostack" "$holder (the $theme colour palette)" "packaging/licenses/$theme"
done
cat <<TN
Files: usr/bin/roostack
Copyright: Folke Lemaitre (the Tokyo Night colour palette)
License: Apache-2.0
 On Debian systems, the full text of the Apache License 2.0 can be found in
 /usr/share/common-licenses/Apache-2.0.
TN
