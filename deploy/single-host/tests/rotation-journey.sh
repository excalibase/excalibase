# Sourced by boot-smoke.sh and mysql-journey.sh (EXC-675, FEATURE_DBREADROLE):
# an Admin is handed a read-only login that reads and changes nothing, and
# rotating the passwords ends a session the owner held open.
# Needs from boot-smoke.sh: c ok ko DOCKER PORT T.

rot_api() { c --max-time 300 -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
rot_json() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

# Postgres trusts 127.0.0.1 from inside its own container, so a password is only
# checked when the client dials the container's address.
rot_host() { [ "$1" = mysql ] && echo 127.0.0.1 || $DOCKER exec "$2" awk 'END{print $1}' /etc/hosts; }

# rot_sql <engine> <container> <user> <password> <database> <sql>: a client inside the database's container.
rot_sql() {
  if [ "$1" = mysql ]; then
    $DOCKER exec -e MYSQL_PWD="$4" "$2" mysql -h 127.0.0.1 -u "$3" --ssl-mode=REQUIRED --batch --skip-column-names -e "$6" "$5" 2>&1
  else
    $DOCKER exec -e PGPASSWORD="$4" "$2" psql -h "$(rot_host "$1" "$2")" -U "$3" -d "$5" -XtA -v ON_ERROR_STOP=1 -c "$6" 2>&1
  fi
}

# rot_sessions <engine> <container> <user>: the user's open sessions, counted as the superuser.
rot_sessions() {
  if [ "$1" = mysql ]; then
    $DOCKER exec "$2" mysql --socket=/tmp/mysqld.sock -uroot --batch --skip-column-names \
      -e "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE USER = '$3'" 2>/dev/null
  else
    $DOCKER exec "$2" psql -U postgres -XtA -c "SELECT count(*) FROM pg_stat_activity WHERE usename = '$3'" 2>/dev/null
  fi
}

rotation_journey() {
  local pid=$1 engine=$2 container=$3 cred user owner_password db rp out held sleep_sql
  echo "== read-only login and rotation ($engine)"
  cred=$(rot_api "https://studio.localhost:$PORT/api/provision/$pid/credentials")
  user=$(echo "$cred" | rot_json 'd["username"]'); owner_password=$(echo "$cred" | rot_json 'd["password"]')
  db=$(echo "$cred" | rot_json 'd["databaseName"]'); rp=$(echo "$cred" | rot_json 'd.get("readerPassword","")')
  [ "$(echo "$cred" | rot_json 'd.get("readerUsername","")')" = excalibase_reader ] && [ -n "$rp" ] \
    && ok "$engine credentials carry the read-only login" || ko "$engine credentials: no read-only login"

  rot_sql "$engine" "$container" "$user" "$owner_password" "$db" "CREATE TABLE rot_notes (id int); INSERT INTO rot_notes VALUES (1);" >/dev/null
  out=$(rot_sql "$engine" "$container" excalibase_reader "$rp" "$db" "SELECT COUNT(*) FROM rot_notes")
  [ "$out" = 1 ] && ok "$engine read-only login reads the owner's table" || ko "$engine reader read: ${out:0:200}"
  if [ "$engine" = mysql ]; then
    out=$(rot_sql "$engine" "$container" excalibase_reader "$rp" "$db" "INSERT INTO rot_notes VALUES (2)")
  else
    out=$(rot_sql "$engine" "$container" excalibase_reader "$rp" "$db" "BEGIN READ WRITE; CREATE TABLE planted (id int); COMMIT")
  fi
  case "$out" in *denied*|*"cannot change the schema"*) ok "$engine read-only login changes nothing" ;; *) ko "$engine reader wrote: ${out:0:200}" ;; esac

  sleep_sql="SELECT pg_sleep(600)"; [ "$engine" = mysql ] && sleep_sql="SELECT SLEEP(600)"
  if [ "$engine" = mysql ]; then
    $DOCKER exec -d -e MYSQL_PWD="$owner_password" "$container" mysql -h 127.0.0.1 -u "$user" --ssl-mode=REQUIRED -e "$sleep_sql" "$db"
  else
    $DOCKER exec -d -e PGPASSWORD="$owner_password" "$container" psql -h "$(rot_host "$engine" "$container")" -U "$user" -d "$db" -c "$sleep_sql"
  fi
  for i in $(seq 30); do held=$(rot_sessions "$engine" "$container" "$user"); [ "${held:-0}" -ge 1 ] && break; sleep 1; done
  [ "${held:-0}" -ge 1 ] && ok "$engine owner session held open" || ko "$engine no held owner session"
  code=$(rot_api -o /dev/null -w '%{http_code}' -X POST "https://studio.localhost:$PORT/api/provision/$pid/credentials/rotate")
  [ "$code" = 200 ] && ok "$engine passwords rotated" || ko "$engine rotate answered $code"
  held=$(rot_sessions "$engine" "$container" "$user")
  [ "$held" = 0 ] && ok "$engine rotation ended the session the old password opened" || ko "$engine sessions left after rotation: $held"
  cred=$(rot_api "https://studio.localhost:$PORT/api/provision/$pid/credentials")
  out=$(rot_sql "$engine" "$container" "$user" "$(echo "$cred" | rot_json 'd["password"]')" "$db" "SELECT COUNT(*) FROM rot_notes")
  [ "$out" = 1 ] && ok "$engine new owner password opens the database" || ko "$engine after rotation: ${out:0:200}"
  out=$(rot_sql "$engine" "$container" "$user" "$owner_password" "$db" "SELECT 1")
  [ "$out" != 1 ] && ok "$engine old owner password is refused" || ko "$engine old password still opens the database"
}
