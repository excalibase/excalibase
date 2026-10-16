#!/usr/bin/env sh
# Sites (EXC-353) on the single-host edge: the real Caddyfile in the compose
# file's Caddy image, with stand-ins for Studio and provisioning. Only a site
# deploy upload passes the 10 MiB cap (up to 150 MiB) and goes straight to
# provisioning's edge listener; Studio's nginx in front of the rest of /api
# keeps its own limit.
#
#   sh deploy/single-host/tests/sites-edge.sh [docker|podman]
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ENGINE=${1:-docker}
CADDY_IMAGE=$(sed -n 's/^ *image: \(docker.io\/library\/caddy:.*\)$/\1/p' "$DIR/../compose.yaml")
[ -n "$CADDY_IMAGE" ] || { echo "no Caddy image in compose.yaml" >&2; exit 1; }
NET=sites-edge-test-$$
FAILURES=0

cleanup() {
  $ENGINE rm -f "$NET-edge" "$NET-fenced" "$NET-provisioning" "$NET-studio" >/dev/null 2>&1 || true
  $ENGINE network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

$ENGINE network create "$NET" >/dev/null
# Each stand-in reads the whole body, as nginx and provisioning do (the edge's
# cap only bites on a body that is read), and answers with its own name.
STUB='import http.server, sys
class Stub(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        self.send_response(200); self.end_headers(); self.wfile.write(sys.argv[2].encode())
http.server.ThreadingHTTPServer(("", int(sys.argv[1])), Stub).serve_forever()'
$ENGINE run -d --name "$NET-provisioning" --network "$NET" --network-alias provisioning \
  docker.io/library/python:3.13-alpine python -c "$STUB" 24006 provisioning >/dev/null
$ENGINE run -d --name "$NET-studio" --network "$NET" --network-alias studio \
  docker.io/library/python:3.13-alpine python -c "$STUB" 80 studio >/dev/null
$ENGINE run -d --name "$NET-edge" --network "$NET" -p 127.0.0.1::443 \
  -e EXCALIBASE_DOMAIN=example.test -e EXCALIBASE_TLS=internal -e STUDIO_ALLOW_CIDRS="0.0.0.0/0 ::/0" \
  -v "$DIR/../edge/Caddyfile:/etc/caddy/Caddyfile:ro" "$CADDY_IMAGE" >/dev/null
# The same edge with a Studio allowlist this client is not on.
$ENGINE run -d --name "$NET-fenced" --network "$NET" -p 127.0.0.1::443 \
  -e EXCALIBASE_DOMAIN=example.test -e EXCALIBASE_TLS=internal -e STUDIO_ALLOW_CIDRS=203.0.113.0/24 \
  -v "$DIR/../edge/Caddyfile:/etc/caddy/Caddyfile:ro" "$CADDY_IMAGE" >/dev/null
EDGE_PORT=$($ENGINE port "$NET-edge" 443/tcp | head -1 | sed 's/.*://')
FENCED_PORT=$($ENGINE port "$NET-fenced" 443/tcp | head -1 | sed 's/.*://')
PORT=$EDGE_PORT
sleep 3

# answer <path> <MiB>: the status and body the edge gives a POST of that size.
answer() {
  head -c $(($2 * 1024 * 1024)) /dev/zero | curl -sk --max-time 120 -o - -w ' %{http_code}' \
    --resolve "studio.example.test:$PORT:127.0.0.1" -X POST --data-binary @- \
    -H 'Content-Type: application/octet-stream' "https://studio.example.test:$PORT$1" | tr -d '\n'
}
expect() {
  label=$1 want=$2; shift 2
  got=$(answer "$@")
  case "$got" in
    *"$want") echo "   PASS $label: $got" ;;
    *) echo "   FAIL $label: got '$got', want '$want'"; FAILURES=$((FAILURES + 1)) ;;
  esac
}

DEPLOY=/api/projects/p1/sites/s1/deploy
expect "a 20 MiB site deploy reaches provisioning directly" "provisioning 200" $DEPLOY 20
expect "a 140 MiB site deploy (provisioning's ceiling) passes" "provisioning 200" $DEPLOY 140
expect "a site deploy past 150 MiB is refused" " 413" $DEPLOY 151
expect "a small site deploy goes to provisioning too" "provisioning 200" $DEPLOY 1
expect "the rest of /api still goes through Studio" "studio 200" /api/projects/p1/sites 1
expect "an app deploy keeps the 10 MiB cap" " 413" /api/projects/p1/apps/a1/deploy 20
expect "a deploy listing keeps the 10 MiB cap" " 413" /api/projects/p1/sites/s1/deploys 20

PORT=$FENCED_PORT
expect "a site deploy from outside the Studio allowlist is refused" " 403" $DEPLOY 1
expect "Studio from outside the allowlist is refused" " 403" /api/projects/p1/sites 1

[ "$FAILURES" -eq 0 ] && echo "all single-host sites edge cases passed" || { echo "$FAILURES failure(s)"; exit 1; }
