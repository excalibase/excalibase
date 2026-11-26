#!/usr/bin/env bash
# Release gate (EXC-774): before migrations run, list every table, partition, view,
# sequence, function, type and schema in the platform database that the connecting
# platform role does not own (directly or through a role it is a member of). A
# migration that touches one fails with "must be owner of ..." and leaves the
# schema dirty (migration 112, 2026-11-17). Read only.
#
#   scripts/check-platform-owned.sh <platform-db-url>
#
# Exit 0: all owned. 1: foreign objects listed. 2: bad input or no connection.
# The password travels in PGPASSWORD, never on the command line, and psql's own
# error text is not printed (it can carry the URL). The check refuses (exit 2) to
# run as a superuser, which owns everything as far as ownership goes, or as a role
# other than the URL's user. PSQL overrides the client: a command that runs psql
# with the PG* variables (PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE,
# PGSSLMODE, PGOPTIONS) in its environment. To reach a database through
# `kubectl exec` or `docker exec`, point PSQL at a small wrapper script that
# forwards them, e.g.  exec docker exec -i -e PGUSER -e PGPASSWORD -e PGDATABASE \
# -e PGOPTIONS container psql "$@"
set -eu

die() { echo "check-platform-owned: $1" >&2; exit 2; }

url=${1:-}
case $url in postgres://* | postgresql://*) ;; *) die "usage: check-platform-owned.sh <postgres://platform-db-url>" ;; esac
case $url in *[[:space:][:cntrl:]]*) die "the URL holds whitespace or control characters" ;; esac

rest=${url#*://}
authority=${rest%%[/?]*}
tail=${rest#"$authority"}
hostport=${authority##*@}
user='' pass=''
if [ "$authority" != "$hostport" ]; then
  userinfo=${authority%@*}
  user=${userinfo%%:*}
  [ "$user" != "$userinfo" ] && pass=${userinfo#*:}
fi
case $hostport in
  \[*\]*) host=${hostport%%\]*}; host=${host#\[}; port=${hostport##*\]}; port=${port#:} ;;
  *:*) host=${hostport%%:*}; port=${hostport##*:} ;;
  *) host=$hostport; port='' ;;
esac
[ -n "$host" ] || die "the URL names no host"
dbname=${tail#/}; dbname=${dbname%%\?*}
query=''
case $tail in *\?*) query=${tail#*\?} ;; esac
# A value is decoded only when every % starts a two-digit hex escape and no raw
# backslash is there to be read as one; anything else is refused, not guessed at.
valid_escapes() {
  local rest=${1//%[0-9A-Fa-f][0-9A-Fa-f]/}
  case $1 in *\\*) return 1 ;; *%00*) return 1 ;; esac
  case $rest in *%*) return 1 ;; esac
}
for part in "$user" "$pass" "$dbname"; do
  valid_escapes "$part" || die "the URL has a bad % escape or a backslash; percent-encode it"
done
decode() { printf '%b' "${1//%/\\x}"; }

export PGHOST="$host" PGUSER="$(decode "$user")" PGPASSWORD="$(decode "$pass")" PGDATABASE="$(decode "$dbname")"
[ -n "$port" ] && export PGPORT="$port"
IFS='&'
for pair in $query; do
  case $pair in sslmode=*) export PGSSLMODE="${pair#sslmode=}" ;; esac
done
unset IFS

sql="
WITH objs AS (
  SELECT 'pg_class'::regclass AS cls, c.oid, n.nspname || '.' || c.relname || ' (' ||
         CASE WHEN c.relispartition THEN 'partition' WHEN c.relkind = 'S' THEN 'sequence'
              WHEN c.relkind IN ('v','m') THEN 'view' ELSE 'table' END || ')' AS label, c.relowner AS owner
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE c.relkind IN ('r','p','v','m','S','f')
  UNION ALL
  SELECT 'pg_proc'::regclass, p.oid, n.nspname || '.' || p.proname || ' (function)', p.proowner
  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
  UNION ALL
  SELECT 'pg_type'::regclass, t.oid, n.nspname || '.' || t.typname || ' (type)', t.typowner
  FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
  WHERE t.typtype IN ('e','d') OR (t.typtype = 'c' AND EXISTS (SELECT 1 FROM pg_class k WHERE k.oid = t.typrelid AND k.relkind = 'c'))
  UNION ALL
  SELECT 'pg_namespace'::regclass, n.oid, n.nspname || ' (schema)', n.nspowner FROM pg_namespace n
  WHERE n.nspname <> 'public' -- owned by postgres before PG15; what is inside it is still checked
)
SELECT label || ' owned by ' || pg_get_userbyid(owner)
FROM objs
WHERE label !~ '^(pg_catalog|information_schema|pg_toast|pg_temp)'
  AND NOT pg_has_role(current_user, owner, 'USAGE')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = objs.cls AND d.objid = objs.oid AND d.deptype = 'e')
ORDER BY 1"

# A superuser has every role's privileges, so ownership would always look fine.
who_sql="SELECT current_user || '|' || rolsuper::int FROM pg_roles WHERE rolname = current_user"
# shellcheck disable=SC2086
if ! who=$(PGOPTIONS="-c default_transaction_read_only=on" ${PSQL:-psql} -X -t -A -v ON_ERROR_STOP=1 -c "$who_sql" 2>/dev/null); then
  die "cannot read the database (check the host, credentials and that the client is installed)"
fi
[ "${who#*|}" = 1 ] && die "connected as a superuser, which owns everything as far as this check can tell; use the platform role"
[ "${who%|*}" = "$PGUSER" ] || die "the session runs as a role other than the URL's user; use the platform role directly"

# shellcheck disable=SC2086
if ! found=$(PGOPTIONS="-c default_transaction_read_only=on" ${PSQL:-psql} -X -t -A -v ON_ERROR_STOP=1 -c "$sql" 2>/dev/null); then
  die "cannot read the database (check the host, credentials and that the client is installed)"
fi
if [ -n "$found" ]; then
  echo "Objects the platform role does not own (migrations touching them will fail):" >&2
  echo "$found" >&2
  echo "Fix each with ALTER ... OWNER TO the platform role before upgrading." >&2
  exit 1
fi
echo "every platform object is owned by the platform role"
