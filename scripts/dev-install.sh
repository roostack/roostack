#!/usr/bin/env bash
# Development install on a Debian 13 VM: builds the Roostack package from this
# checkout and installs it, as a real install would (TASKS 1.12).
#
# Usage (as root, from a checkout with binaries already built into bin/):
#   make build            # on the dev machine
#   sudo scripts/dev-install.sh
set -euo pipefail

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
REPO=$(cd "$(dirname "$0")/.." && pwd)

# A development build: it may trust a development registry key, and no
# real one until the key ceremony (scripts/build-deb.sh).
deb=$(ROOSTACK_DEV_BUILD=1 "$REPO/scripts/build-deb.sh")
arch=$(dpkg --print-architecture)
[[ $deb == *_"$arch".deb ]] || { echo "$deb isn't for this server ($arch): build with GOARCH=$arch make build" >&2; exit 1; }
apt-get update -q
# --allow-downgrades: a dev VM installs whatever is checked out, which
# may be an older commit than the last install. --reinstall: uncommitted
# changes keep the last commit's version, which apt would skip.
# --no-install-recommends: as scripts/install.sh installs it.
apt-get install -y -q --no-install-recommends --allow-downgrades --reinstall "$REPO/$deb"

cat <<EOM

Development notes:
  - From the dev machine, scripts/dev-vm.sh ui tunnels the UI to
    http://127.0.0.1:18481 (VM n: see scripts/dev-vm.sh, e.g. 28481 for VM 2).
  - The setup code: sudo cat /var/lib/roostack/setup-token
  - To deploy new binaries later: sudo install -m755 ~/roostack/bin/roostack ~/roostack/bin/roostack-agent /usr/bin/
    and sudo systemctl restart roostack (or run this script again).
EOM
