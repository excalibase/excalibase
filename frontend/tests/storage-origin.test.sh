#!/bin/sh
# 20-storage-origin.sh puts the storage origin into Studio's connect-src (EXC-809).
#   sh frontend/tests/storage-origin.test.sh
set -u
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SCRIPT="$DIR/../docker-entrypoint.d/20-storage-origin.sh"
NGINX="$DIR/../nginx.conf"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
FAILURES=0

fail() { echo "   FAIL $1"; FAILURES=$((FAILURES + 1)); }
pass() { echo "   PASS $1"; }

# run <value> [unset]: runs the script on a fresh copy of nginx.conf; sets RC, OUT, CONF.
run() {
  CONF="$WORK/default.conf"
  cp "$NGINX" "$CONF"
  if [ "${2:-}" = unset ]; then
    OUT=$(env -u STORAGE_PUBLIC_ORIGIN NGINX_CONF="$CONF" sh "$SCRIPT" 2>&1)
  else
    OUT=$(STORAGE_PUBLIC_ORIGIN="$1" NGINX_CONF="$CONF" sh "$SCRIPT" 2>&1)
  fi
  RC=$?
}
connect_src() { sed -n "s/.*\(connect-src [^;]*\);.*/\1/p" "$CONF"; }

expect_origin() {
  run "$1"
  if [ "$RC" -eq 0 ] && [ "$(connect_src)" = "connect-src 'self' $2" ]; then pass "$1 is allowed"
  else fail "$1: rc=$RC connect-src='$(connect_src)'"; fi
}
expect_refused() {
  run "$1"
  if [ "$RC" -ne 0 ] && [ "$(connect_src)" = "connect-src 'self'" ] && printf '%s' "$OUT" | grep -q 'STORAGE_PUBLIC_ORIGIN'; then
    pass "refused: $(printf '%s' "$1" | head -c 40 | tr '\n' '~')"
  else fail "not refused: $(printf '%s' "$1" | head -c 40 | tr '\n' '~') rc=$RC connect-src='$(connect_src)'"; fi
}

expect_origin "https://files.example.com" "https://files.example.com"
expect_origin "https://files.localhost:8443" "https://files.localhost:8443"
expect_origin "http://objectstore.excalibase-platform.svc:9000" "http://objectstore.excalibase-platform.svc:9000"
expect_origin "https://acct123.r2.cloudflarestorage.com/" "https://acct123.r2.cloudflarestorage.com"

run "" unset
if [ "$RC" -eq 0 ] && [ "$(connect_src)" = "connect-src 'self'" ] && [ "$(printf '%s\n' "$OUT" | grep -c .)" -eq 1 ] && printf '%s' "$OUT" | grep -qi 'warn'; then
  pass "unset keeps 'self' and logs one warning"
else fail "unset: rc=$RC out='$OUT'"; fi
run ""
if [ "$RC" -eq 0 ] && [ "$(connect_src)" = "connect-src 'self'" ]; then pass "empty is treated as unset"; else fail "empty: rc=$RC"; fi

expect_refused "https://files.example.com/bucket"
expect_refused "https://*.example.com"
expect_refused "*"
expect_refused "https:"
expect_refused "files.example.com"
expect_refused "ftp://files.example.com"
expect_refused "https://files.example.com; script-src *"
expect_refused "https://files.example.com'"
expect_refused 'https://files.example.com"'
expect_refused "https://a.example.com https://b.example.com"
expect_refused "https://user@files.example.com"
expect_refused "https://files.example.com:99999x"
expect_refused "https://files.example.com:0"
expect_refused "https://"
expect_refused "https://files.example.com
frame-ancestors *"

# A restart runs the script again on the file it already changed.
CONF="$WORK/default.conf"; cp "$NGINX" "$CONF"
STORAGE_PUBLIC_ORIGIN=https://one.example.com NGINX_CONF="$CONF" sh "$SCRIPT" >/dev/null 2>&1
STORAGE_PUBLIC_ORIGIN=https://two.example.com NGINX_CONF="$CONF" sh "$SCRIPT" >/dev/null 2>&1
if [ "$(connect_src)" = "connect-src 'self' https://two.example.com" ]; then pass "a second start replaces the origin, never adds"
else fail "second start: '$(connect_src)'"; fi
env -u STORAGE_PUBLIC_ORIGIN NGINX_CONF="$CONF" sh "$SCRIPT" >/dev/null 2>&1
if [ "$(connect_src)" = "connect-src 'self'" ]; then pass "unsetting it on a later start takes it back out"
else fail "unset on restart: '$(connect_src)'"; fi

# A policy without connect-src is a config this script cannot serve: it stops.
CONF="$WORK/default.conf"; sed "s/connect-src 'self'; //" "$NGINX" > "$CONF"
if STORAGE_PUBLIC_ORIGIN=https://one.example.com NGINX_CONF="$CONF" sh "$SCRIPT" >/dev/null 2>&1; then fail "a config without connect-src was served"; else pass "a config without connect-src stops the start"; fi

# Everything else in the policy is untouched.
run "https://files.example.com"
if grep -q "script-src 'self'; style-src" "$CONF" && grep -q "frame-ancestors 'none'" "$CONF" && grep -q "object-src 'none'" "$CONF"; then pass "the rest of the policy is unchanged"
else fail "policy changed elsewhere"; fi

[ "$FAILURES" -eq 0 ] || { echo "$FAILURES failure(s)"; exit 1; }
echo "storage-origin ok"
