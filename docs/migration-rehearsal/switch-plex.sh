#!/usr/bin/env bash
# What "Switch to this data" would do for Plex, by hand.
set -euo pipefail
R() { sudo -u roostack -H bash -c "cd ~ && XDG_RUNTIME_DIR=/run/user/$(id -u roostack) DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u roostack)/bus $*"; }
for i in $(seq 1 60); do [ "$(R systemctl --user is-active roostack-plex-app.service)" = active ] && curl -sf localhost:$(sudo grep -A2 'web:' /var/lib/roostack/services/plex.yaml | grep -o '[0-9]\+')/identity >/dev/null && break; sleep 5; done
PORT=$(sudo grep -A2 'web:' /var/lib/roostack/services/plex.yaml | grep -o '[0-9]\+')
echo "fresh Plex in Roostack: $(curl -s localhost:$PORT/identity | grep -o 'machineIdentifier="[^"]*"')"
t0=$(date +%s)
R systemctl --user stop roostack-plex-app.service
OLD="/srv/compose/plex/config/Library/Application Support/Plex Media Server"
NEW=/srv/roostack/services/plex/config
stamp=$(date +%Y%m%d-%H%M%S)
sudo mv $NEW $NEW.before-import-$stamp
sudo install -d -o roostack -g roostack -m 750 "$NEW/Library/Application Support/Plex Media Server"
# The person's copy, made readable to Roostack (Preferences.xml is 0600 and the old PLEX_UID's).
for e in "$OLD"/*; do
  case $(basename "$e") in Cache|Codecs|"Crash Reports"|Logs|Updates) continue ;; esac
  sudo cp -a "$e" "$NEW/Library/Application Support/Plex Media Server/"
done
sudo chown -R roostack:roostack $NEW
R systemctl --user start roostack-plex-app.service
for i in $(seq 1 60); do curl -sf localhost:$PORT/identity >/dev/null && break; sleep 2; done
echo "switched in $(( $(date +%s) - t0 ))s; port $PORT"
