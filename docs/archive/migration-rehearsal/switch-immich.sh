#!/usr/bin/env bash
# What "Switch to this data" would do for Immich, by hand: stop the old app,
# dump, copy by container path, load with the restore loader's command, start.
set -euo pipefail
R() { sudo -u roostack -H bash -c "cd ~ && XDG_RUNTIME_DIR=/run/user/$(id -u roostack) DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u roostack)/bus $*"; }
OLD=/srv/compose/immich/library
NEW=/srv/roostack/media/immich
t0=$(date +%s)
echo "== stop the old app (its database keeps running)"
(cd /srv/compose/immich && sudo docker-compose stop immich-server immich-machine-learning 2>&1 | tail -1)
echo "== dump"
sudo docker exec immich_postgres pg_dump --format=custom --no-owner --username=postgres immich > ~/main.dump
ls -la ~/main.dump
sudo docker exec -i immich_postgres pg_restore --list < ~/main.dump | sed -n '1,14p'
echo "== stop the service in Roostack"
R systemctl --user stop roostack-immich-server.service
echo "== move its data aside, copy by container path (as roostack)"
stamp=$(date +%Y%m%d-%H%M%S)
for s in library thumbs encoded; do sudo mv $NEW/$s $NEW/$s.before-import-$stamp; sudo install -d -o roostack -g roostack -m 750 $NEW/$s; done
# /data <- old mount, without the package's own submounts
for e in $(sudo ls -A $OLD); do
  case $e in thumbs|encoded-video) continue ;; esac
  sudo -u roostack cp -r --preserve=timestamps "$OLD/$e" "$NEW/library/"
done
sudo -u roostack cp -r --preserve=timestamps $OLD/thumbs/. $NEW/thumbs/
sudo -u roostack cp -r --preserve=timestamps $OLD/encoded-video/. $NEW/encoded/
echo "== load the dump (the restore loader's command)"
R podman exec -i roostack-immich-db-main pg_restore --clean --if-exists --no-owner --username=immich --dbname=immich < ~/main.dump && echo "pg_restore ok"
echo "== start"
R systemctl --user start roostack-immich-server.service
for i in $(seq 1 30); do curl -sf localhost:20000/api/server/ping >/dev/null && break; sleep 3; done
echo "switched in $(( $(date +%s) - t0 ))s"
