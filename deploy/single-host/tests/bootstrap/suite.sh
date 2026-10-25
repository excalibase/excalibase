#!/bin/sh
# In-container half of the single-host bootstrap test (see ../bootstrap.sh).
# Runs /bootstrap.sh against the stubbed curl and asserts which tokens it keeps,
# revokes and re-mints, above all on an install whose stored token predates a
# permission this version adds (EXC-662).
set -u

apk add --no-cache jq openssl >/dev/null 2>&1 || { echo "could not install jq/openssl"; exit 1; }

STUB=/stub
mkdir -p "$STUB"
cp /suite/curl "$STUB/curl"
printf '#!/bin/sh\nexit 0\n' > "$STUB/apk"
chmod +x "$STUB/curl" "$STUB/apk"
PATH="$STUB:$PATH"
export PATH

# The lists the script under test asks for, read from it so they never drift.
WANT_AUTH=$(sed -n "s/^AUTH_PERMISSIONS='\(.*\)'$/\1/p" /bootstrap.sh)
WANT_GRAPHQL=$(sed -n "s/^GRAPHQL_PERMISSIONS='\(.*\)'$/\1/p" /bootstrap.sh)
OLD_AUTH='["vault:read:pki/signing/*","vault:read:projects/*/credentials/auth_admin","projects:info:read","email:send"]'
PROVIDERS_GRANT='vault:read:projects/*/auth/providers/*'
export WANT_AUTH WANT_GRAPHQL

CALL_LOG=/tmp/calls.log
OUTPUT=/tmp/case.out
SECRETS_DIR=/tmp/secrets
PROVISIONING_URL=http://provisioning.test:24005
ADMIN_EMAIL=admin@example.test
SETUP_TOKEN=setup-token
export CALL_LOG SECRETS_DIR PROVISIONING_URL ADMIN_EMAIL SETUP_TOKEN
PASSED=0
FAILED=0
CASE=

start() {
  CASE=$1
  unset ME_FAIL VAULT_STATUS REGISTERED LOGIN_FAIL PRINCIPALS_EXIST STORED_AUTH_PERMISSIONS \
    STORED_GRAPHQL_PERMISSIONS LISTING_CODE REVOKE_FAIL ADMIN_PASSWORD
  SIGNING_PRIVATE='{"key":"stored-private-key"}'
  export SIGNING_PRIVATE
  rm -rf "$SECRETS_DIR" /tmp/stub-principal-*
  mkdir -p "$SECRETS_DIR"
}

# installed — an install whose two tokens authenticate; "recorded" also files
# the permission lists this version asks for beside them.
installed() {
  printf 'excb_svc-auth' > "$SECRETS_DIR/auth-token"
  printf 'excb_svc-graphql' > "$SECRETS_DIR/graphql-token"
  if [ "${1:-}" = recorded ]; then
    printf '%s' "$WANT_AUTH" > "$SECRETS_DIR/auth-token.permissions"
    printf '%s' "$WANT_GRAPHQL" > "$SECRETS_DIR/graphql-token.permissions"
  fi
}

run() {
  : > "$CALL_LOG"
  sh /bootstrap.sh > "$OUTPUT" 2>&1
  RC=$?
}

ok() { PASSED=$((PASSED + 1)); echo "  ok   $CASE: $1"; }
bad() {
  FAILED=$((FAILED + 1))
  echo "  FAIL $CASE: $1"
  sed 's/^/       | /' "$OUTPUT"
}
expect_rc() { if [ "$RC" = "$1" ]; then ok "exit $1"; else bad "exit $RC, want $1"; fi; }
called() { grep -q -- "$1" "$CALL_LOG"; }
expect_called() { if called "$1"; then ok "called $1"; else bad "never called $1"; fi; }
expect_not_called() { if called "$1"; then bad "called $1"; else ok "did not call $1"; fi; }
expect_file() {
  if [ "$(cat "$1" 2>/dev/null)" = "$2" ]; then ok "$(basename "$1") = $2"; else bad "$(basename "$1") = $(cat "$1" 2>/dev/null), want $2"; fi
}
expect_recorded() {
  if jq -e --argjson want "$2" 'sort == ($want | sort)' "$SECRETS_DIR/$1.permissions" >/dev/null 2>&1; then
    ok "$1 permissions recorded"
  else
    bad "$1 permissions recorded as $(cat "$SECRETS_DIR/$1.permissions" 2>/dev/null)"
  fi
}

case "$WANT_AUTH" in
  *"$PROVIDERS_GRANT"*) echo "  ok   svc-auth asks for the OAuth provider secrets" ;;
  *) echo "  FAIL svc-auth does not ask for $PROVIDERS_GRANT"; FAILED=$((FAILED + 1)) ;;
esac

start "fresh install"
PRINCIPALS_EXIST= REGISTERED=1
export PRINCIPALS_EXIST REGISTERED
run
expect_rc 0
expect_file "$SECRETS_DIR/auth-token" excb_svc-auth-new
expect_file "$SECRETS_DIR/graphql-token" excb_svc-graphql-new
expect_recorded auth-token "$WANT_AUTH"
expect_recorded graphql-token "$WANT_GRAPHQL"
expect_not_called "DELETE"
if grep -F '/api/auth/tokens {"name":"svc-auth"' "$CALL_LOG" | grep -qF "$PROVIDERS_GRANT"; then
  ok "svc-auth minted with the provider grant"
else bad "svc-auth minted without $PROVIDERS_GRANT"; fi

start "restart with current tokens"
installed recorded
run
expect_rc 0
expect_not_called "/api/auth/login"
expect_not_called "/api/auth/register"
expect_file "$SECRETS_DIR/auth-token" excb_svc-auth

start "upgrade without the admin password keeps the old tokens"
installed
run
expect_rc 0
expect_not_called "/api/auth/login"
expect_not_called "/api/auth/register"
expect_not_called "/api/auth/tokens"
expect_file "$SECRETS_DIR/auth-token" excb_svc-auth
if grep -q "WARN: the service tokens" "$OUTPUT"; then ok "warns"; else bad "no warning"; fi

start "upgrade with the admin password re-mints the drifted token only"
installed
ADMIN_PASSWORD=admin-password STORED_AUTH_PERMISSIONS=$OLD_AUTH
export ADMIN_PASSWORD STORED_AUTH_PERMISSIONS
run
expect_rc 0
expect_called "DELETE http://provisioning.test:24005/api/auth/tokens/hash-svc-auth"
expect_not_called "tokens/hash-svc-graphql"
expect_file "$SECRETS_DIR/auth-token" excb_svc-auth-new
expect_file "$SECRETS_DIR/graphql-token" excb_svc-graphql
expect_recorded auth-token "$WANT_AUTH"
expect_recorded graphql-token "$WANT_GRAPHQL"

start "upgrade with the admin password and current tokens records them"
installed
ADMIN_PASSWORD=admin-password
export ADMIN_PASSWORD
run
expect_rc 0
expect_not_called "DELETE"
expect_file "$SECRETS_DIR/auth-token" excb_svc-auth
expect_recorded auth-token "$WANT_AUTH"

start "a recorded list that differs re-mints"
installed recorded
printf '%s' "$OLD_AUTH" > "$SECRETS_DIR/auth-token.permissions"
ADMIN_PASSWORD=admin-password STORED_AUTH_PERMISSIONS=$OLD_AUTH
export ADMIN_PASSWORD STORED_AUTH_PERMISSIONS
run
expect_rc 0
expect_file "$SECRETS_DIR/auth-token" excb_svc-auth-new
expect_recorded auth-token "$WANT_AUTH"

start "an unreadable token listing stops bootstrap"
installed
ADMIN_PASSWORD=admin-password LISTING_CODE=500
export ADMIN_PASSWORD LISTING_CODE
run
if [ "$RC" != 0 ] && ! grep -q "BOOTSTRAP COMPLETE" "$OUTPUT"; then ok "failed"; else bad "exit $RC"; fi
expect_file "$SECRETS_DIR/auth-token" excb_svc-auth

start "a failed revoke stops bootstrap before minting"
installed
ADMIN_PASSWORD=admin-password STORED_AUTH_PERMISSIONS=$OLD_AUTH REVOKE_FAIL=1
export ADMIN_PASSWORD STORED_AUTH_PERMISSIONS REVOKE_FAIL
run
if [ "$RC" != 0 ] && ! grep -q "BOOTSTRAP COMPLETE" "$OUTPUT"; then ok "failed"; else bad "exit $RC"; fi
expect_not_called '/api/auth/tokens {"name":"svc-auth"'

start "a token that no longer authenticates is re-minted"
installed recorded
ME_FAIL=excb_svc-auth ADMIN_PASSWORD=admin-password
export ME_FAIL ADMIN_PASSWORD
run
expect_rc 0
expect_called "DELETE http://provisioning.test:24005/api/auth/tokens/hash-svc-auth"
expect_file "$SECRETS_DIR/auth-token" excb_svc-auth-new
expect_file "$SECRETS_DIR/graphql-token" excb_svc-graphql

echo "bootstrap: $PASSED passed, $FAILED failed"
[ "$FAILED" = 0 ]
