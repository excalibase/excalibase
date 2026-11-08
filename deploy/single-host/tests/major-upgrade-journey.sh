# Sourced by boot-smoke.sh (EXC-683): a major upgrade left open by a real firewall
# outage says why and how to leave, keeps its 7 days when deleted, and a teardown
# takes the old database with it; a tenant running under a PAUSED row is stopped
# by Resume then Pause, as the docs say.
# Needs from boot-smoke.sh: c ok ko DOCKER ENGINE PORT T ORG.

UAPI=https://studio.localhost:$PORT/api
ujson() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
uapi() { c --max-time 900 -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
ustatus() { uapi "$UAPI/provision/$1/" | ujson 'd.get("status","")'; }
uwait() {
  local st=
  for i in $(seq 120); do
    st=$(ustatus "$1")
    [ "$st" = "$2" ] && break
    sleep 3
  done
  echo "$st"
}
uprovision() {
  local id
  id=$(uapi -X POST "$UAPI/provision/" -d "{\"projectName\":\"$1\",\"orgId\":\"$ORG\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"16\"}" | ujson 'd.get("projectId","")')
  uwait "$id" ACTIVE >/dev/null
  echo "$id"
}
usql() { $DOCKER exec "excalibase-$1-postgres" psql -qtAX -U postgres -d "$(uapi "$UAPI/provision/$1/" | ujson 'd.get("databaseName") or "postgres"')" -c "$2" 2>&1; }
urun() { uapi "$UAPI/provision/$1/major-upgrade" | ujson "(d.get('latest') or {}).get('$2') or ''"; }
kept_container() { $DOCKER ps -a --format '{{.Names}}' | grep "^excalibase-$1-postgres-pre-upgrade-" | head -1; }
firewall_up() {
  $DOCKER start excalibase-host-firewall >/dev/null
  for i in $(seq 90); do
    [ "$($DOCKER inspect excalibase-host-firewall --format '{{.State.Health.Status}}' 2>/dev/null)" = healthy ] && return 0
    sleep 2
  done
  return 1
}
# left_open <project>: the firewall goes down once the old database is set aside, so the new one cannot
# start. The window is the dump's copy into the new container, so the dump has to take a moment.
left_open() {
  local code
  usql "$1" "CREATE TABLE ballast AS SELECT md5(i::text) || md5(i::text) AS body FROM generate_series(1, 4000000) i" >/dev/null
  # 503 is a check that could not run yet, and says to try again.
  for i in $(seq 20); do
    code=$(uapi -X POST "$UAPI/provision/$1/major-upgrade" -d '{"targetVersion":"17","confirm":true}' -o /dev/null -w '%{http_code}')
    [ "$code" != 503 ] && break
    sleep 10
  done
  case "$code" in 2*) ;; *) ko "upgrade of $1 refused: $code"; return 1 ;; esac
  for i in $(seq 3000); do
    [ -n "$(kept_container "$1")" ] && break
    sleep 0.2
  done
  $DOCKER stop -t 0 excalibase-host-firewall >/dev/null
  for i in $(seq 150); do
    [ "$(urun "$1" step)" = UPGRADE_WAITING ] && return 0
    sleep 3
  done
  ko "the run of $1 never waited: $(uapi "$UAPI/provision/$1/major-upgrade" | head -c 400)"
  return 1
}

# udelete <project>: protection off, then DELETE; a sweep or a driver holding the lease answers 409 for a moment.
udelete() {
  local code
  uapi -X PATCH "$UAPI/provision/$1/deletion-protection" -d '{"enabled":false}' -o /dev/null
  for i in $(seq 60); do
    code=$(uapi -X DELETE "$UAPI/provision/$1/" -d '{}' -o /dev/null -w '%{http_code}')
    [ "$code" != 409 ] && break
    sleep 5
  done
  case "$code" in 2*) ;; *) ko "delete of $1 answered $code" ;; esac
}

major_upgrade_journey() {
  local one two said backup kept due job restored left
  echo "== major upgrade left open"
  [ "$ENGINE" = docker ] || { ok "rootless Podman has no host firewall to lose"; return; }
  one=$(uprovision upgrade-one)
  usql "$one" "CREATE TABLE kept (body text); INSERT INTO kept VALUES ('before the upgrade')" >/dev/null
  left_open "$one" || { firewall_up; return; }
  ok "a firewall lost in the swap leaves the run open"
  said=$(urun "$one" error); backup=$(urun "$one" safetyBackupId); kept=$(kept_container "$one")
  case "$said" in *"host firewall"*"checked again"*"$backup"*"new project"*) ok "the run says why, the backup and the way out" ;;
    *) ko "the run says: $said (backup $backup)" ;; esac
  [ -n "$kept" ] && ok "the old database is kept: $kept" || ko "the old database is gone"

  udelete "$one"
  case "$(uwait "$one" PENDING_DELETION)" in PENDING_DELETION) ok "delete keeps the project for its grace period" ;; *) ko "after delete: $(ustatus "$one")" ;; esac
  due=$(uapi "$UAPI/provision/$one/" | ujson 'd.get("deletionDueAt") or ""')
  python3 -c "import datetime,sys;d=datetime.datetime.fromisoformat(sys.argv[1].replace('Z','+00:00'));sys.exit(0 if (d-datetime.datetime.now(datetime.timezone.utc)).days>=6 else 1)" "$due" \
    && ok "due 7 days from now: $due" || ko "due: '$due'"
  [ "$($DOCKER inspect "excalibase-$one-postgres" --format '{{.State.Running}}' 2>/dev/null)" != true ] && ok "the database is stopped through the grace period" || ko "the database runs through the grace period"
  [ -n "$(kept_container "$one")" ] && ok "the old database stays for the grace period" || ko "the old database went with the schedule"

  uapi -X POST "$UAPI/provision/$one/deletion/cancel" -o /dev/null
  case "$(uwait "$one" UPGRADING)" in UPGRADING) ok "a cancel goes back to the open upgrade" ;; *) ko "after the cancel: $(ustatus "$one")" ;; esac
  firewall_up && ok "firewall healthy again" || ko "the firewall did not come back"

  # The way out the message names, taken while the run is still open.
  job=$(uapi -X POST "$UAPI/provision/$one/backup/restore" -d "{\"newProjectName\":\"upgrade-copy\",\"backupId\":\"$backup\"}")
  restored=$(pg_wait_restore "$one" "$job")
  case "$restored" in COMPLETED*) ok "the backup it names restores into a new project" ;; *) ko "restore of $backup: $restored ($job)" ;; esac

  case "$(uwait "$one" ACTIVE)" in ACTIVE) ok "recovery settles the run once the firewall is back" ;; *) ko "after recovery: $(ustatus "$one") $(urun "$one" error)" ;; esac
  [ "$(usql "$one" 'SELECT body FROM kept')" = "before the upgrade" ] && ok "the data came through" || ko "data: $(usql "$one" 'SELECT body FROM kept')"
  [ -z "$(kept_container "$one")" ] && ok "a finished upgrade leaves no old database" || ko "left: $(kept_container "$one")"

  echo "== a tenant running under a PAUSED row"
  uapi -X POST "$UAPI/provision/$one/pause" -o /dev/null
  uwait "$one" PAUSED >/dev/null
  $DOCKER start "excalibase-$one-postgres" >/dev/null
  uapi -X POST "$UAPI/provision/$one/pause" -o /dev/null
  sleep 2
  [ "$($DOCKER inspect "excalibase-$one-postgres" --format '{{.State.Running}}')" = true ] && ok "Pause alone changes nothing, as the docs warn" || ko "Pause alone stopped it: drop the warning"
  uapi -X POST "$UAPI/provision/$one/resume" -o /dev/null
  uwait "$one" ACTIVE >/dev/null
  uapi -X POST "$UAPI/provision/$one/pause" -o /dev/null
  case "$(uwait "$one" PAUSED)" in PAUSED) ;; *) ko "the pause: $(ustatus "$one")" ;; esac
  [ "$($DOCKER inspect "excalibase-$one-postgres" --format '{{.State.Running}}')" = false ] && ok "Resume then Pause stops it" || ko "still running after Resume then Pause"

  udelete "$one"
  case "$(uwait "$one" PENDING_DELETION)" in PENDING_DELETION) ;; *) ko "the delete: $(ustatus "$one")" ;; esac
  $DOCKER start "excalibase-$one-postgres" >/dev/null
  case "$(uapi -X POST "$UAPI/provision/$one/resume" -o /dev/null -w '%{http_code}'):$(uapi -X POST "$UAPI/provision/$one/pause" -o /dev/null -w '%{http_code}')" in
    2*:*|*:2*) ko "a project scheduled for deletion took a resume or a pause" ;; *) ok "scheduled for deletion, both are refused" ;; esac
  uapi -X POST "$UAPI/provision/$one/deletion/cancel" -o /dev/null
  uwait "$one" PAUSED >/dev/null
  uapi -X POST "$UAPI/provision/$one/resume" -o /dev/null
  uwait "$one" ACTIVE >/dev/null
  uapi -X POST "$UAPI/provision/$one/pause" -o /dev/null
  uwait "$one" PAUSED >/dev/null
  [ "$($DOCKER inspect "excalibase-$one-postgres" --format '{{.State.Running}}')" = false ] && ok "cancel, Resume, Pause stops one that was scheduled" || ko "still running after cancel, Resume, Pause"

  teardown_part
}
teardown_part() {
  local two kept left
  echo "== teardown of an upgrade left open"
  two=$(uprovision upgrade-two)
  left_open "$two" || { firewall_up; return; }
  kept=$(kept_container "$two")
  udelete "$two"
  case "$(uwait "$two" PENDING_DELETION)" in PENDING_DELETION) ;; *) ko "the delete: $(ustatus "$two")" ;; esac
  firewall_up || ko "the firewall did not come back"
  $DOCKER exec excalibase-postgres psql -qU excalibase -d excalibase_platform \
    -c "UPDATE database_instances SET deletion_due_at = now() - interval '1 minute' WHERE project_id = '$two'" >/dev/null
  for i in $(seq 110); do
    [ -z "$($DOCKER ps -aq --filter name="excalibase-$two-")" ] && break
    sleep 10
  done
  left=$($DOCKER ps -a --format '{{.Names}}' | grep "excalibase-$two-" | tr '\n' ' ')
  [ -n "$kept" ] && [ -z "$left" ] && ok "the grace period over, the old database ($kept) went too" \
    || ko "left behind: $left (kept was $kept); project: $($DOCKER exec excalibase-postgres psql -qtAX -U excalibase -d excalibase_platform -c "SELECT status, current_step, deletion_due_at, now(), failure_reason FROM database_instances WHERE project_id = '$two'"); $($DOCKER logs excalibase-provisioning 2>&1 | grep -iE 'deletion.sweep' | tail -5)"
  [ -z "$($DOCKER volume ls -q | grep "$two")" ] && ok "no volume of the project is left" || ko "volumes left: $($DOCKER volume ls -q | grep "$two")"
}
pg_wait_restore() {
  local id js
  id=$(echo "$2" | ujson 'd.get("id","")')
  for i in $(seq 200); do
    js=$(uapi "$UAPI/provision/$1/backup/restore/$id" | ujson 'd.get("status","")+" "+(d.get("failureReason") or "")')
    case "$js" in COMPLETED*|FAILED*) break ;; esac
    sleep 3
  done
  echo "${js:-$2}"
}
major_upgrade_journey
