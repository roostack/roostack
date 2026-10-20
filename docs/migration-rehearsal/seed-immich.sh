#!/usr/bin/env bash
# Fill the Compose Immich with test data: an admin, a second person, photos,
# videos, an album and a shared link. Test-only values.
set -euo pipefail
B=http://localhost:2283/api
j() { curl -sf -H 'Content-Type: application/json' "$@"; }
j -X POST $B/auth/admin-sign-up -d '{"email":"admin@example.test","password":"rehearsal-admin","name":"Admin"}' >/dev/null || true
TOKEN=$(j -X POST $B/auth/login -d '{"email":"admin@example.test","password":"rehearsal-admin"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["accessToken"])')
A=(-H "Authorization: Bearer $TOKEN")
j "${A[@]}" -X POST $B/admin/users -d '{"email":"sam@example.test","password":"rehearsal-sam","name":"Sam"}' >/dev/null || true
n=0
for f in ~/testmedia/photos/*.jpg ~/testmedia/videos/*.mp4; do
  n=$((n+1))
  d=$(date -u -d "2024-01-01 +$n hours" +%Y-%m-%dT%H:%M:%S.000Z)
  curl -sf "${A[@]}" -X POST $B/assets -F "assetData=@$f" -F "deviceAssetId=rehearsal-$n" -F deviceId=rehearsal \
    -F "fileCreatedAt=$d" -F "fileModifiedAt=$d" >/dev/null
done
echo "uploaded $n"
IDS=$(curl -sf "${A[@]}" -X POST $B/search/metadata -H 'Content-Type: application/json' -d '{"size":20}' | python3 -c 'import sys,json;print(json.dumps([a["id"] for a in json.load(sys.stdin)["assets"]["items"]]))')
ALBUM=$(j "${A[@]}" -X POST $B/albums -d "{\"albumName\":\"Holiday\",\"assetIds\":$IDS}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
j "${A[@]}" -X POST $B/shared-links -d "{\"type\":\"ALBUM\",\"albumId\":\"$ALBUM\"}" | python3 -c 'import sys,json;print("shared link key", json.load(sys.stdin)["key"])'
echo "$TOKEN" > ~/immich-token
