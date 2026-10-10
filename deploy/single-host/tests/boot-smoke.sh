#!/bin/bash
# Boots the bundle from fresh state and walks a user's first hour: weak secret
# refused, install, sign in, a Postgres project through the engine proxy at its
# tier's limits, GraphQL and REST with a secret key, a backup and a file in the
# bundled object store (EXC-578).
#   boot-smoke.sh <docker|podman>      (COMPOSE="podman compose" DOCKER=podman for Podman)
# MANUAL_UNSEAL=1 installs with --manual-unseal (EXC-579): the admin initializes
# the vault, a provisioning restart seals it, and the CLI unseals it.
# APPS=1 installs with --apps and walks Containers (EXC-575, apps-journey.sh):
# under gVisor where the engine has runsc, else the documented opt-out
# (--app-sandbox none), which the run says. APP_SANDBOX forces either.
# DOCUMENTDB=1 installs with --documentdb and walks DocumentDB (EXC-576,
# documentdb-journey.sh): mongosh on the host, Studio's browser, Mongo users,
# pause, resume, backup and restore.
# PLATFORM_TAG names locally built provisioning/studio images (default: TAG).
# Destroys the bundle's containers and volumes; never run it on an install you keep.
set -uo pipefail
ENGINE=${1:-docker}
BUNDLE=$(cd "$(dirname "$0")/.." && pwd)
TESTS=$BUNDLE/tests
W=$(mktemp -d)
cp "$(dirname "$0")/boot-smoke.override.yml" "$W/override.yml"
PORT=${EDGE_PORT:-28443}
COMPOSE=${COMPOSE:-"docker compose"}
DOCKER=${DOCKER:-docker}
P="-p excalibase-smoke --env-file $W/.env -f $BUNDLE/compose.yaml -f $W/override.yml"
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
# Logs are read whole before matching: with pipefail, `logs | grep -q` fails
# when grep stops early and the engine's log writer gets SIGPIPE (Podman 4.9).
logs_have() { local out; out=$($DOCKER logs "$1" 2>&1); grep -qF -- "$2" <<< "$out"; }
c() { curl -sk --max-time 30 --resolve studio.localhost:$PORT:127.0.0.1 --resolve api.localhost:$PORT:127.0.0.1 --resolve files.localhost:$PORT:127.0.0.1 "$@"; }
# Apps leave per-project networks and disks; they go with the managed containers.
wipe_managed() {
  $DOCKER rm -f "$SMOKE_DNS" >/dev/null 2>&1
  # A DocumentDB gateway holds its database's network namespace: Podman removes it first.
  $DOCKER rm -f $($DOCKER ps -aq --filter label=excalibase.managed=true --filter name=-documentdb) >/dev/null 2>&1
  $DOCKER rm -f -v $($DOCKER ps -aq --filter label=excalibase.managed=true) >/dev/null 2>&1
  $DOCKER volume rm $($DOCKER volume ls -q --filter label=excalibase.managed=true) >/dev/null 2>&1
  $DOCKER network rm $($DOCKER network ls -q --filter label=excalibase.managed=true) >/dev/null 2>&1
}
APP_ARGS=
if [ -n "${APPS:-}" ]; then
  if [ -z "${APP_SANDBOX:-}" ]; then
    if $DOCKER info 2>/dev/null | grep -q runsc; then APP_SANDBOX=runsc; else APP_SANDBOX=none; fi
  fi
  [ "$APP_SANDBOX" = none ] && echo "info: no runsc on this engine: apps run with the documented opt-out, --app-sandbox none (no gVisor)"
  APP_ARGS="--apps --app-sandbox $APP_SANDBOX"
fi
# The DNS server the apps journey verifies a custom domain against.
SMOKE_DNS=smoke-dns-excalibase
status() { c https://studio.localhost:$PORT/api/vault/status | python3 -c 'import json,sys;d=json.load(sys.stdin);print("uninitialized" if not d["initialized"] else ("sealed" if d["sealed"] else "open"))' 2>/dev/null; }

echo "== fresh state"
cd "$BUNDLE"
$COMPOSE $P down -v --remove-orphans >/dev/null 2>&1
wipe_managed
rm -f "$W/.env"
# Container names are fixed: another stack holding them would answer instead.
for name in excalibase-preflight excalibase-provisioning excalibase-edge; do
  if $DOCKER inspect "$name" >/dev/null 2>&1; then echo "another stack holds $name; tear it down first"; exit 2; fi
done

echo "== weak secret is refused"
./init.sh --env-file "$W/.env" --domain localhost --admin-email owner@example.com --engine "$ENGINE" >/dev/null || { echo "init refused"; exit 1; }
sed -i 's/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=password123/' "$W/.env"
timeout 300 $COMPOSE $P up preflight >/dev/null 2>&1
pcode=$($DOCKER inspect excalibase-preflight --format '{{.State.ExitCode}}' 2>/dev/null)
if [ -n "$pcode" ] && [ "$pcode" != 0 ] && logs_have excalibase-preflight POSTGRES_PASSWORD; then ok "preflight refused password123"; else ko "preflight did not refuse password123 (exit '$pcode')"; fi
$COMPOSE $P down -v >/dev/null 2>&1; rm -f "$W/.env"

echo "== install"
./init.sh --env-file "$W/.env" --domain localhost --admin-email owner@example.com --engine "$ENGINE" ${MANUAL_UNSEAL:+--manual-unseal} $APP_ARGS ${DOCUMENTDB:+--documentdb} || exit 1
sed -i '/^EDGE_HTTP_PORT=\|^EDGE_HTTPS_PORT=/d; s|^FILES_PUBLIC_URL=.*|FILES_PUBLIC_URL=https://files.localhost:'"$PORT"'|' "$W/.env"
[ -n "${APPS:-}" ] && echo "APP_DOMAIN_RESOLVER=$SMOKE_DNS:53" >> "$W/.env"
printf 'EDGE_BIND_ADDRESS=127.0.0.1\nEDGE_HTTP_PORT=%s\nEDGE_HTTPS_PORT=%s\n' $((PORT-363)) $PORT >> "$W/.env"
timeout 900 $COMPOSE $P up -d > "$W/up.log" 2>&1; echo "up exit $?"; grep -iE "^Error|error:" "$W/up.log" | sort -u | head -5
for i in $(seq 90); do [ "$($DOCKER inspect excalibase-objectstore-setup --format '{{.State.Status}}' 2>/dev/null)" = exited ] && break; sleep 2; done
if [ "$($DOCKER inspect excalibase-objectstore-setup --format '{{.State.ExitCode}}' 2>/dev/null)" = 0 ] && logs_have excalibase-objectstore-setup 'OBJECT STORE READY'; then
  ok "bundled object store set up"
else
  ko "objectstore-setup: $($DOCKER logs excalibase-objectstore-setup 2>&1 | tail -3)"
fi
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
if [ -n "${MANUAL_UNSEAL:-}" ]; then
  echo "== manual unseal: the admin initializes the vault"
  [ "$(status)" = uninitialized ] && ok "manual install waits uninitialized" || ko "vault is $(status) on a manual install"
  blog=$($DOCKER logs excalibase-bootstrap 2>&1)
  case "$blog" in *"the vault is uninitialized"*) ok "bootstrap says the vault waits for the admin" ;; *) ko "bootstrap did not report the waiting vault: $(echo "$blog" | tail -8)" ;; esac
  UNSEAL_KEY=$(c -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' https://studio.localhost:$PORT/api/vault/init -d '{"shares":1,"threshold":1}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["shares"][0])' 2>/dev/null)
  [ -n "$UNSEAL_KEY" ] && [ "$(status)" = open ] && ok "vault initialized by the admin" || ko "vault init: $(status)"
fi
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

echo "== customer files in the bundled store"
bq=$(c -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' https://studio.localhost:$PORT/api/projects/$PID/storage/buckets -d '{"name":"smoke-files","public":false}' -w ' %{http_code}')
case "$bq" in *" 200"|*" 201") ok "bucket created" ;; *) ko "bucket: $bq" ;; esac
printf 'kept on a single host' > "$W/hello.txt"
U=$(c -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' https://studio.localhost:$PORT/api/projects/$PID/storage/buckets/smoke-files/upload-url -d '{"key":"hello.txt","mimeType":"text/plain","size":21}')
URL=$(echo "$U" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("url",""))' 2>/dev/null)
case "$URL" in https://files.localhost:$PORT/*) ok "upload link signed for files.<domain>" ;; *) ko "upload link: ${U:0:300}" ;; esac
pre=$(c -i -X OPTIONS "$URL" -H "Origin: https://studio.localhost:$PORT" -H 'Access-Control-Request-Method: PUT' -H 'Access-Control-Request-Headers: content-type' | tr -d '\r' | grep -i '^access-control-allow-origin')
[ -n "$pre" ] && ok "browser preflight allowed ($pre)" || ko "no CORS on the file bucket"
put=$(c -o /dev/null -w '%{http_code}' -X PUT -H 'Content-Type: text/plain' --data-binary @"$W/hello.txt" "$URL")
[ "$put" = 200 ] && ok "file uploaded through the edge" || ko "upload PUT $put"
UID_=$(echo "$U" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("uploadId",""))' 2>/dev/null)
cf=$(c -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' https://studio.localhost:$PORT/api/projects/$PID/storage/buckets/smoke-files/confirm-upload -d "{\"key\":\"hello.txt\",\"uploadId\":\"$UID_\"}" -w ' %{http_code}')
case "$cf" in *" 200"|*" 201") ok "upload confirmed" ;; *) ko "confirm: $cf" ;; esac
DL=$(c -H "Authorization: Bearer $T" https://studio.localhost:$PORT/api/projects/$PID/storage/buckets/smoke-files/download-url/hello.txt | python3 -c 'import json,sys;print(json.load(sys.stdin).get("url",""))' 2>/dev/null)
[ "$(c "$DL")" = "kept on a single host" ] && ok "file downloaded with the same bytes" || ko "download: $(c "$DL" | head -c 200)"
[ "$(c -o /dev/null -w '%{http_code}' https://files.localhost:$PORT/excalibase-backups/)" = 404 ] && ok "the backup bucket is not served at files.<domain>" || ko "files.<domain> serves the backup bucket"

if [ -n "${MANUAL_UNSEAL:-}" ]; then
  echo "== manual unseal: a restart seals the vault; the CLI unseals it"
  $COMPOSE $P restart provisioning >/dev/null 2>&1
  for i in $(seq 60); do s_=$(status); [ -n "$s_" ] && break; sleep 2; done
  [ "$(status)" = sealed ] && ok "sealed after a restart" || ko "after a restart the vault is $(status)"
  cli=$(printf '%s\n%s\n' "$T" "$UNSEAL_KEY" | $DOCKER exec -i excalibase-provisioning excalibase-provisioning vault unseal 2>&1)
  case "$cli" in *"The vault is unsealed"*) ok "unsealed with the CLI" ;; *) ko "the CLI did not unseal: $cli" ;; esac
  [ "$(status)" = open ] && ok "vault open again" || ko "vault is $(status)"
  for i in $(seq 30); do G=$(c -X POST -H "Authorization: Bearer $AT" -H 'Content-Type: application/json' https://api.localhost:$PORT/$PID/graphql -d '{"query":"{ publicNotes { id body } }"}'); echo "$G" | grep -q 'hello from a single host' && break; sleep 3; done
  echo "$G" | grep -q 'hello from a single host' && ok "GraphQL answers after the unseal" || ko "GraphQL after unseal: $G"
  AT2=$(c -X POST -H 'Content-Type: application/json' https://api.localhost:$PORT/auth/$SLUG/$PID/token -d "{\"grant_type\":\"api_key\",\"api_key\":\"$KEY\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("accessToken",""))' 2>/dev/null)
  [ -n "$AT2" ] && ok "a new key exchange works after the unseal" || ko "key exchange after the unseal failed"
  leaked=0
  for name in excalibase-provisioning excalibase-auth excalibase-bootstrap; do logs_have "$name" "$UNSEAL_KEY" && leaked=1; done
  $DOCKER exec excalibase-provisioning grep -rqF "$UNSEAL_KEY" /var/lib/excalibase 2>/dev/null && leaked=1
  [ "$leaked" = 0 ] && ok "the unseal key is in no log and not on provisioning's volume" || ko "the unseal key leaked"
fi

[ -n "${APPS:-}" ] && . "$TESTS/apps-journey.sh"
[ -n "${DOCUMENTDB:-}" ] && . "$TESTS/documentdb-journey.sh"

echo "== studio health"
code=$(c -o /dev/null -w '%{http_code}' https://studio.localhost:$PORT/); h=$($DOCKER inspect excalibase-studio --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null)
[ "$code" = 200 ] && ok "studio serves through the edge (container health: $h)" || ko "studio $code"

echo "== result: $pass passed, $fail failed"
if [ "$fail" -gt 0 ]; then
  $COMPOSE $P ps -a 2>&1 | tail -20
  $COMPOSE $P logs --tail 40 provisioning engine-proxy 2>&1 | tail -80
fi
[ -n "${KEEP:-}" ] || { $COMPOSE $P down -v >/dev/null 2>&1; wipe_managed; }
[ "$fail" -eq 0 ]
