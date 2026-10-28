#!/usr/bin/env sh
# EXC-661: deploy.sh uploads the outbound Worker with its rules and never shows
# the token or the account id. curl is faked; nothing reaches Cloudflare.
#
#   sh edge/sites-outbound/test/deploy.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$WORK/bin"
cat >"$WORK/bin/curl" <<'EOF'
#!/usr/bin/env sh
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
OK_BODY='{"success":true,"errors":[],"result":{}}'

run() {
  rm -rf "$WORK/fake" && mkdir -p "$WORK/fake"
  env PATH="$WORK/bin:$PATH" FAKE_DIR="$WORK/fake" \
    FAKE_STATUS="${FAKE_STATUS:-200}" FAKE_BODY="${FAKE_BODY:-$OK_BODY}" \
    "$@" >"$WORK/out" 2>&1
}
good_args="--api-host api.excalibase.io --platform-hosts .excalibase.io,studio.example.com,148.113.0.42"
creds="CLOUDFLARE_API_TOKEN=$TOKEN CLOUDFLARE_ACCOUNT_ID=$ACCOUNT"
leaks() { grep -q -e "$TOKEN" -e "$ACCOUNT" "$@"; }

run sh "$DIR/deploy.sh" $good_args --dry-run || { cat "$WORK/out"; fail "dry run failed"; }
[ ! -e "$WORK/fake/args" ] || fail "dry run called curl"
grep -q '"name":"API_HOST","text":"api.excalibase.io"' "$WORK/out" || fail "dry run hides the API host"

run env $creds sh "$DIR/deploy.sh" $good_args || { cat "$WORK/out"; fail "deploy failed"; }
leaks "$WORK/out" && fail "deploy printed a credential"
leaks "$WORK/fake/args" && fail "a credential is on curl's command line"
grep -qx "PUT" "$WORK/fake/args" || fail "not a PUT"
# An account Worker, not a user Worker of the namespace.
grep -qx "url = \"https://api.cloudflare.com/client/v4/accounts/$ACCOUNT/workers/scripts/excalibase-sites-outbound\"" "$WORK/fake/config" ||
  fail "wrong upload URL"
cmp -s "$WORK/fake/part-index.js" "$DIR/src/index.js" || fail "uploaded module is not src/index.js"
meta="$WORK/fake/part-metadata"
grep -q '"main_module":"index.js"' "$meta" || fail "no main module"
grep -q '"type":"plain_text","name":"API_HOST","text":"api.excalibase.io"' "$meta" || fail "no API host"
grep -q '"type":"plain_text","name":"PLATFORM_HOSTS","text":".excalibase.io,studio.example.com,148.113.0.42"' "$meta" || fail "no platform hosts"

run env $creds sh "$DIR/deploy.sh" $good_args --script outbound-staging || fail "--script refused"
grep -q "/workers/scripts/outbound-staging\"" "$WORK/fake/config" || fail "--script ignored"

FAKE_STATUS=400 FAKE_BODY="{\"success\":false,\"errors\":[{\"code\":10021,\"message\":\"bad in accounts/$ACCOUNT\"}]}" \
  run env $creds sh "$DIR/deploy.sh" $good_args && fail "a refused upload counted as success"
leaks "$WORK/out" && fail "a refusal printed a credential"
grep -q 10021 "$WORK/out" || fail "a refusal hid Cloudflare's error code"

for bad in \
  "--platform-hosts .excalibase.io" \
  "--api-host api.excalibase.io" \
  "--api-host bad_host --platform-hosts .excalibase.io" \
  "--api-host api.excalibase.io --platform-hosts a\",b" \
  "--api-host api.excalibase.io --platform-hosts ,," \
  "$good_args --script Bad/Name" \
  "$good_args --unknown"; do
  run env $creds sh "$DIR/deploy.sh" $bad && fail "accepted: $bad"
  [ ! -e "$WORK/fake/args" ] || fail "called curl for: $bad"
done

run env CLOUDFLARE_ACCOUNT_ID=$ACCOUNT sh "$DIR/deploy.sh" $good_args && fail "ran without a token"
grep -q CLOUDFLARE_API_TOKEN "$WORK/out" || fail "missing token not named"

echo "deploy ok"
