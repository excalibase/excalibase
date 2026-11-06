#!/usr/bin/env bash
# The guard in the built image, with real Postgres, shaped like a pod: Postgres
# and the gateway share a network namespace and restart apart. Usage: <image>.
set -u
IMAGE="$1"
HERE="$(cd "$(dirname "$0")" && pwd)"
CATALOG="$HERE/../../../server-go/internal/config/postgres_catalog.yaml"
POSTGRES_IMAGE="$(awk '/major: "17"/ {found=1} found && /image:/ {print $2; exit}' "$CATALOG")"
[ -n "$POSTGRES_IMAGE" ] || { echo "no Postgres 17 image in the catalogue"; exit 2; }

NAME="exc587-$$"
VOLUME="$NAME-data"
WORK="$(mktemp -d)"
failures=0

cleanup() {
  docker rm -f "$NAME-gw" "$NAME-db" "$NAME-ns" >/dev/null 2>&1
  docker volume rm -f "$VOLUME" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: expected '$2', got '$3'"; failures=$((failures + 1)); fi
}

cat > "$WORK/start.sh" <<'START'
set -eu
export PGDATA=/var/lib/postgresql/data
[ -s "$PGDATA/PG_VERSION" ] || initdb -D "$PGDATA" -U postgres --auth-local=trust --auth-host=trust -E UTF8 >/dev/null
exec postgres -D "$PGDATA" -c listen_addresses='*' -c shared_preload_libraries=pg_cron,pg_documentdb_core,pg_documentdb \
  -c cron.database_name=postgres -c cron.host=/var/run/postgresql -c documentdb.localhost_connection_string=host=/var/run/postgresql
START
chmod 644 "$WORK/start.sh"; chmod 755 "$WORK"

db_exec() { docker exec "$NAME-db" "$@"; }
sql() { db_exec psql -h /var/run/postgresql -U postgres -d postgres -XAtc "$1"; }
start_db() {
  docker rm -f "$NAME-db" >/dev/null 2>&1
  docker run -d --name "$NAME-db" --network "container:$NAME-ns" -v "$VOLUME:/var/lib/postgresql" -v "$WORK/start.sh:/tmp/start.sh:ro" \
    --stop-signal SIGINT --entrypoint bash "$POSTGRES_IMAGE" /tmp/start.sh >/dev/null
  for _ in $(seq 60); do db_exec pg_isready -q -h /var/run/postgresql && return; sleep 1; done
  echo "Postgres did not start"; docker logs "$NAME-db" | tail -20; exit 2
}
restarts() { docker inspect "$NAME-gw" --format '{{.RestartCount}}'; }
gateway_log() { docker logs "$NAME-gw" 2>&1; }
# Waits until the gateway's log shows $1 at least $2 times, up to $3 seconds.
await_log() {
  local deadline=$((SECONDS + $3))
  while [ "$SECONDS" -lt "$deadline" ]; do
    [ "$(gateway_log | grep -c -- "$1")" -ge "$2" ] && return 0
    sleep 1
  done
  return 1
}
# A TLS client holding a connection; its pid is in $client when it is up.
connect_client() {
  timeout 120 openssl s_client -connect "$GATEWAY_ADDRESS" -quiet < <(sleep 200) >/dev/null 2>"$WORK/client" &
  client=$!
  sleep 2
}
# Seconds until the connection ended, or "open".
await_client_end() {
  local started=$SECONDS
  while [ $((SECONDS - started)) -lt "$1" ]; do
    kill -0 "$client" 2>/dev/null || { echo ended; return; }
    sleep 1
  done
  echo open
}
demote() {
  docker stop -t 30 "$NAME-db" >/dev/null
  docker run --rm -v "$VOLUME:/var/lib/postgresql" --entrypoint touch "$POSTGRES_IMAGE" /var/lib/postgresql/data/standby.signal
  start_db
}

docker run -d --name "$NAME-ns" -p 127.0.0.1::10260 --entrypoint sh "$POSTGRES_IMAGE" -c 'exec sleep 3600' >/dev/null
GATEWAY_ADDRESS="$(docker port "$NAME-ns" 10260/tcp | head -1)"
start_db
sql 'CREATE EXTENSION IF NOT EXISTS documentdb CASCADE' >/dev/null 2>&1
sql 'CREATE ROLE documentdb LOGIN' >/dev/null
sql 'GRANT documentdb_admin_role TO documentdb' >/dev/null

docker run -d --name "$NAME-gw" --restart=always --network "container:$NAME-ns" "$IMAGE" --create-user false --pg-port 5432 >/dev/null
if ! await_log 'armed' 1 60; then echo "FAIL the guard never armed"; gateway_log | tail -20; exit 1; fi
await_log 'Gateway ready' 1 30
check 'the probe logs in with the role it names, on a real Postgres' 0 "$(gateway_log | grep -c 'probe failing')"
check 'the gateway runs under the guard' 1 "$(docker top "$NAME-gw" -eo pid,args | grep -c '/usr/bin/documentdb_gateway')"

echo "-- demotion"
connect_client
demote
check 'Postgres really is a standby' t "$(sql 'SELECT pg_is_in_recovery()')"
check 'the client connection is ended' ended "$(await_client_end 40)"
check 'the guard says why' 1 "$(gateway_log | grep -c 'no longer the primary')"
await_log 'Gateway ready' 2 40
check 'the gateway came back once' 1 "$(restarts)"
sleep 10
check 'and stays up as a standby, not looping' 1 "$(restarts)"

echo "-- outage"
db_exec pg_ctl promote -D /var/lib/postgresql/data -w >/dev/null
await_log 'armed' 2 20 || echo "not re-armed"
connect_client
docker stop -t 30 "$NAME-db" >/dev/null
check 'an outage past the limit ends the client connection' ended "$(await_client_end 40)"
check 'the guard says why' 1 "$(gateway_log | grep -c 'not answered for 15s')"
check 'the gateway restarted' 2 "$(restarts)"
sleep 20
check 'and waits for Postgres, not looping' 2 "$(restarts)"
start_db
await_log 'armed' 3 30 || echo "not re-armed after the outage"
check 'the guard arms again when Postgres is back' 3 "$(gateway_log | grep -c 'armed')"
# Until the gateway is up, its entrypoint has SIGINT ignored and a stop falls back to the kill.
await_log 'Gateway ready' 3 40
check 'and the gateway serves' 3 "$(gateway_log | grep -c 'Gateway ready')"

echo "-- clean stop"
started=$SECONDS
docker stop -t 30 --signal SIGINT "$NAME-gw" >/dev/null
check 'SIGINT stops it promptly' yes "$([ $((SECONDS - started)) -lt 5 ] && echo yes || echo "no: $((SECONDS - started))s")"
check 'every end so far was the gateway leaving cleanly' 3 "$(gateway_log | grep -c 'Ctrl+C received')"
check 'with its exit code' 0 "$(docker inspect "$NAME-gw" --format '{{.State.ExitCode}}')"
docker start "$NAME-gw" >/dev/null
await_log 'Gateway ready' 4 40
started=$SECONDS
docker stop -t 30 "$NAME-gw" >/dev/null
check 'SIGTERM, which upstream ignores as PID 1, stops it promptly' yes "$([ $((SECONDS - started)) -lt 5 ] && echo yes || echo "no: $((SECONDS - started))s")"

exit "$failures"
