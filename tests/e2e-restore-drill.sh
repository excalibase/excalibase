#!/usr/bin/env bash
# e2e-restore-drill: prove disaster-recovery actually works end-to-end.
#
# F25 in e2e-aio-self.sh verifies backup BYTES land in R2.  This script
# verifies the bytes are actually restorable: it provisions project A,
# inserts a canary row, triggers a backup, restores into project B, and
# asserts the canary survives.  Cleanup tears down both projects.
#
# Requires: kubectl, jq, curl. Port-forwards on 24005/24000 already up.
#
# The restore reads from whatever object store the provisioning deployment
# is configured to back up to (BACKUP_DEFAULT_ENDPOINT / R2_ENDPOINT env,
# or vault backup/s3). Nothing here assumes a particular endpoint; the
# preflight below only surfaces what the server will use so a mis-wired
# cluster fails fast instead of wedging a CNPG recovery.

set -uo pipefail

API="${API_PROV:-http://localhost:24005}"
NS=excalibase-platform
PROV_DEPLOY="${PROV_DEPLOY:-provisioning}"
PASS=0
FAIL=0
PROJ_A=""
PROJ_B=""

pass() { printf "  \033[32m✓\033[0m %s\n" "$1"; PASS=$((PASS+1)); }
fail() { printf "  \033[31m✗\033[0m %s: %s\n" "$1" "${2:-}"; FAIL=$((FAIL+1)); }
section() { printf "\n\033[36m=== %s ===\033[0m\n" "$1"; }

cleanup() {
  echo ""
  echo "--- cleanup ---"
  # Both projects are real platform projects (EXC-366): the restored one is
  # registered exactly like a provisioned one, so both deprovision via the API.
  for p in "$PROJ_A" "$PROJ_B"; do
    [ -n "$p" ] && curl -sf -X DELETE -H "$AUTH" "$API/api/provision/$p/" > /dev/null 2>&1 && echo "  deprovisioned $p (api)" || true
  done
}

# api_query runs SQL against a project through the platform's schema plane and
# echoes the first cell of the first row. Going through the API is the point:
# it proves the project row AND its vault credentials exist.
api_query() {
  curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' \
    -d "$(jq -n --arg q "$2" '{query:$q}')" "$API/api/schema/$1/query" \
    | jq -r '.rows[0][0] // empty' 2>/dev/null
}
[ "${NO_CLEANUP:-0}" = "1" ] || trap cleanup EXIT

# ---------- preflight: backup store the server restores from ----------
section "preflight"
STORE_ENDPOINT="${BACKUP_DEFAULT_ENDPOINT:-${R2_ENDPOINT:-}}"
if [ -z "$STORE_ENDPOINT" ]; then
  STORE_ENDPOINT=$(kubectl -n $NS get deploy "$PROV_DEPLOY" -o json 2>/dev/null \
    | jq -r '[.spec.template.spec.containers[].env[]? | select(.name=="BACKUP_DEFAULT_ENDPOINT" or .name=="R2_ENDPOINT") | .value // empty] | first // empty')
fi
if [ -n "$STORE_ENDPOINT" ]; then
  pass "restore reads from configured store: $STORE_ENDPOINT"
else
  echo "  (no BACKUP_DEFAULT_ENDPOINT / R2_ENDPOINT visible in env or deploy $PROV_DEPLOY — relying on vault backup/s3)"
fi

# ---------- auth ----------
section "auth"
ADMIN_PASS=$(kubectl -n $NS get secret platform-bootstrap -o jsonpath='{.data.admin-pass}' | base64 -d 2>/dev/null)
[ -z "$ADMIN_PASS" ] && { fail "bootstrap secret" "admin-pass empty"; exit 1; }
TOKEN=$(curl -sf -X POST "$API/api/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" | jq -r .token)
[ -z "$TOKEN" ] || [ "$TOKEN" = "null" ] && { fail "login" "no token"; exit 1; }
AUTH="Authorization: Bearer $TOKEN"
pass "admin token"

ORG_ID=$(curl -sf -H "$AUTH" "$API/api/orgs" | jq -r '.[] | select(.slug=="acme") | .id')
[ -z "$ORG_ID" ] && { fail "org" "no acme org"; exit 1; }
pass "org acme: $ORG_ID"

# ---------- provision project A ----------
section "provision project A"
NAME_A="restore-src-$(date +%s)"
REQ_A=$(jq -n --arg n "$NAME_A" --arg o "$ORG_ID" \
  '{projectName:$n, orgId:$o, databaseType:"POSTGRESQL", tier:"STANDARD"}')
PROJ_A=$(curl -sf -X POST -H "$AUTH" -H 'Content-Type: application/json' \
  -d "$REQ_A" "$API/api/provision/" | jq -r .projectId)
[ -z "$PROJ_A" ] || [[ ! "$PROJ_A" == proj-* ]] && { fail "provision A" "got: $PROJ_A"; exit 1; }
pass "project A: $PROJ_A"

for i in $(seq 1 60); do
  S=$(curl -sf -H "$AUTH" "$API/api/provision/$PROJ_A/" | jq -r .status 2>/dev/null)
  [ "$S" = "ACTIVE" ] && break
  sleep 2
done
[ "$S" = "ACTIVE" ] || { fail "provision A active" "final=$S"; exit 1; }
pass "project A ACTIVE"

NS_A="$ORG_ID-$PROJ_A"
PG_A_POD=$(kubectl -n "$NS_A" get pod -l cnpg.io/cluster -o jsonpath='{.items[?(@.metadata.labels.role=="primary")].metadata.name}')
[ -z "$PG_A_POD" ] && { fail "find A primary" "no primary pod in $NS_A"; exit 1; }
pass "A primary pod: $PG_A_POD"

# ---------- insert canary ----------
section "insert canary into A"
CANARY="canary-$(date +%s%N | head -c 16)"
api_query "$PROJ_A" "CREATE TABLE IF NOT EXISTS restore_drill (marker text PRIMARY KEY, written_at timestamptz DEFAULT now())" > /dev/null
api_query "$PROJ_A" "INSERT INTO restore_drill (marker) VALUES ('$CANARY')" > /dev/null
COUNT=$(api_query "$PROJ_A" "SELECT count(*) FROM restore_drill WHERE marker = '$CANARY'")
[ "$COUNT" = "1" ] && pass "canary present in A: $CANARY" || { fail "canary verify in A" "got $COUNT rows"; exit 1; }

# ---------- trigger backup ----------
section "trigger backup of A"
TRIG=$(curl -s -X POST -H "$AUTH" "$API/api/provision/$PROJ_A/backup/trigger")
echo "$TRIG" | jq -e . > /dev/null 2>&1 && pass "trigger returned JSON" || fail "trigger" "$TRIG"

echo "    waiting up to 180s for backup to appear in CNPG..."
BACKUP_ID=""
for i in $(seq 1 90); do
  LIST=$(curl -sf -H "$AUTH" "$API/api/provision/$PROJ_A/backup/list" 2>/dev/null)
  BACKUP_ID=$(echo "$LIST" | jq -r '.backups[]? | select((.status|ascii_downcase)=="completed") | .id' 2>/dev/null | head -1)
  [ -n "$BACKUP_ID" ] && [ "$BACKUP_ID" != "null" ] && break
  sleep 2
done
[ -n "$BACKUP_ID" ] && [ "$BACKUP_ID" != "null" ] && pass "backup completed: $BACKUP_ID" \
  || { fail "backup wait" "no completed backup after 180s; list=$(echo "$LIST" | head -c 200)"; exit 1; }

# ---------- force end-of-backup WAL to archive ----------
# CNPG marks the manual backup COMPLETED as soon as barman-cloud-backup uploads
# the base tarball, but the trailing WAL segment containing the backup's
# end-LSN doesn't ship until archive_timeout (5 min default) or the next WAL
# switch. A restore started before that WAL is archived dies with
# "WAL ends before consistent recovery point" and corrupts PGData (then every
# retry sees a non-empty data dir and the cluster wedges).
#
# Production users restoring an old backup never hit this — the WAL has long
# since shipped. The drill scenario (back up + restore immediately) does.
# Mirror what an operator would do: issue pg_switch_wal on the source so the
# end-of-backup WAL segment closes and gets archived right away.
section "force WAL archive after backup"
kubectl -n "$NS_A" exec "$PG_A_POD" -- psql -U postgres -d app -c "CHECKPOINT;" > /dev/null 2>&1 || true
SWITCHED_WAL=$(kubectl -n "$NS_A" exec "$PG_A_POD" -- psql -U postgres -d app -tAc \
  "SELECT pg_walfile_name(pg_switch_wal());" 2>/dev/null | tr -d '[:space:]')
[ -n "$SWITCHED_WAL" ] && pass "switched WAL: $SWITCHED_WAL" || { fail "pg_switch_wal" ""; exit 1; }

echo "    waiting up to 120s for $SWITCHED_WAL.gz to appear in R2..."
for i in $(seq 1 60); do
  HAVE=$(kubectl -n "$NS_A" exec "$PG_A_POD" -- bash -c \
    "ls /var/lib/postgresql/data/pgdata/pg_wal/archive_status/${SWITCHED_WAL}.done 2>/dev/null && echo OK" 2>/dev/null | grep -c OK || true)
  [ "$HAVE" -ge 1 ] && break
  sleep 2
done
[ "$HAVE" -ge 1 ] && pass "end-of-backup WAL archived" \
  || { fail "wal archive" "no .done marker for $SWITCHED_WAL after 120s"; exit 1; }

# ---------- restore into project B ----------
section "restore A → new project B"
NAME_B="restore-dst-$(date +%s)"
RREQ=$(jq -n --arg b "$BACKUP_ID" --arg n "$NAME_B" '{backupId:$b, newProjectName:$n}')
RRESP=$(curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' \
  -d "$RREQ" "$API/api/provision/$PROJ_A/backup/restore")
JOB_ID=$(echo "$RRESP" | jq -r '.jobId // .id // empty' 2>/dev/null)
PROJ_B=$(echo "$RRESP" | jq -r '.newProjectId // .projectId // empty' 2>/dev/null)

if [ -n "$JOB_ID" ] && [ "$JOB_ID" != "null" ]; then
  pass "restore job: $JOB_ID"
  echo "    polling job up to 240s..."
  for i in $(seq 1 120); do
    JOB=$(curl -sf -H "$AUTH" "$API/api/provision/$PROJ_A/backup/restore/$JOB_ID" 2>/dev/null)
    JS=$(echo "$JOB" | jq -r '.status' 2>/dev/null)
    PROJ_B=${PROJ_B:-$(echo "$JOB" | jq -r '.newProjectId // .targetProjectId // empty' 2>/dev/null)}
    case "$JS" in COMPLETED|SUCCESS|completed|succeeded) break;; FAILED|failed) fail "restore job" "$JOB"; exit 1;; esac
    sleep 2
  done
  pass "restore job final: $JS"
else
  pass "restore synchronous (no job id)"
fi

[ -z "$PROJ_B" ] || [ "$PROJ_B" = "null" ] && { fail "find B projectId" "resp=$(echo "$RRESP" | head -c 200)"; exit 1; }
pass "project B: $PROJ_B"

# The restore registers B as a real project (EXC-366), so it is visible on the
# platform API the moment the job completes — no kubectl needed.
section "project B is registered"
BS=""
for i in $(seq 1 60); do
  BS=$(curl -sf -H "$AUTH" "$API/api/provision/$PROJ_B/" | jq -r .status 2>/dev/null)
  [ "$BS" = "ACTIVE" ] && break
  sleep 2
done
[ "$BS" = "ACTIVE" ] && pass "project B registered and ACTIVE" || { fail "B registration" "status=$BS"; exit 1; }

SRC=$(curl -sf -H "$AUTH" "$API/api/provision/$PROJ_B/" | jq -r '.restoredFromProjectId // empty')
[ "$SRC" = "$PROJ_A" ] && pass "B records its restore provenance ($SRC)" || fail "B provenance" "got '$SRC', want $PROJ_A"

# ---------- verify canary survived, through the API ----------
section "verify canary in B"

GOT=$(api_query "$PROJ_B" "SELECT count(*) FROM restore_drill WHERE marker = '$CANARY'")
[ "$GOT" = "1" ] && pass "canary survived restore (queried via API): $CANARY" || fail "canary in B" "got '$GOT' rows (expected 1)"

# ---------- summary ----------
section "summary"
printf "%d passed, %d failed\n" $PASS $FAIL
exit $FAIL
