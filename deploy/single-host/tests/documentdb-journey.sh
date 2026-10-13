# Sourced by boot-smoke.sh when DOCUMENTDB=1 (EXC-576): DocumentDB on a single
# host. A Studio user creates a DocumentDB project, a Mongo client on the host
# writes and reads it over TLS at 127.0.0.1, Studio browses it, a Mongo user is
# created and used, it is paused, resumed, backed up and restored into a new
# project that serves Mongo again and in place, and deleting the copy stops both
# containers. Its two host ports and its CA stay the same throughout (EXC-594).
# Needs from boot-smoke.sh: c ok ko DOCKER PORT T ORG W.

MONGO_IMAGE=docker.io/library/mongo@sha256:d0d926f94df099bff534b7ee5b5986458131a22489dfff8664509af0c1e2ca9c
DAPI=https://studio.localhost:$PORT/api
djson() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
dapi() { c --max-time 600 -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
# wait_status <project> <status>: polls until the project is in it or FAILED.
wait_status() {
  local st=
  for i in $(seq 100); do
    st=$(dapi "$DAPI/provision/$1/" | djson 'd.get("status","")+" "+(d.get("failureReason") or "")')
    case "$st" in "$2 "*|FAILED*) break ;; esac
    sleep 3
  done
  echo "$st"
}
# mongo <project> <user> <password> <script>: mongosh on the host's network,
# verifying the gateway against the CA Studio hands out for the project.
mongo() {
  local endpoint ca port
  endpoint=$(dapi "$DAPI/projects/$1/db-endpoint")
  ca=$(echo "$endpoint" | djson 'd["caCertificate"]')
  port=$(echo "$endpoint" | djson 'd["mongoPort"]')
  $DOCKER run --rm --network host -e CA="$ca" -e MUSER="$2" -e MPASS="$3" --entrypoint bash "$MONGO_IMAGE" -c \
    'printf "%s" "$CA" > /tmp/ca.crt && mongosh --quiet --host 127.0.0.1 --port '"$port"' --tls --tlsCAFile /tmp/ca.crt \
       --authenticationMechanism SCRAM-SHA-256 -u "$MUSER" -p "$MPASS" --eval "$0"' "$4" 2>&1
}

# endpoint_of <project>: its Postgres port, Mongo port and a digest of its CA.
endpoint_of() {
  dapi "$DAPI/projects/$1/db-endpoint" | djson '"%s %s %s" % (d.get("port"), d.get("mongoPort"), __import__("hashlib").sha256(d.get("caCertificate","").encode()).hexdigest()[:12])'
}
# kept_endpoint <when>: the project answers on the ports and CA it had at create.
kept_endpoint() {
  local now
  now=$(endpoint_of "$DPID")
  [ "$now" = "$DENDPOINT" ] && ok "$1 kept both host ports and the CA ($now)" || ko "$1 moved the endpoint: $DENDPOINT -> $now"
}

echo "== documentdb project"
$DOCKER pull -q "$MONGO_IMAGE" >/dev/null 2>&1
R=$(dapi -X POST "$DAPI/provision/" -d "{\"projectName\":\"docs\",\"orgId\":\"$ORG\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"17\",\"documentDb\":true}")
DPID=$(echo "$R" | djson 'd.get("projectId","")')
[ -n "$DPID" ] || ko "DocumentDB provision refused: ${R:0:300}"
st=$(wait_status "$DPID" ACTIVE)
case "$st" in ACTIVE*) ok "DocumentDB project ACTIVE" ;; *) ko "DocumentDB project: $st" ;; esac
DB=excalibase-$DPID-postgres; GW=excalibase-$DPID-documentdb
mode=$($DOCKER inspect "$GW" --format '{{.HostConfig.NetworkMode}} {{len .HostConfig.PortBindings}}' 2>/dev/null)
case "$mode" in "container:"*" 0") ok "gateway in the database's network namespace, publishing nothing ($mode)" ;; *) ko "gateway network: '$mode'" ;; esac
dports=$($DOCKER port "$DB" 2>/dev/null | tr '\n' ' ')
case "$dports" in *0.0.0.0*|*"[::]"*|"") ko "database ports: '$dports'" ;; *10260*) ok "Postgres and Mongo published on loopback ($dports)" ;; *) ko "no Mongo port: $dports" ;; esac
limits=$($DOCKER inspect "$DB" "$GW" --format '{{.HostConfig.Memory}}' 2>/dev/null | paste -sd' ')
case "$limits" in *" "*) [ "${limits#0 }" = "$limits" ] && ok "tier split between database and gateway (memory $limits)" || ko "limits: $limits" ;; *) ko "limits: $limits" ;; esac
# The gateway shares the database's network namespace: neither reaches NATS (EXC-612).
$DOCKER exec "$DB" timeout 3 bash -c "</dev/tcp/127.0.0.1/5432" >/dev/null 2>&1 || ko "the probe cannot run in $DB"
bus=
for ip in $($DOCKER inspect excalibase-nats --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}'); do
  $DOCKER exec "$DB" timeout 3 bash -c "</dev/tcp/$ip/4222" >/dev/null 2>&1 && bus="$bus $ip"
done
[ -z "$bus" ] && ok "the DocumentDB database and its gateway cannot reach NATS" || ko "DocumentDB reaches NATS at$bus"
[ -z "$($DOCKER ps -aq --filter name="excalibase-$DPID-watcher")" ] && ok "a DocumentDB project runs no watcher" || ko "a DocumentDB project got a watcher"

echo "== mongo from the host"
E=$(dapi "$DAPI/projects/$DPID/db-endpoint")
[ "$(echo "$E" | djson 'd.get("singleHost") and d["host"]=="127.0.0.1" and d.get("mongoAvailable") and bool(d["caCertificate"])')" = True ] \
  && ok "Studio shows 127.0.0.1 with the Mongo port and gateway CA" || ko "endpoint: ${E:0:400}"
DENDPOINT=$(endpoint_of "$DPID")
read -r dpg dmg _ <<< "$DENDPOINT"
[ "${dpg:-0}" -ge 30000 ] && [ "${dpg:-0}" -le 30999 ] && [ "${dmg:-0}" -ge 30000 ] && [ "${dmg:-0}" -le 30999 ] && [ "$dpg" != "$dmg" ] \
  && ok "Postgres and Mongo hold ports in the tenant range ($dpg, $dmg)" || ko "host ports: $DENDPOINT"
CRED=$(dapi "$DAPI/provision/$DPID/credentials")
DUSER=$(echo "$CRED" | djson 'd["username"]'); DPASS=$(echo "$CRED" | djson 'd["password"]')
out=$(mongo "$DPID" "$DUSER" "$DPASS" 'db.getSiblingDB("shop").items.insertOne({sku:"a1",qty:3}); print("count=" + db.getSiblingDB("shop").items.countDocuments({sku:"a1"}))')
case "$out" in *count=1*) ok "mongosh inserts and finds over verified TLS" ;; *) ko "mongosh: ${out:0:400}" ;; esac

echo "== studio document browser"
ins=$(dapi -X POST "$DAPI/projects/$DPID/documentdb/databases/shop/collections/items/documents" -d '{"sku":"b2","qty":5}' -w ' %{http_code}')
case "$ins" in *" 201") ok "document inserted through Studio" ;; *) ko "insert: ${ins:0:300}" ;; esac
found=$(dapi "$DAPI/projects/$DPID/documentdb/databases/shop/collections/items/documents?limit=10")
case "$found" in *a1*b2*|*b2*a1*) ok "Studio lists both documents" ;; *) ko "browse: ${found:0:300}" ;; esac

echo "== mongo users"
MU=$(dapi -X POST "$DAPI/provision/$DPID/documentdb/users/" -d '{"username":"reporting","role":"read"}')
MPASS=$(echo "$MU" | djson 'd["password"]')
[ -n "$MPASS" ] && ok "Mongo user created" || ko "Mongo user: ${MU:0:300}"
out=$(mongo "$DPID" reporting "$MPASS" 'print("count=" + db.getSiblingDB("shop").items.countDocuments({}))')
case "$out" in *count=2*) ok "the Mongo user reads the project" ;; *) ko "Mongo user read: ${out:0:300}" ;; esac
out=$(mongo "$DPID" reporting "$MPASS" 'db.getSiblingDB("shop").items.insertOne({sku:"x"})')
case "$out" in *"not authorized"*|*Unauthorized*) ok "a read user cannot write" ;; *) ko "read user wrote: ${out:0:300}" ;; esac

echo "== pause and resume"
dapi -X POST "$DAPI/provision/$DPID/pause" -o /dev/null
st=$(wait_status "$DPID" PAUSED)
running=$($DOCKER ps -q --filter name="$GW" --filter name="$DB" | wc -l)
case "$st" in PAUSED*) [ "$running" = 0 ] && ok "paused: both containers stopped" || ko "paused with $running running" ;; *) ko "pause: $st" ;; esac
dapi -X POST "$DAPI/provision/$DPID/resume" -o /dev/null
st=$(wait_status "$DPID" ACTIVE)
out=$(mongo "$DPID" "$DUSER" "$DPASS" 'print("count=" + db.getSiblingDB("shop").items.countDocuments({}))')
case "$st$out" in ACTIVE*count=2*) ok "resumed: Mongo answers with the data" ;; *) ko "resume: $st ${out:0:300}"; $DOCKER logs excalibase-provisioning 2>&1 | grep -i "resume $DPID" | tail -3 ;; esac
kept_endpoint "pause and resume"

echo "== backup and restore"
B=$(dapi -X POST "$DAPI/provision/$DPID/backup/trigger" -w ' %{http_code}')
case "$B" in *" 200"|*" 201"|*" 202") ok "DocumentDB backup taken" ;; *) ko "backup: ${B:0:300}" ;; esac
for i in $(seq 40); do dapi "$DAPI/provision/$DPID/backup/list" | grep -q '"COMPLETED"' && break; sleep 3; done
dapi "$DAPI/provision/$DPID/backup/list" | grep -q '"COMPLETED"' && ok "DocumentDB backup COMPLETED" || ko "backup not completed"
DB1=$(dapi "$DAPI/provision/$DPID/backup/list" | djson 'max((b for b in (d if isinstance(d,list) else d.get("backups",[])) if b.get("status")=="COMPLETED"), key=lambda b: b.get("timestamp",""))["id"]')
RJ=$(dapi -X POST "$DAPI/provision/$DPID/backup/restore" -d '{"newProjectName":"docs-restored"}')
RPID=$(echo "$RJ" | djson 'd.get("newProjectId","")'); JOB=$(echo "$RJ" | djson 'd.get("id","")')
for i in $(seq 100); do
  js=$(dapi "$DAPI/provision/$DPID/backup/restore/$JOB" | djson 'd.get("status","")+" "+(d.get("failureReason") or "")')
  case "$js" in COMPLETED*|FAILED*) break ;; esac
  sleep 3
done
case "$js" in COMPLETED*) ok "restore job completed" ;; *) ko "restore job: ${js:-$RJ}" ;; esac
st=$(wait_status "$RPID" ACTIVE)
# A restored project is a new project with credentials of its own.
RCRED=$(dapi "$DAPI/provision/$RPID/credentials")
out=$(mongo "$RPID" "$(echo "$RCRED" | djson 'd["username"]')" "$(echo "$RCRED" | djson 'd["password"]')" 'print("count=" + db.getSiblingDB("shop").items.countDocuments({}))')
case "$st$out" in ACTIVE*count=2*) ok "restored project serves Mongo through its own gateway" ;; *) ko "restored: $st ${out:0:300}" ;; esac
read -r rpg rmg _ <<< "$(endpoint_of "$RPID")"
case " $dpg $dmg " in *" $rpg "*|*" $rmg "*) ko "the copy took the source's ports ($rpg $rmg)" ;; *) [ "$rpg" != "$rmg" ] && ok "the copy holds ports of its own ($rpg, $rmg)" || ko "copy ports: $rpg $rmg" ;; esac

echo "== restore in place"
out=$(mongo "$DPID" "$DUSER" "$DPASS" 'db.getSiblingDB("shop").items.insertOne({sku:"c3"}); print("count=" + db.getSiblingDB("shop").items.countDocuments({}))')
case "$out" in *count=3*) ok "a document written after the backup" ;; *) ko "write after backup: ${out:0:300}" ;; esac
IJ=$(dapi -X POST "$DAPI/provision/$DPID/backup/restore" -d "{\"mode\":\"in_place\",\"backupId\":\"$DB1\",\"confirmReplace\":true}")
IJOB=$(echo "$IJ" | djson 'd.get("id","")')
for i in $(seq 200); do
  js=$(dapi "$DAPI/provision/$DPID/backup/restore/$IJOB" | djson 'd.get("status","")+" "+(d.get("failureReason") or "")')
  case "$js" in COMPLETED*|FAILED*) break ;; esac
  sleep 3
done
st=$(wait_status "$DPID" ACTIVE)
out=$(mongo "$DPID" "$DUSER" "$DPASS" 'print("count=" + db.getSiblingDB("shop").items.countDocuments({}))')
case "$js $st $out" in COMPLETED*ACTIVE*count=2*) ok "restored in place: the backup's 2 documents, same credentials" ;; *) ko "in-place restore: ${js:-$IJ} / $st / ${out:0:300}" ;; esac
kept_endpoint "the in-place restore"
out=$(mongo "$DPID" reporting "$MPASS" 'print("count=" + db.getSiblingDB("shop").items.countDocuments({}))')
case "$out" in *count=2*) ok "the Mongo user still signs in after the in-place restore" ;; *) ko "Mongo user after in-place: ${out:0:300}" ;; esac
found=$(dapi "$DAPI/projects/$DPID/documentdb/databases/shop/collections/items/documents?limit=10")
case "$found" in *a1*b2*|*b2*a1*) ok "Studio browses the restored project" ;; *) ko "browse after in-place: ${found:0:300}" ;; esac
mode=$($DOCKER inspect "$GW" --format '{{.HostConfig.NetworkMode}}' 2>/dev/null)
dbid=$($DOCKER inspect "$DB" --format '{{.Id}}' 2>/dev/null)
case "$mode" in "container:"*"${dbid:-none}"*) ok "the rebuilt gateway joined the rebuilt database" ;; *) ko "gateway network after in-place: $mode (database $dbid)" ;; esac

echo "== delete"
# Deletion keeps a grace period: the project stops now and is removed later
# (the real-engine integration test covers the removal itself).
dapi -X PATCH "$DAPI/provision/$RPID/deletion-protection" -d '{"enabled":false}' -o /dev/null
del=$(dapi -X DELETE "$DAPI/provision/$RPID/" -w ' %{http_code}')
st=$(wait_status "$RPID" PENDING_DELETION)
running=$($DOCKER ps -q --filter name="excalibase-$RPID-" | wc -l)
case "$st" in PENDING_DELETION*) [ "$running" = 0 ] && ok "a deleted project stops its database and gateway" || ko "$running still running" ;; *) ko "delete: $st (${del:0:300})" ;; esac
