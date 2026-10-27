#!/bin/sh
# Docker/Podman AIO bootstrap — mirrors the k8s platform-bootstrap Job.
#
# Flow (idempotent, each step checks state and skips if already done):
#   1. wait for provisioning /healthz
#   2. if both service tokens on disk still authenticate and were minted with
#      this version's permission lists, there is nothing to do
#   3. otherwise register (first run) or reuse the platform admin and log in for
#      a 12h session — the session is never written anywhere
#   4. ensure the svc-auth and svc-graphql service principals exist and mint one
#      non-expiring capability token each, written to $SECRETS_DIR; a token whose
#      permission list differs from the one below is revoked and re-minted
#   5. seed the ES256 signing keypair into vault (bbolt auto-init/unseal in
#      selfhosted/docker — no /api/vault/init or /unseal needed). Under manual
#      unseal (EXC-579) the vault waits for the admin: this step is skipped and
#      the admin initializes or unseals it in Studio, which seeds the key.
#
# The tokens are FILES, not env vars: auth and graphql re-read them on change,
# so re-minting here does not need the stack restarted. There is no shared
# admin PAT any more.
set -eu

apk add --no-cache curl jq openssl >/dev/null

PROV_URL="${PROVISIONING_URL:-http://provisioning:24005}"
SECRETS_DIR="${SECRETS_DIR:-/var/run/excalibase}"
AUTH_TOKEN_FILE="$SECRETS_DIR/auth-token"
GRAPHQL_TOKEN_FILE="$SECRETS_DIR/graphql-token"
# Next to each token, the permission list it was minted with, so a restart can
# tell without an admin session that this version asks for the same list.
PERMISSIONS_SUFFIX=.permissions
ADMIN_EMAIL="${ADMIN_EMAIL:?ADMIN_EMAIL is required: the first platform admin address}"
ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"

# Permission lists — keep in sync with charts/platform-aio values.yaml.
AUTH_PERMISSIONS='["vault:read:pki/signing/*","vault:read:projects/*/credentials/auth_admin","projects:info:read","email:send"]'
# Each project's own Google/GitHub client secret, only while end-user OAuth is on (EXC-662).
if [ "${FEATURE_AUTHOAUTH:-false}" = true ]; then
  AUTH_PERMISSIONS=$(printf '%s' "$AUTH_PERMISSIONS" | jq -c '. + ["vault:read:projects/*/auth/providers/*"]')
fi
# Each project's guest Turnstile secret, only while guest accounts are on (EXC-662).
if [ "${FEATURE_AUTHGUEST:-false}" = true ]; then
  AUTH_PERMISSIONS=$(printf '%s' "$AUTH_PERMISSIONS" | jq -c '. + ["vault:read:projects/*/auth/captcha"]')
fi
GRAPHQL_PERMISSIONS='["vault:read:projects/*/credentials/excalibase_app","projects:info:read","policies:read"]'

# Each token is read by one container only, so it is 0600 and owned by that
# container's user: auth runs as distroless nonroot (65532), graphql as uid
# 1000. The directory stays 0755 so both can reach their own file.
AUTH_TOKEN_UID="${AUTH_TOKEN_UID:-65532}"
GRAPHQL_TOKEN_UID="${GRAPHQL_TOKEN_UID:-1000}"
mkdir -p "$SECRETS_DIR"
chmod 0755 "$SECRETS_DIR"

echo "[1/5] waiting for provisioning at $PROV_URL ..."
for _ in $(seq 1 60); do
  if curl -sf "$PROV_URL/healthz" >/dev/null 2>&1; then
    echo "  provisioning up"; break
  fi
  sleep 2
done

# token_valid <file> — a capability token may always call /api/auth/me, so this
# check works for every token shape without widening any permission list.
token_valid() {
  [ -s "$1" ] || return 1
  curl -fsS "$PROV_URL/api/auth/me" -H "Authorization: Bearer $(cat "$1")" >/dev/null 2>&1
}

# permissions_recorded <token-file> <permissions-json> — true when the list
# recorded beside the token is the one this version asks for, in any order.
permissions_recorded() {
  [ -s "$1$PERMISSIONS_SUFFIX" ] &&
    jq -e --argjson want "$2" 'sort == ($want | sort)' "$1$PERMISSIONS_SUFFIX" >/dev/null 2>&1
}

# restrict_token <token-file> <uid> — readable by that container's user only;
# also tightens a token an older version left 0644.
restrict_token() {
  chown "$2:$2" "$1"
  chmod 0600 "$1"
}

# record_permissions <token-file> <permissions-json>
record_permissions() {
  ( umask 022; printf '%s' "$2" | jq -c 'sort' > "$1$PERMISSIONS_SUFFIX" )
  chmod 0644 "$1$PERMISSIONS_SUFFIX"
}

# Returns the first bytes of the vault signing key (empty when absent).
signing_key_present() {
  curl -sS "$PROV_URL/api/vault/secrets/pki/signing/private" \
    -H "Authorization: Bearer $1" 2>/dev/null | jq -r '.key // empty' | head -c 10
}

# vault_state: open, sealed or uninitialized. A provisioning that does not
# answer yet is retried; one that never answers stops bootstrap rather than
# being taken for a waiting vault.
vault_state() {
  for _ in $(seq 1 60); do
    state=$(curl -fsS "$PROV_URL/api/vault/status" 2>/dev/null |
      jq -r 'if (.initialized|not) then "uninitialized" elif .sealed then "sealed" else "open" end' 2>/dev/null) || state=
    case "$state" in open | sealed | uninitialized) echo "$state"; return 0 ;; esac
    sleep 2
  done
  echo "ERROR: provisioning did not report the vault state at $PROV_URL/api/vault/status" >&2
  return 1
}
waiting_for_admin() {
  echo "  the vault is $1: sign in to Studio (/setup) as the platform admin to"
  echo "  initialize or unseal it, or run 'excalibase-provisioning vault unseal' in the"
  echo "  provisioning container. The unseal key is never stored on this host."
}

echo "[2/5] checking the stored service tokens ..."
if token_valid "$AUTH_TOKEN_FILE" && token_valid "$GRAPHQL_TOKEN_FILE"; then
  echo "  both service tokens are valid"
  restrict_token "$AUTH_TOKEN_FILE" "$AUTH_TOKEN_UID"
  restrict_token "$GRAPHQL_TOKEN_FILE" "$GRAPHQL_TOKEN_UID"
  VAULT=$(vault_state) || exit 1
  KEY_SETTLED=0
  if [ "$VAULT" != open ] || [ -n "$(signing_key_present "$(cat "$AUTH_TOKEN_FILE")")" ]; then
    KEY_SETTLED=1
  fi
  if permissions_recorded "$AUTH_TOKEN_FILE" "$AUTH_PERMISSIONS" &&
    permissions_recorded "$GRAPHQL_TOKEN_FILE" "$GRAPHQL_PERMISSIONS"; then
    if [ "$VAULT" != open ]; then
      waiting_for_admin "$VAULT"
      echo "BOOTSTRAP COMPLETE"
      exit 0
    fi
    if [ "$KEY_SETTLED" = 1 ]; then
      echo "  signing key present — nothing to do"
      echo "BOOTSTRAP COMPLETE"
      exit 0
    fi
    echo "  signing key missing — an admin session is needed to seed it"
  elif [ "$KEY_SETTLED" = 1 ] && [ -z "${ADMIN_PASSWORD:-}" ]; then
    # The admin's password is stored nowhere, so a doomed login (which counts
    # toward the admin's lockout) is not attempted; the old tokens keep working.
    echo "WARN: the service tokens may carry another permission list than this version"
    echo "      declares. Set ADMIN_PASSWORD (the platform admin's password) and run the"
    echo "      bootstrap service again to re-mint them."
    [ "$VAULT" = open ] || waiting_for_admin "$VAULT"
    echo "BOOTSTRAP COMPLETE"
    exit 0
  else
    echo "  their permission lists may differ from this version's — re-minting needs an admin session"
  fi
fi

echo "[3/5] platform admin ..."
ADMIN_REGISTERED=0
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  # Prefix satisfies the complexity rule (upper+lower+digit); hex supplies entropy.
  ADMIN_PASSWORD="Aa1$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
fi
# EXC-451: provisioning only promotes this registration to platform_admin
# when it presents the one-time setup token — provisioning was started with
# the same SETUP_TOKEN value (compose.yaml requires both share it).
[ -n "${SETUP_TOKEN:-}" ] || { echo "ERROR: SETUP_TOKEN is empty — cannot register the initial platform admin" >&2; exit 1; }
REG=$(curl -sS -X POST "$PROV_URL/api/auth/register" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USERNAME\",\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\",\"setupToken\":\"$SETUP_TOKEN\"}")
if echo "$REG" | jq -e '.token // .id // .user' >/dev/null 2>&1; then
  ADMIN_REGISTERED=1
  echo "  registered $ADMIN_EMAIL"
else
  echo "  admin already exists — reusing it"
fi

SESSION=$(curl -sS -X POST "$PROV_URL/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USERNAME\",\"password\":\"$ADMIN_PASSWORD\"}" | jq -r '.token')
if [ -z "$SESSION" ] || [ "$SESSION" = "null" ]; then
  echo "ERROR: admin login failed. The platform admin already exists and its"
  echo "       password is not stored anywhere — re-run with ADMIN_PASSWORD set."
  exit 1
fi

# SERVICE_PRINCIPAL_ABSENT is what service_tokens returns for a principal that
# does not exist yet: the first state of every install, not a failure.
SERVICE_PRINCIPAL_ABSENT=2

# service_tokens <principal> — prints the principal's token listing (metadata
# only, never a secret). curl runs without -f so a 404 is told apart from a
# transport failure, and a non-200 body is never read as a listing.
service_tokens() {
  BODY=/tmp/service-tokens.json
  CODE=$(curl -sS -o "$BODY" -w '%{http_code}' \
    "$PROV_URL/api/admin/service-accounts/$1/tokens" \
    -H "Authorization: Bearer $SESSION") || return 1
  case "$CODE" in
    200) cat "$BODY" ;;
    404) return $SERVICE_PRINCIPAL_ABSENT ;;
    *)   return 1 ;;
  esac
}

# permissions_current <principal> <permissions-json> — true when the principal
# owns at least one token and every one carries exactly this list. Called
# directly, never as $(...), so its exit really ends the script.
permissions_current() {
  LISTING=$(service_tokens "$1")
  case $? in
    0) ;;
    "$SERVICE_PRINCIPAL_ABSENT") return 1 ;;
    *) echo "ERROR: reading the $1 token listing failed" >&2; exit 1 ;;
  esac
  printf '%s' "$LISTING" | jq -e --argjson want "$2" \
    'length > 0 and all(.[]; ((.permissions // []) | sort) == ($want | sort))' >/dev/null
}

# revoke_service_tokens <principal> — retires every token it owns, so a
# re-mint never leaves the old permission list in circulation.
revoke_service_tokens() {
  LISTING=$(service_tokens "$1")
  case $? in
    0) ;;
    "$SERVICE_PRINCIPAL_ABSENT") return 0 ;;
    *) return 1 ;;
  esac
  for HASH in $(printf '%s' "$LISTING" | jq -r '.[].tokenHash'); do
    curl -fsS -X DELETE "$PROV_URL/api/auth/tokens/$HASH" \
      -H "Authorization: Bearer $SESSION" >/dev/null || return 1
  done
}

# mint_service_token <name> <permissions-json> <file> — creates the principal if
# absent (the endpoint is idempotent by name) and writes its token to the file.
# The secret is never echoed to the log.
mint_service_token() {
  ID=$(curl -fsS -X POST "$PROV_URL/api/admin/service-accounts" \
    -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\"}" | jq -r '.id')
  if [ -z "$ID" ] || [ "$ID" = "null" ]; then
    echo "ERROR: could not create the $1 service account"; exit 1
  fi
  TOKEN=$(curl -fsS -X POST "$PROV_URL/api/auth/tokens" \
    -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\",\"expiresIn\":\"never\",\"userId\":\"$ID\",\"permissions\":$2}" \
    | jq -r '.token')
  if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
    echo "ERROR: could not mint the $1 token"; exit 1
  fi
  # No trailing newline — the consumers trim, but keep the file clean.
  ( umask 077; printf '%s' "$TOKEN" > "$3" )
  echo "  minted $1"
}

# ensure_service_token <name> <permissions-json> <file> <reader-uid> — keeps a
# stored token that authenticates and carries exactly this list; otherwise
# revokes every token the principal owns and mints one.
ensure_service_token() {
  if token_valid "$3" && permissions_current "$1" "$2"; then
    echo "  $1 token is valid"
  else
    revoke_service_tokens "$1" || { echo "ERROR: could not revoke the stored $1 tokens"; exit 1; }
    mint_service_token "$1" "$2" "$3"
  fi
  restrict_token "$3" "$4"
  record_permissions "$3" "$2"
}

echo "[4/5] service identities ..."
ensure_service_token svc-auth "$AUTH_PERMISSIONS" "$AUTH_TOKEN_FILE" "$AUTH_TOKEN_UID"
ensure_service_token svc-graphql "$GRAPHQL_PERMISSIONS" "$GRAPHQL_TOKEN_FILE" "$GRAPHQL_TOKEN_UID"

echo "[5/5] signing key ..."
VAULT=$(vault_state) || exit 1
if [ "$VAULT" != open ]; then
  waiting_for_admin "$VAULT"
elif [ -z "$(signing_key_present "$SESSION")" ]; then
  openssl ecparam -name prime256v1 -genkey -noout -out /tmp/priv.pem
  openssl ec -in /tmp/priv.pem -pubout -out /tmp/pub.pem 2>/dev/null
  PRIV=$(jq -Rs . < /tmp/priv.pem)
  PUB=$(jq -Rs . < /tmp/pub.pem)
  curl -sS -X PUT "$PROV_URL/api/vault/secrets/pki/signing/private" \
    -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
    -d "{\"key\":$PRIV}" >/dev/null
  curl -sS -X PUT "$PROV_URL/api/vault/secrets/pki/signing/public" \
    -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
    -d "{\"key\":$PUB}" >/dev/null
  echo "  seeded ES256 signing keypair"
else
  echo "  already present"
fi

if [ "$ADMIN_REGISTERED" = "1" ]; then
  echo "admin credentials (logged here only — not written to any file):"
  echo "  email:    $ADMIN_EMAIL"
  echo "  username: $ADMIN_USERNAME"
  echo "  password: $ADMIN_PASSWORD"
  echo "  (save these now — the password is not persisted anywhere)"
fi

echo "BOOTSTRAP COMPLETE"
