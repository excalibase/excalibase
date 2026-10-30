# Sourced by boot-smoke.sh with HOST_LOGS=1 (EXC-672): a line a project's
# database writes is stored in the bundled Loki under that project's labels,
# found by its own log search and never by another project's, and still found
# after Loki and provisioning restart and after the container itself is gone.
# Needs T, PID, TC, ORG, PORT, DOCKER, COMPOSE, P, c, ok, ko.
echo "== stored log search"
lapi() { local path=$1; shift; c -H "Authorization: Bearer $T" "$@" "https://studio.localhost:$PORT/api$path"; }
quote() { python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"; }
# Loki is reached only from provisioning's network; ask it from there.
loki_lines() {
  $DOCKER exec excalibase-provisioning wget -qO- \
    "http://loki:3100/loki/api/v1/query_range?query=$(quote "$1")&start=$(( ($(date +%s) - 3600) * 1000000000 ))&limit=100" 2>/dev/null
}

[ "$($DOCKER inspect excalibase-loki --format '{{.State.Status}}' 2>/dev/null)" = running ] && ok "the bundled Loki runs" || ko "no running Loki"
lnets=$($DOCKER inspect excalibase-loki --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' 2>/dev/null)
[ "$lnets" = "excalibase-logs " ] && ok "Loki is on its own network only" || ko "Loki networks: $lnets"
$DOCKER exec "$TC" getent hosts loki >/dev/null 2>&1 && ko "a tenant container resolves Loki" || ok "a tenant container cannot reach Loki"

R2=$(c --max-time 600 -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "https://studio.localhost:$PORT/api/provision/" \
  -d "{\"projectName\":\"other\",\"orgId\":\"$ORG\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"17\"}")
PID2=$(echo "$R2" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("projectId",""))' 2>/dev/null)
for i in $(seq 60); do st2=$(lapi "/provision/$PID2/" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status"))' 2>/dev/null); case "$st2" in ACTIVE|FAILED) break;; esac; sleep 3; done
[ "$st2" = ACTIVE ] && ok "a second project ($PID2) for isolation" || ko "second project: $st2 ${R2:0:300}"

MARK=exc672_$(date +%s)_$RANDOM
c -o /dev/null -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "https://studio.localhost:$PORT/api/schema/$PID/query" \
  -d "{\"query\":\"SELECT * FROM $MARK\"}"
LG=
for i in $(seq 40); do LG=$(lapi "/provision/$PID/logs?lines=200&since=1h&q=$MARK"); case "$(loki_lines "{project_id=\"$PID\"} |= \"$MARK\"")" in *"$MARK"*) break ;; esac; sleep 3; done
case "$(loki_lines "{project_id=\"$PID\",component=\"database\"} |= \"$MARK\"")" in *"$MARK"*) ok "the line is stored under its project's labels" ;; *) ko "not in Loki: $(loki_lines "{project_id=\"$PID\"}" | head -c 300)" ;; esac
case "$(loki_lines "{project_id=\"$PID2\"} |= \"$MARK\"")" in *"$MARK"*) ko "the line is stored under the other project" ;; *) ok "the other project's streams do not hold it" ;; esac
case "$LG" in *"$MARK"*) ok "the project's log search finds its line" ;; *) ko "search: ${LG:0:300}" ;; esac
LB=$(lapi "/provision/$PID2/logs?lines=500&since=1h&q=$MARK" -w ' %{http_code}')
case "$LB" in *"$MARK"*) ko "the other project's search shows the line" ;; *" 200") ok "the other project's search does not show it" ;; *) ko "other project search: ${LB:0:300}" ;; esac
LI=$(lapi "/provision/$PID2/logs?lines=500&since=1h&q=$(quote "\"} or {project_id=\"$PID")" -w ' %{http_code}')
case "$LI" in *"$MARK"*) ko "a selector-shaped term reached the other project" ;; *) ok "a selector-shaped term stays a literal filter (${LI: -3})" ;; esac

echo "info: Loki after the run: $($DOCKER stats --no-stream --format '{{.CPUPerc}} cpu, {{.MemUsage}}' excalibase-loki 2>/dev/null)"

$COMPOSE $P restart loki provisioning >/dev/null 2>&1
if [ -n "${MANUAL_UNSEAL:-}" ]; then
  for i in $(seq 60); do [ "$(status)" = sealed ] && break; sleep 2; done
  printf '%s\n%s\n' "$T" "$UNSEAL_KEY" | $DOCKER exec -i excalibase-provisioning excalibase-provisioning vault unseal >/dev/null 2>&1
fi
for i in $(seq 60); do c -o /dev/null -w "%{http_code}" "https://studio.localhost:$PORT/api/auth/me" 2>/dev/null | grep -q 401 && break; sleep 2; done
$DOCKER rm -f "$TC" >/dev/null 2>&1
$DOCKER inspect "$TC" >/dev/null 2>&1 && ko "the database container is still there" || ok "Loki and provisioning restarted; the database container is gone with its engine log"
LA=
for i in $(seq 40); do LA=$(lapi "/provision/$PID/logs?lines=200&since=1h&q=$MARK"); case "$LA" in *"$MARK"*) break ;; esac; sleep 3; done
case "$LA" in *"$MARK"*) ok "the line is still found after the restart, from Loki alone" ;; *) ko "after the restart: ${LA:0:300}" ;; esac
case "$(lapi "/provision/$PID2/logs?lines=500&since=1h&q=$MARK")" in *"$MARK"*) ko "after the restart the other project sees the line" ;; *) ok "after the restart the other project still does not" ;; esac

# EXC-678: deleting a project removes its stored lines through Loki's delete
# API, for its own project_id alone; the other project's lines stay.
echo "== stored log deletion"
loki_deletes() { $DOCKER exec excalibase-provisioning wget -qO- "http://loki:3100/loki/api/v1/delete" 2>/dev/null; }
DD=$(c -X DELETE -H "Authorization: Bearer $T" "https://studio.localhost:$PORT/api/admin/projects/$PID2" -w ' %{http_code}')
case "$DD" in *" 200"|*" 202"|*" 204") ok "the second project is deleted" ;; *) ko "delete $PID2: ${DD:0:300}" ;; esac
LD=
for i in $(seq 60); do LD=$(loki_deletes); case "$LD" in *"project_id=\\\"$PID2\\\""*) break ;; esac; sleep 5; done
case "$LD" in *"project_id=\\\"$PID2\\\""*) ok "Loki holds a delete request for the deleted project" ;; *) ko "no delete request: ${LD:0:300}" ;; esac
echo "$LD" | python3 -c '
import json, re, sys
requests = json.load(sys.stdin)
bad = [r["query"] for r in requests if not re.fullmatch(r"\{(namespace|project_id)=\"[a-z0-9]([-a-z0-9]*[a-z0-9])?\"\}", r["query"])]
sys.exit(f"a wider delete request reached Loki: {bad}" if bad else 0)' && ok "every delete request names one project exactly" || ko "a delete request is wider than one project"
case "$LD" in *"project_id=\\\"$PID\\\""*) ko "the kept project's lines were asked to be deleted" ;; *) ok "no delete request for the kept project" ;; esac
case "$(loki_lines "{project_id=\"$PID\"} |= \"$MARK\"")" in *"$MARK"*) ok "the kept project's line is still stored" ;; *) ko "the kept project's line is gone" ;; esac
$DOCKER logs excalibase-provisioning 2>&1 | grep -q "log deletion: on" && ok "provisioning reports log deletion on" || ko "provisioning did not turn log deletion on"
