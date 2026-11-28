#!/usr/bin/env bash
# The documented switch from --host-logs to --host-logs-s3 (EXC-905) with real docker compose: the two Loki
# services come from compose.yaml as rendered, cut down to a project of their own (names, networks and volume
# prefixed exc905-sw) beside a local S3-compatible store. Steps as docs/deployment/docker-local.md gives them:
# remove the old service, change the profile, up -d. The old container is gone, the new one answers as loki on
# the same data volume. Removes everything it created on exit.
#
#   flock /tmp/claude-1000/heavy.lock bash deploy/single-host/tests/log-s3-switch.sh
set -euo pipefail

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
WORK=$(mktemp -d)
P=exc905-sw
COMPOSE="docker compose -p $P -f $WORK/compose.yaml"
STORE_IMAGE=docker.io/rustfs/rustfs:1.0.1@sha256:1803faef57627e2d9c2e7d89d655d712ddded5389040054987163043fecb6a3c
RC_IMAGE=docker.io/rustfs/rc:v0.1.36@sha256:ab024bfebee49a750ce886b4c70963ccd9ddaa03f491704a90710641d7a26699
CURL_IMAGE=docker.io/curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69
ok() { echo "ok   $*"; }
fail() { echo "FAIL $*" >&2; exit 1; }
cleanup() { $COMPOSE --profile hostlogs --profile hostlogs-s3 down -v >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

hex=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
env -i PATH="$PATH" HOME="$HOME" POSTGRES_PASSWORD=$hex SETUP_TOKEN=$hex EXCALIBASE_DOMAIN=example.test EXCALIBASE_TLS=internal \
  STUDIO_ALLOW_CIDRS="0.0.0.0/0 ::/0" ADMIN_EMAIL=a@example.test ENGINE_SOCKET=/var/run/docker.sock ENGINE_SOCKET_GID=999 \
  OBJECTSTORE_ROOT_SECRET=${hex%?}1 OBJECTSTORE_BACKUPS_SECRET=${hex%?}2 OBJECTSTORE_FILES_SECRET=${hex%?}3 FILES_PUBLIC_URL=https://files.example.test \
  COMPOSE_PROFILES=hostlogs,hostlogs-s3 LOG_S3_ENDPOINT=http://store:9000 LOG_S3_BUCKET=excalibase-logs LOG_S3_REGION=auto \
  LOG_S3_FROM=$(date -u -d yesterday +%F) LOG_S3_ACCESS_KEY_ID=logsid LOG_S3_SECRET_ACCESS_KEY=logssecret1 \
  docker compose --project-directory "$DIR/.." -f "$DIR/../compose.yaml" config --format json >"$WORK/rendered.json"
python3 - "$WORK" "$P" "$STORE_IMAGE" <<'PY'
import json, sys
work, p, store = sys.argv[1:4]
doc = json.load(open(f"{work}/rendered.json"))
services = {}
for name in ("loki", "loki-s3"):
    svc = doc["services"][name]
    svc.pop("depends_on", None)
    svc["container_name"] = f"{p}-{svc['container_name']}"
    svc["networks"] = {("logs" if n == "logs" else n): (v or {}) for n, v in svc["networks"].items()}
    svc["volumes"] = [v for v in svc["volumes"]]
    services[name] = svc
services["store"] = {"image": store, "container_name": f"{p}-store", "user": "10001:10001", "profiles": ["hostlogs-s3"],
  "environment": {"RUSTFS_VOLUMES": "/data", "RUSTFS_ADDRESS": ":9000", "RUSTFS_CONSOLE_ENABLE": "false",
                  "RUSTFS_OBS_LOG_DIRECTORY": "/tmp", "RUSTFS_ACCESS_KEY": "logsid", "RUSTFS_SECRET_KEY": "logssecret1"},
  "volumes": [{"type": "volume", "source": "store_data", "target": "/data"}], "networks": ["logs-out"]}
nets = {n: dict(doc["networks"][n], name=f"{p}-{n}") for n in ("logs", "logs-out")}
vols = {"logs_data": {"name": f"{p}-logs"}, "store_data": {"name": f"{p}-store-data"}}
json.dump({"services": services, "networks": nets, "volumes": vols}, open(f"{work}/compose.yaml", "w"))
PY
docker volume create $P-store-data >/dev/null
docker run --rm -v $P-store-data:/data --user 0 --entrypoint chown $CURL_IMAGE -R 10001:10001 /data 2>/dev/null || true
docker volume create $P-logs >/dev/null
docker run --rm -v $P-logs:/loki --user 0 --entrypoint chown $CURL_IMAGE -R 10001:10001 /loki 2>/dev/null || true

ready() { # ready SERVICE: asks Loki as `loki` on the logs network
  for _ in $(seq 60); do
    docker run --rm --network $P-logs $CURL_IMAGE -s -o /dev/null -w '%{http_code}' --max-time 5 http://loki:3100/ready 2>/dev/null | grep -q 200 && return 0
    sleep 3
  done
  docker logs $P-excalibase-$1 2>&1 | tail -15; return 1
}

echo "== an install with --host-logs"
COMPOSE_PROFILES=hostlogs $COMPOSE up -d loki >/dev/null
ready loki && ok "excalibase-loki answers as loki"
[ "$(docker ps -q -f name=^$P-excalibase-loki\$)" ] || fail "the filesystem Loki is not running"
docker run --rm --network $P-logs $CURL_IMAGE -s -X POST -H 'Content-Type: application/json' http://loki:3100/loki/api/v1/push \
  --data "{\"streams\":[{\"stream\":{\"job\":\"exc905sw\"},\"values\":[[\"$(date +%s)000000000\",\"before the switch\"]]}]}" >/dev/null

echo "== the documented switch"
# The bucket first (as the operator creates it), then the three steps of the docs.
COMPOSE_PROFILES=hostlogs-s3 $COMPOSE up -d store >/dev/null
for _ in $(seq 40); do
  docker run --rm --network $P-logs-out --user 100 -e HOME=/tmp -e RC_CONFIG_DIR=/tmp/.rc --entrypoint sh $RC_IMAGE \
    -c "rc alias set m http://store:9000 logsid logssecret1 >/dev/null && rc mb m/excalibase-logs" >/dev/null 2>&1 && break; sleep 2
done
$COMPOSE --profile hostlogs rm -sf loki >/dev/null 2>&1
[ -z "$(docker ps -aq -f name=^$P-excalibase-loki\$)" ] && ok "the old container is stopped and removed" || fail "the old container is still there"
COMPOSE_PROFILES=hostlogs-s3 $COMPOSE up -d >/dev/null 2>"$WORK/up.err" || fail "up -d after the switch: $(cat "$WORK/up.err")"
ready loki-s3 && ok "excalibase-loki-s3 answers as loki"
[ -n "$(docker ps -q -f name=^$P-excalibase-loki-s3\$)" ] && [ -z "$(docker ps -q -f name=^$P-excalibase-loki\$)" ] \
  && ok "only the S3 Loki runs" || fail "containers: $(docker ps --format '{{.Names}}' -f name=$P)"
docker inspect $P-excalibase-loki-s3 --format '{{range .Mounts}}{{.Name}} {{end}}' | grep -q "$P-logs" && ok "it reuses the data volume of the old one" || fail "volume"
echo "all live checks passed"
