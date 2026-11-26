#!/bin/sh
# Studio is not for search engines: /robots.txt disallows everything and every response, the SPA
# fallback included, carries X-Robots-Tag noindex (Search Console "Soft 404").
# Runs the real nginx config in a container.
#   sh frontend/tests/noindex.test.sh
set -u
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
IMAGE=${NGINX_IMAGE:-${IMAGE_MIRROR:+$IMAGE_MIRROR/}nginx:1.29-alpine}
WORK=$(mktemp -d)
NAME="noindex-test-$$"
PORT=$((27300 + $$ % 500))
trap 'docker rm -fv "$NAME" >/dev/null 2>&1; rm -rf "$WORK"' EXIT
FAILURES=0

fail() { echo "   FAIL $1"; FAILURES=$((FAILURES + 1)); }
pass() { echo "   PASS $1"; }

mkdir "$WORK/html"
echo '<!doctype html><title>Studio</title>' > "$WORK/html/index.html"
sed 's/__DNS_RESOLVER__/127.0.0.11/' "$DIR/../nginx.conf" > "$WORK/default.conf"
chmod -R a+rX "$WORK"
docker run -d --name "$NAME" -p "127.0.0.1:$PORT:80" \
  -v "$WORK/default.conf:/etc/nginx/conf.d/default.conf:ro" -v "$WORK/html:/usr/share/nginx/html:ro" "$IMAGE" >/dev/null || { echo "could not start $IMAGE"; exit 1; }
for _ in $(seq 1 30); do curl -fs "http://127.0.0.1:$PORT/" >/dev/null 2>&1 && break; sleep 1; done

for path in /robots.txt / /some/deep/route /index.html; do
  if curl -si "http://127.0.0.1:$PORT$path" | tr -d '\r' | grep -qi '^x-robots-tag: noindex, nofollow$'; then pass "$path is noindex"; else fail "$path has no X-Robots-Tag"; fi
done
# The header belongs to the Studio page and its assets, not to the proxied API and MCP answers.
for path in /api/health /mcp; do
  if curl -si "http://127.0.0.1:$PORT$path" | tr -d '\r' | grep -qi '^x-robots-tag:'; then fail "$path carries X-Robots-Tag"; else pass "$path carries no X-Robots-Tag"; fi
done
body=$(curl -s "http://127.0.0.1:$PORT/robots.txt")
if printf '%s' "$body" | grep -q '^User-agent: \*$' && printf '%s' "$body" | grep -q '^Disallow: /$'; then pass "robots.txt disallows everything"; else fail "robots.txt: '$body'"; fi
if curl -si "http://127.0.0.1:$PORT/robots.txt" | tr -d '\r' | grep -qi '^content-type: text/plain'; then pass "robots.txt is text/plain"; else fail "robots.txt content type"; fi

[ "$FAILURES" -eq 0 ] || { echo "$FAILURES failure(s)"; exit 1; }
echo "noindex ok"
