#!/bin/bash
NS="excalibase-platform"
PROJECT="e2e-$(date +%s)"
PASS=0
FAIL=0

pass() { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1: $2"; FAIL=$((FAIL+1)); }

cleanup() { pkill -f "port-forward" 2>/dev/null || true; }
trap cleanup EXIT
cleanup

kubectl -n $NS port-forward svc/provisioning 24005:24005 > /dev/null 2>&1 &
kubectl -n $NS port-forward svc/auth 24000:24000 > /dev/null 2>&1 &
kubectl -n $NS port-forward svc/graphql-excalibase-graphql 10000:10000 > /dev/null 2>&1 &
kubectl -n $NS port-forward svc/vault 24010:24010 > /dev/null 2>&1 &
sleep 5

# Set a known admin password via SQL (argon2id hash of "admin123")
ARGON_HASH='$argon2id$v=19$m=65536,t=3,p=4$v+JsXj2jpVvAVjQj4GZyrA$CRDttmrCAHuPbr+sEA13dE247P2+tEaFq1u6BirM98I'
kubectl -n $NS exec platform-db-1 -- psql -U postgres -d platform -c "UPDATE users SET password_hash = '${ARGON_HASH}' WHERE username = 'admin';" > /dev/null 2>&1
ADMIN_PASS="admin123"
echo "Admin password set to: admin123"

# Ensure selfhosted mode
kubectl -n $NS set env deployment/provisioning DEPLOYMENT_MODE=selfhosted > /dev/null 2>&1
kubectl -n $NS rollout status deployment/provisioning --timeout=30s > /dev/null 2>&1
sleep 5
pkill -f "port-forward.*24005" 2>/dev/null || true
kubectl -n $NS port-forward svc/provisioning 24005:24005 > /dev/null 2>&1 &
sleep 4

echo ""
echo "===== SELFHOSTED MODE E2E (Fresh Setup) ====="
echo ""

# 1. Config
echo "1. Config endpoint"
R=$(curl -s http://localhost:24005/api/config)
echo "$R" | jq -r '.deploymentMode' | grep -q selfhosted && pass "selfhosted mode" || fail "config" "$R"

# 2. Vault
echo "2. Vault status"
R=$(curl -s http://localhost:24010/status)
echo "$R" | jq -r '.sealed' | grep -q false && pass "vault unsealed" || fail "vault" "$R"

# 3. JWKS
echo "3. Auth JWKS"
R=$(curl -s http://localhost:24000/.well-known/jwks.json)
echo "$R" | jq -r '.keys[0].kty' | grep -q EC && pass "JWKS ES256" || fail "jwks" "$R"

# 4. Login (fresh admin with generated password)
echo "4. Platform login (fresh admin)"
TOKEN=$(curl -s http://localhost:24005/api/auth/login -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" | jq -r '.token')
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && pass "admin login (fresh bootstrap)" || fail "login" "token=$TOKEN"

# 5. Default org exists
echo "5. Default org bootstrapped"
R=$(curl -s http://localhost:24005/api/orgs/ -H "Authorization: Bearer $TOKEN")
SLUG=$(echo "$R" | jq -r '.[0].slug')
ORG_ID=$(echo "$R" | jq -r '.[0].id')
[ "$SLUG" = "default" ] && pass "default org (slug=default)" || fail "default org" "$R"

# 6. Provision — server generates opaque project ref, display name is free-form
echo "6. Provision project (display name '$PROJECT')"
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"$PROJECT\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"tier\":\"FREE\"}")
PROJECT_ID=$(echo "$R" | jq -r '.projectId')
[ -n "$PROJECT_ID" ] && [[ "$PROJECT_ID" == proj-* ]] && pass "provision project (ref=$PROJECT_ID)" || fail "provision" "$R"

# 6b. Validation rejects bad postgres version (stage 1 failure, no K8s side effects)
echo "6b. Validation: reject invalid postgres version"
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"bad-ver\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"tier\":\"FREE\",\"postgresVersion\":\"9.2\"}")
echo "$R" | grep -q "postgres version" && pass "validation rejects pg 9.2" || fail "validation" "$R"

# 7. Vault credentials (now keyed by generated ref, not display name)
echo "7. Vault credentials (waiting 30s for role creation...)"
sleep 30
R=$(curl -s "http://localhost:24010/secrets-list?prefix=projects/default/$PROJECT_ID/" -H "Authorization: Bearer vault-service-token")
COUNT=$(echo "$R" | jq '.paths | length')
[ "$COUNT" -ge 3 ] && pass "3 credential paths in vault" || fail "vault creds ($COUNT)" "$R"

# 8. Auth register — path uses generated projectId (opaque ref)
echo "8. Auth register"
R=$(curl -s -X POST "http://localhost:24000/auth/default/$PROJECT_ID/register" \
  -H 'Content-Type: application/json' \
  -d '{"email":"fresh@test.com","password":"secret123","fullName":"Fresh User"}')
JWT=$(echo "$R" | jq -r '.accessToken')
[ -n "$JWT" ] && [ "$JWT" != "null" ] && pass "register + JWT" || fail "register" "$R"

# 9. Validate
echo "9. Validate JWT"
R=$(curl -s -X POST "http://localhost:24000/auth/default/$PROJECT_ID/validate" \
  -H 'Content-Type: application/json' -d "{\"token\":\"$JWT\"}")
echo "$R" | jq -r '.valid' | grep -q true && pass "JWT valid" || fail "validate" "$R"

# Refresh port-forwards
pkill -f "port-forward.*10000" 2>/dev/null || true
kubectl -n $NS port-forward svc/graphql-excalibase-graphql 10000:10000 > /dev/null 2>&1 &
sleep 3

# 10. GraphQL (retry once — first request triggers tenant schema load)
echo "10. GraphQL introspection"
curl -s -X POST http://localhost:10000/graphql -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $JWT" \
  -d '{"query":"{ __schema { queryType { name } } }"}' > /dev/null 2>&1
sleep 2
R=$(curl -s -X POST http://localhost:10000/graphql -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $JWT" \
  -d '{"query":"{ __schema { queryType { name } } }"}')
echo "$R" | jq -r '.data.__schema.queryType.name' | grep -q Query && pass "graphql works" || fail "graphql" "$R"

echo ""
echo "============================="
echo "SELFHOSTED: $PASS passed, $FAIL failed"
echo "============================="

# --- CLOUD MODE ---
echo ""
echo "===== CLOUD MODE E2E ====="
echo ""

# Switch to cloud mode
kubectl -n $NS set env deployment/provisioning DEPLOYMENT_MODE=cloud > /dev/null 2>&1
kubectl -n $NS rollout status deployment/provisioning --timeout=30s > /dev/null 2>&1
sleep 5

# Re-establish port-forward (provisioning restarted)
pkill -f "port-forward.*24005" 2>/dev/null || true
kubectl -n $NS port-forward svc/provisioning 24005:24005 > /dev/null 2>&1 &
sleep 4

CLOUD_PASS=0
CLOUD_FAIL=0
cpass() { echo "  ✓ $1"; CLOUD_PASS=$((CLOUD_PASS+1)); }
cfail() { echo "  ✗ $1: $2"; CLOUD_FAIL=$((CLOUD_FAIL+1)); }

# C1. Cloud mode config
echo "C1. Config endpoint"
R=$(curl -s http://localhost:24005/api/config)
echo "$R" | jq -r '.deploymentMode' | grep -q cloud && cpass "cloud mode" || cfail "config" "$R"

# C2. Login
echo "C2. Platform login"
TOKEN=$(curl -s http://localhost:24005/api/auth/login -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" | jq -r '.token')
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && cpass "admin login" || cfail "login" "token=$TOKEN"

# C3. Create a fresh STANDARD org for cloud tests
echo "C3. Create STANDARD org for cloud"
CLOUD_ORG_ID=$(curl -s -X POST http://localhost:24005/api/orgs/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Cloud Corp","slug":"cloud-corp"}' | jq -r '.id')
kubectl -n $NS exec platform-db-1 -- psql -U postgres -d platform -c "UPDATE orgs SET tier = 'STANDARD' WHERE id = '$CLOUD_ORG_ID';" > /dev/null 2>&1
[ -n "$CLOUD_ORG_ID" ] && [ "$CLOUD_ORG_ID" != "null" ] && cpass "STANDARD org created" || cfail "create org" "$CLOUD_ORG_ID"

# C4. K8s provisioning in cloud mode
CLOUD_PROJECT="cloud-$(date +%s)"
echo "C4. K8s provisioning (cloud mode, display name '$CLOUD_PROJECT')"
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"$CLOUD_PROJECT\",\"orgId\":\"$CLOUD_ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"tier\":\"STANDARD\"}")
CLOUD_PROJECT_ID=$(echo "$R" | jq -r '.projectId')
[ -n "$CLOUD_PROJECT_ID" ] && [[ "$CLOUD_PROJECT_ID" == proj-* ]] && cpass "k8s provision in cloud (ref=$CLOUD_PROJECT_ID)" || cfail "k8s provision" "$R"

# C5. Wait for credentials
echo "C5. Cloud vault credentials (waiting 30s...)"
sleep 30
R=$(curl -s "http://localhost:24010/secrets-list?prefix=projects/cloud-corp/$CLOUD_PROJECT_ID/" -H "Authorization: Bearer vault-service-token")
COUNT=$(echo "$R" | jq '.paths | length')
[ "$COUNT" -ge 3 ] && cpass "cloud project creds in vault ($COUNT)" || cfail "cloud vault creds ($COUNT)" "$R"

# C6. Tier enforcement (downgrade back to FREE)
kubectl -n $NS exec platform-db-1 -- psql -U postgres -d platform -c "UPDATE orgs SET tier = 'FREE' WHERE slug = 'default';" > /dev/null 2>&1
echo "C6. Tier enforcement (FREE limit=1, now has 2 projects)"
R=$(curl -s -X POST http://localhost:24005/api/provision/ \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"cloud-blocked\",\"orgId\":\"$ORG_ID\",\"databaseType\":\"POSTGRESQL\",\"tier\":\"FREE\"}")
echo "$R" | grep -q "reached the maximum" && cpass "tier enforcement blocks over-limit" || cfail "tier" "$R"

# C7. BYOC still works in cloud
echo "C7. BYOC provision (display name 'byoc-test')"
R=$(curl -s -X POST http://localhost:24005/api/provision/byoc \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"byoc-test\",\"orgId\":\"$ORG_ID\",\"host\":\"external-db.example.com\",\"port\":5432,\"database\":\"mydb\",\"username\":\"user\",\"password\":\"pass\"}")
BYOC_PROJECT_ID=$(echo "$R" | jq -r '.projectId')
[ -n "$BYOC_PROJECT_ID" ] && [[ "$BYOC_PROJECT_ID" == proj-* ]] && cpass "BYOC provision (ref=$BYOC_PROJECT_ID)" || cfail "byoc" "$R"

# C5. Vault has BYOC credentials
echo "C8. BYOC credentials in vault"
R=$(curl -s "http://localhost:24010/secrets-list?prefix=projects/default/$BYOC_PROJECT_ID/" -H "Authorization: Bearer vault-service-token")
COUNT=$(echo "$R" | jq '.paths | length')
[ "$COUNT" -ge 1 ] && cpass "BYOC creds in vault" || cfail "byoc vault ($COUNT)" "$R"

echo ""
echo "============================="
echo "CLOUD: $CLOUD_PASS passed, $CLOUD_FAIL failed"
echo "============================="

TOTAL_PASS=$((PASS + CLOUD_PASS))
TOTAL_FAIL=$((FAIL + CLOUD_FAIL))
echo ""
echo "============================="
echo "TOTAL: $TOTAL_PASS passed, $TOTAL_FAIL failed"
echo "============================="
[ "$TOTAL_FAIL" -eq 0 ] && exit 0 || exit 1
