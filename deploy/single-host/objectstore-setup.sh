#!/bin/sh
# Prepares the bundled object store (EXC-578): buckets, one user per bucket
# whose policy covers that bucket alone, and the file bucket's CORS.
# Idempotent. Keys arrive as env and are never echoed.
set -eu
fail() { echo "ERROR: $1" >&2; exit 1; }

echo "[1/4] waiting for the store"
i=0
until rc alias set store "$ENDPOINT" "$ROOT_ID" "$ROOT_SECRET" >/dev/null 2>&1 && rc ls store/ >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || fail "the store at $ENDPOINT did not answer"
  sleep 3
done

echo "[2/4] buckets"
for bucket in "$BACKUPS_BUCKET" "$FILES_BUCKET"; do
  rc mb store/"$bucket" >/dev/null || fail "creating bucket $bucket failed"
done

# user <name> <id> <secret> <bucket>
user() {
  printf '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::%s","arn:aws:s3:::%s/*"]}]}' \
    "$4" "$4" > /tmp/policy.json
  rc admin policy create store/ "excalibase-$1" /tmp/policy.json >/dev/null || fail "creating policy excalibase-$1 failed"
  rc admin user add store/ "$2" "$3" >/dev/null || fail "creating the $1 user failed"
  rc admin policy attach store/ "excalibase-$1" --user "$2" >/dev/null || fail "attaching policy excalibase-$1 failed"
  echo "  $1: bucket $4 only"
}

echo "[3/4] users"
user backups "$BACKUPS_ID" "$BACKUPS_SECRET" "$BACKUPS_BUCKET"
user files "$FILES_ID" "$FILES_SECRET" "$FILES_BUCKET"

echo "[4/4] customer-file CORS"
# set -f: the origin "*" must not expand to file names.
rules=""
set -f
for origin in $(echo "$CORS_ORIGINS" | tr ',' ' '); do
  rules="$rules<AllowedOrigin>$origin</AllowedOrigin>"
done
set +f
printf '<CORSConfiguration><CORSRule>%s<AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod><AllowedMethod>HEAD</AllowedMethod><AllowedHeader>*</AllowedHeader><ExposeHeader>ETag</ExposeHeader><MaxAgeSeconds>3600</MaxAgeSeconds></CORSRule></CORSConfiguration>' \
  "$rules" > /tmp/cors.xml
rc bucket cors set store/"$FILES_BUCKET" /tmp/cors.xml >/dev/null || fail "setting the file bucket's CORS failed"
echo "OBJECT STORE READY"
