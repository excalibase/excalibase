# Sourced by boot-smoke.sh when MYSQL=1 (EXC-592): MySQL on a single host.
# A Studio user creates a MySQL project; a client on the host and one in an
# app container connect over TLS and plaintext is refused; it is backed up,
# restored to a second between two writes into a new project and in place to
# its first backup, paused and resumed; deleting the copy with its backups
# purges them once the grace period ends (brought forward here).
# Needs from boot-smoke.sh: c ok ko DOCKER COMPOSE P PORT T ORG.

MAPI=https://studio.localhost:$PORT/api
mjson() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
mapi() { c --max-time 900 -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
mwait() {
  local st=
  for i in $(seq 120); do
    st=$(mapi "$MAPI/provision/$1/" | mjson 'd.get("status","")+" "+(d.get("failureReason") or "")')
    case "$st" in "$2 "*|FAILED*) break ;; esac
    sleep 3
  done
  echo "$st"
}
# mysql_on <project> <where: host|app> <ssl-mode> <sql>: the catalogue
# image's client, on the host's loopback port or beside the apps.
mysql_on() {
  local cred user pass database image port where
  cred=$(mapi "$MAPI/provision/$1/credentials")
  user=$(echo "$cred" | mjson 'd["username"]'); pass=$(echo "$cred" | mjson 'd["password"]')
  database=$(echo "$cred" | mjson 'd["databaseName"]')
  image=$($DOCKER inspect "excalibase-$1-mysql" --format '{{.Config.Image}}')
  if [ "$2" = host ]; then
    port=$(mapi "$MAPI/projects/$1/db-endpoint" | mjson 'd["port"]')
    where=(--network host -h 127.0.0.1 -P "$port")
  else
    where=(--network excalibase-tenants -h "excalibase-$1-mysql" -P 3306)
  fi
  $DOCKER run --rm "${where[@]:0:2}" -e MYSQL_PWD="$pass" --entrypoint mysql "$image" \
    "${where[@]:2}" -u "$user" --ssl-mode="$3" --batch --skip-column-names -e "$4" "$database" 2>&1
}
restore_job() {
  local job js
  job=$(echo "$2" | mjson 'd.get("id","")')
  for i in $(seq 150); do
    js=$(mapi "$MAPI/provision/$1/backup/restore/$job" | mjson 'd.get("status","")+" "+(d.get("failureReason") or "")')
    case "$js" in COMPLETED*|FAILED*) break ;; esac
    sleep 3
  done
  echo "${js:-$2}"
}
backup_now() {
  local before
  before=$(mapi "$MAPI/provision/$1/backup/list" | grep -o '"COMPLETED"' | wc -l)
  mapi -X POST "$MAPI/provision/$1/backup/trigger" -o /dev/null
  for i in $(seq 60); do
    [ "$(mapi "$MAPI/provision/$1/backup/list" | grep -o '"COMPLETED"' | wc -l)" -gt "$before" ] && break
    sleep 3
  done
  mapi "$MAPI/provision/$1/backup/list" | mjson '[b["id"] for b in (d if isinstance(d,list) else d.get("backups",[])) if b.get("status")=="COMPLETED"][-1]'
}
backup_keys() {
  $COMPOSE $P run --rm -T --no-deps --entrypoint sh objectstore-setup -c \
    'rc alias set store "$ENDPOINT" "$BACKUPS_ID" "$BACKUPS_SECRET" >/dev/null && rc ls --recursive store/excalibase-backups/backups/'"$1"'/ 2>/dev/null' 2>/dev/null | grep -c mysql
}

echo "== mysql project"
R=$(mapi -X POST "$MAPI/provision/" -d "{\"projectName\":\"orders\",\"orgId\":\"$ORG\",\"databaseType\":\"MYSQL\",\"mysqlVersion\":\"8.4.9\"}")
MPID=$(echo "$R" | mjson 'd.get("projectId","")')
[ -n "$MPID" ] || ko "MySQL provision refused: ${R:0:300}"
st=$(mwait "$MPID" ACTIVE)
case "$st" in ACTIVE*) ok "MySQL project ACTIVE" ;; *) ko "MySQL project: $st" ;; esac
mports=$($DOCKER port "excalibase-$MPID-mysql" 2>/dev/null | tr '\n' ' ')
case "$mports" in *0.0.0.0*|*"[::]"*|"") ko "mysql ports: '$mports'" ;; 3306*) ok "mysqld published on loopback ($mports)" ;; *) ko "mysql ports: $mports" ;; esac
mlim=$($DOCKER inspect "excalibase-$MPID-mysql" --format '{{.HostConfig.Memory}} {{.HostConfig.NanoCpus}}')
case "$mlim" in "0 0"|"") ko "mysql has no limits" ;; *) ok "mysql limited: memory/nanocpus $mlim" ;; esac
E=$(mapi "$MAPI/projects/$MPID/db-endpoint")
[ "$(echo "$E" | mjson 'd.get("engine")=="mysql" and d.get("requireTls") and d["host"]=="127.0.0.1" and d.get("available")')" = True ] \
  && ok "Studio shows the TLS-only loopback endpoint" || ko "endpoint: ${E:0:400}"
MPORT=$(echo "$E" | mjson 'd["port"]')
[ "${MPORT:-0}" -ge 30000 ] && [ "$MPORT" -le 30999 ] && ok "the project holds port $MPORT from the tenant window" || ko "port '$MPORT' outside 30000-30999"
# same_port <step>: the customer's connection string survives the step.
same_port() {
  local now
  now=$(mapi "$MAPI/projects/$MPID/db-endpoint" | mjson 'd["port"]')
  [ "$now" = "$MPORT" ] && ok "port $MPORT kept after $1" || ko "port moved from $MPORT to '$now' after $1"
}
CR=$(mapi "$MAPI/provision/$MPID/credentials")
[ "$(echo "$CR" | mjson 'd.get("engine")')" = mysql ] && ok "credentials carry the mysql engine" || ko "credentials: $(echo "$CR" | mjson 'd.get("engine")')"

echo "== mysql clients"
out=$(mysql_on "$MPID" host REQUIRED "CREATE TABLE notes (id INT AUTO_INCREMENT PRIMARY KEY, body TEXT); INSERT INTO notes (body) VALUES ('a'),('b'),('c'); SHOW STATUS LIKE 'Ssl_cipher';")
case "$out" in *Ssl_cipher*[A-Z]*) ok "host client writes over TLS" ;; *) ko "host client: ${out:0:300}" ;; esac
out=$(mysql_on "$MPID" host DISABLED "SELECT 1")
secure=$(mysql_on "$MPID" host REQUIRED "SELECT @@require_secure_transport")
# Without TLS the password exchange itself fails, before the transport check.
case "$secure $out" in "1 "*"insecure transport"*|"1 "*"Access denied"*) ok "plaintext is refused (require_secure_transport on)" ;; *) ko "plaintext: $secure ${out:0:300}" ;; esac
out=$(mysql_on "$MPID" app REQUIRED "SELECT COUNT(*) FROM notes")
[ "$out" = 3 ] && ok "an app container reads it over TLS" || ko "app client: ${out:0:300}"
# EXC-604: the login apps get is the project's own user, never root, and holds nothing beyond the project's database.
who=$(mysql_on "$MPID" app REQUIRED "SELECT CURRENT_USER()")
case "$who" in root@*|"") ko "the app login is '$who'" ;; *) ok "the app login is $who, not root" ;; esac
grants=$(mysql_on "$MPID" app REQUIRED "SHOW GRANTS")
global=$(grep -F ' ON *.* ' <<< "$grants" | grep -v '^GRANT USAGE ON ')
[ -z "$global" ] && [ -n "$grants" ] && ok "no global privileges for the app login" || ko "the app login holds global privileges: ${global:0:300}"
out=$(mysql_on "$MPID" app REQUIRED "SELECT COUNT(*) FROM excalibase.migrations")
case "$out" in *"denied"*) ok "the app login is refused on the platform database" ;; *) ko "platform database: ${out:0:300}" ;; esac

echo "== mysql backups and restores"
B1=$(backup_now "$MPID")
[ -n "$B1" ] && ok "MySQL backup COMPLETED ($B1)" || ko "no MySQL backup"
mysql_on "$MPID" host REQUIRED "INSERT INTO notes (body) VALUES ('d'),('e')" >/dev/null
sleep 2; POINT=$(date -u +%Y-%m-%dT%H:%M:%SZ); sleep 2
mysql_on "$MPID" host REQUIRED "INSERT INTO notes (body) VALUES ('f'),('g')" >/dev/null
B2=$(backup_now "$MPID")
[ -n "$B2" ] && [ "$B2" != "$B1" ] && ok "second backup covers the point" || ko "second backup: '$B2'"
RJ=$(mapi -X POST "$MAPI/provision/$MPID/backup/restore" -d "{\"newProjectName\":\"orders-pitr\",\"targetTime\":\"$POINT\"}")
RPID=$(echo "$RJ" | mjson 'd.get("newProjectId","")')
js=$(restore_job "$MPID" "$RJ")
st=$(mwait "$RPID" ACTIVE)
out=$(mysql_on "$RPID" host REQUIRED "SELECT COUNT(*) FROM notes")
case "$js $st $out" in COMPLETED*ACTIVE*" 5") ok "restored to $POINT into a new project: 5 rows" ;; *) ko "point restore: $js / $st / ${out:0:200}" ;; esac
IJ=$(mapi -X POST "$MAPI/provision/$MPID/backup/restore" -d "{\"mode\":\"in_place\",\"backupId\":\"$B1\",\"confirmReplace\":true}")
js=$(restore_job "$MPID" "$IJ")
st=$(mwait "$MPID" ACTIVE)
out=$(mysql_on "$MPID" host REQUIRED "SELECT COUNT(*) FROM notes")
case "$js $st $out" in COMPLETED*ACTIVE*" 3") ok "restored in place to $B1: 3 rows" ;; *) ko "in-place restore: $js / $st / ${out:0:200}" ;; esac
same_port "the in-place restore"

echo "== mysql pause and resume"
mapi -X POST "$MAPI/provision/$MPID/pause" -o /dev/null
st=$(mwait "$MPID" PAUSED)
running=$($DOCKER ps -q --filter name="excalibase-$MPID-mysql" | wc -l)
case "$st" in PAUSED*) [ "$running" = 0 ] && ok "paused: mysqld stopped" || ko "paused with mysqld running" ;; *) ko "pause: $st" ;; esac
mapi -X POST "$MAPI/provision/$MPID/resume" -o /dev/null
st=$(mwait "$MPID" ACTIVE)
out=$(mysql_on "$MPID" host REQUIRED "SELECT COUNT(*) FROM notes")
case "$st $out" in ACTIVE*" 3") ok "resumed with its rows" ;; *) ko "resume: $st ${out:0:200}" ;; esac
same_port "pause and resume"

echo "== mysql minor upgrade"
up=$(mapi -X POST "$MAPI/provision/$MPID/upgrade" -w ' %{http_code}')
case "$up" in *" 200") ok "minor upgrade accepted" ;; *) ko "upgrade: ${up:0:300}" ;; esac
for i in $(seq 60); do
  [ "$(mapi "$MAPI/provision/$MPID/" | mjson 'd.get("mysqlVersion")')" = 8.4.10 ] && break
  sleep 5
done
out=$(mysql_on "$MPID" host REQUIRED "SELECT CONCAT(VERSION(), ' ', (SELECT COUNT(*) FROM notes))")
[ "$out" = "8.4.10 3" ] && ok "upgraded to 8.4.10 with its rows" || ko "after the upgrade: ${out:0:300}"
same_port "the minor upgrade"

echo "== mysql delete purges backups"
backup_now "$RPID" >/dev/null
keys=$(backup_keys "$RPID")
mapi -X PATCH "$MAPI/provision/$RPID/deletion-protection" -d '{"enabled":false}' -o /dev/null
mapi -X DELETE "$MAPI/provision/$RPID/" -d '{"confirmDeleteBackups":true}' -o /dev/null
st=$(mwait "$RPID" PENDING_DELETION)
case "$st" in PENDING_DELETION*) ok "copy pending deletion with its backups ($keys objects)" ;; *) ko "delete: $st" ;; esac
$DOCKER exec excalibase-postgres psql -qU excalibase -d excalibase_platform \
  -c "UPDATE database_instances SET deletion_due_at = now() - interval '1 minute' WHERE project_id = '$RPID'" >/dev/null
for i in $(seq 110); do
  [ -z "$($DOCKER ps -aq --filter name="excalibase-$RPID-mysql")" ] && [ "$(backup_keys "$RPID")" = 0 ] && break
  sleep 10
done
left=$(backup_keys "$RPID")
[ "${keys:-0}" -gt 0 ] && [ "$left" = 0 ] && [ -z "$($DOCKER ps -aq --filter name="excalibase-$RPID-mysql")" ] \
  && ok "the grace period over, the copy and its backups are gone" || ko "after deletion: $left backup objects left"
