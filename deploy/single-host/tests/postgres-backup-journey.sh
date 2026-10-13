# Sourced by boot-smoke.sh (EXC-593): a single host's Postgres project is
# backed up by default. It has its plan's schedule from create, archives WAL
# into the bundled store, restores to a second between two writes into a new
# project and in place, keeps archiving across pause and resume, and deleting
# a copy with its backups purges them once the grace period ends.
# Needs from boot-smoke.sh: c ok ko DOCKER COMPOSE P PORT T ORG PID TC.

PAPI=https://studio.localhost:$PORT/api
pjson() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
papi() { c --max-time 900 -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
pwait() {
  local st=
  for i in $(seq 120); do
    st=$(papi "$PAPI/provision/$1/" | pjson 'd.get("status","")+" "+(d.get("failureReason") or "")')
    case "$st" in "$2 "*|FAILED*) break ;; esac
    sleep 3
  done
  echo "$st"
}
# psql_on <project> <sql>: the project's own superuser over its local socket.
psql_on() {
  local database
  database=$(papi "$PAPI/provision/$1/" | pjson 'd.get("databaseName") or "postgres"')
  $DOCKER exec "excalibase-$1-postgres" psql -qtAX -U postgres -d "$database" -c "$2" 2>&1
}
pg_restore_job() {
  local job js
  job=$(echo "$2" | pjson 'd.get("id","")')
  for i in $(seq 200); do
    js=$(papi "$PAPI/provision/$1/backup/restore/$job" | pjson 'd.get("status","")+" "+(d.get("failureReason") or "")')
    case "$js" in COMPLETED*|FAILED*) break ;; esac
    sleep 3
  done
  echo "${js:-$2}"
}
pg_backup_now() {
  local before
  before=$(papi "$PAPI/provision/$1/backup/list" | grep -o '"COMPLETED"' | wc -l)
  papi -X POST "$PAPI/provision/$1/backup/trigger" -o /dev/null
  for i in $(seq 60); do
    [ "$(papi "$PAPI/provision/$1/backup/list" | grep -o '"COMPLETED"' | wc -l)" -gt "$before" ] && break
    sleep 3
  done
  papi "$PAPI/provision/$1/backup/list" | pjson '[b["id"] for b in (d if isinstance(d,list) else d.get("backups",[])) if b.get("status")=="COMPLETED"][-1]'
}
pg_keys() {
  $COMPOSE $P run --rm -T --no-deps --entrypoint sh objectstore-setup -c \
    'rc alias set store "$ENDPOINT" "$BACKUPS_ID" "$BACKUPS_SECRET" >/dev/null && rc ls --recursive store/excalibase-backups/backups/'"$1"'/ 2>/dev/null' 2>/dev/null | grep -c "$2"
}
# archived <project>: closes the current WAL segment and waits for it to be
# in the bucket, as the platform ships it on its own.
archived() {
  local before
  before=$(pg_keys "$1" wals/)
  psql_on "$1" "SELECT pg_switch_wal()" >/dev/null
  for i in $(seq 60); do
    [ "$(pg_keys "$1" wals/)" -gt "$before" ] && return 0
    sleep 10
  done
  return 1
}

# pg_port <project>: the host port its 5432 is published on.
pg_port() { $DOCKER port "excalibase-$1-postgres" 5432 2>/dev/null | head -1 | sed 's/.*://'; }
# kept_port <step>: the project's clients keep their connection string.
kept_port() {
  local now
  now=$(pg_port "$PID")
  [ "$now" = "$PPORT" ] && ok "port $PPORT kept after $1" || ko "port moved from $PPORT to '$now' after $1"
}

echo "== postgres backups by default"
PPORT=$(pg_port "$PID")
[ "${PPORT:-0}" -ge 30000 ] && [ "$PPORT" -le 30999 ] && ok "the project holds port $PPORT from the tenant window" || ko "port '$PPORT' outside 30000-30999"
SCH=$($DOCKER exec excalibase-postgres psql -qtAX -U excalibase -d excalibase_platform \
  -c "SELECT cron_spec FROM backup_schedules WHERE project_id = '$PID' AND enabled")
REC=$(papi "$PAPI/provision/$PID/" | pjson 'str(d.get("backupEnabled"))+" "+(d.get("backupSchedule") or "")')
[ -n "$SCH" ] && [ "$REC" = "True $SCH" ] && ok "the project runs its plan's backup schedule ($SCH)" || ko "schedule: '$SCH', project says '$REC'"
arch=$(psql_on "$PID" "SELECT current_setting('archive_mode')||' '||(current_setting('archive_command')<>'')")
[ "$arch" = "on true" ] && ok "WAL archiving is on" || ko "archive settings: $arch"
psql_on "$PID" "CREATE TABLE ledger (id serial PRIMARY KEY, body text); INSERT INTO ledger (body) VALUES ('a'),('b'),('c')" >/dev/null
P1=$(pg_backup_now "$PID")
[ -n "$P1" ] && ok "Postgres backup COMPLETED ($P1)" || ko "no Postgres backup"
psql_on "$PID" "INSERT INTO ledger (body) VALUES ('d'),('e')" >/dev/null
sleep 2; PPOINT=$(date -u +%Y-%m-%dT%H:%M:%SZ); sleep 2
psql_on "$PID" "INSERT INTO ledger (body) VALUES ('f'),('g')" >/dev/null
archived "$PID" && ok "WAL shipped to the bundled store without a backup" || ko "no WAL reached the bucket"
failed=$(psql_on "$PID" "SELECT failed_count FROM pg_stat_archiver")
[ "$failed" = 0 ] && ok "archiver has no failures" || ko "archiver failures: $failed"

echo "== postgres point-in-time and in-place restores"
RJ=$(papi -X POST "$PAPI/provision/$PID/backup/restore" -d "{\"newProjectName\":\"shop-pitr\",\"targetTime\":\"$PPOINT\"}")
PRPID=$(echo "$RJ" | pjson 'd.get("newProjectId","")')
js=$(pg_restore_job "$PID" "$RJ")
st=$(pwait "$PRPID" ACTIVE)
out=$(psql_on "$PRPID" "SELECT count(*) FROM ledger")
case "$js $st $out" in COMPLETED*ACTIVE*" 5") ok "restored to $PPOINT into a new project: 5 rows" ;; *) ko "point restore: $js / $st / ${out:0:200}" ;; esac
IJ=$(papi -X POST "$PAPI/provision/$PID/backup/restore" -d "{\"mode\":\"in_place\",\"targetTime\":\"$PPOINT\",\"confirmReplace\":true}")
js=$(pg_restore_job "$PID" "$IJ")
st=$(pwait "$PID" ACTIVE)
out=$(psql_on "$PID" "SELECT count(*) FROM ledger")
case "$js $st $out" in COMPLETED*ACTIVE*" 5") ok "restored in place to $PPOINT: 5 rows" ;; *) ko "in-place restore: $js / $st / ${out:0:200}" ;; esac
arch=$(psql_on "$PID" "SELECT current_setting('archive_mode')")
[ "$arch" = on ] && archived "$PID" && ok "the restored project archives WAL" || ko "after the in-place restore archive_mode=$arch"
kept_port "the in-place restore"
# The restore's new database gets the watcher back, on a fresh slot (EXC-612).
streaming=
for i in $(seq 40); do streaming=$(psql_on "$PID" "SELECT string_agg(slot_name || ':' || active, ',') FROM pg_replication_slots"); [ "$streaming" = cdc_watcher:t ] && break; sleep 3; done
[ "$streaming" = cdc_watcher:t ] && ok "the watcher streams the restored database, one slot" || ko "after the in-place restore slots: '$streaming'"

echo "== postgres pause and resume"
papi -X POST "$PAPI/provision/$PID/pause" -o /dev/null
st=$(pwait "$PID" PAUSED)
case "$st" in PAUSED*) ok "paused" ;; *) ko "pause: $st" ;; esac
papi -X POST "$PAPI/provision/$PID/resume" -o /dev/null
st=$(pwait "$PID" ACTIVE)
out=$(psql_on "$PID" "SELECT count(*) FROM ledger")
case "$st $out" in ACTIVE*" 5") ok "resumed with its rows" ;; *) ko "resume: $st ${out:0:200}" ;; esac
archived "$PID" && ok "archiving goes on after the resume" || ko "no WAL shipped after the resume"
kept_port "pause and resume"

echo "== postgres delete purges backups"
pg_backup_now "$PRPID" >/dev/null
keys=$(pg_keys "$PRPID" /)
papi -X PATCH "$PAPI/provision/$PRPID/deletion-protection" -d '{"enabled":false}' -o /dev/null
papi -X DELETE "$PAPI/provision/$PRPID/" -d '{"confirmDeleteBackups":true}' -o /dev/null
st=$(pwait "$PRPID" PENDING_DELETION)
case "$st" in PENDING_DELETION*) ok "copy pending deletion with its backups ($keys objects)" ;; *) ko "delete: $st" ;; esac
$DOCKER exec excalibase-postgres psql -qU excalibase -d excalibase_platform \
  -c "UPDATE database_instances SET deletion_due_at = now() - interval '1 minute' WHERE project_id = '$PRPID'" >/dev/null
for i in $(seq 110); do
  [ -z "$($DOCKER ps -aq --filter name="excalibase-$PRPID-postgres")" ] && [ "$(pg_keys "$PRPID" /)" = 0 ] && break
  sleep 10
done
left=$(pg_keys "$PRPID" /)
[ "${keys:-0}" -gt 0 ] && [ "$left" = 0 ] && [ -z "$($DOCKER ps -aq --filter name="excalibase-$PRPID-postgres")" ] \
  && ok "the grace period over, the copy and its backups are gone" || ko "after deletion: $left backup objects left"
