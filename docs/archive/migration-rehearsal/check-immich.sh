#!/usr/bin/env bash
set -uo pipefail
R() { sudo -u roostack -H bash -c "cd ~ && XDG_RUNTIME_DIR=/run/user/$(id -u roostack) DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u roostack)/bus $*"; }
q() { R podman exec roostack-immich-db-main psql -U immich -d immich -tAc "\"$1\""; }
echo "== server log about the media location"
R podman logs roostack-immich-server 2>&1 | grep -i -E 'media location|location|migrat|error|warn' | head -15
echo "== rows"
for t in asset asset_file smart_search '\"user\"' album shared_link; do echo "$t: $(q "select count(*) from $t")"; done
q "select value from system_metadata where key='MediaLocation'"
q "select \\\"originalPath\\\" from asset limit 2"
echo "== every original and every thumbnail on disk"
missing=0
while read -r p; do
  h=${p/#\/data\/thumbs//srv/roostack/media/immich/thumbs}
  h=${h/#\/data\/encoded-video//srv/roostack/media/immich/encoded}
  h=${h/#\/data//srv/roostack/media/immich/library}
  sudo test -f "$h" || { missing=$((missing+1)); echo "missing $p"; }
done < <(q "select \\\"originalPath\\\" from asset union all select path from asset_file union all select \\\"encodedVideoPath\\\" from asset where \\\"encodedVideoPath\\\" <> ''" )
echo "missing: $missing"
echo "== sign in with the old passwords"
B=http://localhost:20000/api
for who in admin:rehearsal-admin sam:rehearsal-sam; do
  u=${who%%:*}; pw=${who#*:}
  curl -s -o /dev/null -w "$u login %{http_code}\n" -H 'Content-Type: application/json' -X POST $B/auth/login -d "{\"email\":\"$u@example.test\",\"password\":\"$pw\"}"
done
TOKEN=$(curl -s -H 'Content-Type: application/json' -X POST $B/auth/login -d '{"email":"admin@example.test","password":"rehearsal-admin"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["accessToken"])')
curl -s -H "Authorization: Bearer $TOKEN" $B/server/statistics | head -c 300; echo
A=$(curl -s -H "Authorization: Bearer $TOKEN" $B/albums | python3 -c 'import sys,json;a=json.load(sys.stdin)[0];print(a["albumName"],a["assetCount"])'); echo "album: $A"
ID=$(curl -s -H "Authorization: Bearer $TOKEN" "$B/assets/random?count=1" 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["id"])' 2>/dev/null || q "select id from asset limit 1")
curl -s -o /dev/null -w "thumbnail %{http_code} %{content_type} %{size_download}B\n" -H "Authorization: Bearer $TOKEN" "$B/assets/$ID/thumbnail"
curl -s -o /dev/null -w "original %{http_code} %{size_download}B\n" -H "Authorization: Bearer $TOKEN" "$B/assets/$ID/original"
curl -s -H "Authorization: Bearer $TOKEN" "$B/shared-links" | python3 -c 'import sys,json;l=json.load(sys.stdin)[0];print("shared link", l["key"][:12]+"…", "album", l["album"]["albumName"])'
