#!/usr/bin/env bash
# Run a local SonarQube scan against the same engine SonarCloud uses.
#
#   ./scripts/sonar-local.sh
#
# Spins up SonarQube Community 26.x in Docker, generates a scanner
# token via the API, and runs sonar-scanner-cli 8.x (latest) against it.
# The container persists across runs (named `sonarqube-local`) so
# subsequent scans are fast.
#
# Open http://localhost:9000 to see the dashboard.
# Default admin password is in $LOCAL_ADMIN_PWD below — for local use only.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONTAINER=sonarqube-local
LOCAL_ADMIN_PWD="LocalDev$(date +%s)!"  # rotated each run for first-time login

# --- 1. Start SonarQube if not running ---
if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER}$"; then
  if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER}$"; then
    echo "[sonar-local] Resuming existing container ${CONTAINER}..."
    docker start "${CONTAINER}" >/dev/null
  else
    echo "[sonar-local] Pulling sonarqube:community (~26.x)..."
    docker pull sonarqube:community
    echo "[sonar-local] Starting fresh container ${CONTAINER}..."
    docker run -d --name "${CONTAINER}" \
      -p 9000:9000 \
      -e SONAR_ES_BOOTSTRAP_CHECKS_DISABLE=true \
      sonarqube:community >/dev/null
  fi
fi

# --- 2. Wait for SonarQube to come up ---
echo "[sonar-local] Waiting for SonarQube /api/system/status..."
until curl -sf "http://localhost:9000/api/system/status" 2>/dev/null | grep -q '"status":"UP"'; do
  sleep 5
done
echo "[sonar-local] SonarQube is UP."

# --- 3. Generate scanner token ---
# First scan ever needs the admin password change. Idempotent: if the
# password is already changed, the change_password call returns 401 and
# we fall back to a token store on disk.
TOKEN_FILE="${REPO_ROOT}/.sonar-local-token"
if [[ ! -f "${TOKEN_FILE}" ]]; then
  echo "[sonar-local] First-time setup: rotating admin password + generating token..."
  curl -sf -u admin:admin -X POST "http://localhost:9000/api/users/change_password" \
    -d "login=admin&previousPassword=admin&password=${LOCAL_ADMIN_PWD}" >/dev/null || true
  TOKEN_JSON=$(curl -sf -u "admin:${LOCAL_ADMIN_PWD}" -X POST \
    "http://localhost:9000/api/user_tokens/generate" \
    -d "name=local-$(date +%s)&type=GLOBAL_ANALYSIS_TOKEN")
  echo "${TOKEN_JSON}" | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])" > "${TOKEN_FILE}"
  echo "[sonar-local] Token saved to ${TOKEN_FILE} (gitignored)."
fi
SONAR_TOKEN=$(cat "${TOKEN_FILE}")

# --- 4. Run scanner ---
echo "[sonar-local] Running sonar-scanner-cli (latest, 8.x)..."
docker run --rm \
  --network host \
  -v "${REPO_ROOT}:/usr/src" \
  -e SONAR_HOST_URL="http://localhost:9000" \
  -e SONAR_TOKEN="${SONAR_TOKEN}" \
  sonarsource/sonar-scanner-cli:latest \
  -Dsonar.organization=

echo ""
echo "[sonar-local] Done. Dashboard: http://localhost:9000/dashboard?id=excalibase"
echo "[sonar-local] Stop the server: docker stop ${CONTAINER}"
echo "[sonar-local] Wipe state: docker rm -f ${CONTAINER} && rm ${TOKEN_FILE}"
