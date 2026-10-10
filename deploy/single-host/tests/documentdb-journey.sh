# Sourced by boot-smoke.sh when DOCUMENTDB=1 (EXC-576): DocumentDB on a single
# host. A Studio user creates a DocumentDB project, a Mongo client on the host
# writes and reads it over TLS at 127.0.0.1, Studio browses it, a Mongo user is
# created and used, it is paused, resumed, backed up and restored into a new
# project that serves Mongo again, and deleting that one stops both containers.
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

echo "== mongo from the host"
E=$(dapi "$DAPI/projects/$DPID/db-endpoint")
[ "$(echo "$E" | djson 'd.get("singleHost") and d["host"]=="127.0.0.1" and d.get("mongoAvailable") and bool(d["caCertificate"])')" = True ] \
  && ok "Studio shows 127.0.0.1 with the Mongo port and gateway CA" || ko "endpoint: ${E:0:400}"
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

echo "== backup and restore"
B=$(dapi -X POST "$DAPI/provision/$DPID/backup/trigger" -w ' %{http_code}')
case "$B" in *" 200"|*" 201"|*" 202") ok "DocumentDB backup taken" ;; *) ko "backup: ${B:0:300}" ;; esac
for i in $(seq 40); do dapi "$DAPI/provision/$DPID/backup/list" | grep -q '"COMPLETED"' && break; sleep 3; done
dapi "$DAPI/provision/$DPID/backup/list" | grep -q '"COMPLETED"' && ok "DocumentDB backup COMPLETED" || ko "backup not completed"
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

echo "== delete"
# Deletion keeps a grace period: the project stops now and is removed later
# (the real-engine integration test covers the removal itself).
dapi -X PATCH "$DAPI/provision/$RPID/deletion-protection" -d '{"enabled":false}' -o /dev/null
del=$(dapi -X DELETE "$DAPI/provision/$RPID/" -w ' %{http_code}')
st=$(wait_status "$RPID" PENDING_DELETION)
running=$($DOCKER ps -q --filter name="excalibase-$RPID-" | wc -l)
case "$st" in PENDING_DELETION*) [ "$running" = 0 ] && ok "a deleted project stops its database and gateway" || ko "$running still running" ;; *) ko "delete: $st (${del:0:300})" ;; esac
