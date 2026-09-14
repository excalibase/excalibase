#!/bin/bash
# E2E full-flow test for Docker provisioner mode in CLOUD storage shape:
# Postgres platform store + Postgres vault store (the same shape AIO helm
# uses in K8s, but with the Docker provisioner instead of CNPG). Catches
# bugs that only surface with the bootstrap-Job init flow (no auto-init) —
# e.g. vault barrier serialization, schema migrations, concurrent
# transaction handling.
#
# Prerequisites:
#   - Docker 24+ running (docker ps works)
#   - Go 1.24+ (builds the server binary)
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
  docker rm -f "$PLATFORM_DB_CONTAINER" >/dev/null 2>&1 || true
  docker ps -a --filter "label=excalibase.managed=true" --format '{{.Names}}' | xargs -r docker rm -f 2>/dev/null || true
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
echo "1. Start platform-db Postgres (cloud mode shape)"
docker run -d --name "$PLATFORM_DB_CONTAINER" \
  -p "$PLATFORM_DB_PORT:5432" \
  -e POSTGRES_DB=platform \
  -e POSTGRES_USER=platform \
  -e POSTGRES_PASSWORD=platformpass \
  postgres:16-alpine > /dev/null
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

# --- Step 3: Start provisioning server in cloud mode ---
echo "3. Start provisioning server (PROVISIONER_MODE=docker, cloud → Postgres + Postgres-vault)"
cd "$ROOT"
PORT=24056 \
DEPLOYMENT_MODE=cloud \
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

# --- Step 4: Confirm cloud mode + Postgres backend ---
echo "4. Confirm storage backends are Postgres-Postgres"
R=$(curl -s "$API/api/config")
echo "$R" | jq -r '.deploymentMode' 2>/dev/null | grep -q cloud && pass "cloud mode" || fail "config" "$R"

# Inspect server log to confirm both stores chose the Postgres path
grep -q "Cloud mode: using PostgreSQL platform store" "$DATA_DIR/server.log" \
  && pass "platform store = Postgres" \
  || fail "platform store" "expected Cloud mode log line"
grep -q "Cloud mode: using PostgreSQL vault store" "$DATA_DIR/server.log" \
  && pass "vault store = Postgres" \
  || fail "vault store" "expected Cloud mode log line"

# --- Step 5: Vault init + unseal (mirrors studio /setup wizard) ---
echo "5. Vault init + unseal"
VAULT_INIT=$(curl -s -X POST "$API/api/vault/init" -H 'Content-Type: application/json' -d '{"shares":1,"threshold":1}')
UNSEAL_KEY=$(echo "$VAULT_INIT" | jq -r '.shares[0]' 2>/dev/null)
if [ -n "$UNSEAL_KEY" ] && [ "$UNSEAL_KEY" != "null" ]; then
  pass "vault initialized (Postgres barrier)"
  R=$(curl -s -X POST "$API/api/vault/unseal" -H 'Content-Type: application/json' -d "{\"share\":\"$UNSEAL_KEY\"}")
  echo "$R" | jq -r '.sealed' 2>/dev/null | grep -q false && pass "vault unsealed" || fail "unseal" "$R"
else
  R=$(curl -s "$API/api/vault/status")
  echo "$R" | jq -r '.sealed' 2>/dev/null | grep -q false && pass "vault already unsealed" || fail "vault" "$R"
fi

# Verify the barrier landed in the Postgres vault_barrier table.
BARRIER_COUNT=$(docker exec "$PLATFORM_DB_CONTAINER" psql -U platform -d platform -tAc \
  "SELECT count(*) FROM vault_barrier" 2>/dev/null || echo "0")
[ "$BARRIER_COUNT" = "1" ] \
  && pass "vault barrier persisted in platform-db" \
  || fail "vault barrier" "expected 1 row in vault_barrier, got '$BARRIER_COUNT'"

# --- Step 6: Register first admin (wizard step 4) ---
echo "6. Register platform admin"
ADMIN_PASS="E2eAdmin123!"
REG_BODY=$(jq -n --arg p "$ADMIN_PASS" '{username:"admin", email:"admin@e2e.local", password:$p}')
R=$(curl -s -X POST "$API/api/auth/register" -H 'Content-Type: application/json' -d "$REG_BODY")
TOKEN=$(echo "$R" | jq -r '.token' 2>/dev/null)
ROLE=$(echo "$R" | jq -r '.user.role' 2>/dev/null)
if [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && [ "$ROLE" = "platform_admin" ]; then
  pass "admin registered + auto-promoted to platform_admin"
else
  fail "auth" "register=$R"
  exit 1
fi

# --- Step 7: Cloud mode: orgs are not auto-bootstrapped ---
# Cloud mode skips BootstrapDefaultOrg (orgs created on-demand via API).
# Create one explicitly so the rest of the flow has somewhere to provision.
echo "7. Create org via API (cloud mode)"
R=$(curl -s -X POST "$API/api/orgs/" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"E2E Cloud Org","slug":"e2e-cloud","tier":"FREE"}')
ORG_ID=$(echo "$R" | jq -r '.id' 2>/dev/null)
[ -n "$ORG_ID" ] && [ "$ORG_ID" != "null" ] \
  && pass "org created (id=$ORG_ID)" \
  || fail "org create" "$R"

# --- Step 8: Provision project (Docker mode) ---
echo "8. Provision project"
R=$(curl -s -X POST "$API/api/provision/" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"e2e-cloud-test\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"tier\":\"FREE\"}")
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
    "$API/api/vault/secrets/projects/e2e-cloud/$PROJECT_ID/credentials/$role")
  echo "$R" | jq -r '.username' 2>/dev/null | grep -q "." \
    && pass "vault has $role credentials" \
    || fail "vault" "missing $role: $R"
done

# --- Step 11: Deprovision ---
echo "11. Deprovision"
R=$(curl -s -X DELETE "$API/api/provision/$PROJECT_ID/" -H "Authorization: Bearer $TOKEN")
echo "$R" | grep -qi "delet" \
  && pass "deprovision started" \
  || fail "deprovision" "$R"
sleep 5
docker ps --format '{{.Names}}' | grep -qv "^$CONTAINER_NAME$" \
  && pass "tenant container cleaned up" \
  || fail "cleanup" "container still exists"

echo ""
echo "============================="
echo "RESULTS: $PASS passed, $FAIL failed"
echo "============================="
[ "$FAIL" -eq 0 ]
