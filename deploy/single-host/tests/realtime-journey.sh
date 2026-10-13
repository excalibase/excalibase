# Sourced by boot-smoke.sh (EXC-612): realtime on a single host. A Studio user
# turns realtime on for a table and a subscriber receives its inserts, updates
# and deletes; NATS restarting, a pause and a resume do not stop the changes;
# neither the tenant database nor a deleted project's watcher is left reaching
# or holding anything.
# Needs from boot-smoke.sh: c ok ko DOCKER PORT T ORG PID AT TC W TESTS.

RAPI=https://studio.localhost:$PORT/api
rjson() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
rapi() { c --max-time 600 -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
rwait() {
  local st=
  for i in $(seq 120); do
    st=$(rapi "$RAPI/provision/$1/" | rjson 'd.get("status","")+" "+(d.get("failureReason") or "")')
    case "$st" in "$2 "*|FAILED*) break ;; esac
    sleep 3
  done
  echo "$st"
}
rsql() { rapi -X POST "$RAPI/schema/$PID/query" -d "$(python3 -c 'import json,sys;print(json.dumps({"query":sys.argv[1]}))' "$1")" -o /dev/null -w '%{http_code}'; }
# tenant_sql <project> <sql>: the project's own superuser over its local socket.
tenant_sql() {
  local database
  database=$(rapi "$RAPI/provision/$1/" | rjson 'd.get("databaseName") or "postgres"')
  $DOCKER exec "excalibase-$1-postgres" psql -qtAX -U postgres -d "$database" -c "$2" 2>&1
}
watcher_of() { echo "excalibase-$1-watcher"; }
watcher_running() { [ "$($DOCKER inspect "$(watcher_of "$1")" --format '{{.State.Running}}' 2>/dev/null)" = true ]; }
# slot_streaming <project>: its watcher holds the cdc_watcher slot open.
slot_streaming() {
  local active=
  for i in $(seq 40); do
    active=$(tenant_sql "$1" "SELECT active FROM pg_replication_slots WHERE slot_name = 'cdc_watcher'")
    [ "$active" = t ] && return 0
    sleep 3
  done
  return 1
}
# subscribe <file> <seconds>: a subscriber to notes, in the background, once it is accepted.
subscribe() {
  : > "$1"
  python3 "$TESTS/realtime_subscriber.py" "$PORT" "$PID" "$AT" publicNotesChanges "$1" "$2" &
  for i in $(seq 30); do grep -q '^ready' "$1" && return 0; grep -q '^closed' "$1" && break; sleep 1; done
  echo "subscriber not accepted: $(head -c 300 "$1")"
  return 1
}
# received <file> <operation> <text>: a change of that kind carrying text arrived.
received() {
  for i in $(seq 40); do
    grep -v '^ready' "$1" | grep -i "\"$2\"" | grep -qF "$3" && return 0
    sleep 1
  done
  return 1
}

echo "== realtime: the tenant database decodes logically"
level=$(tenant_sql "$PID" "SHOW wal_level")
[ "$level" = logical ] && ok "the new tenant runs wal_level=logical" || ko "tenant wal_level: '$level'"

echo "== realtime: a watcher streams the project's changes"
rt=$(rapi -X PUT "$RAPI/projects/$PID/realtime/tables/public/notes" -w ' %{http_code}')
case "$rt" in *" 200") ok "realtime on for public.notes" ;; *) ko "set realtime: ${rt:0:300}" ;; esac
for i in $(seq 30); do watcher_running "$PID" && break; sleep 2; done
watcher_running "$PID" && ok "watcher $(watcher_of "$PID") runs" || ko "no watcher for $PID: $($DOCKER ps -a --filter name=-watcher --format '{{.Names}} {{.Status}}')"
wnets=$($DOCKER inspect "$(watcher_of "$PID")" --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' 2>/dev/null)
[ "$wnets" = "excalibase-cdc excalibase-tenants " ] && ok "watcher on excalibase-cdc and excalibase-tenants only" || ko "watcher networks: '$wnets'"
wports=$($DOCKER port "$(watcher_of "$PID")" 2>/dev/null)
[ -z "$wports" ] && ok "watcher publishes no port" || ko "watcher publishes $wports"
wcaps=$($DOCKER inspect "$(watcher_of "$PID")" --format '{{.HostConfig.CapDrop}} {{.HostConfig.SecurityOpt}} {{.HostConfig.Memory}}' 2>/dev/null)
case "$wcaps" in *ALL*no-new-privileges*" 0") ko "watcher has no memory limit: $wcaps" ;; *ALL*no-new-privileges*) ok "watcher drops capabilities, no new privileges, limited ($wcaps)" ;; *) ko "watcher hardening: $wcaps" ;; esac
echo "info: watcher memory $($DOCKER stats --no-stream --format '{{.MemUsage}}' "$(watcher_of "$PID")" 2>/dev/null)"
slot_streaming "$PID" && ok "the cdc_watcher slot is streaming" || ko "slot: $(tenant_sql "$PID" 'SELECT slot_name, active FROM pg_replication_slots')"
identity=$(tenant_sql "$PID" "SELECT relreplident FROM pg_class WHERE oid = 'public.notes'::regclass")
[ "$identity" = f ] && ok "the realtime table logs full old rows (REPLICA IDENTITY FULL)" || ko "replica identity: '$identity'"

echo "== realtime: a subscriber receives inserts, updates and deletes"
S1=$W/realtime-1.jsonl
if subscribe "$S1" 120; then
  ok "subscription accepted"
  [ "$(rsql "INSERT INTO notes (body) VALUES ('live insert')")" = 200 ] || ko "insert refused"
  received "$S1" INSERT 'live insert' && ok "INSERT delivered" || ko "no INSERT: $(head -c 400 "$S1")"
  [ "$(rsql "UPDATE notes SET body = 'live update' WHERE body = 'live insert'")" = 200 ] || ko "update refused"
  received "$S1" UPDATE 'live update' && ok "UPDATE delivered" || ko "no UPDATE: $(head -c 600 "$S1")"
  [ "$(rsql "DELETE FROM notes WHERE body = 'live update'")" = 200 ] || ko "delete refused"
  received "$S1" DELETE 'live update' && ok "DELETE delivered with the old row" || ko "no DELETE: $(head -c 800 "$S1")"
else
  ko "subscription refused: $(head -c 300 "$S1")"
fi

echo "== realtime: NATS restarts and changes still arrive"
$DOCKER restart excalibase-nats >/dev/null
for i in $(seq 30); do [ "$($DOCKER inspect excalibase-nats --format '{{.State.Health.Status}}' 2>/dev/null)" = healthy ] && break; sleep 2; done
S2=$W/realtime-2.jsonl
delivered=
for attempt in 1 2 3 4 5 6; do
  subscribe "$S2" 40 >/dev/null || { sleep 5; continue; }
  rsql "INSERT INTO notes (body) VALUES ('after nats restart $attempt')" >/dev/null
  received "$S2" INSERT "after nats restart $attempt" && { delivered=$attempt; break; }
done
[ -n "$delivered" ] && ok "a change after a NATS restart is delivered (attempt $delivered)" || ko "nothing after the NATS restart: $(head -c 400 "$S2")"

echo "== realtime: pause and resume"
rapi -X POST "$RAPI/provision/$PID/pause" -o /dev/null
st=$(rwait "$PID" PAUSED)
case "$st" in PAUSED*) ok "paused" ;; *) ko "pause: $st" ;; esac
watcher_running "$PID" && ko "the watcher runs on a paused project" || ok "the watcher stops with the project"
rapi -X POST "$RAPI/provision/$PID/resume" -o /dev/null
st=$(rwait "$PID" ACTIVE)
case "$st" in ACTIVE*) ok "resumed" ;; *) ko "resume: $st" ;; esac
slot_streaming "$PID" && ok "the watcher streams again after the resume" || ko "after the resume: $(tenant_sql "$PID" 'SELECT slot_name, active FROM pg_replication_slots')"
slots=$(tenant_sql "$PID" "SELECT count(*) FROM pg_replication_slots")
[ "$slots" = 1 ] && ok "one replication slot, none orphaned" || ko "replication slots: $slots"
S3=$W/realtime-3.jsonl
if subscribe "$S3" 60; then
  rsql "INSERT INTO notes (body) VALUES ('after resume')" >/dev/null
  received "$S3" INSERT 'after resume' && ok "changes arrive after the resume" || ko "nothing after the resume: $(head -c 400 "$S3")"
else
  ko "no subscription after the resume"
fi

echo "== realtime: the bus is out of the tenant's reach"
# The probes themselves work: a container on the bus reaches NATS, the tenant its own port.
$DOCKER run --rm --network excalibase-cdc docker.io/library/busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e nc -z -w 3 nats 4222 \
  && ok "NATS answers on the bus network" || ko "NATS does not answer on the bus network"
$DOCKER exec "$TC" timeout 3 bash -c "</dev/tcp/127.0.0.1/5432" >/dev/null 2>&1 && ok "the tenant probe works ($TC)" || ko "the tenant probe cannot run in $TC"
reach=
for ip in $($DOCKER inspect excalibase-nats --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}'); do
  $DOCKER exec "$TC" timeout 3 bash -c "</dev/tcp/$ip/4222" >/dev/null 2>&1 && reach="$reach $ip"
done
$DOCKER exec "$TC" getent hosts nats >/dev/null 2>&1 && reach="$reach (resolves nats)"
[ -z "$reach" ] && ok "the tenant database cannot reach NATS" || ko "the tenant database reaches NATS at$reach"

echo "== realtime: a deleted project leaves no watcher"
RD=$(rapi -X POST "$RAPI/provision/" -d "{\"projectName\":\"fleeting\",\"orgId\":\"$ORG\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"17\"}")
DPID=$(echo "$RD" | rjson 'd.get("projectId","")')
st=$(rwait "$DPID" ACTIVE)
case "$st" in ACTIVE*) ok "second project ACTIVE" ;; *) ko "second project: $st ${RD:0:200}" ;; esac
slot_streaming "$DPID" && ok "its watcher streams" || ko "second project's slot: $(tenant_sql "$DPID" 'SELECT slot_name, active FROM pg_replication_slots')"
rapi -X PATCH "$RAPI/provision/$DPID/deletion-protection" -d '{"enabled":false}' -o /dev/null
rapi -X DELETE "$RAPI/provision/$DPID/" -d '{"confirmDeleteBackups":true}' -o /dev/null
st=$(rwait "$DPID" PENDING_DELETION)
case "$st" in PENDING_DELETION*) ok "second project pending deletion" ;; *) ko "delete: $st" ;; esac
$DOCKER exec excalibase-postgres psql -qU excalibase -d excalibase_platform \
  -c "UPDATE database_instances SET deletion_due_at = now() - interval '1 minute' WHERE project_id = '$DPID'" >/dev/null
for i in $(seq 110); do [ -z "$($DOCKER ps -aq --filter name="excalibase-$DPID-")" ] && break; sleep 10; done
left=$($DOCKER ps -a --filter name="excalibase-$DPID-" --format '{{.Names}}' | tr '\n' ' ')
[ -z "$left" ] && ok "the deleted project's database and watcher are gone" || ko "left after deletion: $left"
slots=$(tenant_sql "$PID" "SELECT count(*) FROM pg_replication_slots")
[ "$slots" = 1 ] && ok "the remaining project still holds exactly its one slot" || ko "remaining project slots: $slots"
