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

deb=$("$REPO/scripts/build-deb.sh")
apt-get update -q
# --allow-downgrades: a dev VM installs whatever is checked out, which
# may be an older commit than the last install. --reinstall: uncommitted
# changes keep the last commit's version, which apt would skip.
apt-get install -y -q --allow-downgrades --reinstall "$REPO/$deb"

cat <<EOM

Development notes:
  - From the dev machine, scripts/dev-vm.sh ui tunnels the UI to
    http://127.0.0.1:18481 (VM n: port 18481 + (n-1)*10000).
  - The setup code: sudo cat /var/lib/roostack/setup-token
  - To deploy new binaries later: sudo install -m755 ~/roostack/bin/roostack ~/roostack/bin/roostack-agent /usr/bin/
    and sudo systemctl restart roostack (or run this script again).
EOM
