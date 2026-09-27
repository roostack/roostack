#!/usr/bin/env bash
# Development install on a Debian 13 VM: builds the MyLab package from this
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
apt-get install -y -q "$REPO/$deb"

cat <<EOM

Development notes:
  - From the dev machine, scripts/dev-vm.sh ui tunnels the UI to
    http://127.0.0.1:18481 (VM n: port 18481 + (n-1)*10000).
  - The setup code: sudo cat /var/lib/mylab/setup-token
  - To deploy new binaries later: sudo install -m755 ~/mylab/bin/mylab ~/mylab/bin/mylab-agent /usr/bin/
    and sudo systemctl restart mylab (or run this script again).
EOM
