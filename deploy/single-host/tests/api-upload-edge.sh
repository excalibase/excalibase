#!/usr/bin/env sh
# /api upload size (EXC-621): the real Caddyfile in the compose file's Caddy
# image in front of the real Studio nginx.conf, with a stand-in for
# provisioning. Like Kubernetes, /api takes 10 MiB, enforced at the edge; Studio's
# nginx must not be stricter, and the Studio allowlist answers before any body.
#
#   sh deploy/single-host/tests/api-upload-edge.sh [docker|podman]
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ENGINE=${1:-docker}
CADDY_IMAGE=$(sed -n 's/^ *image: \(docker.io\/library\/caddy:.*\)$/\1/p' "$DIR/../compose.yaml")
[ -n "$CADDY_IMAGE" ] || { echo "no Caddy image in compose.yaml" >&2; exit 1; }
NGINX_IMAGE=$(sed -n 's/^FROM \(nginx:.*\)$/\1/p' "$DIR/../../../frontend/Dockerfile")
[ -n "$NGINX_IMAGE" ] || { echo "no nginx image in frontend/Dockerfile" >&2; exit 1; }
NET=api-upload-edge-test-$$
FAILURES=0

cleanup() {
  $ENGINE rm -f "$NET-edge" "$NET-fenced" "$NET-provisioning" "$NET-studio" >/dev/null 2>&1 || true
  $ENGINE network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

$ENGINE network create "$NET" >/dev/null
# Provisioning reads the whole body, as the real one does, and answers 200.
STUB='import http.server
class Stub(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        self.send_response(200); self.end_headers(); self.wfile.write(b"provisioning")
http.server.ThreadingHTTPServer(("", 24006), Stub).serve_forever()'
$ENGINE run -d --name "$NET-provisioning" --network "$NET" --network-alias provisioning \
  docker.io/library/python:3.13-alpine python -c "$STUB" >/dev/null
# The real nginx.conf and resolver script, as the Studio image runs them.
# (copied in, not mounted: the entrypoint script edits the file in place)
$ENGINE create --name "$NET-studio" --network "$NET" --network-alias studio "$NGINX_IMAGE" >/dev/null
$ENGINE cp "$DIR/../../../frontend/nginx.conf" "$NET-studio:/etc/nginx/conf.d/default.conf"
# The Dockerfile marks the resolver script executable; a checkout may not, so do the same on a copy.
STAGE=$(mktemp -d)
cp "$DIR/../../../frontend/docker-entrypoint.d/10-resolver.sh" "$STAGE/" && chmod +x "$STAGE/10-resolver.sh"
$ENGINE cp "$STAGE/10-resolver.sh" "$NET-studio:/docker-entrypoint.d/10-resolver.sh"
rm -rf "$STAGE"
$ENGINE start "$NET-studio" >/dev/null
$ENGINE run -d --name "$NET-edge" --network "$NET" -p 127.0.0.1::443 \
  -e EXCALIBASE_DOMAIN=example.test -e EXCALIBASE_TLS=internal -e STUDIO_ALLOW_CIDRS="0.0.0.0/0 ::/0" \
  -v "$DIR/../edge/Caddyfile:/etc/caddy/Caddyfile:ro" "$CADDY_IMAGE" >/dev/null
# The same edge with a Studio allowlist this client is not on.
$ENGINE run -d --name "$NET-fenced" --network "$NET" -p 127.0.0.1::443 \
  -e EXCALIBASE_DOMAIN=example.test -e EXCALIBASE_TLS=internal -e STUDIO_ALLOW_CIDRS=203.0.113.0/24 \
  -v "$DIR/../edge/Caddyfile:/etc/caddy/Caddyfile:ro" "$CADDY_IMAGE" >/dev/null
PORT=$($ENGINE port "$NET-edge" 443/tcp | head -1 | sed 's/.*://')
FENCED_PORT=$($ENGINE port "$NET-fenced" 443/tcp | head -1 | sed 's/.*://')
sleep 4

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

UPLOAD=/api/projects/p1/apps/a1/deploy
expect "a 2 MiB upload reaches provisioning" "provisioning 200" $UPLOAD 2
expect "a 9 MiB upload reaches provisioning" "provisioning 200" $UPLOAD 9
expect "an 11 MiB upload is refused" " 413" $UPLOAD 11

PORT=$FENCED_PORT
expect "an upload from outside the Studio allowlist is refused" " 403" $UPLOAD 2

[ "$FAILURES" -eq 0 ] && echo "all single-host /api upload cases passed" || { echo "$FAILURES failure(s)"; exit 1; }
