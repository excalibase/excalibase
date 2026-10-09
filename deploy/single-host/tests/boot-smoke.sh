#!/bin/bash
# Boots the bundle from fresh state and walks a user's first hour: weak secret
# refused, install, sign in, a Postgres project through the engine proxy at its
# tier's limits, GraphQL and REST with a secret key, a backup.
#   boot-smoke.sh <docker|podman>      (COMPOSE="podman compose" DOCKER=podman for Podman)
# PLATFORM_TAG names locally built provisioning/studio images (default: TAG).
# Destroys the bundle's containers and volumes; never run it on an install you keep.
set -uo pipefail
ENGINE=${1:-docker}
BUNDLE=$(cd "$(dirname "$0")/.." && pwd)
W=$(mktemp -d)
cp "$(dirname "$0")/boot-smoke.override.yml" "$W/override.yml"
PORT=${EDGE_PORT:-28443}
COMPOSE=${COMPOSE:-"docker compose"}
DOCKER=${DOCKER:-docker}
P="-p excalibase-smoke --env-file $W/.env -f $BUNDLE/compose.yaml -f $W/override.yml"
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
c() { curl -sk --max-time 30 --resolve studio.localhost:$PORT:127.0.0.1 --resolve api.localhost:$PORT:127.0.0.1 "$@"; }

echo "== fresh state"
cd "$BUNDLE"
$COMPOSE $P down -v --remove-orphans >/dev/null 2>&1
$DOCKER rm -f -v $($DOCKER ps -aq --filter label=excalibase.managed=true) >/dev/null 2>&1
rm -f "$W/.env"
# Container names are fixed: another stack holding them would answer instead.
for name in excalibase-preflight excalibase-provisioning excalibase-edge; do
  if $DOCKER inspect "$name" >/dev/null 2>&1; then echo "another stack holds $name; tear it down first"; exit 2; fi
done

echo "== weak secret is refused"
./init.sh --env-file "$W/.env" --domain localhost --admin-email owner@example.com --engine "$ENGINE" >/dev/null || { echo "init refused"; exit 1; }
sed -i 's/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=password123/' "$W/.env"
echo "TEST_S3_SECRET=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')" >> "$W/.env"
timeout 300 $COMPOSE $P up preflight >/dev/null 2>&1
pcode=$($DOCKER inspect excalibase-preflight --format '{{.State.ExitCode}}' 2>/dev/null)
if [ -n "$pcode" ] && [ "$pcode" != 0 ] && $DOCKER logs excalibase-preflight 2>&1 | grep -q POSTGRES_PASSWORD; then ok "preflight refused password123"; else ko "preflight did not refuse password123 (exit '$pcode')"; fi
$COMPOSE $P down -v >/dev/null 2>&1; rm -f "$W/.env"

echo "== install"
./init.sh --env-file "$W/.env" --domain localhost --admin-email owner@example.com --engine "$ENGINE" || exit 1
s3=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
printf 'TEST_S3_SECRET=%s\nBACKUP_DEFAULT_ACCESS_KEY_ID=testbackups\nBACKUP_DEFAULT_SECRET_ACCESS_KEY=%s\nBACKUP_DEFAULT_ENDPOINT=http://test-s3:9000\nBACKUP_DEFAULT_BUCKET=excalibase-backups\nBACKUP_DEFAULT_REGION=us-east-1\n' "$s3" "$s3" >> "$W/.env"
printf 'EDGE_BIND_ADDRESS=127.0.0.1\nEDGE_HTTP_PORT=%s\nEDGE_HTTPS_PORT=%s\n' $((PORT-363)) $PORT >> "$W/.env"
timeout 900 $COMPOSE $P up -d > "$W/up.log" 2>&1; echo "up exit $?"; grep -iE "^Error|error:" "$W/up.log" | sort -u | head -5
$DOCKER run --rm --network excalibase-platform --entrypoint sh docker.io/rustfs/rc:v0.1.36@sha256:ab024bfebee49a750ce886b4c70963ccd9ddaa03f491704a90710641d7a26699 -c "for i in \$(seq 60); do rc alias set s http://test-s3:9000 testbackups $s3 >/dev/null 2>&1 && rc ls s/ >/dev/null 2>&1 && break; sleep 2; done; rc mb s/excalibase-backups" >/dev/null 2>&1 && ok "test bucket created" || ko "test bucket not created"
for i in $(seq 90); do c -o /dev/null -w "%{http_code}" https://studio.localhost:$PORT/api/auth/me 2>/dev/null | grep -q 401 && break; sleep 2; done
for i in $(seq 60); do [ "$($DOCKER inspect excalibase-bootstrap --format '{{.State.Status}}' 2>/dev/null)" = exited ] && break; sleep 2; done
[ "$($DOCKER inspect excalibase-bootstrap --format '{{.State.ExitCode}}')" = 0 ] && ok "bootstrap completed" || ko "bootstrap exit $($DOCKER inspect excalibase-bootstrap --format '{{.State.ExitCode}}')"

echo "== exposure"
published=$($DOCKER ps --filter name=excalibase- --format '{{.Names}} {{.Ports}}' | grep -- '->' | awk '{print $1}' | sort -u | tr '\n' ' ')
[ "$published" = "excalibase-edge " ] && ok "only the edge is published" || ko "published: $published"
mounts=$($DOCKER inspect excalibase-provisioning --format '{{range .Mounts}}{{.Source}} {{end}}')
case "$mounts" in *sock*) ko "provisioning mounts a socket: $mounts" ;; *) ok "provisioning holds no engine socket" ;; esac
c -o /dev/null -w '%{http_code}' https://api.localhost:$PORT/actuator/health | grep -q 404 && ok "api host hides actuator" || ko "actuator reachable"

echo "== studio sign-in"
PW=$($DOCKER logs excalibase-bootstrap 2>&1 | sed -n 's/.*password: //p' | tail -1)
T=$(c -X POST https://studio.localhost:$PORT/api/auth/login -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"$PW\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("token",""))')
[ -n "$T" ] && ok "admin signs in through the edge" || ko "sign-in failed"
ORG=$(c -H "Authorization: Bearer $T" https://studio.localhost:$PORT/api/orgs | python3 -c 'import json,sys;d=json.load(sys.stdin);d=d if isinstance(d,list) else (d.get("orgs") or d.get("items"));print(d[0]["id"])')

echo "== postgres project"
R=$(c --max-time 600 -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' https://studio.localhost:$PORT/api/provision/ -d "{\"projectName\":\"shop\",\"orgId\":\"$ORG\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"17\"}")
PID=$(echo "$R" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("projectId",""))' 2>/dev/null)
[ -n "$PID" ] || { ko "provision refused: $R"; }
st=
for i in $(seq 60); do st=$(c -H "Authorization: Bearer $T" https://studio.localhost:$PORT/api/provision/$PID/ | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d.get("status"),d.get("failureReason") or "")' 2>/dev/null); case "$st" in ACTIVE*|FAILED*) break;; esac; sleep 3; done
case "$st" in ACTIVE*) ok "project ACTIVE" ;; *) ko "project status: $st" ;; esac
TC=$($DOCKER ps --filter label=excalibase.managed=true --format '{{.Names}}' | head -1)
[ -n "$TC" ] && ok "tenant container $TC created through the proxy" || ko "no tenant container"
nets=$($DOCKER inspect "$TC" --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' 2>/dev/null)
[ "$nets" = "excalibase-tenants " ] && ok "tenant on excalibase-tenants only" || ko "tenant networks: $nets"
tports=$($DOCKER port "$TC" 2>/dev/null | tr '\n' ' ')
case "$tports" in "") ko "tenant port unknown" ;; *0.0.0.0*|*::*|*"[::]"*) ko "tenant port on all addresses: $tports" ;; *) ok "tenant port on loopback ($tports)" ;; esac
$DOCKER logs excalibase-engine-proxy 2>&1 | grep -c ' 403 ' | xargs -I{} echo "info: proxy refusals during the run: {}"

echo "== tier limits"
mem=$($DOCKER inspect "$TC" --format '{{.HostConfig.Memory}} {{.HostConfig.NanoCpus}}' 2>/dev/null)
case "$mem" in "0 0"|"") ko "tenant has no limits ($mem)" ;; *) ok "tenant limited: memory/nanocpus $mem" ;; esac

echo "== data plane"
SLUG=$(c -H "Authorization: Bearer $T" https://studio.localhost:$PORT/api/orgs | python3 -c 'import json,sys;d=json.load(sys.stdin);d=d if isinstance(d,list) else (d.get("orgs") or d.get("items"));print(d[0].get("slug",""))')
q=$(c -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' https://studio.localhost:$PORT/api/schema/$PID/query -d '{"query":"CREATE TABLE notes (id serial primary key, body text not null); INSERT INTO notes (body) VALUES ('"'"'hello from a single host'"'"');"}' -w ' %{http_code}')
case "$q" in *" 200") ok "schema created through Studio's API" ;; *) ko "schema query: $q" ;; esac
KEY=$(c -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' https://studio.localhost:$PORT/api/projects/$PID/sdk-keys/ -d '{"name":"verify","keyType":"secret"}' | python3 -c 'import json,sys;print(json.load(sys.stdin).get("plaintext",""))')
[ -n "$KEY" ] && ok "secret key issued" || ko "no secret key"
AT=$(c -X POST -H 'Content-Type: application/json' https://api.localhost:$PORT/auth/$SLUG/$PID/token -d "{\"grant_type\":\"api_key\",\"api_key\":\"$KEY\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("accessToken",""))')
[ -n "$AT" ] && ok "key exchanged at api.<domain>/auth" || ko "key exchange failed"
G=""
for i in $(seq 20); do G=$(c -X POST -H "Authorization: Bearer $AT" -H 'Content-Type: application/json' https://api.localhost:$PORT/$PID/graphql -d '{"query":"{ publicNotes { id body } }"}'); echo "$G" | grep -q 'hello from a single host' && break; sleep 3; done
echo "$G" | grep -q 'hello from a single host' && ok "GraphQL answers through the edge" || ko "GraphQL: $G"
RS=$(c -H "Authorization: Bearer $AT" https://api.localhost:$PORT/$PID/api/v1/notes)
echo "$RS" | grep -q 'hello from a single host' && ok "REST answers through the edge" || ko "REST: $RS"
AN=$(c -X POST -H 'Content-Type: application/json' https://api.localhost:$PORT/$PID/graphql -d '{"query":"{ publicNotes { id body } }"}' -w ' %{http_code}')
echo "$AN" | grep -q 'hello from a single host' && ko "anonymous read without a grant: $AN" || ok "no token, no rows (${AN: -3})"

echo "== backups"
B=$(c -X POST -H "Authorization: Bearer $T" https://studio.localhost:$PORT/api/provision/$PID/backup/trigger -w ' %{http_code}')
case "$B" in *" 200"|*" 201"|*" 202") ok "manual backup taken (${B: -3})" ;; *) ko "manual backup: $B" ;; esac
L=$(c -H "Authorization: Bearer $T" https://studio.localhost:$PORT/api/provision/$PID/backup/list)
echo "$L" | grep -q '"COMPLETED"' && ok "backup listed COMPLETED" || ko "backups: ${L:0:300}"

echo "== studio health"
code=$(c -o /dev/null -w '%{http_code}' https://studio.localhost:$PORT/); h=$($DOCKER inspect excalibase-studio --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null)
[ "$code" = 200 ] && ok "studio serves through the edge (container health: $h)" || ko "studio $code"

echo "== result: $pass passed, $fail failed"
if [ "$fail" -gt 0 ]; then
  $COMPOSE $P ps -a 2>&1 | tail -20
  $COMPOSE $P logs --tail 40 provisioning engine-proxy 2>&1 | tail -80
fi
[ -n "${KEEP:-}" ] || { $COMPOSE $P down -v >/dev/null 2>&1; $DOCKER rm -f -v $($DOCKER ps -aq --filter label=excalibase.managed=true) >/dev/null 2>&1; }
[ "$fail" -eq 0 ]
