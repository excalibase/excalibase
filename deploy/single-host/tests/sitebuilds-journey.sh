# Sourced by boot-smoke.sh with SITE_BUILDS=1 (EXC-671): a developer connects a
# GitHub repository to a site and pushes; the single host builds it under the
# sandbox runtime, behind the build egress proxy, and deploys it. GitHub and
# Cloudflare are stand-ins (sitebuild_fakes.py); the build, the engine proxy,
# the sandbox and the deploy path are the real ones. The fixture's own probe
# script records what the build could reach.
# Uses: c, ok, ko, T, PID, PORT, DOCKER, TESTS, W, SITE_FAKES, SITE_WEBHOOK_SECRET.

echo "== git builds of a site on a single host"
S=https://studio.localhost:$PORT/api/projects/$PID/sites
auth=(-H "Authorization: Bearer $T")
json() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

SITE=$(c "${auth[@]}" -H 'Content-Type: application/json' -X POST "$S/" -d '{"name":"smoke-site"}' | json 'd.get("id","")')
[ -n "$SITE" ] && ok "site created" || ko "site create"

# Sign in to GitHub as Studio does: the state comes back on the callback with the browser's cookie.
JAR=$W/git-cookies
URL=$(c -c "$JAR" "${auth[@]}" -X POST "$S/$SITE/git/sign-in" | json 'd.get("url","")')
STATE=$(python3 -c 'import sys,urllib.parse as u;print(u.parse_qs(u.urlparse(sys.argv[1]).query).get("state",[""])[0])' "$URL")
cb=$(c -b "$JAR" -o /dev/null -w '%{http_code} %{redirect_url}' "https://studio.localhost:$PORT/api/sites/github/callback?code=smoke-code&state=$STATE")
case "$cb" in "302 "*"git=signed-in"*) ok "GitHub sign-in came back to Studio" ;; *) ko "sign-in callback: $cb" ;; esac
REPOS=$(c "${auth[@]}" "$S/$SITE/git/repositories")
echo "$REPOS" | grep -q '"acme/site"' && ok "the signed-in user's repository is offered" || ko "repositories: ${REPOS:0:300}"

# A service on every host address, which the build must not reach directly.
HOST_PORT=$((PORT + 101))
python3 -m http.server "$HOST_PORT" --bind 0.0.0.0 >/dev/null 2>&1 &
HOST_LISTENER=$!
HOST_GATEWAY=$($DOCKER network inspect excalibase-net-build-egress --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}')
SETTINGS=$(printf '{"repositoryId":777,"repository":"acme/site","settings":{"branch":"main","outputDir":"dist","nodeVersion":"22","buildCommand":"bash probe.sh && npm run build","env":{"GREETING":"built on a single host","HOST_PORT":"%s","HOST_GATEWAY":"%s"}}}' "$HOST_PORT" "$HOST_GATEWAY")
con=$(c "${auth[@]}" -H 'Content-Type: application/json' -X PUT "$S/$SITE/git" -d "$SETTINGS" -w ' %{http_code}')
case "$con" in *" 200") ok "repository connected" ;; *) ko "connect: ${con:0:300}" ;; esac

# A push, signed as GitHub signs its deliveries.
SHA=$($DOCKER exec "$SITE_FAKES" wget -qO- http://127.0.0.1:8080/_sha | json 'd["sha"]')
PUSH=$(printf '{"ref":"refs/heads/main","after":"%s","deleted":false,"installation":{"id":4242},"repository":{"id":777,"full_name":"acme/site"},"head_commit":{"message":"smoke site"}}' "$SHA")
SIG=$(printf '%s' "$PUSH" | openssl dgst -sha256 -hmac "$SITE_WEBHOOK_SECRET" | sed 's/^.* //')
hook=$(c -o /dev/null -w '%{http_code}' -X POST "https://studio.localhost:$PORT/api/sites/github/webhook" -H 'Content-Type: application/json' \
  -H 'X-GitHub-Event: push' -H "X-GitHub-Delivery: smoke-$(date +%s)" -H "X-Hub-Signature-256: sha256=$SIG" --data-binary "$PUSH")
[ "$hook" = 202 ] && ok "signed push accepted" || ko "webhook answered $hook"

# Seen while it runs: the steps under the sandbox runtime, on an internal network.
st=; BUILD=
for i in $(seq 200); do
  B=$(c "${auth[@]}" "$S/$SITE/builds")
  BUILD=$(echo "$B" | json '(d if isinstance(d,list) else d.get("builds",[]))[0]["id"]')
  st=$(echo "$B" | json '(d if isinstance(d,list) else d.get("builds",[]))[0]["status"]')
  if [ -z "${seen_runtime:-}" ] && [ -n "$BUILD" ]; then
    rt=$($DOCKER inspect "excalibase-build-$BUILD-build" --format '{{.HostConfig.Runtime}} {{.HostConfig.NetworkMode}} {{.HostConfig.ReadonlyRootfs}}' 2>/dev/null)
    [ -n "$rt" ] && seen_runtime=$rt
  fi
  case "$st" in SUCCEEDED|FAILED|CANCELLED) break ;; esac
  sleep 3
done
[ "$st" = SUCCEEDED ] && ok "the build succeeded" || ko "build $BUILD: $st $(c "${auth[@]}" "$S/$SITE/builds/$BUILD" | head -c 3000)"
want_rt=${APP_SANDBOX:-runsc}
if [ "${seen_runtime#* }" = "excalibase-net-build-$BUILD true" ] && { [ "$want_rt" = none ] || [ "${seen_runtime%% *}" = "$want_rt" ]; }; then
  ok "the build step ran under ${seen_runtime%% *} (sandbox $want_rt), on its own network, read-only root"
else
  ko "build step: '${seen_runtime:-never seen}'"
fi

SEEN=$($DOCKER exec "$SITE_FAKES" wget -qO- http://127.0.0.1:8080/_seen)
echo "$SEEN" | json 'd["files"].get("/index.html","")' | grep -q 'built on a single host' && ok "the built index.html reached the deploy" || ko "deployed files: ${SEEN:0:600}"
PROBES=$(echo "$SEEN" | json 'd["files"].get("/probes.txt","")')
for want in direct-internet=closed direct-metadata=closed direct-host=closed direct-host-gateway=closed proxy-metadata=403 proxy-private=403 proxy-internet=200; do
  case "$PROBES" in *"$want"*) ok "build probe $want" ;; *) ko "build probe $want missing: $PROBES" ;; esac
done
DEPLOYED=$(c "${auth[@]}" "$S/$SITE/builds/$BUILD" | json 'd.get("deployId","")')
LIVE=$(c "${auth[@]}" "$S/$SITE" | json 'd.get("liveDeployId","")')
[ -n "$DEPLOYED" ] && [ "$DEPLOYED" = "$LIVE" ] && ok "the build's deploy is the live one" || ko "deploy '$DEPLOYED', live '$LIVE'"
left=$($DOCKER ps -aq --filter "label=excalibase.build=$BUILD" | wc -l)
[ "$left" = 0 ] && ok "the build's containers are gone" || ko "$left build containers left"
$DOCKER volume ls -q | grep -q "build-$BUILD" && ko "the build's work volume is left" || ok "the build's work volume is gone"
LOGB=$(c "${auth[@]}" "$S/$SITE/builds/$BUILD")
echo "$LOGB" | grep -q 'ghs_smoke_clone' && ko "the clone token reached the build log" || ok "the clone token is not in the build log"
kill "$HOST_LISTENER" 2>/dev/null
