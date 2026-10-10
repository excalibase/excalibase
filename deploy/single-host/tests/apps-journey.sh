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
