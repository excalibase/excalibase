#!/usr/bin/env sh
# EXC-353: deploy.sh uploads the router with its bindings and never shows the
# token or the account id. curl is faked; nothing reaches Cloudflare.
#
#   sh edge/sites-router/test/deploy.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$WORK/bin"
cat >"$WORK/bin/curl" <<'EOF'
#!/usr/bin/env sh
# Records what deploy.sh sent and answers $FAKE_STATUS with $FAKE_BODY.
printf '%s\n' "$@" >"$FAKE_DIR/args"
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift ;;
    -K) [ "$2" = "-" ] && cat >"$FAKE_DIR/config"; shift ;;
    -F) part=${2%%=*}; file=${2#*=@}; file=${file%%;*}; cp "$file" "$FAKE_DIR/part-$part"; shift ;;
  esac
  shift
done
printf '%s' "$FAKE_BODY" >"$out"
printf '%s' "$FAKE_STATUS"
EOF
chmod +x "$WORK/bin/curl"

TOKEN=tok_SECRET_value_123
ACCOUNT=0123456789abcdef0123456789abcdef
KV=fedcba9876543210fedcba9876543210

OK_BODY='{"success":true,"errors":[],"result":{}}'

# run calls deploy.sh with a fresh fake; output goes to $WORK/out.
run() {
  rm -rf "$WORK/fake" && mkdir -p "$WORK/fake"
  env PATH="$WORK/bin:$PATH" FAKE_DIR="$WORK/fake" \
    FAKE_STATUS="${FAKE_STATUS:-200}" FAKE_BODY="${FAKE_BODY:-$OK_BODY}" \
    "$@" >"$WORK/out" 2>&1
}
good_args="--namespace sites-prod --hosts-kv-id $KV --base-domain excalibase.app --abuse-url mailto:abuse@example.com"
creds="CLOUDFLARE_API_TOKEN=$TOKEN CLOUDFLARE_ACCOUNT_ID=$ACCOUNT"
leaks() { grep -q -e "$TOKEN" -e "$ACCOUNT" "$@"; }

# Dry run: no credentials needed, nothing sent, the bindings shown.
run sh "$DIR/deploy.sh" $good_args --dry-run || { cat "$WORK/out"; fail "dry run without credentials failed"; }
[ ! -e "$WORK/fake/args" ] || fail "dry run called curl"
grep -q '"type":"dispatch_namespace","name":"SITES","namespace":"sites-prod"' "$WORK/out" || fail "dry run hides the namespace binding"
grep -q "\"type\":\"kv_namespace\",\"name\":\"SITE_HOSTS\",\"namespace_id\":\"$KV\"" "$WORK/out" || fail "dry run hides the KV binding"

# Dry run with credentials set still prints neither.
run env $creds sh "$DIR/deploy.sh" $good_args --dry-run || fail "dry run with credentials failed"
leaks "$WORK/out" && fail "dry run printed a credential"
[ ! -e "$WORK/fake/args" ] || fail "dry run with credentials called curl"

# Real run: one PUT of the module and its metadata; secrets only on curl's stdin.
run env $creds sh "$DIR/deploy.sh" $good_args || { cat "$WORK/out"; fail "deploy failed"; }
leaks "$WORK/out" && fail "deploy printed a credential"
leaks "$WORK/fake/args" && fail "a credential is on curl's command line"
grep -qx -- "-X" "$WORK/fake/args" && grep -qx "PUT" "$WORK/fake/args" || fail "not a PUT"
grep -qx "url = \"https://api.cloudflare.com/client/v4/accounts/$ACCOUNT/workers/scripts/excalibase-sites-router\"" "$WORK/fake/config" ||
  fail "wrong upload URL"
grep -qx "header = \"Authorization: Bearer $TOKEN\"" "$WORK/fake/config" || fail "no bearer header"
cmp -s "$WORK/fake/part-index.js" "$DIR/src/index.js" || fail "uploaded module is not src/index.js"
meta="$WORK/fake/part-metadata"
grep -q '"main_module":"index.js"' "$meta" || fail "metadata names no main module"
grep -q '"compatibility_date":"[0-9-]*"' "$meta" || fail "metadata has no compatibility date"
grep -q '"type":"dispatch_namespace","name":"SITES","namespace":"sites-prod"' "$meta" || fail "no dispatch binding"
grep -q "\"type\":\"kv_namespace\",\"name\":\"SITE_HOSTS\",\"namespace_id\":\"$KV\"" "$meta" || fail "no KV binding"
grep -q '"type":"plain_text","name":"BASE_DOMAIN","text":"excalibase.app"' "$meta" || fail "no base domain var"
grep -q '"type":"plain_text","name":"ABUSE_URL","text":"mailto:abuse@example.com"' "$meta" || fail "no abuse var"

# Without --outbound, no outbound Worker and no OUTBOUND var: server sites stay off.
grep -q '"outbound"' "$meta" && fail "an outbound binding without --outbound"
grep -q '"name":"OUTBOUND"' "$meta" && fail "an OUTBOUND var without --outbound"

# EXC-661: --outbound sends every fetch of site code through that Worker, with
# the site's identity as its parameters (binding shape unverified on Cloudflare: R5).
run env $creds sh "$DIR/deploy.sh" $good_args --outbound excalibase-sites-outbound || { cat "$WORK/out"; fail "--outbound refused"; }
meta="$WORK/fake/part-metadata"
grep -q '"type":"dispatch_namespace","name":"SITES","namespace":"sites-prod","outbound":{"worker":{"service":"excalibase-sites-outbound"},"params":\[{"name":"siteId"},{"name":"projectId"}\]}}' "$meta" ||
  fail "no outbound Worker on the dispatch binding: $(cat "$meta")"
grep -q '"type":"plain_text","name":"OUTBOUND","text":"excalibase-sites-outbound"' "$meta" || fail "no OUTBOUND var"

# Another script name is honoured.
run env $creds sh "$DIR/deploy.sh" $good_args --script sites-router-staging || fail "--script refused"
grep -q "/workers/scripts/sites-router-staging\"" "$WORK/fake/config" || fail "--script ignored"

# Cloudflare refusing: non-zero, and the account id it echoes is not shown.
FAKE_STATUS=400 FAKE_BODY="{\"success\":false,\"errors\":[{\"code\":10021,\"message\":\"bad binding in accounts/$ACCOUNT\"}]}" \
  run env $creds sh "$DIR/deploy.sh" $good_args && fail "a refused upload counted as success"
leaks "$WORK/out" && fail "a refusal printed a credential"
grep -q 10021 "$WORK/out" || fail "a refusal hid Cloudflare's error code"
FAKE_STATUS=200 FAKE_BODY='{"success":false,"errors":[]}' \
  run env $creds sh "$DIR/deploy.sh" $good_args && fail "success:false counted as success"

# Refused input: nothing is sent.
for bad in \
  "--hosts-kv-id $KV --base-domain excalibase.app --abuse-url mailto:a@b.c" \
  "--namespace a/b --hosts-kv-id $KV --base-domain excalibase.app --abuse-url mailto:a@b.c" \
  "--namespace sites --hosts-kv-id nothex --base-domain excalibase.app --abuse-url mailto:a@b.c" \
  "--namespace sites --hosts-kv-id $KV --base-domain bad_domain --abuse-url mailto:a@b.c" \
  "--namespace sites --hosts-kv-id $KV --base-domain excalibase.app --abuse-url javascript:alert(1)" \
  "--namespace sites --hosts-kv-id $KV --base-domain excalibase.app --abuse-url https://x.com/\"" \
  "$good_args --script Bad/Name" \
  "$good_args --outbound Bad/Name" \
  "$good_args --outbound" \
  "$good_args --unknown"; do
  run env $creds sh "$DIR/deploy.sh" $bad && fail "accepted: $bad"
  [ ! -e "$WORK/fake/args" ] || fail "called curl for: $bad"
done

# Missing credentials: named, not shown.
run env CLOUDFLARE_ACCOUNT_ID=$ACCOUNT sh "$DIR/deploy.sh" $good_args && fail "ran without a token"
grep -q CLOUDFLARE_API_TOKEN "$WORK/out" || fail "missing token not named"
run env CLOUDFLARE_API_TOKEN=$TOKEN sh "$DIR/deploy.sh" $good_args && fail "ran without an account"
grep -q CLOUDFLARE_ACCOUNT_ID "$WORK/out" || fail "missing account not named"
leaks "$WORK/out" && fail "a missing-credential message printed one"

echo "deploy ok"
