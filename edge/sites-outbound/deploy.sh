#!/usr/bin/env sh
# Uploads the Sites outbound Worker (EXC-661, ADR 0045 decision 5): every
# fetch() of a server site's code passes through it. The router binds it on
# the dispatch namespace (edge/sites-router/deploy.sh --outbound <script>).
#
#   CLOUDFLARE_API_TOKEN=... CLOUDFLARE_ACCOUNT_ID=... sh edge/sites-outbound/deploy.sh \
#     --api-host api.excalibase.io --platform-hosts .excalibase.io,studio.example.com \
#     [--script excalibase-sites-outbound] [--dry-run]
#
# --platform-hosts lists the platform's own hosts and addresses site code may
# not call; ".example.io" also covers its subdomains, and an origin's IPv6
# address goes in as written (lowercase). The token and the
# account id travel only on curl's stdin and are never printed.
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
API=https://api.cloudflare.com/client/v4
COMPATIBILITY_DATE=2025-09-01
HOST='([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}'
IPV4='[0-9]{1,3}(\.[0-9]{1,3}){3}'
IPV6='[0-9a-f:]*:[0-9a-f:]+'

die() { echo "deploy.sh: $*" >&2; exit 1; }
# One line only: grep checks line by line, so a newline could carry JSON of its own past it.
matches() {
  case "$1" in *"
"*) return 1 ;; esac
  printf '%s' "$1" | grep -Eq "$2"
}

api_host="" platform_hosts="" script=excalibase-sites-outbound dry_run=0
while [ $# -gt 0 ]; do
  case "$1" in
    --api-host) api_host=${2-}; shift ;;
    --platform-hosts) platform_hosts=${2-}; shift ;;
    --script) script=${2-}; shift ;;
    --dry-run) dry_run=1 ;;
    *) die "unknown argument: $1" ;;
  esac
  shift
done

# Every value lands inside JSON or a URL, so each is held to a shape that needs no escaping.
matches "$api_host" "^$HOST\$" || die "--api-host must be a lowercase hostname"
matches "$platform_hosts" "^(\\.?$HOST|$IPV4|$IPV6)(,(\\.?$HOST|$IPV4|$IPV6))*\$" ||
  die "--platform-hosts must be a comma list of hostnames, .domains, IPv4 or IPv6 addresses"
matches "$script" '^[a-z0-9][a-z0-9-]{0,62}$' || die "--script must be a lowercase Worker name"

# Fetches to hostnames on our own zones go out through the public edge, never straight to an origin.
metadata=$(printf '{"main_module":"index.js","compatibility_date":"%s","compatibility_flags":["global_fetch_strictly_public"],"bindings":[%s,%s]}' \
  "$COMPATIBILITY_DATE" \
  "{\"type\":\"plain_text\",\"name\":\"API_HOST\",\"text\":\"$api_host\"}" \
  "{\"type\":\"plain_text\",\"name\":\"PLATFORM_HOSTS\",\"text\":\"$platform_hosts\"}")

if [ "$dry_run" = 1 ]; then
  echo "dry run: would PUT $API/accounts/<CLOUDFLARE_ACCOUNT_ID>/workers/scripts/$script"
  echo "module: $DIR/src/index.js"
  echo "metadata: $metadata"
  exit 0
fi

[ -n "${CLOUDFLARE_API_TOKEN:-}" ] || die "CLOUDFLARE_API_TOKEN is not set"
[ -n "${CLOUDFLARE_ACCOUNT_ID:-}" ] || die "CLOUDFLARE_ACCOUNT_ID is not set"
matches "$CLOUDFLARE_API_TOKEN" '^[A-Za-z0-9_-]+$' || die "CLOUDFLARE_API_TOKEN has an unexpected shape"
matches "$CLOUDFLARE_ACCOUNT_ID" '^[0-9a-f]{32}$' || die "CLOUDFLARE_ACCOUNT_ID must be 32 hex characters"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
printf '%s' "$metadata" >"$WORK/metadata.json"

status=$(printf 'url = "%s"\nheader = "Authorization: Bearer %s"\n' \
  "$API/accounts/$CLOUDFLARE_ACCOUNT_ID/workers/scripts/$script" "$CLOUDFLARE_API_TOKEN" |
  curl -sS -X PUT -o "$WORK/response.json" -w '%{http_code}' -K - \
    -F "metadata=@$WORK/metadata.json;type=application/json" \
    -F "index.js=@$DIR/src/index.js;type=application/javascript+module;filename=index.js") ||
  die "could not reach the Cloudflare API"

if [ "$status" = 200 ] && grep -q '"success": *true' "$WORK/response.json"; then
  echo "uploaded outbound Worker $script"
  exit 0
fi
echo "deploy.sh: Cloudflare refused the upload (HTTP $status):" >&2
sed "s/$CLOUDFLARE_ACCOUNT_ID/<account>/g" "$WORK/response.json" | head -c 2000 >&2
echo >&2
exit 1
