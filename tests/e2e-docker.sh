#!/bin/bash
# E2E full-flow test for Docker provisioner mode.
#
# Prerequisites:
#   - Docker 24+ running (docker ps works)
#   - Go 1.24+ (builds the server binary)
#   - Deno installed (for edge functions)
#   - jq, curl
#
# Run: bash tests/e2e-docker.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SERVER_DIR="$ROOT/server-go"
DENO_DIR="$ROOT/deno-server"
DATA_DIR=$(mktemp -d)
PLATFORM_DB_NAME="excalibase-e2e-platform-db-$$"
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
  # Remove any project containers we provisioned
  docker ps -a --filter "label=excalibase.managed=true" --format '{{.Names}}' | xargs -r docker rm -f 2>/dev/null || true
  rm -rf "$DATA_DIR"
  echo "Cleanup done."
}
trap cleanup EXIT

echo "===== E2E Docker Mode Full Flow ====="
echo "Data dir: $DATA_DIR"
echo ""

# --- Step 0: Build server binary ---
echo "0. Build server binary"
cd "$SERVER_DIR"
go build -o "$DATA_DIR/excalibase-server" ./cmd/server/ 2>&1
pass "server binary built"

# --- Step 1: Self-hosted uses SQLite + bbolt, no external platform-db needed ---
echo "1. Self-hosted mode (SQLite + bbolt, no platform-db container)"
pass "no platform-db needed (using SQLite at $DATA_DIR/excalibase.db)"

# --- Step 2: Start Deno runtime ---
echo "2. Start Deno runtime"
DENO_RUNTIME_SECRET="e2e-secret-$(date +%s)"
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
# Wait for Deno health
for i in $(seq 1 15); do
  if curl -sf -H "X-Runtime-Secret: $DENO_RUNTIME_SECRET" http://127.0.0.1:8000/health > /dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -sf -H "X-Runtime-Secret: $DENO_RUNTIME_SECRET" http://127.0.0.1:8000/health > /dev/null 2>&1 && pass "deno runtime healthy" || fail "deno" "not healthy after 15s"

# --- Step 3: Start provisioning server ---
echo "3. Start provisioning server (PROVISIONER_MODE=docker, self-hosted SQLite)"
cd "$ROOT"
# Self-hosted mode uses SQLite for the platform store and bbolt for vault.
# STORAGE_PATH must be writable; DB_PATH controls the SQLite file location.
PORT=24055 \
DEPLOYMENT_MODE=selfhosted \
PROVISIONER_MODE=docker \
STORAGE_PATH="$DATA_DIR" \
DB_PATH="$DATA_DIR/excalibase.db" \
CORS_ORIGINS="*" \
PUBLIC_BASE_URL="http://localhost:24055" \
DENO_RUNTIME_URL="http://127.0.0.1:8000" \
DENO_RUNTIME_SECRET="$DENO_RUNTIME_SECRET" \
LOG_LEVEL=info \
"$DATA_DIR/excalibase-server" > "$DATA_DIR/server.log" 2>&1 &
SERVER_PID=$!
# Wait for server health
for i in $(seq 1 20); do
  if curl -sf http://localhost:24055/healthz > /dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -sf http://localhost:24055/healthz > /dev/null 2>&1 && pass "provisioning server healthy" || { fail "server" "not healthy after 20s"; cat "$DATA_DIR/server.log"; exit 1; }

API="http://localhost:24055"

# --- Step 4: Config endpoint ---
echo "4. Config endpoint"
R=$(curl -s "$API/api/config")
echo "$R" | jq -r '.deploymentMode' 2>/dev/null | grep -q selfhosted && pass "selfhosted mode" || fail "config" "$R"

# --- Step 5: Vault init + unseal ---
echo "5. Vault init + unseal"
VAULT_INIT=$(curl -s -X POST "$API/api/vault/init" -H 'Content-Type: application/json' -d '{"secretShares":1,"secretThreshold":1}')
UNSEAL_KEY=$(echo "$VAULT_INIT" | jq -r '.keys[0]' 2>/dev/null)
if [ -n "$UNSEAL_KEY" ] && [ "$UNSEAL_KEY" != "null" ]; then
  pass "vault initialized"
  curl -s -X POST "$API/api/vault/unseal" -H 'Content-Type: application/json' -d "{\"key\":\"$UNSEAL_KEY\"}" > /dev/null
  pass "vault unsealed"
else
  # Already initialized — check status
  R=$(curl -s "$API/api/vault/status")
  echo "$R" | jq -r '.sealed' 2>/dev/null | grep -q false && pass "vault already unsealed" || fail "vault" "$R"
fi

# --- Step 6: Login (admin bootstrapped on first run) ---
echo "6. Platform login"
# Get the bootstrap password from server log (format: "  Password: <hex>")
ADMIN_PASS=$(grep -oP 'Password: \K\S+' "$DATA_DIR/server.log" 2>/dev/null | head -1 || echo "")
if [ -z "$ADMIN_PASS" ]; then
  ADMIN_PASS="admin"
fi
TOKEN=$(curl -s -X POST "$API/api/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" | jq -r '.token')
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && pass "admin login (token obtained)" || fail "login" "token=$TOKEN"

# --- Step 7: Default org ---
echo "7. Default org"
R=$(curl -s "$API/api/orgs/" -H "Authorization: Bearer $TOKEN")
ORG_ID=$(echo "$R" | jq -r '.[0].id' 2>/dev/null)
ORG_SLUG=$(echo "$R" | jq -r '.[0].slug' 2>/dev/null)
[ "$ORG_SLUG" = "default" ] && pass "default org exists (slug=default)" || fail "default org" "$R"

# --- Step 8: Provision project (Docker mode) ---
echo "8. Provision project (Docker mode)"
R=$(curl -s -X POST "$API/api/provision/" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"e2e-docker-test\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"tier\":\"FREE\"}")
PROJECT_ID=$(echo "$R" | jq -r '.projectId' 2>/dev/null)
[ -n "$PROJECT_ID" ] && [[ "$PROJECT_ID" == proj_* ]] && pass "provision started (ref=$PROJECT_ID)" || fail "provision" "$R"

# --- Step 9: Wait for provisioning to complete ---
echo "9. Wait for provisioning (Docker container + pg_isready)"
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
  tail -20 "$DATA_DIR/server.log" 2>/dev/null
  echo "  --- end ---"
fi

# Verify Docker container exists — name uses the project ref, not display name
CONTAINER_NAME="excalibase-$PROJECT_ID-postgres"
docker ps --format '{{.Names}}' | grep -q "$CONTAINER_NAME" 2>/dev/null && pass "postgres container running" || fail "container" "not found: $CONTAINER_NAME"

# --- Step 10: Deploy edge function ---
echo "10. Deploy edge function"
FN_BODY=$(cat <<'FNEOF'
{
  "id": "e2e-fn",
  "name": "E2E Test Function",
  "files": [
    {
      "path": "index.ts",
      "content": "export default async (req: Request): Promise<Response> => {\n  console.log('e2e function invoked', { ts: Date.now() });\n  const url = Deno.env.get('EXCALIBASE_URL') || 'not-set';\n  const pid = Deno.env.get('EXCALIBASE_PROJECT_ID') || 'not-set';\n  return Response.json({ ok: true, url, pid });\n};"
    }
  ]
}
FNEOF
)
R=$(curl -s -X POST "$API/api/projects/$PROJECT_ID/functions/" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$FN_BODY")
echo "$R" | jq -r '.id' 2>/dev/null | grep -q "e2e-fn" && pass "function deployed" || fail "deploy fn" "$R"

# --- Step 11: Invoke edge function ---
echo "11. Invoke edge function"
R=$(curl -s -X POST "$API/api/projects/$PROJECT_ID/functions/e2e-fn/invoke" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{}')
# The invoke endpoint forwards the function's body directly (not wrapped)
OK=$(echo "$R" | jq -r '.ok' 2>/dev/null)
PID=$(echo "$R" | jq -r '.pid' 2>/dev/null)
[ "$OK" = "true" ] && pass "function invoked" || fail "invoke" "$R"
[ "$PID" = "$PROJECT_ID" ] && pass "EXCALIBASE_PROJECT_ID injected" || fail "env injection" "pid=$PID"

# --- Step 12: Fetch logs ---
echo "12. Fetch function logs"
sleep 1
R=$(curl -s "$API/api/projects/$PROJECT_ID/functions/e2e-fn/logs" \
  -H "Authorization: Bearer $TOKEN")
LOG_COUNT=$(echo "$R" | jq '.logs | length' 2>/dev/null)
[ "$LOG_COUNT" -ge 1 ] && pass "logs captured ($LOG_COUNT entries)" || fail "logs" "$R"

# --- Step 13: List + Get function ---
echo "13. List functions"
R=$(curl -s "$API/api/projects/$PROJECT_ID/functions/" -H "Authorization: Bearer $TOKEN")
FN_COUNT=$(echo "$R" | jq 'length' 2>/dev/null)
[ "$FN_COUNT" -eq 1 ] && pass "1 function listed" || fail "list" "$R"

# --- Step 14: Delete function ---
echo "14. Delete function"
R=$(curl -s -X DELETE "$API/api/projects/$PROJECT_ID/functions/e2e-fn" -H "Authorization: Bearer $TOKEN")
echo "$R" | jq -r '.status' 2>/dev/null | grep -q deleted && pass "function deleted" || fail "delete fn" "$R"

# --- Step 15: Deprovision ---
echo "15. Deprovision project"
R=$(curl -s -X DELETE "$API/api/provision/$PROJECT_ID/" \
  -H "Authorization: Bearer $TOKEN")
echo "$R" | grep -qi "deleted\|deprovisioned\|success" && pass "deprovisioned" || fail "deprovision" "$R"

# Verify container removed
sleep 2
docker ps --format '{{.Names}}' | grep -q "$CONTAINER_NAME" 2>/dev/null && fail "container cleanup" "still running" || pass "container cleaned up"

echo ""
echo "============================="
echo "RESULTS: $PASS passed, $FAIL failed"
echo "============================="
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
