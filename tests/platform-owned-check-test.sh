#!/usr/bin/env bash
# scripts/check-platform-owned.sh against a throwaway local Postgres: an object the
# platform role does not own is reported and fails the check (EXC-774).
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
check="$here/../scripts/check-platform-owned.sh"
name="owned-check-$$"
port=$((26100 + RANDOM % 200))
cleanup() { docker rm -fv "$name" >/dev/null 2>&1 || true; rm -f "$wrapper"; }
wrapper=$(mktemp)
trap cleanup EXIT
cat > "$wrapper" <<WRAP
#!/usr/bin/env bash
exec docker exec -i -e PGHOST -e PGPORT -e PGUSER -e PGPASSWORD -e PGDATABASE -e PGSSLMODE -e PGOPTIONS $name psql "\$@"
WRAP
chmod +x "$wrapper"
docker run -d --name "$name" -p "127.0.0.1:$port:5432" -e POSTGRES_PASSWORD=pw "${IMAGE_MIRROR:+$IMAGE_MIRROR/}postgres:16-alpine" >/dev/null
for _ in $(seq 1 40); do
  docker exec "$name" pg_isready -U postgres >/dev/null 2>&1 && break
  sleep 1
done
sleep 2
admin() { docker exec -i "$name" psql -v ON_ERROR_STOP=1 -U postgres -d "${DB:-platform}" -q "$@"; }
docker exec "$name" psql -U postgres -q -c "CREATE ROLE platform LOGIN PASSWORD 'pw'" -c "CREATE ROLE platform_ops NOLOGIN" -c "CREATE ROLE switcher LOGIN PASSWORD 'pw'" -c "GRANT platform TO switcher" -c "ALTER ROLE switcher SET role = platform" -c "GRANT platform_ops TO platform" -c "CREATE DATABASE platform OWNER platform"
admin -c "GRANT ALL ON SCHEMA public TO platform"
admin -c "SET ROLE platform; CREATE TABLE users (id text); CREATE SEQUENCE users_seq; CREATE TABLE events (at date) PARTITION BY RANGE (at);"
export PSQL="$wrapper"
url="postgres://platform:pw@localhost/platform"
fail=0
expect() { # expect <want rc> <label> [args]
  want=$1; label=$2; shift 2
  set +e; out=$(bash "$check" "$@" 2>&1); rc=$?; set -e
  [ "$rc" -eq "$want" ] || { echo "FAIL $label: want rc $want got $rc: $out"; fail=1; }
  LAST=$out
}
names() { # names <label> <word>
  case $LAST in *"$2"*) ;; *) echo "FAIL $1: output lacks $2: $LAST"; fail=1 ;; esac
}

expect 0 "all owned" "$url"
admin -c "CREATE TABLE abuse_suspensions (user_id text)"
expect 1 "foreign table" "$url"; names "foreign table" abuse_suspensions
admin -c "ALTER TABLE abuse_suspensions OWNER TO platform"
expect 0 "owner fixed" "$url"

admin -c "CREATE TABLE events_2026 PARTITION OF events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')"
expect 1 "foreign partition" "$url"; names "foreign partition" events_2026
admin -c "ALTER TABLE events_2026 OWNER TO platform"
admin -c "CREATE FUNCTION hand_made_fn() RETURNS int LANGUAGE sql AS 'SELECT 1'"
expect 1 "foreign function" "$url"; names "foreign function" hand_made_fn
admin -c "ALTER FUNCTION hand_made_fn() OWNER TO platform"
admin -c "CREATE TYPE hand_made_enum AS ENUM ('a')"
expect 1 "foreign enum" "$url"; names "foreign enum" hand_made_enum
admin -c "ALTER TYPE hand_made_enum OWNER TO platform"
admin -c "CREATE DOMAIN hand_made_dom AS int"
expect 1 "foreign domain" "$url"; names "foreign domain" hand_made_dom
admin -c "ALTER DOMAIN hand_made_dom OWNER TO platform"
admin -c "CREATE SCHEMA hand_made_schema"
expect 1 "foreign schema" "$url"; names "foreign schema" hand_made_schema
admin -c "ALTER SCHEMA hand_made_schema OWNER TO platform"
expect 0 "everything handed over" "$url"

admin -c "CREATE TABLE ops_owned (id int)" -c "ALTER TABLE ops_owned OWNER TO platform_ops"
expect 0 "owned by a role the platform role is a member of" "$url"

# A superuser passes every ownership test, so the check refuses to run as one.
expect 2 "superuser" "postgres://postgres:pw@localhost/platform"
case $LAST in *superuser*) ;; *) echo "FAIL superuser refusal not explained: $LAST"; fail=1 ;; esac
# A session that has switched to another role than the URL's user is refused too.
expect 2 "session role differs from the URL user" "postgres://switcher:pw@localhost/platform"

# Before PG15 the public schema belongs to postgres; its own owner is not a finding,
# what is inside it still is.
admin -c "ALTER SCHEMA public OWNER TO postgres" -c "GRANT ALL ON SCHEMA public TO platform"
expect 0 "public schema owned by postgres" "$url"
admin -c "CREATE TABLE public.stray (id int)"
expect 1 "foreign table inside public" "$url"; names "foreign table inside public" stray
admin -c "DROP TABLE public.stray"

# Escapes are decoded strictly: a bad one is refused, never read as something else.
expect 2 "bad percent escape" "postgres://platform:p%zzw@localhost/platform"
expect 2 "truncated percent escape" "postgres://platform:pw%4@localhost/platform"
expect 2 "percent in a user name" "postgres://plat%form:pw@localhost/platform"
expect 2 "raw backslash" 'postgres://platform:p\x41w@localhost/platform'
expect 2 "NUL escape" "postgres://platform:p%00w@localhost/platform"
expect 0 "valid escapes still decode" "postgres://platform:%70%77@localhost/platform"

expect 2 "no url"
expect 2 "not a url" "not-a-url; id"
expect 0 "percent-encoded password and a query string" "postgres://platform:p%40ss%2Fw@localhost:5432/platform?sslmode=disable"
expect 0 "postgresql scheme" "postgresql://platform:pw@localhost/platform"

secret='Sup3rS3cret!pw'
expect 2 "unreachable host" "postgres://platform:$secret@nonexistent.invalid/platform"
case $LAST in *"$secret"*) echo "FAIL password printed on a connection failure: $LAST"; fail=1 ;; esac
case $LAST in *nonexistent.invalid*) echo "FAIL connection error text echoed: $LAST"; fail=1 ;; esac
# the password never reaches an argument list
ARGV_LOG=$(mktemp)
cat > "$wrapper" <<WRAP
#!/usr/bin/env bash
printf '%s\n' "\$@" > $ARGV_LOG
exit 0
WRAP
bash "$check" "postgres://platform:$secret@localhost/platform" >/dev/null 2>&1 || true
if grep -q "$secret" "$ARGV_LOG"; then echo "FAIL password on the psql command line"; fail=1; fi
rm -f "$ARGV_LOG"

[ "$fail" -eq 0 ] && echo "platform owned check tests passed"
exit "$fail"
