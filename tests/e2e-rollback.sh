#!/bin/bash
# E2E test for provisioning validation + failure surfacing.
# Verifies that invalid requests are rejected at stage 1 (VALIDATING) with
# a proper error message, BEFORE any K8s or vault side effects.
#
# Rollback for post-validation failures is covered by unit tests
# (internal/service/provisioning_test.go), since triggering a mid-pipeline
# failure in a live cluster reliably requires debug hooks we don't ship.

NS="excalibase-platform"
PASS=0
FAIL=0

pass() { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1: $2"; FAIL=$((FAIL+1)); }

cleanup() { pkill -f "port-forward" 2>/dev/null || true; }
trap cleanup EXIT
cleanup

kubectl -n $NS port-forward svc/provisioning 24005:24005 > /dev/null 2>&1 &
sleep 4

# Login
TOKEN=$(curl -s http://localhost:24005/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | jq -r '.token')
if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
  echo "  ✗ login failed — is the admin password 'admin123'? run e2e-fresh.sh first"
  exit 1
fi

ORG_ID=$(curl -s http://localhost:24005/api/orgs/ \
  -H "Authorization: Bearer $TOKEN" | jq -r '.[0].id')

echo ""
echo "===== PROVISIONING VALIDATION E2E ====="
echo ""

# 1. Empty project name — should be rejected at stage 1
echo "1. Empty project name rejected"
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\"}")
echo "$R" | grep -q "project name" && pass "empty project name rejected" || fail "empty project name" "$R"

# 2. Empty org id
echo "2. Empty org id rejected"
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectName":"valid","orgId":"","databaseType":"POSTGRESQL"}')
echo "$R" | grep -q "org id" && pass "empty org id rejected" || fail "empty org id" "$R"

# 3. Invalid postgres version — fail fast, no namespace created
echo "3. Invalid postgres version rejected"
BEFORE_NS=$(kubectl get ns -o name 2>/dev/null | wc -l)
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"bad-ver\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"9.2\"}")
AFTER_NS=$(kubectl get ns -o name 2>/dev/null | wc -l)
echo "$R" | grep -q "postgres version" && pass "pg 9.2 rejected (message)" || fail "pg version" "$R"
[ "$BEFORE_NS" -eq "$AFTER_NS" ] && pass "no namespace created on validation failure" || fail "ns leak" "before=$BEFORE_NS after=$AFTER_NS"

# 4. Backup retention out of range
echo "4. Backup retention > 365 rejected"
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"bad-backup\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"backup\":{\"enabled\":true,\"retention\":9999}}")
echo "$R" | grep -q "retention" && pass "retention 9999 rejected" || fail "retention" "$R"

# 5. Valid postgres version still works
echo "5. Valid postgres version 17 accepted"
PROJECT="rb-check-$(date +%s)"
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"$PROJECT\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"17\"}")
PROJECT_ID=$(echo "$R" | jq -r '.projectId')
if [[ "$PROJECT_ID" == proj-* ]]; then
  pass "valid request accepted (ref=$PROJECT_ID)"
  # Clean up
  curl -s -X DELETE "http://localhost:24005/api/provision/$PROJECT_ID" \
    -H "Authorization: Bearer $TOKEN" > /dev/null
else
  fail "valid pg 17 rejected" "$R"
fi

echo ""
echo "============================="
echo "TOTAL: $PASS passed, $FAIL failed"
echo "============================="
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
