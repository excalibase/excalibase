#!/usr/bin/env sh
# Uploads the Sites router Worker (EXC-353, ADR 0043) and binds it to a
# dispatch namespace. It does not touch routes: pointing `*.<base domain>/*`
# at the router is a separate, deliberate step (see the Sites runbook).
#
#   CLOUDFLARE_API_TOKEN=... CLOUDFLARE_ACCOUNT_ID=... sh edge/sites-router/deploy.sh \
#     --namespace <dispatch namespace> --hosts-kv-id <KV namespace id> \
#     --base-domain excalibase.app --abuse-url mailto:abuse@example.com \
#     [--script excalibase-sites-router] [--outbound excalibase-sites-outbound] [--dry-run]
#
# --outbound names the outbound Worker (edge/sites-outbound, EXC-661) every
# fetch of a site's code goes through; server sites are offered only with it.
#
# The token and the account id travel only on curl's stdin and are never printed.
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
API=https://api.cloudflare.com/client/v4
# Runtime behaviour the router is published with; matches the site Workers.
COMPATIBILITY_DATE=2025-09-01

die() { echo "deploy.sh: $*" >&2; exit 1; }
# One line only: grep checks line by line, so a newline could carry JSON of its own past it.
matches() {
  case "$1" in *"
"*) return 1 ;; esac
  printf '%s' "$1" | grep -Eq "$2"
}

namespace="" kv_id="" base_domain="" abuse_url="" script=excalibase-sites-router outbound="" dry_run=0
while [ $# -gt 0 ]; do
  case "$1" in
    --namespace) namespace=${2-}; shift ;;
    --hosts-kv-id) kv_id=${2-}; shift ;;
    --base-domain) base_domain=${2-}; shift ;;
    --abuse-url) abuse_url=${2-}; shift ;;
    --script) script=${2-}; shift ;;
    --outbound) outbound=${2-}; [ -n "$outbound" ] || die "--outbound needs a Worker name"; shift ;;
    --dry-run) dry_run=1 ;;
    *) die "unknown argument: $1" ;;
  esac
  shift
done

# Every value lands inside JSON or a URL, so each is held to a shape that needs no escaping.
matches "$namespace" '^[A-Za-z0-9_-]{1,63}$' || die "--namespace must be a dispatch namespace name"
matches "$kv_id" '^[0-9a-f]{32}$' || die "--hosts-kv-id must be a 32-hex KV namespace id"
matches "$base_domain" '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$' || die "--base-domain must be a lowercase domain"
matches "$abuse_url" '^(https://|mailto:)[^"\\[:space:][:cntrl:]]+$' || die "--abuse-url must be an https: or mailto: link"
matches "$script" '^[a-z0-9][a-z0-9-]{0,62}$' || die "--script must be a lowercase Worker name"
[ -z "$outbound" ] || matches "$outbound" '^[a-z0-9][a-z0-9-]{0,62}$' || die "--outbound must be a lowercase Worker name"

# The outbound Worker gets the site's identity from the router on every call.
dispatch="{\"type\":\"dispatch_namespace\",\"name\":\"SITES\",\"namespace\":\"$namespace\"}"
outbound_var=""
if [ -n "$outbound" ]; then
  dispatch="{\"type\":\"dispatch_namespace\",\"name\":\"SITES\",\"namespace\":\"$namespace\",\"outbound\":{\"worker\":{\"service\":\"$outbound\"},\"params\":[{\"name\":\"siteId\"},{\"name\":\"projectId\"}]}}"
  outbound_var=",{\"type\":\"plain_text\",\"name\":\"OUTBOUND\",\"text\":\"$outbound\"}"
fi

metadata=$(printf '{"main_module":"index.js","compatibility_date":"%s","bindings":[%s,%s,%s,%s%s]}' \
  "$COMPATIBILITY_DATE" \
  "$dispatch" \
  "{\"type\":\"kv_namespace\",\"name\":\"SITE_HOSTS\",\"namespace_id\":\"$kv_id\"}" \
  "{\"type\":\"plain_text\",\"name\":\"BASE_DOMAIN\",\"text\":\"$base_domain\"}" \
  "{\"type\":\"plain_text\",\"name\":\"ABUSE_URL\",\"text\":\"$abuse_url\"}" \
  "$outbound_var")

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
  echo "uploaded Worker $script, bound to dispatch namespace $namespace"
  exit 0
fi
echo "deploy.sh: Cloudflare refused the upload (HTTP $status):" >&2
sed "s/$CLOUDFLARE_ACCOUNT_ID/<account>/g" "$WORK/response.json" | head -c 2000 >&2
echo >&2
exit 1
