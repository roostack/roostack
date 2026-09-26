#!/usr/bin/env bash
# Development install for a fresh Debian 13 VM. NOT the production
# installer (that will be a .deb, TASKS 1.12).
#
# UNTESTED: written before a Debian 13 VM was available. Expect to fix it.
#
# Usage (as root, from a checkout with binaries already built into bin/):
#   make build            # on the dev machine
#   sudo scripts/dev-install.sh
set -euo pipefail

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
REPO=$(cd "$(dirname "$0")/.." && pwd)
[[ -x $REPO/bin/mylab && -x $REPO/bin/mylab-agent ]] || { echo "build first: go build -o bin/ ./cmd/..." >&2; exit 1; }

step() { printf '\n==> %s\n' "$*"; }

# as_mylab runs a command as the mylab user, from its home directory, with
# the environment rootless podman and systemctl --user need outside a login
# session (docs/ASSUMPTIONS.md A3).
as_mylab() {
    local uid
    uid=$(id -u mylab)
    (cd / && sudo -u mylab -H env XDG_RUNTIME_DIR="/run/user/$uid" \
        DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" \
        sh -c 'cd "$HOME" && exec "$@"' sh "$@")
}

step "Installing packages"
apt-get update -q
apt-get install -y -q podman passt nftables uidmap crun catatonit restic unattended-upgrades

step "Creating the mylab user"
if ! id mylab &>/dev/null; then
    useradd --system --create-home --home-dir /var/lib/mylab/home \
        --shell /usr/sbin/nologin mylab
fi
# System users get no subordinate IDs by default; rootless podman needs them
# (docs/ASSUMPTIONS.md B1).
if ! grep -q '^mylab:' /etc/subuid; then
    usermod --add-subuids 200000-265535 --add-subgids 200000-265535 mylab
fi
# GPU access for rootless containers (docs/ASSUMPTIONS.md A7).
for g in render video; do
    getent group "$g" >/dev/null && usermod -aG "$g" mylab
done
# Start the mylab user's systemd manager at boot (docs/ASSUMPTIONS.md A1).
loginctl enable-linger mylab
MYLAB_UID=$(id -u mylab)

step "Creating directories"
install -d -o mylab -g mylab -m 0750 /var/lib/mylab /var/lib/mylab/services
install -d -o mylab -g mylab -m 0750 /srv/mylab /srv/mylab/services /srv/mylab/media

step "Installing binaries and catalog"
install -m 0755 "$REPO/bin/mylab" "$REPO/bin/mylab-agent" /usr/local/bin/
for pkg in "$REPO"/docs/examples/*.yaml; do
    name=$(basename "$pkg" .yaml)
    [[ $name == lab ]] && continue
    install -D -m 0644 "$pkg" "/usr/share/mylab/catalog/$name/package.yaml"
done
if [[ ! -f /var/lib/mylab/lab.yaml ]]; then
    install -o mylab -g mylab -m 0640 "$REPO/docs/examples/lab.yaml" /var/lib/mylab/lab.yaml
    echo "Edit /var/lib/mylab/lab.yaml to set your domain and DNS provider."
fi

step "Configuring automatic OS updates"
install -m 0644 "$REPO/packaging/apt/52mylab-unattended-upgrades" /etc/apt/apt.conf.d/

step "Installing systemd units"
install -m 0644 "$REPO/packaging/systemd/mylab-agent.service" /etc/systemd/system/
sed "s/@MYLAB_UID@/$MYLAB_UID/g" "$REPO/packaging/systemd/mylab.service" \
    > /etc/systemd/system/mylab.service
systemctl daemon-reload
systemctl enable --now mylab-agent.service

step "Building the MyLab Caddy image (as mylab)"
tmp=$(mktemp -d)
cp "$REPO/build/caddy/Containerfile" "$tmp/"
chown -R mylab:mylab "$tmp"
as_mylab podman build -t mylab-caddy "$tmp"
rm -rf "$tmp"

step "Starting the web UI"
systemctl enable --now mylab.service

cat <<EOF

Done. Next steps:
  0. The UI listens on 127.0.0.1:8480 only. From the dev machine run
     scripts/dev-vm.sh ui, open http://127.0.0.1:18481, and create the
     Owner account with the setup code from: sudo cat /var/lib/mylab/setup-token
  1. Edit /var/lib/mylab/lab.yaml (domain, email, DNS provider), then
     restart the UI: sudo systemctl restart mylab
  2. Install a service by creating /var/lib/mylab/services/<name>.yaml, e.g.:
       printf 'package: vaultwarden\nversion: 1.34.3-mylab.1\n' \\
         | sudo -u mylab tee /var/lib/mylab/services/vaultwarden.yaml
  3. Preview and apply as the mylab user:
       sudo -u mylab XDG_RUNTIME_DIR=/run/user/$MYLAB_UID mylab apply --dry-run
       sudo -u mylab XDG_RUNTIME_DIR=/run/user/$MYLAB_UID \\
         MYLAB_SECRET_MYLAB_CADDY_DNS_TOKEN=<token> mylab apply
EOF
