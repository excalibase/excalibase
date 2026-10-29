# Sourced by boot-smoke.sh when APPS=1 (EXC-575): Containers on a single host.
# A Studio user deploys a small public image by digest, opens it over HTTPS at
# its app hostname, reads its logs, deploys a second revision and rolls back,
# keeps data on a disk, pauses, resumes and deletes it; another project's app
# reaches neither this app nor this project's database.
# Needs from boot-smoke.sh: c ok ko DOCKER PORT T ORG PID W.

APP_IMAGE=docker.io/library/busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
API=https://studio.localhost:$PORT/api
json() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
# Lifecycle calls answer once the containers have moved: a stop waits out the app's grace period.
api() { c --max-time 300 -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
# app_body serves BODY at / and appends one line to the disk per start.
app_spec() {
  cat <<JSON
{"name":"$1","image":"$APP_IMAGE","port":8080,"healthCheckPath":"/","replicas":1,
 "env":[{"name":"BODY","kind":"literal","value":"$2"}],
 "args":["sh","-c","mkdir -p /tmp/www && echo \"\$BODY\" > /tmp/www/index.html && date >> /data/starts && echo \"serving \$BODY\" && exec httpd -f -p 8080 -h /tmp/www"],
 "disk":{"mountPath":"/data","size":"1Gi"}}
JSON
}
# deploy_and_wait <project> <app>: the newest deploy, once it succeeded or failed.
wait_deploy() {
  local state=
  for i in $(seq 90); do
    state=$(api "$API/projects/$1/apps/$2/deploys" | json 'd[0]["status"] if isinstance(d,list) else (d.get("deploys") or d.get("items"))[0]["status"]')
    case "$state" in succeeded|failed|superseded) break ;; esac
    sleep 3
  done
  echo "$state"
}
deploys() { api "$API/projects/$1/apps/$2/deploys" | json 'json.dumps(d if isinstance(d,list) else (d.get("deploys") or d.get("items")))'; }
app_container() { $DOCKER ps --filter "label=excalibase.app=$1" --filter label=excalibase.component=app --format '{{.Names}}' | head -1; }
open_app() { curl -sk --max-time 10 --resolve "$1:$PORT:127.0.0.1" "https://$1:$PORT/"; }

echo "== apps: deploy an image by digest"
A=$(api -X POST "$API/projects/$PID/apps/" -d "$(app_spec web one)")
AID=$(echo "$A" | json 'd["id"]')
[ -n "$AID" ] && ok "app created" || ko "app create: ${A:0:300}"
dq=$(api -X POST "$API/projects/$PID/apps/$AID/deploy" -w ' %{http_code}')
case "$dq" in *" 202") ok "deploy accepted" ;; *) ko "deploy: ${dq:0:300}" ;; esac
[ "$(wait_deploy "$PID" "$AID")" = succeeded ] && ok "first deploy succeeded" || ko "first deploy: $(deploys "$PID" "$AID" | head -c 600)"
HOST=$(deploys "$PID" "$AID" | json 'd[0]["spec"]["url"].split("//")[1]' | tr -d '\r')
case "$HOST" in web-*.apps.localhost) ok "app host $HOST" ;; *) ko "app host: '$HOST'" ;; esac
body=""
for i in $(seq 20); do body=$(open_app "$HOST"); [ "$body" = one ] && break; sleep 2; done
[ "$body" = one ] && ok "app answers over HTTPS at its host through the edge" || ko "app answered '$body'"
cert=$(echo | openssl s_client -connect 127.0.0.1:$PORT -servername "$HOST" 2>/dev/null | openssl x509 -noout -ext subjectAltName 2>/dev/null | tr -d '\n ')
case "$cert" in *"DNS:$HOST"*) ok "the edge serves a certificate for the app host" ;; *) ko "certificate: $cert" ;; esac
logs=$(api "$API/projects/$PID/apps/$AID/logs?tail=50")
case "$logs" in *"serving one"*) ok "logs read through the API" ;; *) ko "logs: ${logs:0:300}" ;; esac

echo "== apps: create_app through MCP answers the https URL the edge serves and allows that origin (EXC-606)"
PAT=$(api -X POST "$API/auth/tokens/" -d "{\"name\":\"smoke-mcp\",\"projectId\":\"$PID\",\"scopes\":[\"read\",\"write\"]}" | json 'd["token"]')
[ -n "$PAT" ] || ko "no personal access token for MCP"
# mcp_call <tool> <arguments JSON>: the tool's own JSON answer.
mcp_call() {
  c --max-time 120 -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    "https://studio.localhost:$PORT/mcp" -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}" \
    | json 'd["result"]["content"][0]["text"]'
}
M=$(mcp_call create_app "{\"name\":\"site\",\"image\":\"$APP_IMAGE\",\"port\":8080,\"health_check_path\":\"/\"}")
MURL=$(echo "$M" | json 'd["app"]["url"]')
MID=$(echo "$M" | json 'd["app"]["id"]')
case "$MURL" in https://site-*.apps.localhost) ok "create_app answers the app's https URL ($MURL)" ;; *) ko "create_app URL: '$MURL' ${M:0:300}" ;; esac
[ -n "$MURL" ] && [ "$(echo "$M" | json 'd.get("corsOriginAdded","")')" = "$MURL" ] && ok "the app's https origin was added to CORS" || ko "CORS origin added: ${M:0:300}"
# What a browser on the app's page sends before a credentialed GraphQL call; a CORS change reaches the
# data plane within about a minute (create_app says so), so the page is retried as a user would.
preflight() {
  c -o /dev/null -D - -X OPTIONS "https://api.localhost:$PORT/$PID/graphql" -H "Origin: $MURL" \
    -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: authorization,content-type' | tr -d '\r'
}
for i in $(seq 40); do pre=$(preflight); grep -qixF "access-control-allow-origin: $MURL" <<< "$pre" && break; sleep 3; done
grep -qixF "access-control-allow-origin: $MURL" <<< "$pre" && ok "a browser preflight from the app's https origin is allowed" || ko "preflight from $MURL: $(head -c 400 <<< "$pre")"
[ -n "$MID" ] && api -X DELETE "$API/projects/$PID/apps/$MID/" -d '{}' -o /dev/null

echo "== apps: an app's server calls the platform by its public name, and MCP's probes reach it (EXC-608)"
NODE_IMAGE=docker.io/library/node@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402
N=$(api -X POST "$API/projects/$PID/apps/" -d '{"name":"caller","image":"'"$NODE_IMAGE"'","port":8080,"healthCheckPath":"/","replicas":1,"args":["node","-e","require(\"http\").createServer((q,s)=>s.end(\"ok\")).listen(8080)"]}')
NID=$(echo "$N" | json 'd["id"]')
api -X POST "$API/projects/$PID/apps/$NID/deploy" >/dev/null
[ "$(wait_deploy "$PID" "$NID")" = succeeded ] || ko "caller app: ${N:0:200} $(deploys "$PID" "$NID" | head -c 300)"
NC=$(app_container "$NID")
# A server-side fetch, as the app's own code makes it: name resolution, the route and the certificate.
called=$($DOCKER exec "$NC" node -e '
fetch(process.argv[1], {method: "POST", headers: {"content-type": "application/json"}, body: JSON.stringify({query: "{__typename}"})})
  .then(r => r.text().then(t => console.log(r.status, t)))
  .catch(e => console.log("ERR", (e.cause && (e.cause.code || e.cause.message)) || e.message))' "https://api.localhost/$PID/graphql" 2>&1)
case "$called" in "200 "*__typename*) ok "the app's server reaches https://api.localhost and trusts its certificate" ;; *) ko "server-side call from the app: ${called:0:300}" ;; esac
st=$(mcp_call get_deploy_status "{\"app_id\":\"$NID\"}")
[ "$(echo "$st" | json 'd["state"]')" = succeeded ] && ok "get_deploy_status: the app's URL answers" || ko "get_deploy_status: ${st:0:400}"
pr=$(mcp_call test_api_request "{\"api\":\"graphql\",\"body\":\"{\\\"query\\\":\\\"{__typename}\\\"}\"}")
case "$pr" in *__typename*) ok "test_api_request reaches the data API" ;; *) ko "test_api_request: ${pr:0:400}" ;; esac
api -X DELETE "$API/projects/$PID/apps/$NID/" -d '{}' -o /dev/null

echo "== apps: a second revision, then a rollback"
FIRST=$(deploys "$PID" "$AID" | json 'd[0]["id"]')
VER=$(api "$API/projects/$PID/apps/$AID/" | json 'd["version"]')
up=$(api -X PATCH -H "If-Match: $VER" "$API/projects/$PID/apps/$AID/" -d '{"env":[{"name":"BODY","kind":"literal","value":"two"}]}' -w ' %{http_code}')
case "$up" in *" 200") ;; *) ko "update: ${up:0:300}" ;; esac
api -X POST "$API/projects/$PID/apps/$AID/deploy" >/dev/null
[ "$(wait_deploy "$PID" "$AID")" = succeeded ] && [ "$(open_app "$HOST")" = two ] && ok "second revision serves 'two'" || ko "second revision: $(open_app "$HOST")"
rb=$(api -X POST "$API/projects/$PID/apps/$AID/deploys/$FIRST/redeploy" -w ' %{http_code}')
case "$rb" in *" 202") ;; *) ko "rollback: ${rb:0:300}" ;; esac
[ "$(wait_deploy "$PID" "$AID")" = succeeded ] && [ "$(open_app "$HOST")" = one ] && ok "rolled back: 'one' again" || ko "rollback serves: $(open_app "$HOST")"
echo "== apps: a custom domain, once its CNAME verifies"
# A DNS server the smoke owns answers shop.example.test CNAME <app host>; provisioning asks it (APP_DOMAIN_RESOLVER).
printf 'example.test:53 {\n  template IN CNAME shop.example.test {\n    answer "{{ .Name }} 60 IN CNAME %s."\n  }\n}\n' "$HOST" > "$W/Corefile"
chmod 644 "$W/Corefile"
$DOCKER rm -f "$SMOKE_DNS" >/dev/null 2>&1
$DOCKER run -d --name "$SMOKE_DNS" --network excalibase-tenants -v "$W/Corefile:/Corefile:ro,z" docker.io/coredns/coredns:1.11.3 -conf /Corefile >/dev/null
DOM=$(api -X POST "$API/projects/$PID/apps/$AID/domains/" -d '{"hostname":"shop.example.test"}')
DID=$(echo "$DOM" | json 'd["id"]')
[ -n "$DID" ] || ko "add domain: ${DOM:0:300}"
vstate=
for i in $(seq 10); do
  vstate=$(api -X POST "$API/projects/$PID/apps/$AID/domains/$DID/verify" | json 'd["status"]')
  case "$vstate" in issuing|active) break ;; esac
  sleep 3
done
case "$vstate" in issuing|active) ok "domain verified by its CNAME ($vstate)" ;; *) ko "domain verify: $vstate" ;; esac
for i in $(seq 60); do
  dstate=$(api "$API/projects/$PID/apps/$AID/domains/" | json '[x for x in (d if isinstance(d,list) else d.get("domains") or d.get("items")) if x["id"]=="'"$DID"'"][0]["status"]')
  [ "$dstate" = active ] && break; sleep 5
done
[ "$dstate" = active ] && ok "domain active once the edge holds its certificate" || ko "domain status: $dstate"
[ "$(open_app shop.example.test)" = one ] && ok "the app answers at its custom domain" || ko "custom domain answered '$(open_app shop.example.test)'"
$DOCKER rm -f "$SMOKE_DNS" >/dev/null 2>&1
AC=$(app_container "$AID")
starts=$($DOCKER exec "$AC" sh -c 'wc -l < /data/starts' 2>/dev/null | tr -d ' ')
[ "${starts:-0}" -ge 3 ] && ok "the disk kept data across 3 deploys ($starts starts)" || ko "disk lines: '$starts'"
nets=$($DOCKER inspect "$AC" --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}')
[ "$nets" = "excalibase-net-$PID " ] && ok "app on its project's network only" || ko "app networks: $nets"
rt=$($DOCKER inspect "$AC" --format '{{.HostConfig.Runtime}} {{.HostConfig.Memory}} {{.HostConfig.PidsLimit}}')
echo "info: app runtime/memory/pids: $rt (sandbox: $APP_SANDBOX)"
DBHOST=$($DOCKER ps --filter label=excalibase.managed=true --format '{{.Names}}' | grep -- '-postgres$' | head -1)
$DOCKER exec "$AC" nc -z -w 3 "$DBHOST" 5432 && ok "the app reaches its own project's database" || ko "app cannot reach $DBHOST"
$DOCKER exec "$AC" wget -q -T 3 -O /dev/null http://example.com/ 2>/dev/null && ko "the app reached the internet without APP_EGRESS" || ok "no egress by default"
# EXC-676: a host service on every address (sshd, a local database) is not reached through the bridge's own address.
HOST_PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("",0));print(s.getsockname()[1])')
python3 -m http.server "$HOST_PORT" --bind 0.0.0.0 --directory "$W" >/dev/null 2>&1 &
HOST_SRV=$!
subnet=$($DOCKER network inspect "excalibase-net-$PID" --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null || $DOCKER network inspect "excalibase-net-$PID" --format '{{range .Subnets}}{{.Subnet}} {{end}}')
BRIDGE=$(python3 -c 'import ipaddress,sys;print(next(str(next(n.hosts())) for n in map(ipaddress.ip_network,sys.argv[1:]) if n.version==4))' $subnet 2>/dev/null)
$DOCKER network create excalibase-smoke-control >/dev/null 2>&1
CONTROL_GW=$($DOCKER network inspect excalibase-smoke-control --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}' 2>/dev/null)
$DOCKER run --rm --network excalibase-smoke-control "$APP_IMAGE" wget -q -T 3 -O /dev/null "http://$CONTROL_GW:$HOST_PORT/" 2>/dev/null \
  || echo "info: a container on a plain bridge cannot reach the host listener either (host firewall, or rootless Podman); the check below proves less"
$DOCKER network rm excalibase-smoke-control >/dev/null 2>&1
if [ -z "$BRIDGE" ]; then
  ko "no subnet on excalibase-net-$PID"
elif $DOCKER exec "$AC" wget -q -T 3 -O /dev/null "http://$BRIDGE:$HOST_PORT/" 2>/dev/null; then
  ko "the app reached a host service through its network's bridge address $BRIDGE"
else
  ok "the app cannot reach a host service through its network's bridge address ($BRIDGE)"
fi
kill "$HOST_SRV" 2>/dev/null
logs_have excalibase-provisioning "WARN: app networks" && echo "info: $($DOCKER logs excalibase-provisioning 2>&1 | grep -m1 'WARN: app networks')"
# NATS has no per-client login: only the projects' watchers may reach it (EXC-612).
bus=
for ip in $($DOCKER inspect excalibase-nats --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}'); do
  $DOCKER exec "$AC" nc -z -w 3 "$ip" 4222 2>/dev/null && bus="$bus $ip"
done
$DOCKER exec "$AC" nc -z -w 3 nats 4222 2>/dev/null && bus="$bus nats"
[ -z "$bus" ] && ok "the app cannot reach NATS" || ko "the app reaches NATS at$bus"

echo "== apps: DATABASE_URL is the restricted project login (EXC-604)"
DBREF='{"name":"DATABASE_URL","kind":"reference","reference":{"sourceKind":"database","sourceName":"app","variable":"DATABASE_URL"}}'
VER=$(api "$API/projects/$PID/apps/$AID/" | json 'd["version"]')
up=$(api -X PATCH -H "If-Match: $VER" "$API/projects/$PID/apps/$AID/" -d '{"env":[{"name":"BODY","kind":"literal","value":"one"},'"$DBREF"']}' -w ' %{http_code}')
case "$up" in *" 200") ;; *) ko "attach DATABASE_URL: ${up:0:300}" ;; esac
api -X POST "$API/projects/$PID/apps/$AID/deploy" >/dev/null
[ "$(wait_deploy "$PID" "$AID")" = succeeded ] || ko "deploy with DATABASE_URL: $(deploys "$PID" "$AID" | head -c 300)"
AC=$(app_container "$AID")
# The app's own value, read from its container into a 0600 file; never printed.
( umask 077; $DOCKER inspect "$AC" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep '^DATABASE_URL=' > "$W/app-db.env" )
[ -s "$W/app-db.env" ] && ok "the app received DATABASE_URL" || ko "the app has no DATABASE_URL"
grep -q '^DATABASE_URL=postgresql://postgres:' "$W/app-db.env" && ko "DATABASE_URL carries the postgres superuser" || ok "DATABASE_URL is not the superuser"
# as_app <sql>: psql with the app's DATABASE_URL, on the app's own network.
as_app() {
  $DOCKER run --rm --network "excalibase-net-$PID" --env-file "$W/app-db.env" docker.io/library/postgres:17-alpine \
    sh -c 'psql "$DATABASE_URL" -q -v ON_ERROR_STOP=1 -tAc "$0"' "$1" 2>&1
}
[ "$(as_app "SELECT rolsuper OR rolbypassrls OR rolreplication FROM pg_roles WHERE rolname = current_user")" = f ] && ok "the app login is no superuser" || ko "the app login: $(as_app 'SELECT current_user')"
out=$(as_app "INSERT INTO notes (body) VALUES ('from the app') RETURNING body")
[ "$out" = "from the app" ] && ok "the app writes a migrated table" || ko "app write: ${out:0:300}"
out=$(as_app "SELECT count(*) FROM notes")
[ "${out:-0}" -ge 2 ] 2>/dev/null && ok "the app reads a migrated table ($out rows)" || ko "app read: ${out:0:300}"
for sql in "SELECT count(*) FROM auth.users" "CREATE TABLE excalibase.planted (id int)" "CREATE TABLE auth.planted (id int)"; do
  out=$(as_app "$sql")
  case "$out" in *"permission denied"*) ok "refused: $sql" ;; *) ko "allowed: $sql -> ${out:0:200}" ;; esac
done
out=$(as_app "CREATE FUNCTION public.app_steal() RETURNS trigger LANGUAGE plpgsql AS \$\$ BEGIN RETURN NEW; END \$\$; CREATE TRIGGER app_steal BEFORE INSERT ON notes FOR EACH ROW EXECUTE FUNCTION public.app_steal()")
case "$out" in *"permission denied"*|*"must be owner"*) ok "the app cannot attach a trigger to a migrated table" ;; *) ko "trigger: ${out:0:300}" ;; esac
# The password is matched from a 0600 file, never passed as an argument.
( umask 077; sed 's/^DATABASE_URL=//' "$W/app-db.env" | python3 -c 'import sys,urllib.parse as u;print(u.urlparse(sys.stdin.read().strip()).password or "")' > "$W/app-db.secret" )
leak=0
for name in excalibase-provisioning excalibase-engine-proxy excalibase-studio; do
  logged=$($DOCKER logs "$name" 2>&1)
  [ -s "$W/app-db.secret" ] && grep -qFf "$W/app-db.secret" <<< "$logged" && leak=1
done
[ -s "$W/app-db.secret" ] && [ "$leak" = 0 ] && ok "the app's database password is in no platform log" || ko "the app's database password leaked into a log (or was empty)"
rm -f "$W/app-db.env" "$W/app-db.secret"

echo "== apps: another project cannot reach this one"
# The single host's one organisation is on the Free plan, one project; the operator raises it.
FREE=$(api "$API/admin/tiers/" | json 'json.dumps(dict([t for t in (d if isinstance(d,list) else d.get("tiers")) if t["tier"]=="FREE"][0], maxProjects=2))')
tq=$(api -X PUT "$API/admin/tiers/FREE" -d "$FREE" -w ' %{http_code}')
case "$tq" in *" 200") ;; *) ko "raise the Free plan to two projects: ${tq:0:300}" ;; esac
R2=$(c --max-time 600 -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$API/provision/" -d "{\"projectName\":\"other\",\"orgId\":\"$ORG\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"17\"}")
PID2=$(echo "$R2" | json 'd.get("projectId","")')
for i in $(seq 60); do s2=$(api "$API/provision/$PID2/" | json 'd.get("status")'); case "$s2" in ACTIVE|FAILED) break;; esac; sleep 3; done
B=$(api -X POST "$API/projects/$PID2/apps/" -d "$(app_spec probe other)")
BID=$(echo "$B" | json 'd["id"]')
api -X POST "$API/projects/$PID2/apps/$BID/deploy" >/dev/null
[ "$(wait_deploy "$PID2" "$BID")" = succeeded ] && ok "project B's app running" || ko "project B's app: ${B:0:200} $(deploys "$PID2" "$BID" | head -c 300)"
BC=$(app_container "$BID")
AIP=$($DOCKER inspect "$AC" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
DBIP=$($DOCKER inspect "$DBHOST" --format "{{(index .NetworkSettings.Networks \"excalibase-net-$PID\").IPAddress}}")
reached=
$DOCKER exec "$BC" wget -q -T 3 -O /dev/null "http://$AC:8080/" 2>/dev/null && reached="$reached A-by-name"
$DOCKER exec "$BC" wget -q -T 3 -O /dev/null "http://$AIP:8080/" 2>/dev/null && reached="$reached A-by-address"
$DOCKER exec "$BC" nc -z -w 3 "$DBHOST" 5432 2>/dev/null && reached="$reached DB-by-name"
[ -n "$DBIP" ] && $DOCKER exec "$BC" nc -z -w 3 "$DBIP" 5432 2>/dev/null && reached="$reached DB-by-address"
[ -z "$reached" ] && ok "project B reaches neither A's app nor A's database ($AIP, $DBIP)" || ko "project B reached:$reached"

echo "== apps: pause, resume, delete"
# As Studio does: the lifecycle answers 202 at once and the page follows the app.
api -X POST -H 'Prefer: respond-async' "$API/projects/$PID/apps/$AID/pause" -o /dev/null
pcode=
for i in $(seq 60); do
  pcode=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 --resolve "$HOST:$PORT:127.0.0.1" "https://$HOST:$PORT/")
  [ "$pcode" = 503 ] && [ -z "$(app_container "$AID")" ] && break; sleep 2
done
[ "$pcode" = 503 ] && [ -z "$(app_container "$AID")" ] && ok "paused: stopped, and the host answers 503" || ko "paused: host answered $pcode, running: $(app_container "$AID")"
api -X POST -H 'Prefer: respond-async' "$API/projects/$PID/apps/$AID/resume" -o /dev/null
body=""
for i in $(seq 40); do body=$(open_app "$HOST"); [ "$body" = one ] && break; sleep 3; done
[ "$body" = one ] && ok "resumed: 'one' again" || ko "after resume: '$body'"
del=$(api -X DELETE -H 'Prefer: respond-async' "$API/projects/$PID/apps/$AID/" -d '{"confirmDeleteDisk":true}' -w ' %{http_code}')
case "$del" in *" 202") ;; *) ko "delete: ${del:0:200}" ;; esac
for i in $(seq 90); do [ -z "$($DOCKER ps -a -q --filter "label=excalibase.app=$AID")$($DOCKER volume ls -q --filter "label=excalibase.app=$AID")" ] && break; sleep 2; done
left="$($DOCKER ps -a -q --filter "label=excalibase.app=$AID") $($DOCKER volume ls -q --filter "label=excalibase.app=$AID")"
[ -z "${left// /}" ] && ok "deleted: no container or disk left" || ko "left after delete: $left"
[ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 --resolve "$HOST:$PORT:127.0.0.1" "https://$HOST:$PORT/")" != 200 ] && ok "the deleted app's host no longer serves" || ko "deleted app still served"
