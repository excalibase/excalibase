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
PLATFORM_DB_PORT=$((25300 + RANDOM % 100))
MINIO_NAME="excalibase-e2e-minio-$$"
MINIO_PORT=$((25400 + RANDOM % 100))
BACKUP_BUCKET="excalibase-backups"
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
  docker rm -f "$PLATFORM_DB_NAME" "$MINIO_NAME" >/dev/null 2>&1 || true
  # Remove any project containers we provisioned
  docker ps -a --filter "label=excalibase.managed=true" --format '{{.Names}}' | xargs -r docker rm -f 2>/dev/null || true
  # MinIO writes its volume as root; drop it from inside a container so the
  # unprivileged cleanup below can remove the rest of the data dir.
  docker run --rm -v "$DATA_DIR/minio:/data" --entrypoint sh quay.io/minio/minio \
    -c 'rm -rf /data/* /data/.[!.]*' >/dev/null 2>&1 || true
  rm -rf "$DATA_DIR" 2>/dev/null || true
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

# --- Step 1: Start platform-db Postgres container ---
# The platform store and the vault are Postgres-only (self-hosted and cloud
# both run a platform-db).
echo "1. Start platform-db Postgres (self-hosted shape)"
docker run -d --name "$PLATFORM_DB_NAME" \
  -p "$PLATFORM_DB_PORT:5432" \
  -e POSTGRES_DB=platform \
  -e POSTGRES_USER=platform \
  -e POSTGRES_PASSWORD=platformpass \
  postgres:16-alpine > /dev/null
for i in $(seq 1 30); do
  if docker exec "$PLATFORM_DB_NAME" pg_isready -U platform -d platform > /dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "$PLATFORM_DB_NAME" pg_isready -U platform -d platform > /dev/null 2>&1 \
  && pass "platform-db ready" || { fail "platform-db" "did not become ready"; exit 1; }

PLATFORM_DB_URL="postgres://platform:platformpass@localhost:$PLATFORM_DB_PORT/platform?sslmode=disable"

# --- Step 1b: Start MinIO as the backup object store ---
# Backup/restore is S3-shaped in every deployment mode; MinIO gives the run a
# local S3 so the restore leg below exercises the real upload/download path.
# A pre-created directory under the data volume becomes the bucket at boot.
echo "1b. Start MinIO (backup object store)"
mkdir -p "$DATA_DIR/minio/$BACKUP_BUCKET"
docker run -d --name "$MINIO_NAME" \
  -p "$MINIO_PORT:9000" \
  -e MINIO_ROOT_USER=e2eminio \
  -e MINIO_ROOT_PASSWORD=e2eminiosecret \
  -v "$DATA_DIR/minio:/data" \
  quay.io/minio/minio server /data > /dev/null
for i in $(seq 1 30); do
  curl -sf "http://localhost:$MINIO_PORT/minio/health/live" > /dev/null 2>&1 && break
  sleep 1
done
curl -sf "http://localhost:$MINIO_PORT/minio/health/live" > /dev/null 2>&1 \
  && pass "minio ready" || { fail "minio" "did not become ready"; exit 1; }

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
echo "3. Start provisioning server (PROVISIONER_MODE=docker, self-hosted → Postgres platform-db + Postgres vault)"
cd "$ROOT"

# start_server boots the binary against the run's platform-db, MinIO and Deno.
# Self-hosted mode: Postgres platform store + Postgres vault. STORAGE_PATH must
# be writable (the auto-generated unseal.key lives there, which is also what
# lets a restart come back unsealed); PLATFORM_DB_URL points at the platform-db.
#
# SCHEMA_DB_HOST/PORT are the schema plane's connection override. They are
# empty for the main flow and set only for the restore leg, where the server
# runs on the host and cannot resolve a container name the way the in-network
# AIO deployment does.
start_server() {
  PORT=24055 \
  DEPLOYMENT_MODE=selfhosted \
  PROVISIONER_MODE=docker \
  STORAGE_PATH="$DATA_DIR" \
  PLATFORM_DB_URL="$PLATFORM_DB_URL" \
  CORS_ORIGINS="*" \
  PUBLIC_BASE_URL="http://localhost:24055" \
  DENO_RUNTIME_URL="http://127.0.0.1:8000" \
  DENO_RUNTIME_SECRET="$DENO_RUNTIME_SECRET" \
  BACKUP_DEFAULT_ENDPOINT="http://localhost:$MINIO_PORT" \
  BACKUP_DEFAULT_ACCESS_KEY_ID="e2eminio" \
  BACKUP_DEFAULT_SECRET_ACCESS_KEY="e2eminiosecret" \
  BACKUP_DEFAULT_BUCKET="$BACKUP_BUCKET" \
  BACKUP_DEFAULT_REGION="us-east-1" \
  SCHEMA_DB_HOST="${SCHEMA_DB_HOST:-}" \
  SCHEMA_DB_PORT="${SCHEMA_DB_PORT:-}" \
  LOG_LEVEL=info \
  "$DATA_DIR/excalibase-server" >> "$DATA_DIR/server.log" 2>&1 &
  SERVER_PID=$!
  for i in $(seq 1 20); do
    curl -sf http://localhost:24055/healthz > /dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

stop_server() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null && wait "$SERVER_PID" 2>/dev/null || true
  SERVER_PID=""
}

start_server && pass "provisioning server healthy" || { fail "server" "not healthy after 20s"; tail -40 "$DATA_DIR/server.log"; exit 1; }

API="http://localhost:24055"

# --- Step 4: Config endpoint ---
echo "4. Config endpoint"
R=$(curl -s "$API/api/config")
echo "$R" | jq -r '.deploymentMode' 2>/dev/null | grep -q selfhosted && pass "selfhosted mode" || fail "config" "$R"

# --- Step 5: Register first admin (wizard step 4) ---
# Legacy auth.Bootstrap was removed; first registration auto-promotes
# to platform_admin and returns a PAT for the rest of the flow. It runs
# BEFORE the vault steps because those need an operator credential.
echo "5. Register platform admin"
ADMIN_PASS="E2eAdmin123!"
REG_BODY=$(jq -n --arg p "$ADMIN_PASS" '{username:"admin", email:"admin@e2e.local", password:$p}')
R=$(curl -s -X POST "$API/api/auth/register" -H 'Content-Type: application/json' -d "$REG_BODY")
TOKEN=$(echo "$R" | jq -r '.token' 2>/dev/null)
ROLE=$(echo "$R" | jq -r '.user.role' 2>/dev/null)
if [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && [ "$ROLE" = "platform_admin" ]; then
  pass "admin registered + auto-promoted to platform_admin"
else
  # Admin already existed (re-run over same DATA_DIR) — fall back to login
  TOKEN=$(curl -s -X POST "$API/api/auth/login" -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" | jq -r '.token')
  [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && pass "admin already existed; logged in" || fail "auth" "register=$R"
fi

# --- Step 6: Vault init + unseal ---
# /api/vault/init and /unseal require an operator credential (EXC-418). The
# call used to go out unauthenticated, was refused, and the script read the
# refusal as "already initialised" — so the vault step tested nothing.
echo "6. Vault init + unseal (mirrors studio /setup wizard)"
vault_initialized() {
  curl -s "$API/api/vault/status" | jq -r '.initialized' 2>/dev/null | grep -q true
}
if vault_initialized; then
  # Idempotent re-run over the same DATA_DIR: nothing to initialise.
  R=$(curl -s "$API/api/vault/status")
  echo "$R" | jq -r '.sealed' 2>/dev/null | grep -q false && pass "vault already unsealed" || fail "vault" "$R"
else
  VAULT_INIT=$(curl -s -X POST "$API/api/vault/init" \
    -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
    -d '{"shares":1,"threshold":1}')
  UNSEAL_KEY=$(echo "$VAULT_INIT" | jq -r '.shares[0]' 2>/dev/null)
  if [ -z "$UNSEAL_KEY" ] || [ "$UNSEAL_KEY" = "null" ]; then
    fail "vault init" "$VAULT_INIT"
    exit 1
  fi
  pass "vault initialized (shares=1, threshold=1)"
  R=$(curl -s -X POST "$API/api/vault/unseal" \
    -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
    -d "{\"share\":\"$UNSEAL_KEY\"}")
  if ! echo "$R" | jq -r '.sealed' 2>/dev/null | grep -q false; then
    fail "unseal" "$R"
    exit 1
  fi
  pass "vault unsealed"
fi

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
  -d "{\"projectName\":\"e2e-docker-test\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\"}")
PROJECT_ID=$(echo "$R" | jq -r '.projectId' 2>/dev/null)
[ -n "$PROJECT_ID" ] && [[ "$PROJECT_ID" == proj-* ]] && pass "provision started (ref=$PROJECT_ID)" || fail "provision" "$R"

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

# --- Step 15: Backup → restore → query the restored project through the API ---
# EXC-366: a restore must end exactly like a provision — instance row, vault
# credentials, PgDog, events — so the restored project is usable immediately.
echo "15. Write a marker row into the source project"
CONTAINER_ID=$(docker ps -q --filter "name=^${CONTAINER_NAME}$")
MARKER="marker-$(date +%s)"
docker exec "$CONTAINER_ID" psql -U postgres -d app -c \
  "CREATE TABLE IF NOT EXISTS restore_drill (marker text PRIMARY KEY);" > /dev/null 2>&1 \
  && docker exec "$CONTAINER_ID" psql -U postgres -d app -c \
  "INSERT INTO restore_drill (marker) VALUES ('$MARKER');" > /dev/null 2>&1 \
  && pass "marker written ($MARKER)" || fail "marker" "could not seed restore_drill"

echo "16. Trigger a backup and wait for COMPLETED"
curl -s -X POST "$API/api/provision/$PROJECT_ID/backup/trigger" -H "Authorization: Bearer $TOKEN" > /dev/null
BACKUP_ID=""
for i in $(seq 1 60); do
  BACKUP_ID=$(curl -s "$API/api/provision/$PROJECT_ID/backup/list" -H "Authorization: Bearer $TOKEN" \
    | jq -r '.backups[]? | select((.status|ascii_downcase)=="completed") | .id' 2>/dev/null | head -1)
  [ -n "$BACKUP_ID" ] && [ "$BACKUP_ID" != "null" ] && break
  sleep 2
done
[ -n "$BACKUP_ID" ] && [ "$BACKUP_ID" != "null" ] && pass "backup completed ($BACKUP_ID)" \
  || { fail "backup" "no COMPLETED backup after 120s"; tail -20 "$DATA_DIR/server.log"; }

echo "17. Restore into a new project"
RESTORED_NAME="restored-$(date +%s)"
RREQ=$(jq -n --arg b "$BACKUP_ID" --arg n "$RESTORED_NAME" '{backupId:$b, newProjectName:$n}')
R=$(curl -s -X POST "$API/api/provision/$PROJECT_ID/backup/restore" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$RREQ")
JOB_ID=$(echo "$R" | jq -r '.id // empty' 2>/dev/null)
# The restored project's id is generated by the platform; the client learns
# it from the restore job.
RESTORED_ID=$(echo "$R" | jq -r '.newProjectId // empty' 2>/dev/null)
[ -n "$JOB_ID" ] && pass "restore job started ($JOB_ID)" || fail "restore" "$R"
[ -n "$RESTORED_ID" ] && pass "restore target id generated ($RESTORED_ID)" || fail "restore" "no newProjectId on the job: $R"
JS=""
for i in $(seq 1 90); do
  JS=$(curl -s "$API/api/provision/$PROJECT_ID/backup/restore/$JOB_ID" -H "Authorization: Bearer $TOKEN" | jq -r '.status' 2>/dev/null)
  case "$JS" in COMPLETED) break;; FAILED) break;; esac
  sleep 2
done
if [ "$JS" = "COMPLETED" ]; then
  pass "restore job COMPLETED"
else
  fail "restore job" "status=$JS $(curl -s "$API/api/provision/$PROJECT_ID/backup/restore/$JOB_ID" -H "Authorization: Bearer $TOKEN" | jq -r '.failureReason // empty')"
fi

echo "18. Restored project is registered on the API"
R=$(curl -s "$API/api/provision/$RESTORED_ID/" -H "Authorization: Bearer $TOKEN")
[ "$(echo "$R" | jq -r '.status' 2>/dev/null)" = "ACTIVE" ] && pass "restored project ACTIVE" || fail "restored project" "$R"
[ "$(echo "$R" | jq -r '.restoredFromProjectId' 2>/dev/null)" = "$PROJECT_ID" ] \
  && pass "restore provenance recorded" || fail "provenance" "$R"

echo "19. Query the restored project through the API"
# The server runs on the host here, so it cannot resolve the restored
# container by name the way the in-network AIO deployment does. Point the
# schema plane at the container's address and restart: everything else
# (credentials, database, user) still comes from vault, which is exactly what
# this leg is proving exists.
RESTORED_CONTAINER="excalibase-$RESTORED_ID-postgres"
RESTORED_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$RESTORED_CONTAINER" 2>/dev/null)
stop_server
SCHEMA_DB_HOST="$RESTORED_IP" SCHEMA_DB_PORT=5432 start_server \
  && pass "server restarted against the restored container" \
  || { fail "server restart" "not healthy"; tail -40 "$DATA_DIR/server.log"; }
GOT=$(curl -s -X POST "$API/api/schema/$RESTORED_ID/query" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg q "SELECT count(*) FROM restore_drill WHERE marker = '$MARKER'" '{query:$q}')")
[ "$(echo "$GOT" | jq -r '.rows[0][0]' 2>/dev/null)" = "1" ] \
  && pass "marker survived the restore and is readable through the API" \
  || fail "restored query" "$GOT"

# --- Step 20: Deprovision both projects ---
echo "20. Deprovision projects"
R=$(curl -s -X DELETE "$API/api/provision/$PROJECT_ID/" \
  -H "Authorization: Bearer $TOKEN")
echo "$R" | grep -qi "deleted\|deprovisioned\|success" && pass "deprovisioned source" || fail "deprovision" "$R"
R=$(curl -s -X DELETE "$API/api/provision/$RESTORED_ID/" \
  -H "Authorization: Bearer $TOKEN")
echo "$R" | grep -qi "deleted\|deprovisioned\|success" && pass "deprovisioned restored project" || fail "deprovision restored" "$R"

# Verify container removed
sleep 2
docker ps --format '{{.Names}}' | grep -q "$CONTAINER_NAME" 2>/dev/null && fail "container cleanup" "still running" || pass "container cleaned up"

echo ""
echo "============================="
echo "RESULTS: $PASS passed, $FAIL failed"
echo "============================="
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
