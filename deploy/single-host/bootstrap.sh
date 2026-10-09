#!/bin/sh
# Docker/Podman AIO bootstrap — mirrors the k8s platform-bootstrap Job.
#
# Flow (idempotent, each step checks state and skips if already done):
#   1. wait for provisioning /healthz
#   2. if both service tokens on disk still authenticate, there is nothing to do
#   3. otherwise register (first run) or reuse the platform admin and log in for
#      a 12h session — the session is never written anywhere
#   4. ensure the svc-auth and svc-graphql service principals exist and mint one
#      non-expiring capability token each, written to $SECRETS_DIR
#   5. seed the ES256 signing keypair into vault (bbolt auto-init/unseal in
#      selfhosted/docker — no /api/vault/init or /unseal needed)
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
ADMIN_EMAIL="${ADMIN_EMAIL:?ADMIN_EMAIL is required: the first platform admin address}"
ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"

# Permission lists — keep in sync with charts/platform-aio values.yaml.
AUTH_PERMISSIONS='["vault:read:pki/signing/*","vault:read:projects/*/credentials/auth_admin","projects:info:read","email:send"]'
GRAPHQL_PERMISSIONS='["vault:read:projects/*/credentials/excalibase_app","projects:info:read","policies:read"]'

# 0755 dir + 0644 files: the volume is mounted read-only into exactly two
# containers (auth, graphql) which run as different non-root UIDs (distroless
# nonroot vs uid 1000), so a 0600 root-owned file would be unreadable by both.
# This mirrors the default mode of a Kubernetes Secret volume: the files are
# reachable only from inside those containers, never from the host filesystem
# of another service.
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

# Returns the first bytes of the vault signing key (empty when absent).
signing_key_present() {
  curl -sS "$PROV_URL/api/vault/secrets/pki/signing/private" \
    -H "Authorization: Bearer $1" 2>/dev/null | jq -r '.key // empty' | head -c 10
}

echo "[2/5] checking the stored service tokens ..."
if token_valid "$AUTH_TOKEN_FILE" && token_valid "$GRAPHQL_TOKEN_FILE"; then
  echo "  both service tokens are valid"
  if [ -n "$(signing_key_present "$(cat "$AUTH_TOKEN_FILE")")" ]; then
    echo "  signing key present — nothing to do"
    echo "BOOTSTRAP COMPLETE"
    exit 0
  fi
  echo "  signing key missing — an admin session is needed to seed it"
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
  ( umask 022; printf '%s' "$TOKEN" > "$3" )
  chmod 0644 "$3"
  echo "  minted $1"
}

echo "[4/5] service identities ..."
if token_valid "$AUTH_TOKEN_FILE"; then
  echo "  svc-auth token is valid"
else
  mint_service_token svc-auth "$AUTH_PERMISSIONS" "$AUTH_TOKEN_FILE"
fi
if token_valid "$GRAPHQL_TOKEN_FILE"; then
  echo "  svc-graphql token is valid"
else
  mint_service_token svc-graphql "$GRAPHQL_PERMISSIONS" "$GRAPHQL_TOKEN_FILE"
fi

echo "[5/5] signing key ..."
if [ -z "$(signing_key_present "$SESSION")" ]; then
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
