#!/usr/bin/env bash
# loki/loki-s3.yaml against a local S3-compatible store (EXC-905), with plain docker:
# a line from 3 days ago goes to the filesystem period, lines from now on to the bucket;
# chunks and index land in the bucket; with the data volume deleted a fresh Loki still
# finds the bucket's lines. Creates exc905-sh-* containers, a network and a volume, and
# removes them on exit.
#
#   flock /tmp/claude-1000/heavy.lock bash deploy/single-host/tests/log-s3-live.sh
set -euo pipefail

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
DOCKER=${DOCKER:-docker}
P=exc905-sh
LOKI_IMAGE=docker.io/grafana/loki:3.7.8@sha256:1107dd5274e0ada47e42472b7a7e71f3b2a2fe878878108f3e2f9e51528f0193
STORE_IMAGE=docker.io/rustfs/rustfs:1.0.1@sha256:1803faef57627e2d9c2e7d89d655d712ddded5389040054987163043fecb6a3c
RC_IMAGE=docker.io/rustfs/rc:v0.1.36@sha256:ab024bfebee49a750ce886b4c70963ccd9ddaa03f491704a90710641d7a26699
CURL_IMAGE=docker.io/curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69
FROM_DAY=$(date -u -d yesterday +%F)
MARKER=exc905sh_$(date +%s)_$RANDOM
OLD_MARKER=${MARKER}_fs
ok() { echo "ok   $*"; }
fail() { echo "FAIL $*" >&2; exit 1; }
cleanup() {
  $DOCKER rm -f $P-loki $P-store >/dev/null 2>&1 || true
  $DOCKER volume rm $P-data $P-store-data >/dev/null 2>&1 || true
  $DOCKER network rm $P-net >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup
$DOCKER network create $P-net >/dev/null

echo "== the store and its bucket"
$DOCKER volume create $P-store-data >/dev/null
$DOCKER run -d --name $P-store --network $P-net --network-alias store --user 10001:10001 \
  -v $P-store-data:/data -e RUSTFS_VOLUMES=/data -e RUSTFS_ADDRESS=:9000 -e RUSTFS_CONSOLE_ENABLE=false \
  -e RUSTFS_OBS_LOG_DIRECTORY=/tmp -e RUSTFS_ACCESS_KEY=logsid -e RUSTFS_SECRET_KEY=logssecret1 $STORE_IMAGE >/dev/null
# The volume is root-owned on first use; give it to the store's user.
$DOCKER run --rm -v $P-store-data:/data --user 0 --entrypoint chown $CURL_IMAGE -R 10001:10001 /data 2>/dev/null || true
$DOCKER restart $P-store >/dev/null
rc() { $DOCKER run --rm --network $P-net --user 100 -e HOME=/tmp -e RC_CONFIG_DIR=/tmp/.rc --entrypoint sh $RC_IMAGE \
  -c "rc alias set m http://store:9000 logsid logssecret1 >/dev/null && $*"; }
for _ in $(seq 40); do rc "rc mb m/excalibase-logs" >/dev/null 2>&1 && break; sleep 2; done
rc "rc ls m/" | grep -q excalibase-logs && ok "bucket created" || fail "no bucket"

start_loki() {
  $DOCKER rm -f $P-loki >/dev/null 2>&1 || true
  $DOCKER run -d --name $P-loki --network $P-net --network-alias loki --user 10001:10001 --read-only --tmpfs /tmp \
    -v "$DIR/../loki/loki-s3.yaml:/etc/loki/loki.yaml:ro" -v $P-data:/loki \
    -e LOG_RETENTION=168h -e LOG_RETENTION_FREE=24h -e LOG_RETENTION_STANDARD=168h -e LOG_RETENTION_ENTERPRISE=168h \
    -e LOG_S3_ENDPOINT=http://store:9000 -e LOG_S3_BUCKET=excalibase-logs -e LOG_S3_REGION=auto -e LOG_S3_FROM="$FROM_DAY" \
    -e LOG_S3_ACCESS_KEY_ID=logsid -e LOG_S3_SECRET_ACCESS_KEY=logssecret1 \
    $LOKI_IMAGE -config.file=/etc/loki/loki.yaml -config.expand-env=true -target=all >/dev/null
  for _ in $(seq 60); do c -o /dev/null -w '%{http_code}' http://loki:3100/ready 2>/dev/null | grep -q 200 && return; sleep 2; done
  $DOCKER logs $P-loki 2>&1 | tail -20; fail "loki is not ready"
}
c() { $DOCKER run --rm --network $P-net $CURL_IMAGE -s --max-time 20 "$@"; }
found() { # found MARK: lines in the last 4 days
  c -G http://loki:3100/loki/api/v1/query_range --data-urlencode "query={job=\"exc905\"} |= \"$1\"" \
    --data-urlencode "start=$(( ($(date +%s) - 4*86400) * 1000000000 ))" --data-urlencode limit=50 | grep -q "$1"
}
push() { # push AGE_SECONDS TEXT
  c -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -X POST http://loki:3100/loki/api/v1/push \
    --data "{\"streams\":[{\"stream\":{\"job\":\"exc905\"},\"values\":[[\"$(( ($(date +%s) - $1) * 1000000000 ))\",\"$2\"]]}]}" | grep -q 204
}

echo "== loki-s3.yaml, from $FROM_DAY"
$DOCKER volume create $P-data >/dev/null
$DOCKER run --rm -v $P-data:/loki --user 0 --entrypoint chown $CURL_IMAGE -R 10001:10001 /loki 2>/dev/null || true
start_loki
push $((3*86400)) "$OLD_MARKER" && ok "a line from 3 days ago (before $FROM_DAY: the filesystem period) is accepted" || fail "push old"
push 5 "$MARKER" && ok "a line from now (the s3 period) is accepted" || fail "push new"
for _ in $(seq 20); do found "$MARKER" && found "$OLD_MARKER" && break; sleep 3; done
found "$MARKER" && found "$OLD_MARKER" && ok "both found through the query API" || fail "query before flush"
# A graceful stop flushes the chunks and uploads the index; the volume keeps the WAL.
c -X POST "http://loki:3100/ingester/shutdown?flush=true&terminate=true" >/dev/null || true
for _ in $(seq 40); do
  rc "rc ls -r m/excalibase-logs" > /tmp/claude-1000/exc905-sh-bucket.txt 2>/dev/null || true
  grep -q 'index/index_' /tmp/claude-1000/exc905-sh-bucket.txt && break; sleep 3
done
head -12 /tmp/claude-1000/exc905-sh-bucket.txt
grep -q 'fake/' /tmp/claude-1000/exc905-sh-bucket.txt && ok "chunks are in the bucket" || fail "no chunks in the bucket"
grep -q 'index/' /tmp/claude-1000/exc905-sh-bucket.txt && ok "the index is in the bucket" || fail "no index in the bucket"

echo "== the data volume is lost"
$DOCKER rm -f $P-loki >/dev/null
$DOCKER volume rm $P-data >/dev/null
$DOCKER volume create $P-data >/dev/null
$DOCKER run --rm -v $P-data:/loki --user 0 --entrypoint chown $CURL_IMAGE -R 10001:10001 /loki 2>/dev/null || true
start_loki
for _ in $(seq 40); do found "$MARKER" && break; sleep 3; done
found "$MARKER" && ok "the s3 period's line is found on a fresh volume" || fail "the bucket's line is not found"
found "$OLD_MARKER" && echo "info the filesystem line survived (unexpected)" || ok "the filesystem period's line lived on the lost volume and is gone, as documented"
$DOCKER logs $P-loki 2>&1 | grep -i -E 'level=error' | head -5 | sed 's/^/   loki: /' || true
echo "all live checks passed"
