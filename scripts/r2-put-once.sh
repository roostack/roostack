#!/usr/bin/env bash
# Sourced by .github/workflows/apt-publish.yml: uploads to the release
# bucket through the S3 API (R2_ENDPOINT, R2_BUCKET and the AWS_* keys in
# the environment).

s3() { aws --endpoint-url "$R2_ENDPOINT" "$@"; }

# put_once FILE KEY [CONTENT-TYPE] uploads FILE as KEY unless it's there
# already. Published files never change (the bucket's lock refuses it for
# pool/ and releases/), so the same file again is fine, which lets a failed
# run be run again, and a different one is an error. The SHA-256 is kept in
# the object's metadata to compare: an ETag is only an MD5 for small,
# single-part uploads.
put_once() {
    local file=$1 key=$2 type=${3:-} sum have
    sum=$(sha256sum < "$file" | cut -d' ' -f1)
    if have=$(s3 s3api head-object --bucket "$R2_BUCKET" --key "$key" --query Metadata.sha256 --output text 2>/dev/null); then
        [ "$have" = "$sum" ] || { echo "$key is already published with different contents" >&2; return 1; }
        echo "$key: already published"
        return 0
    fi
    s3 s3 cp --no-progress --metadata "sha256=$sum" ${type:+--content-type "$type"} "$file" "s3://$R2_BUCKET/$key"
}
