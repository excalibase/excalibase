# Sourced by boot-smoke.sh with REQUEST_METRICS=1 (EXC-673): a project's GraphQL,
# REST and auth calls appear in its own request charts and not another's; a
# slow statement shows in its own slow queries and not another's; tenant
# containers cannot reach the counters. Needs T, PID, TC, ORG, SLUG, KEY, PORT,
# DOCKER, c, ok, ko.
echo "== request charts and slow queries"
rapi() { c -H "Authorization: Bearer $T" "https://studio.localhost:$PORT/api$1"; }
sql() { c -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "https://studio.localhost:$PORT/api/schema/$1/query" -d "{\"query\":\"$2\"}" -o /dev/null -w '%{http_code}'; }

R=$(c --max-time 600 -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' https://studio.localhost:$PORT/api/provision/ -d "{\"projectName\":\"quiet\",\"orgId\":\"$ORG\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"17\"}")
QPID=$(echo "$R" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("projectId",""))' 2>/dev/null)
qst=
for i in $(seq 60); do qst=$(rapi /provision/$QPID/ | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status"))' 2>/dev/null); case "$qst" in ACTIVE|FAILED) break;; esac; sleep 3; done
[ "$qst" = ACTIVE ] && ok "second project ACTIVE" || ko "second project: $qst ${R:0:200}"

# Slow queries: both projects were created with statistics on.
SQ=$(rapi "/provision/$PID/slow-queries/?sort=total")
case "$SQ" in *'"enabled":true'*) ok "a new project keeps statement statistics" ;; *) ko "slow queries off: ${SQ:0:300}" ;; esac
[ "$(sql "$PID" "SELECT pg_sleep(0.3) AS exc673_shop_marker")" = 200 ] && [ "$(sql "$QPID" "SELECT 1 AS exc673_quiet_marker")" = 200 ] \
  && ok "statements run in both projects" || ko "statements refused"
SQ=$(rapi "/provision/$PID/slow-queries/?sort=mean")
QQ=$(rapi "/provision/$QPID/slow-queries/?sort=mean")
case "$SQ" in *exc673_shop_marker*) case "$SQ" in *exc673_quiet_marker*) ko "a project sees another's statement" ;; *) ok "the slow statement shows in its own project" ;; esac ;; *) ko "own statement missing: ${SQ:0:400}" ;; esac
case "$QQ" in *exc673_shop_marker*) ko "the other project sees the slow statement" ;; *exc673_quiet_marker*) ok "the other project sees only its own statements" ;; *) ko "other project's statements: ${QQ:0:400}" ;; esac
EN=$(c -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $T" "https://studio.localhost:$PORT/api/provision/$QPID/slow-queries/enable")
[ "$EN" = 202 ] && ok "turning statistics on again is accepted (no second restart)" || ko "enable answered $EN"

# Requests: traffic for the first project only, then its charts and the other's.
# The first scrape after provisioning starts only sets the baseline, so calls
# keep coming until a sample has counted each family.
calls() {
  AT3=$(c -X POST -H 'Content-Type: application/json' https://api.localhost:$PORT/auth/$SLUG/$PID/token -d "{\"grant_type\":\"api_key\",\"api_key\":\"$KEY\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("accessToken",""))' 2>/dev/null)
  c -o /dev/null -X POST -H "Authorization: Bearer $AT3" -H 'Content-Type: application/json' https://api.localhost:$PORT/$PID/graphql -d '{"query":"{ publicNotes { id } }"}'
  c -o /dev/null -H "Authorization: Bearer $AT3" https://api.localhost:$PORT/$PID/api/v1/notes
}
totals() { python3 -c "import json,sys
d=json.load(sys.stdin)
print(' '.join(f\"{t['family']}={t['requests']}\" for t in d.get('totals') or [] if t['requests']))" 2>/dev/null; }
RC=
for i in $(seq 40); do
  calls
  RC=$(rapi "/provision/$PID/requests?range=1h" | totals)
  case "$RC" in *graphql=*rest=*auth=*) break ;; esac
  sleep 6
done
case "$RC" in *graphql=*rest=*auth=*) ok "the project's charts count its calls ($RC)" ;; *) ko "request charts: '$RC'" ;; esac
QC=$(rapi "/provision/$QPID/requests?range=1h" | totals)
[ -z "$QC" ] && ok "the other project's charts show none of them" || ko "the other project's charts: $QC"

# The counters bind the metrics network alone: a tenant container reaches neither port.
gip=$($DOCKER inspect excalibase-graphql --format '{{(index .NetworkSettings.Networks "excalibase-metrics").IPAddress}}' 2>/dev/null)
aip=$($DOCKER inspect excalibase-auth --format '{{(index .NetworkSettings.Networks "excalibase-metrics").IPAddress}}' 2>/dev/null)
reached=
for target in excalibase-graphql:9464 excalibase-auth:9465 graphql-metrics:9464 auth-metrics:9465 "$gip:9464" "$aip:9465"; do
  host=${target%:*}; port=${target##*:}
  [ -n "$host" ] || continue
  if $DOCKER exec "$TC" timeout 3 bash -c "exec 3<>/dev/tcp/$host/$port" >/dev/null 2>&1; then reached="$reached $target"; fi
done
[ -n "$gip" ] && [ -n "$aip" ] && [ -z "$reached" ] && ok "a tenant container cannot reach the request counters" || ko "counters reached from a tenant:$reached (metrics ips '$gip' '$aip')"
$DOCKER exec "$TC" timeout 3 bash -c "exec 3<>/dev/tcp/excalibase-graphql/10000" >/dev/null 2>&1 \
  && ok "(control) the same probe reaches graphql's API port" || ko "the probe itself does not work from the tenant container"
