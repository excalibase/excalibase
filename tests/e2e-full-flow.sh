#!/bin/bash

NS="excalibase-platform"
PROJECT="e2e-$(date +%s)"
PASS=0
FAIL=0

pass() { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1: $2"; FAIL=$((FAIL+1)); }

# Start port-forwards
cleanup() { pkill -f "port-forward" 2>/dev/null || true; }
trap cleanup EXIT
cleanup

kubectl -n $NS port-forward svc/provisioning 24005:24005 > /dev/null 2>&1 &
kubectl -n $NS port-forward svc/auth 24000:24000 > /dev/null 2>&1 &
kubectl -n $NS port-forward svc/graphql-excalibase-graphql 10000:10000 > /dev/null 2>&1 &
kubectl -n $NS port-forward svc/vault 24010:24010 > /dev/null 2>&1 &
sleep 5

echo "===== E2E Full Flow Test ====="
echo ""

# TEST 1: Config
echo "1. Config endpoint"
R=$(curl -s http://localhost:24005/api/config)
echo "$R" | jq -r '.deploymentMode' | grep -q selfhosted && pass "selfhosted mode" || fail "config" "$R"

# TEST 2: Vault
echo "2. Vault status"
R=$(curl -s http://localhost:24010/status)
echo "$R" | jq -r '.sealed' | grep -q false && pass "vault unsealed" || fail "vault" "$R"

# TEST 3: Auth JWKS
echo "3. Auth JWKS"
R=$(curl -s http://localhost:24000/.well-known/jwks.json)
echo "$R" | jq -r '.keys[0].kty' | grep -q EC && pass "JWKS ES256" || fail "jwks" "$R"

# TEST 4: Login
echo "4. Platform login"
TOKEN=$(curl -s http://localhost:24005/api/auth/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin123"}' | jq -r '.token')
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && pass "admin login" || fail "login" "token=$TOKEN"

# TEST 5: Provision
echo "5. Provision project (selfhosted, no tier limit)"
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"$PROJECT\",\"orgId\":\"a06b19b4-9599-41c6-95f2-dae79f60de9a\",\"databaseType\":\"POSTGRESQL\",\"tier\":\"FREE\"}")
echo "$R" | jq -r '.projectId' | grep -q $PROJECT && pass "provision project" || fail "provision" "$R"

# TEST 6: Wait for credentials in vault
echo "6. Vault credentials (waiting 30s for role creation...)"
sleep 30
R=$(curl -s "http://localhost:24010/secrets-list?prefix=projects/duc-corp/$PROJECT/" -H "Authorization: Bearer vault-service-token")
COUNT=$(echo "$R" | jq '.paths | length')
[ "$COUNT" -ge 3 ] && pass "3 credential paths" || fail "vault creds ($COUNT paths)" "$R"

# TEST 7: Auth register
echo "7. Auth register user"
R=$(curl -s -X POST "http://localhost:24000/auth/duc-corp/$PROJECT/register" \
  -H 'Content-Type: application/json' \
  -d '{"email":"flow@test.com","password":"secret123","fullName":"Flow User"}')
JWT=$(echo "$R" | jq -r '.accessToken')
[ -n "$JWT" ] && [ "$JWT" != "null" ] && pass "register + JWT" || fail "register" "$R"

# TEST 8: Validate JWT
echo "8. Validate JWT"
R=$(curl -s -X POST "http://localhost:24000/auth/duc-corp/$PROJECT/validate" \
  -H 'Content-Type: application/json' -d "{\"token\":\"$JWT\"}")
echo "$R" | jq -r '.valid' | grep -q true && pass "JWT valid" || fail "validate" "$R"

# TEST 9: GraphQL with JWT
echo "9. GraphQL introspection"
R=$(curl -s -X POST http://localhost:10000/graphql -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $JWT" \
  -d '{"query":"{ __schema { queryType { name } } }"}')
echo "$R" | jq -r '.data.__schema.queryType.name' | grep -q Query && pass "graphql introspection" || fail "graphql" "$R"

echo ""
echo "============================="
echo "RESULTS: $PASS passed, $FAIL failed"
echo "============================="
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
