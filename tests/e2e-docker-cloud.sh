#!/bin/bash
# E2E full-flow test for the Docker provisioner with the Postgres platform store
# and Postgres vault barrier, vault initialised by hand (no auto-init), the
# shape a single-host install runs. DEPLOYMENT_MODE=cloud with the Docker
# provisioner is refused at boot on purpose (multi-tenant control plane on a
# runtime with no cross-tenant isolation), so this runs self-hosted. Catches bugs
# in vault barrier serialization, schema migrations and concurrent transactions.
#
# Prerequisites:
#   - Docker 24+ running (docker ps works)
#   - Go 1.26.10+ (builds the server binary)
#   - Deno installed (for edge functions)
#   - jq, curl
#
# Run: bash tests/e2e-docker-cloud.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SERVER_DIR="$ROOT/server-go"
DENO_DIR="$ROOT/deno-server"
DATA_DIR=$(mktemp -d)
PLATFORM_DB_CONTAINER="excalibase-e2e-platform-db-$$"
PLATFORM_DB_PORT=$((25400 + RANDOM % 100))
SERVER_PID=""
PROJECT_ID=""
DENO_PID=""
PASS=0
FAIL=0

pass() { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1: $2"; FAIL=$((FAIL+1)); }

cleanup() {
  echo ""
  echo "--- Cleanup ---"
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null && wait "$SERVER_PID" 2>/dev/null || true
  [ -n "$DENO_PID" ] && kill "$DENO_PID" 2>/dev/null && wait "$DENO_PID" 2>/dev/null || true
  docker rm -fv "$PLATFORM_DB_CONTAINER" >/dev/null 2>&1 || true
  # Only the tenant this run provisioned: other managed containers on the host are not ours.
  if [ -n "$PROJECT_ID" ] && [ "$PROJECT_ID" != null ]; then
    docker ps -a --filter "label=excalibase.managed=true" --filter "name=$PROJECT_ID" --format '{{.Names}}' | xargs -r docker rm -fv 2>/dev/null || true
  fi
  rm -rf "$DATA_DIR"
  echo "Cleanup done."
}
trap cleanup EXIT

echo "===== E2E Docker Mode (Postgres platform + Postgres vault) ====="
echo "Data dir: $DATA_DIR"
echo "Platform DB: $PLATFORM_DB_CONTAINER on localhost:$PLATFORM_DB_PORT"
echo ""

# --- Step 0: Build server binary ---
echo "0. Build server binary"
cd "$SERVER_DIR"
go build -o "$DATA_DIR/excalibase-server" ./cmd/server/ 2>&1
pass "server binary built"

# --- Step 1: Start platform-db Postgres container ---
echo "1. Start platform-db Postgres"
docker run -d --name "$PLATFORM_DB_CONTAINER" \
  -p "$PLATFORM_DB_PORT:5432" \
  -e POSTGRES_DB=platform \
  -e POSTGRES_USER=platform \
  -e POSTGRES_PASSWORD=platformpass \
  "${IMAGE_MIRROR:+$IMAGE_MIRROR/}postgres:16-alpine" > /dev/null
# Wait for ready
for i in $(seq 1 30); do
  if docker exec "$PLATFORM_DB_CONTAINER" pg_isready -U platform -d platform > /dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "$PLATFORM_DB_CONTAINER" pg_isready -U platform -d platform > /dev/null 2>&1 \
  && pass "platform-db ready" || { fail "platform-db" "did not become ready"; exit 1; }

PLATFORM_DB_URL="postgres://platform:platformpass@localhost:$PLATFORM_DB_PORT/platform?sslmode=disable"

# --- Step 2: Start Deno runtime ---
echo "2. Start Deno runtime"
DENO_RUNTIME_SECRET="e2e-cloud-secret-$(date +%s)"
DENO_BIN=$(which deno 2>/dev/null || echo "$HOME/.deno/bin/deno")
if [ ! -x "$DENO_BIN" ]; then
  fail "deno" "binary not found"
  exit 1
fi
cd "$DENO_DIR"
RUNTIME_SECRET="$DENO_RUNTIME_SECRET" "$DENO_BIN" run \
  --allow-env --allow-net --allow-read --unstable-worker-options \
  server.ts > "$DATA_DIR/deno.log" 2>&1 &
DENO_PID=$!
for i in $(seq 1 15); do
  if curl -sf -H "X-Runtime-Secret: $DENO_RUNTIME_SECRET" http://127.0.0.1:8000/health > /dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -sf -H "X-Runtime-Secret: $DENO_RUNTIME_SECRET" http://127.0.0.1:8000/health > /dev/null 2>&1 \
  && pass "deno runtime healthy" || fail "deno" "not healthy after 15s"

SETUP_TOKEN="e2e-setup-token-$(date +%s)-0123456789abcdef"

# --- Step 3: Start provisioning server ---
echo "3. Start provisioning server (PROVISIONER_MODE=docker, selfhosted → Postgres + Postgres-vault)"
cd "$ROOT"
PORT=24056 \
PUBLIC_PORT=24057 \
KUBECONFIG=/nonexistent \
TRUSTED_PROXY_CIDRS="127.0.0.1/32" \
STUDIO_URL="http://localhost:5174" \
SETUP_TOKEN="$SETUP_TOKEN" \
DEPLOYMENT_MODE=selfhosted \
VAULT_UNSEAL_PROVIDER=manual \
PROVISIONER_MODE=docker \
PLATFORM_DB_URL="$PLATFORM_DB_URL" \
STORAGE_PATH="$DATA_DIR" \
CORS_ORIGINS="*" \
PUBLIC_BASE_URL="http://localhost:24056" \
DENO_RUNTIME_URL="http://127.0.0.1:8000" \
DENO_RUNTIME_SECRET="$DENO_RUNTIME_SECRET" \
LOG_LEVEL=info \
"$DATA_DIR/excalibase-server" > "$DATA_DIR/server.log" 2>&1 &
SERVER_PID=$!
for i in $(seq 1 30); do
  if curl -sf http://localhost:24056/healthz > /dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -sf http://localhost:24056/healthz > /dev/null 2>&1 \
  && pass "provisioning server healthy" \
  || { fail "server" "not healthy after 30s"; tail -30 "$DATA_DIR/server.log"; exit 1; }

API="http://localhost:24056"

# --- Step 4: Confirm self-hosted mode ---
echo "4. Confirm deployment mode"
R=$(curl -s "$API/api/config")
echo "$R" | jq -r '.deploymentMode' 2>/dev/null | grep -q selfhosted && pass "selfhosted mode" || fail "config" "$R"

# --- Step 5: Register first admin (wizard step 4) ---
# Runs BEFORE the vault steps, which need an operator credential.
echo "5. Register platform admin"
ADMIN_PASS="E2eAdmin123!"
REG_BODY=$(jq -n --arg p "$ADMIN_PASS" --arg t "$SETUP_TOKEN" '{username:"admin", email:"admin@e2e.local", password:$p, setupToken:$t}')
R=$(curl -s -X POST "$API/api/auth/register" -H "X-Return-Session-Token: true" -H 'Content-Type: application/json' -d "$REG_BODY")
TOKEN=$(echo "$R" | jq -r '.token' 2>/dev/null)
ROLE=$(echo "$R" | jq -r '.user.role' 2>/dev/null)
if [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && [ "$ROLE" = "platform_admin" ]; then
  pass "admin registered + auto-promoted to platform_admin"
else
  fail "auth" "register=$R"
  exit 1
fi

# --- Step 6: Vault init + unseal (mirrors studio /setup wizard) ---
# /api/vault/init and /unseal require an operator credential (EXC-418). The
# call used to go out unauthenticated, was refused, and the script read the
# refusal as "already initialised" — so the vault step tested nothing.
echo "6. Vault init + unseal"
if curl -s "$API/api/vault/status" | jq -r '.initialized' 2>/dev/null | grep -q true; then
  # Manual unseal provider: a fresh database is never initialised, so this means
  # the vault init and unseal below would not run. That is a failed test.
  fail "vault" "already initialised before the init step: $(curl -s "$API/api/vault/status")"
  exit 1
else
  VAULT_INIT=$(curl -s -X POST "$API/api/vault/init" \
    -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
    -d '{"shares":1,"threshold":1}')
  UNSEAL_KEY=$(echo "$VAULT_INIT" | jq -r '.shares[0]' 2>/dev/null)
  if [ -z "$UNSEAL_KEY" ] || [ "$UNSEAL_KEY" = "null" ]; then
    fail "vault init" "$VAULT_INIT"
    exit 1
  fi
  pass "vault initialized (Postgres barrier)"
  R=$(curl -s -X POST "$API/api/vault/unseal" \
    -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
    -d "{\"share\":\"$UNSEAL_KEY\"}")
  if ! echo "$R" | jq -r '.sealed' 2>/dev/null | grep -q false; then
    fail "unseal" "$R"
    exit 1
  fi
  pass "vault unsealed"
fi

# Verify the barrier landed in the Postgres vault_barrier table.
BARRIER_COUNT=$(docker exec "$PLATFORM_DB_CONTAINER" psql -U platform -d platform -tAc \
  "SELECT count(*) FROM vault_barrier" 2>/dev/null || echo "0")
[ "$BARRIER_COUNT" = "1" ] \
  && pass "vault barrier persisted in platform-db" \
  || fail "vault barrier" "expected 1 row in vault_barrier, got '$BARRIER_COUNT'"

# --- Step 7: Default org ---
# Self-hosted bootstraps one org; cloud mode alone creates orgs on demand.
echo "7. Default org"
R=$(curl -s "$API/api/orgs/" -H "Authorization: Bearer $TOKEN")
ORG_ID=$(echo "$R" | jq -r '.[0].id' 2>/dev/null)
[ "$(echo "$R" | jq -r '.[0].slug' 2>/dev/null)" = "default" ] \
  && pass "default org exists (id=$ORG_ID)" \
  || fail "default org" "$R"

# --- Step 8: Provision project (Docker mode) ---
# Backups are off here: this run covers the Postgres stores, and a backup target needs an object store.
echo "8. Provision project"
R=$(curl -s -X POST "$API/api/provision/" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"e2e-cloud-test\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"17\",\"backup\":{\"enabled\":false}}")
PROJECT_ID=$(echo "$R" | jq -r '.projectId' 2>/dev/null)
[ -n "$PROJECT_ID" ] && [[ "$PROJECT_ID" == proj-* ]] \
  && pass "provision started (ref=$PROJECT_ID)" \
  || fail "provision" "$R"

# --- Step 9: Wait for provisioning ---
echo "9. Wait for provisioning"
for i in $(seq 1 60); do
  R=$(curl -s "$API/api/provision/$PROJECT_ID/" -H "Authorization: Bearer $TOKEN")
  STATUS=$(echo "$R" | jq -r '.status' 2>/dev/null)
  [ "$STATUS" = "COMPLETED" ] || [ "$STATUS" = "ACTIVE" ] && break
  [ "$STATUS" = "FAILED" ] && break
  sleep 2
done
if [ "$STATUS" = "COMPLETED" ] || [ "$STATUS" = "ACTIVE" ]; then
  pass "provisioning completed (status=$STATUS)"
else
  fail "provisioning" "status=$STATUS"
  echo "  --- server log (last 20 lines) ---"
  tail -20 "$DATA_DIR/server.log"
fi

CONTAINER_NAME="excalibase-$PROJECT_ID-postgres"
docker ps --format '{{.Names}}' | grep -q "$CONTAINER_NAME" 2>/dev/null \
  && pass "tenant postgres container running" \
  || fail "container" "not found: $CONTAINER_NAME"

# --- Step 10: Cred + role separation came through Postgres vault ---
echo "10. Verify per-project credentials in Postgres vault"
# Three secrets should exist for the project: admin, excalibase_app, cdc_watcher,
# auth_admin (4 paths total). Read them back through the vault API.
for role in admin excalibase_app cdc_watcher auth_admin; do
  R=$(curl -s -H "Authorization: Bearer $TOKEN" \
    "$API/api/vault/secrets/projects/default/$PROJECT_ID/credentials/$role")
  echo "$R" | jq -r '.username' 2>/dev/null | grep -q "." \
    && pass "vault has $role credentials" \
    || fail "vault" "missing $role: $R"
done

# --- Step 11: Deprovision ---
# The owner's own delete (Admin role or higher), not the operator console: this
# server runs without ADMIN_HOST, so /api/admin answers 404 by design
# (EXC-757). A new project is deletion-protected, so the owner turns that off
# first. The delete stops the tenant (a grace period holds the rest), so the
# container must be gone from the running list.
echo "11. Deprovision"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X PATCH "$API/api/provision/$PROJECT_ID/deletion-protection" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"enabled":false}')
[ "$CODE" = "200" ] && pass "deletion protection turned off" || fail "deletion protection" "HTTP $CODE"
CODE=$(curl -s -o "$DATA_DIR/delete.out" -w '%{http_code}' -X DELETE "$API/api/provision/$PROJECT_ID/" \
  -H "Authorization: Bearer $TOKEN")
case $CODE in
  200|202) pass "owner delete accepted (HTTP $CODE)" ;;
  *) fail "deprovision" "HTTP $CODE $(cat "$DATA_DIR/delete.out")" ;;
esac
GONE=no
for _ in $(seq 1 30); do
  if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then GONE=yes; break; fi
  sleep 2
done
[ "$GONE" = yes ] && pass "tenant container no longer running" || fail "cleanup" "$CONTAINER_NAME still running 60s after the delete"

echo ""
echo "============================="
echo "RESULTS: $PASS passed, $FAIL failed"
echo "============================="
[ "$FAIL" -eq 0 ]
