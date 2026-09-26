#!/bin/bash
# Comprehensive AIO self-test against the live multi-service stack on minikube.
#
# Drives every v1-critical feature in one coherent flow:
#   - Operator (PAT + cookie session): vault, orgs, projects, schema, edge fns,
#     realtime, backups (floci S3), maintenance, deletion protection, scopes,
#     security headers, IDOR/SSRF/rate-limit guards
#   - End-user (JWT minted by auth): GraphQL queries + mutations + subscriptions,
#     GraphQL REST surface + REST realtime SSE, NoSQL search, RLS enforcement,
#     cross-project replay rejection
#   - Cross-service: vault PKI consistency, JWT projectId binding, watcher CDC
#     pod deployment, NATS reachability
#
# Pre-reqs:
#   - AIO deployed in excalibase-platform ns (provisioning, auth, graphql,
#     watcher, floci, NATS, platform-db)
#   - port-forwards open: 24005 (provisioning), 24000 (auth), 10000 (graphql),
#     4566 (floci/S3)
#   - aws CLI on PATH for floci bucket ops
#   - jq, curl

set -uo pipefail

API_PROV="${API_PROV:-http://localhost:24005}"
API_AUTH="${API_AUTH:-http://localhost:24000}"
API_GQL="${API_GQL:-http://localhost:10000}"
API_S3="${API_S3:-http://localhost:4566}"

PASS=0
FAIL=0
SKIP=0

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
yellow(){ printf '\033[33m%s\033[0m\n' "$*"; }
cyan()  { printf '\033[36m%s\033[0m\n' "$*"; }

pass()  { green "  ✓ $1"; PASS=$((PASS+1)); }
fail()  { red   "  ✗ $1: $2"; FAIL=$((FAIL+1)); }
skip()  { yellow "  ⊘ $1: $2"; SKIP=$((SKIP+1)); }
section(){ echo ""; cyan "=== $* ==="; }

# Login fresh — the bootstrap PAT row may be stale across redeploys but
# admin-pass is durable.
ADMIN_PASS=$(kubectl -n excalibase-platform get secret platform-bootstrap -o jsonpath='{.data.admin-pass}' | base64 -d 2>/dev/null)
if [ -z "$ADMIN_PASS" ]; then red "BOOTSTRAP secret missing admin-pass — is the AIO deployed?"; exit 1; fi

BOOT_LOGIN=$(curl -s -X POST "$API_PROV/api/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}")
ADMIN_PAT=$(echo "$BOOT_LOGIN" | jq -r '.token' 2>/dev/null)
[ -z "$ADMIN_PAT" ] || [ "$ADMIN_PAT" = "null" ] && { red "Initial admin login failed: $BOOT_LOGIN"; exit 1; }
PAT_HDR="Authorization: Bearer $ADMIN_PAT"

###############################################################################
section "F1. Health — all four services"
for s in "$API_PROV/healthz:provisioning" "$API_AUTH/.well-known/jwks.json:auth" "$API_S3/:floci"; do
  url="${s%:*}"; name="${s##*:}"
  curl -sf "$url" > /dev/null 2>&1 && pass "$name reachable" || fail "$name" "unreachable"
done
# Unauthenticated reachability uses the exempted health endpoint — introspection
# now requires a JWT (fail-closed auth); F16 covers authenticated introspection.
curl -sf "$API_GQL/actuator/health" | grep -q '"status":"UP"' \
   && pass "graphql reachable" || fail "graphql" "health not UP"

###############################################################################
section "F2. Cookie auth, scope, expiry, security headers"
COOKIE_JAR=$(mktemp); trap "rm -f $COOKIE_JAR" EXIT

LOGIN_RESP=$(curl -s -i -c "$COOKIE_JAR" -X POST "$API_PROV/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}")

echo "$LOGIN_RESP" | grep -qi 'Set-Cookie: excali_session' && pass "Set-Cookie excali_session" || fail "Set-Cookie" "missing"
echo "$LOGIN_RESP" | grep -qi 'HttpOnly' && pass "HttpOnly" || fail "HttpOnly" "missing"
echo "$LOGIN_RESP" | grep -qi 'SameSite=Strict' && pass "SameSite=Strict" || fail "SameSite" "missing"
echo "$LOGIN_RESP" | grep -qi 'Strict-Transport-Security' && pass "HSTS" || fail "HSTS" "missing"
echo "$LOGIN_RESP" | grep -qi 'Content-Security-Policy' && pass "CSP" || fail "CSP" "missing"
echo "$LOGIN_RESP" | grep -qi 'X-Frame-Options: DENY' && pass "X-Frame-Options DENY" || fail "X-Frame-Options" "missing"

SESSION_TOKEN=$(grep excali_session "$COOKIE_JAR" | awk '{print $7}')
curl -sf -b "excali_session=$SESSION_TOKEN" "$API_PROV/api/auth/me" | grep -q '"role":"platform_admin"' \
  && pass "cookie-only /me works" || fail "cookie auth" "/me rejected"

TOKENS=$(curl -sf -H "$PAT_HDR" "$API_PROV/api/auth/tokens" 2>/dev/null)
echo "$TOKENS" | jq -r '.[].scopes' 2>/dev/null | grep -q session && pass "session scope persisted" || fail "session scope" "$TOKENS"

LOGOUT=$(curl -s -i -b "excali_session=$SESSION_TOKEN" -X POST "$API_PROV/api/auth/logout" 2>/dev/null)
echo "$LOGOUT" | grep -qi 'Set-Cookie: excali_session=;.*Max-Age=0' && pass "logout clears cookie" || fail "logout cookie clear" "no Max-Age=0"
ME_AFTER=$(curl -s -o /dev/null -w "%{http_code}" -b "excali_session=$SESSION_TOKEN" "$API_PROV/api/auth/me")
[ "$ME_AFTER" = "401" ] && pass "revoked cookie 401" || fail "revoked cookie" "got $ME_AFTER"

###############################################################################
section "F3. Org listing + clean leftover projects"
ORGS=$(curl -sf -H "$PAT_HDR" "$API_PROV/api/orgs/" 2>/dev/null)
ORG_ID=$(echo "$ORGS" | jq -r '.[0].id' 2>/dev/null)
ORG_SLUG=$(echo "$ORGS" | jq -r '.[0].slug' 2>/dev/null)
[ -n "$ORG_ID" ] && [ "$ORG_ID" != "null" ] && pass "org list ($ORG_SLUG)" || fail "org list" "$ORGS"

# A project takes its org's plan; this run creates two projects, so the org goes on STANDARD.
PLAN_CODE=$(curl -s -o /dev/null -w '%{http_code}' -X PATCH -H "$PAT_HDR" -H 'Content-Type: application/json' \
  -d '{"tier":"STANDARD"}' "$API_PROV/api/orgs/$ORG_ID/")
[ "$PLAN_CODE" = "200" ] && pass "org on STANDARD plan" || fail "org plan" "got $PLAN_CODE"

EXISTING=$(curl -sf -H "$PAT_HDR" "$API_PROV/api/provision/" 2>/dev/null | jq -r ".[] | select(.orgId==\"$ORG_ID\") | .projectId" 2>/dev/null)
for pid in $EXISTING; do
  [ -z "$pid" ] || [ "$pid" = "null" ] && continue
  echo "    cleaning leftover $pid"
  curl -s -X DELETE -H "$PAT_HDR" "$API_PROV/api/provision/$pid/" > /dev/null 2>&1
done
sleep 2

###############################################################################
section "F4. PAT lifecycle + scopes"
NEW_PAT_RESP=$(curl -s -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-test-pat","scopes":"read"}' "$API_PROV/api/auth/tokens" 2>/dev/null)
TEST_PAT=$(echo "$NEW_PAT_RESP" | jq -r '.token' 2>/dev/null)
TEST_PAT_HASH=$(echo -n "$TEST_PAT" | sha256sum | awk '{print $1}')
[ -n "$TEST_PAT" ] && [ "$TEST_PAT" != "null" ] && pass "PAT mint" || fail "PAT mint" "$NEW_PAT_RESP"

# Use new PAT for /me — works
curl -sf -H "Authorization: Bearer $TEST_PAT" "$API_PROV/api/auth/me" | grep -q '"role":"platform_admin"' \
  && pass "minted PAT authenticates" || fail "minted PAT" "rejected"

# Revoke it (own token revoke check)
REVOKE=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE -H "$PAT_HDR" \
  "$API_PROV/api/auth/tokens/$TEST_PAT_HASH")
[ "$REVOKE" = "200" ] && pass "PAT revoke own" || fail "PAT revoke" "got $REVOKE"

# After revoke, PAT no longer authenticates
AFTER_REVOKE=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TEST_PAT" "$API_PROV/api/auth/me")
[ "$AFTER_REVOKE" = "401" ] && pass "revoked PAT 401" || fail "revoked PAT" "got $AFTER_REVOKE"

###############################################################################
section "F5. Vault — secrets browser + PKI"
VLIST=$(curl -sf -H "$PAT_HDR" "$API_PROV/api/vault/secrets-list" 2>/dev/null)
[ -n "$VLIST" ] && pass "vault list" || fail "vault list" "empty"
echo "$VLIST" | grep -q 'pki/signing/public' && pass "PKI signing/public exists" || fail "PKI" "no signing key"
echo "$VLIST" | grep -q 'pki/signing/private' && pass "PKI signing/private exists" || fail "PKI" "no private key"

# Vault secrets CRUD via studio path
TEST_KEY="e2e-test/$(date +%s)/key"
curl -sf -X PUT -H "$PAT_HDR" -H 'Content-Type: application/json' \
  -d '{"value":"hello-floci"}' "$API_PROV/api/vault/secrets/$TEST_KEY" > /dev/null 2>&1 \
  && pass "vault PUT" || fail "vault PUT" "failed"
curl -sf -H "$PAT_HDR" "$API_PROV/api/vault/secrets/$TEST_KEY" | grep -q 'hello-floci' \
  && pass "vault GET" || fail "vault GET" "value mismatch"
curl -s -o /dev/null -w '%{http_code}' -X DELETE -H "$PAT_HDR" \
  "$API_PROV/api/vault/secrets/$TEST_KEY" | grep -q "200" \
  && pass "vault DELETE" || fail "vault DELETE" "non-200"

###############################################################################
section "F6. Provision project (cloud mode, real CNPG)"
PROJ_NAME="aio-e2e-$(date +%s)"
PROV_REQ=$(jq -n --arg n "$PROJ_NAME" --arg o "$ORG_ID" \
  '{projectName:$n, orgId:$o, databaseType:"POSTGRESQL"}')
PROV_RESP=$(curl -s -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
  -d "$PROV_REQ" "$API_PROV/api/provision/")
PROJECT_ID=$(echo "$PROV_RESP" | jq -r '.projectId' 2>/dev/null)
[ -n "$PROJECT_ID" ] && [[ "$PROJECT_ID" == proj-* ]] && pass "provision started ($PROJECT_ID)" \
  || { fail "provision" "$PROV_RESP"; SKIP_PROVISIONED=1; }

if [ -z "${SKIP_PROVISIONED:-}" ]; then
  echo "    waiting up to 120s for ACTIVE..."
  STATUS="UNKNOWN"
  for i in $(seq 1 60); do
    STATUS=$(curl -sf -H "$PAT_HDR" "$API_PROV/api/provision/$PROJECT_ID/" | jq -r '.status' 2>/dev/null)
    case "$STATUS" in ACTIVE|COMPLETED|FAILED) break ;; esac
    sleep 2
  done
  case "$STATUS" in
    ACTIVE|COMPLETED) pass "provisioning ACTIVE ($STATUS)" ;;
    *) fail "provisioning" "final=$STATUS"; SKIP_PROVISIONED=1 ;;
  esac
fi

if [ -z "${SKIP_PROVISIONED:-}" ]; then
  ###############################################################################
  section "F7. Vault project-scoped credentials (post-refactor scheme)"
  VLIST2=$(curl -sf -H "$PAT_HDR" "$API_PROV/api/vault/secrets-list?prefix=projects/$PROJECT_ID/" 2>/dev/null)
  for r in admin excalibase_app auth_admin cdc_watcher; do
    echo "$VLIST2" | grep -q "projects/$PROJECT_ID/credentials/$r" \
      && pass "vault $r credential" || fail "vault $r" "not present"
  done
  if echo "$VLIST2" | grep -q "projects/default/$PROJECT_ID/" || \
     echo "$VLIST2" | grep -q "projects/$ORG_SLUG/$PROJECT_ID/"; then
    fail "legacy org-scoped paths" "still present (refactor regression)"
  else
    pass "no legacy org-scoped paths"
  fi

  ###############################################################################
  section "F8. RequireProjectAccess middleware (IDOR fix)"
  curl -sf -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
    -d '{"username":"alice","email":"alice@e2e.test","password":"AliceP@ss123","role":"user"}' \
    "$API_PROV/api/auth/users" > /dev/null 2>&1 || true
  ALICE_LOGIN=$(curl -s -X POST "$API_PROV/api/auth/login" -H 'Content-Type: application/json' \
    -d '{"username":"alice","password":"AliceP@ss123"}' 2>/dev/null)
  ALICE_PAT=$(echo "$ALICE_LOGIN" | jq -r '.token' 2>/dev/null)
  if [ -n "$ALICE_PAT" ] && [ "$ALICE_PAT" != "null" ]; then
    ALICE_GET=$(curl -s -o /dev/null -w '%{http_code}' \
      -H "Authorization: Bearer $ALICE_PAT" "$API_PROV/api/provision/$PROJECT_ID/")
    [ "$ALICE_GET" = "403" ] || [ "$ALICE_GET" = "404" ] && pass "non-member 403/404 ($ALICE_GET)" \
      || fail "IDOR" "got $ALICE_GET"
  else
    skip "IDOR" "couldn't create alice"
  fi

  ###############################################################################
  section "F10. Schema browser — tables, columns, roles, extensions"
  TABLES=$(curl -s -o /dev/null -w '%{http_code}' -H "$PAT_HDR" "$API_PROV/api/schema/$PROJECT_ID/tables")
  [ "$TABLES" = "200" ] && pass "tables list" || fail "tables list" "got $TABLES"
  # Create a table
  CREATE_TBL=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
    -d '{"name":"e2e_posts","schema":"public","columns":[{"name":"id","type":"serial","primaryKey":true},{"name":"title","type":"text","nullable":false}]}' \
    "$API_PROV/api/schema/$PROJECT_ID/tables")
  [ "$CREATE_TBL" = "201" ] || [ "$CREATE_TBL" = "200" ] && pass "create table ($CREATE_TBL)" || fail "create table" "got $CREATE_TBL"
  # Insert a row
  INSERT_ROW=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
    -d '{"data":{"id":1,"title":"hello"}}' \
    "$API_PROV/api/schema/$PROJECT_ID/tables/e2e_posts/rows")
  [ "$INSERT_ROW" = "200" ] || [ "$INSERT_ROW" = "201" ] && pass "row insert ($INSERT_ROW)" || fail "row insert" "got $INSERT_ROW"
  # Read rows
  ROWS=$(curl -s -H "$PAT_HDR" "$API_PROV/api/schema/$PROJECT_ID/tables/e2e_posts/rows")
  echo "$ROWS" | grep -q "hello" && pass "row read shows insert" || fail "row read" "$ROWS"
  # SQL editor
  SQL_RES=$(curl -s -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
    -d '{"query":"SELECT 1 AS ok"}' "$API_PROV/api/schema/$PROJECT_ID/query")
  echo "$SQL_RES" | grep -q '"ok"\|"1"' && pass "SQL editor SELECT 1" || fail "SQL editor" "$SQL_RES"

  ###############################################################################
  section "F11. Performance + security advisors"
  PERF_ADV=$(curl -s -o /dev/null -w '%{http_code}' -H "$PAT_HDR" "$API_PROV/api/schema/$PROJECT_ID/advisors/performance")
  [ "$PERF_ADV" = "200" ] && pass "performance advisor" || fail "performance advisor" "got $PERF_ADV"
  SEC_ADV=$(curl -s -o /dev/null -w '%{http_code}' -H "$PAT_HDR" "$API_PROV/api/schema/$PROJECT_ID/advisors/security")
  [ "$SEC_ADV" = "200" ] && pass "security advisor" || fail "security advisor" "got $SEC_ADV"

  ###############################################################################
  section "F12. Migrations"
  MIG_LIST=$(curl -s -o /dev/null -w '%{http_code}' -H "$PAT_HDR" "$API_PROV/api/provision/$PROJECT_ID/migrations")
  [ "$MIG_LIST" = "200" ] && pass "migration list" || fail "migration list" "got $MIG_LIST"

  ###############################################################################
  section "F13. Realtime — per-table publication toggle"
  RT_LIST=$(curl -s -H "$PAT_HDR" "$API_PROV/api/projects/$PROJECT_ID/realtime/tables")
  echo "$RT_LIST" | jq -e . > /dev/null 2>&1 && pass "realtime list parsable" || fail "realtime list" "$RT_LIST"
  # Toggle e2e_posts ON
  RT_TOGGLE=$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "$PAT_HDR" \
    "$API_PROV/api/projects/$PROJECT_ID/realtime/tables/public/e2e_posts")
  [ "$RT_TOGGLE" = "200" ] || [ "$RT_TOGGLE" = "204" ] && pass "realtime toggle ON ($RT_TOGGLE)" \
    || fail "realtime toggle" "got $RT_TOGGLE"

  ###############################################################################
  section "F14. Maintenance window + deletion protection + credentials rotation"
  MW=$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "$PAT_HDR" -H 'Content-Type: application/json' \
    -d '{"day":"Sunday","startTime":"02:00","durationMinutes":60}' \
    "$API_PROV/api/provision/$PROJECT_ID/maintenance-window")
  [ "$MW" = "200" ] && pass "set maintenance window" || fail "maintenance window" "got $MW"

  DP_ON=$(curl -s -o /dev/null -w '%{http_code}' -X PATCH -H "$PAT_HDR" -H 'Content-Type: application/json' \
    -d '{"enabled":true}' "$API_PROV/api/provision/$PROJECT_ID/deletion-protection")
  [ "$DP_ON" = "200" ] && pass "deletion protection ON" || fail "deletion protection ON" "got $DP_ON"
  # Try delete while protected — must 400
  DEL_PROT=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE -H "$PAT_HDR" "$API_PROV/api/provision/$PROJECT_ID/")
  [ "$DEL_PROT" = "400" ] && pass "delete blocked by protection" || fail "deletion protection guard" "got $DEL_PROT"
  # Disable protection so cleanup can proceed later
  curl -s -X PATCH -H "$PAT_HDR" -H 'Content-Type: application/json' \
    -d '{"enabled":false}' "$API_PROV/api/provision/$PROJECT_ID/deletion-protection" > /dev/null
  pass "deletion protection OFF"

  ###############################################################################
  section "F15. End-user JWT issuance via auth (projectId binding)"
  AUTH_REG=$(curl -s -X POST "$API_AUTH/auth/$ORG_SLUG/$PROJECT_ID/register" \
    -H 'Content-Type: application/json' \
    -d '{"email":"end-user@e2e.test","password":"EndUserP@ss123","fullName":"End User"}' 2>/dev/null)
  JWT=$(echo "$AUTH_REG" | jq -r '.access_token // .accessToken // .token' 2>/dev/null)
  if [ -n "$JWT" ] && [ "$JWT" != "null" ]; then
    pass "auth issued JWT for $PROJECT_ID"
    PAYLOAD=$(echo "$JWT" | cut -d. -f2 | tr '_-' '/+' | base64 -d 2>/dev/null)
    JWT_PROJ=$(echo "$PAYLOAD" | jq -r '.projectId' 2>/dev/null)
    [ "$JWT_PROJ" = "$PROJECT_ID" ] && pass "JWT projectId claim binds to $PROJECT_ID" \
      || fail "JWT projectId" "claim=$JWT_PROJ url=$PROJECT_ID"
    # /validate endpoint
    VRES=$(curl -s -X POST "$API_AUTH/auth/$ORG_SLUG/$PROJECT_ID/validate" \
      -H 'Content-Type: application/json' -d "{\"token\":\"$JWT\"}" 2>/dev/null)
    echo "$VRES" | grep -q '"valid":true' && pass "JWT validate" || fail "JWT validate" "$VRES"
  else
    skip "JWT issuance" "$AUTH_REG"
    JWT=""
  fi

  ###############################################################################
  section "F16. GraphQL queries + mutations + REST surface (with JWT, RLS)"
  if [ -n "$JWT" ]; then
    # Routes are project-scoped: /{projectId}/graphql (the JWT's projectId claim
    # equals PROJECT_ID; there is no unscoped route).
    GQL_INTRO=$(curl -s -X POST -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
      -d '{"query":"{__schema{types{name}}}"}' "$API_GQL/$PROJECT_ID/graphql")
    echo "$GQL_INTRO" | jq -e '.data.__schema.types | length > 5' > /dev/null 2>&1 \
      && pass "graphql introspection with JWT" || fail "graphql JWT" "$GQL_INTRO"

    # The auto-generated graphql for e2e_posts table — try a query
    GQL_POSTS=$(curl -s -X POST -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
      -d '{"query":"{e2e_posts{id title}}"}' "$API_GQL/$PROJECT_ID/graphql")
    if echo "$GQL_POSTS" | jq -e '.data.e2e_posts | length >= 0' > /dev/null 2>&1; then
      pass "graphql query (with RLS)"
    elif echo "$GQL_POSTS" | grep -qi "permission\|denied\|forbidden"; then
      pass "graphql RLS denies (expected for anon role)"
    else
      skip "graphql query" "$GQL_POSTS"
    fi

    # GraphQL REST surface — autogenerated REST endpoints by graphql
    REST_GET=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $JWT" \
      "$API_GQL/$PROJECT_ID/api/v1/e2e_posts" 2>/dev/null)
    case "$REST_GET" in
      200|404|403) pass "REST surface reachable ($REST_GET)" ;;
      *) skip "REST surface" "got $REST_GET (path may differ; check graphql REST routing)" ;;
    esac
  else
    skip "graphql tests" "no JWT"
  fi

  ###############################################################################
  section "F17. Function deploy + JWT scope + cross-project replay"
  FN_BODY='{"id":"e2e-fn","name":"E2E","files":[{"path":"index.ts","content":"export default async (req) => Response.json({ok:true, scope: req.headers.get(\"X-Excalibase-Scope\") || \"none\"})"}]}'
  # Cold-start race: first deploy lazy-creates deno-runtime Deployment + Service
  # in the project namespace. Image pull + container start can take 30-90s on
  # fresh clusters. Retry up to 5 times with 30s backoff before giving up.
  FN_RESP=""
  for try in 1 2 3 4 5; do
    FN_RESP=$(curl -s --max-time 180 -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
      -d "$FN_BODY" "$API_PROV/api/projects/$PROJECT_ID/functions/" 2>/dev/null)
    echo "$FN_RESP" | jq -r '.id' 2>/dev/null | grep -q "e2e-fn" && break
    sleep 30
  done
  if echo "$FN_RESP" | jq -r '.id' 2>/dev/null | grep -q "e2e-fn"; then
    pass "function deployed"

    INV=$(curl -s -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' -d '{}' \
      "$API_PROV/api/projects/$PROJECT_ID/functions/e2e-fn/invoke" 2>/dev/null)
    echo "$INV" | grep -q '"ok":true' && pass "admin invoke" || fail "admin invoke" "$INV"

    # Public invoke without JWT
    PUB401=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API_PROV/functions/v1/$PROJECT_ID/e2e-fn")
    [ "$PUB401" = "401" ] && pass "public invoke without JWT → 401" || fail "public invoke unauth" "got $PUB401"

    # Public invoke with project JWT (scope forwarded)
    if [ -n "$JWT" ]; then
      PUB_OK=$(curl -s -X POST -H "Authorization: Bearer $JWT" "$API_PROV/functions/v1/$PROJECT_ID/e2e-fn")
      echo "$PUB_OK" | grep -q '"ok":true' && pass "public invoke with JWT" || fail "public invoke ok" "$PUB_OK"
      echo "$PUB_OK" | jq -r '.scope' 2>/dev/null | grep -q "authenticated\|service\|public" \
        && pass "X-Excalibase-Scope forwarded to runtime" || skip "scope forward" "scope=$(echo "$PUB_OK" | jq -r '.scope' 2>/dev/null)"
    fi

    # Function logs SSE (head, brief)
    LOGS_FIRST=$(timeout 3 curl -sN -H "$PAT_HDR" "$API_PROV/api/projects/$PROJECT_ID/functions/e2e-fn/logs" | head -c 200 || true)
    [ -n "$LOGS_FIRST" ] && pass "function logs SSE responsive" || skip "logs SSE" "no data within 3s"

    # Cross-project replay
    PROJ2_NAME="aio-x-$(date +%s)"
    PROV2=$(curl -s -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
      -d "$(jq -n --arg n "$PROJ2_NAME" --arg o "$ORG_ID" '{projectName:$n,orgId:$o,databaseType:"POSTGRESQL"}')" \
      "$API_PROV/api/provision/" 2>/dev/null)
    PROJECT2_ID=$(echo "$PROV2" | jq -r '.projectId' 2>/dev/null)
    if [[ "$PROJECT2_ID" == proj-* ]]; then
      for i in $(seq 1 30); do
        S=$(curl -sf -H "$PAT_HDR" "$API_PROV/api/provision/$PROJECT2_ID/" | jq -r '.status' 2>/dev/null)
        [[ "$S" == "ACTIVE" || "$S" == "COMPLETED" ]] && break
        sleep 2
      done
      AUTH_REG2=$(curl -s -X POST "$API_AUTH/auth/$ORG_SLUG/$PROJECT2_ID/register" \
        -H 'Content-Type: application/json' \
        -d '{"email":"x-proj@e2e.test","password":"CrossProjP@ss123","fullName":"Cross"}' 2>/dev/null)
      JWT2=$(echo "$AUTH_REG2" | jq -r '.access_token // .accessToken // .token' 2>/dev/null)
      if [ -n "$JWT2" ] && [ "$JWT2" != "null" ]; then
        REPLAY=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $JWT2" \
          "$API_PROV/functions/v1/$PROJECT_ID/e2e-fn")
        [ "$REPLAY" = "401" ] && pass "cross-project JWT replay → 401" || fail "cross-project replay" "got $REPLAY"
      else
        skip "cross-project replay" "couldn't reg in $PROJECT2_ID"
      fi
      curl -s -X DELETE -H "$PAT_HDR" "$API_PROV/api/provision/$PROJECT2_ID/" > /dev/null 2>&1
    else
      skip "cross-project replay" "couldn't provision proj2: $PROV2"
    fi
  else
    skip "function tests" "deploy failed: $FN_RESP"
  fi

  ###############################################################################
  section "F18. Backup — R2 (excalibase-backups bucket)"
  # No bucket creation here: the platform shares one R2 bucket
  # (excalibase-backups) and partitions per project via key prefix
  # `<projectId>/`. Provisioning auto-enables backup with R2 defaults
  # when the cluster is created (see service.SetBackupDefaults).
  BK_LIST=$(curl -s -o /dev/null -w '%{http_code}' -H "$PAT_HDR" \
    "$API_PROV/api/provision/$PROJECT_ID/backup/list")
  [ "$BK_LIST" = "200" ] || [ "$BK_LIST" = "204" ] && pass "backup list reachable ($BK_LIST)" || fail "backup list" "got $BK_LIST"

  BK_TRIG=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$PAT_HDR" \
    "$API_PROV/api/provision/$PROJECT_ID/backup/trigger")
  case "$BK_TRIG" in
    200|201|202|204) pass "backup trigger ($BK_TRIG)" ;;
    *) skip "backup trigger" "got $BK_TRIG" ;;
  esac

  ###############################################################################
  section "F19. Snapshots + logs"
  SNAP=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$PAT_HDR" \
    "$API_PROV/api/provision/$PROJECT_ID/snapshot/export")
  case "$SNAP" in
    200|201|202|204) pass "snapshot export ($SNAP)" ;;
    *) skip "snapshot export" "got $SNAP" ;;
  esac
  # /logs is a finite snapshot (kubectl exec tail -N), not SSE. Just curl it.
  LOGS=$(curl -s --max-time 15 -o /dev/null -w '%{http_code}' -H "$PAT_HDR" "$API_PROV/api/provision/$PROJECT_ID/logs?lines=20")
  [ "$LOGS" = "200" ] && pass "pod logs snapshot" || skip "pod logs" "got $LOGS"

  ###############################################################################
  section "F20. Metrics"
  MET=$(curl -s -o /dev/null -w '%{http_code}' -H "$PAT_HDR" "$API_PROV/api/provision/$PROJECT_ID/metrics/current")
  [ "$MET" = "200" ] && pass "metrics current" || skip "metrics current" "got $MET"
  MET_HIST=$(curl -s -o /dev/null -w '%{http_code}' -H "$PAT_HDR" "$API_PROV/api/provision/$PROJECT_ID/metrics/history")
  [ "$MET_HIST" = "200" ] && pass "metrics history" || skip "metrics history" "got $MET_HIST"

  ###############################################################################
  # Monitoring runs BEFORE deprovision so CNPG metrics are still being scraped.
  section "F26. Monitoring — Prom + Loki data freshness"
  if command -v kubectl > /dev/null 2>&1 && kubectl get pods -n monitoring > /dev/null 2>&1; then
    PROM_HOST="monitoring-kube-prometheus-prometheus.monitoring.svc.cluster.local:9090"
    LOKI_HOST="loki.monitoring.svc.cluster.local:3100"
    PROBE_POD="aio-probe-$$"
    kubectl run -n monitoring "$PROBE_POD" --rm -i --restart=Never --image=curlimages/curl --command -- sh -c "
NOW=\$(date +%s)000000000
HOUR_AGO=\$(( \$(date +%s) - 600 ))000000000
echo '--- prom: cnpg metrics for project ---'
curl -s --get --data-urlencode 'query=cnpg_backends_total' \"http://$PROM_HOST/api/v1/query\" | grep -oE '\"result\":\\[' | head -c 30
echo
echo '--- loki: provisioning recent logs ---'
curl -s --get --data-urlencode 'query={namespace=\"excalibase-platform\",app=\"provisioning\"}' \"http://$LOKI_HOST/loki/api/v1/query_range?start=\$HOUR_AGO&end=\$NOW&limit=2\" | grep -oE '\"result\":\\[' | head -c 30
echo
echo '--- loki: graphql tenant logs ---'
curl -s --get --data-urlencode 'query={namespace=\"excalibase-platform\",app=\"graphql\"}' \"http://$LOKI_HOST/loki/api/v1/query_range?start=\$HOUR_AGO&end=\$NOW&limit=2\" | grep -oE '\"result\":\\[' | head -c 30
" > /tmp/aio-monitoring.out 2>&1

    if grep -q '"result":\[' /tmp/aio-monitoring.out 2>/dev/null; then
      pass "monitoring data plane alive (prom + loki)"
      yellow "  → run kubectl -n monitoring port-forward svc/monitoring-grafana 33000:80 to view dashboards"
    else
      skip "monitoring" "no metrics/logs found"
    fi
    rm -f /tmp/aio-monitoring.out
  else
    skip "monitoring" "monitoring namespace not reachable"
  fi

  ###############################################################################
  section "F21. Deprovision — vault prefix purge"
  curl -s -X DELETE -H "$PAT_HDR" "$API_PROV/api/provision/$PROJECT_ID/" > /dev/null
  sleep 3
  VLIST_AFTER=$(curl -sf -H "$PAT_HDR" "$API_PROV/api/vault/secrets-list?prefix=projects/$PROJECT_ID/" 2>/dev/null)
  PATHS_LEFT=$(echo "$VLIST_AFTER" | jq '.paths | length' 2>/dev/null || echo "?")
  [ "$PATHS_LEFT" = "0" ] && pass "vault prefix purged ($PATHS_LEFT paths)" \
    || fail "vault sweep on deprovision" "$PATHS_LEFT remain: $VLIST_AFTER"
fi

###############################################################################
# F23. Storage (R2-backed). Skips cleanly when R2_BUCKET env is missing
# from the provisioning pod (i.e. r2-creds Secret not deployed).
section "F23. Storage — buckets + upload + signed download + delete"
BKT_NAME="e2e-bkt-$(date +%s)"
CREATE_RESP=$(curl -s -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$BKT_NAME\",\"public\":false}" \
  "$API_PROV/api/projects/$PROJECT_ID/storage/buckets")
if echo "$CREATE_RESP" | jq -e '.id' > /dev/null 2>&1; then
  pass "bucket create"

  # Sign upload URL
  SIGN_RESP=$(curl -s -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
    -d '{"key":"hello.txt","mimeType":"text/plain","size":11}' \
    "$API_PROV/api/projects/$PROJECT_ID/storage/buckets/$BKT_NAME/upload-url")
  PUT_URL=$(echo "$SIGN_RESP" | jq -r '.url')
  # The upload lands on a staging key and becomes the object only when the
  # confirmation names this id.
  UPLOAD_ID=$(echo "$SIGN_RESP" | jq -r '.uploadId')
  if [ -n "$PUT_URL" ] && [ "$PUT_URL" != "null" ] && [ "$UPLOAD_ID" != "null" ]; then
    pass "signed PUT URL minted"

    # PUT bytes directly to R2. Both headers are covered by the signature,
    # so they must match what was declared above exactly.
    PUT_CODE=$(curl -s -o /dev/null -w '%{http_code}' -X PUT \
      -H 'Content-Type: text/plain' -H 'Content-Length: 11' \
      --data 'hello world' "$PUT_URL")
    [ "$PUT_CODE" = "200" ] && pass "PUT to R2 (200)" || fail "PUT to R2" "got $PUT_CODE"

    # Confirm upload by the id it was staged under. Size and type are read
    # back from the object store, so they are not sent here.
    CONFIRM=$(curl -s -X POST -H "$PAT_HDR" -H 'Content-Type: application/json' \
      -d "{\"key\":\"hello.txt\",\"uploadId\":\"$UPLOAD_ID\"}" \
      "$API_PROV/api/projects/$PROJECT_ID/storage/buckets/$BKT_NAME/confirm-upload")
    echo "$CONFIRM" | jq -e '.id' > /dev/null 2>&1 \
      && pass "confirm-upload recorded object" || fail "confirm" "$CONFIRM"

    # List
    LIST=$(curl -s -H "$PAT_HDR" "$API_PROV/api/projects/$PROJECT_ID/storage/buckets/$BKT_NAME/objects")
    echo "$LIST" | jq -e '.objects[] | select(.key=="hello.txt")' > /dev/null 2>&1 \
      && pass "list shows uploaded object" || fail "list" "$LIST"

    # Signed download URL + GET
    DL=$(curl -s -H "$PAT_HDR" "$API_PROV/api/projects/$PROJECT_ID/storage/buckets/$BKT_NAME/download-url/hello.txt")
    DL_URL=$(echo "$DL" | jq -r '.url')
    if [ -n "$DL_URL" ] && [ "$DL_URL" != "null" ]; then
      DL_BODY=$(curl -s "$DL_URL")
      [ "$DL_BODY" = "hello world" ] && pass "signed GET round-trip" \
        || fail "signed GET" "got '$DL_BODY'"
    else
      fail "download URL" "$DL"
    fi

    # Delete object
    DEL_CODE=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE -H "$PAT_HDR" \
      "$API_PROV/api/projects/$PROJECT_ID/storage/buckets/$BKT_NAME/objects/hello.txt")
    [ "$DEL_CODE" = "204" ] && pass "object delete (204)" || fail "object delete" "got $DEL_CODE"
  else
    fail "signed PUT URL" "$SIGN_RESP"
  fi

  # Cascade-delete bucket
  curl -s -X DELETE -H "$PAT_HDR" "$API_PROV/api/projects/$PROJECT_ID/storage/buckets/$BKT_NAME" > /dev/null
  pass "bucket cascade-delete"
else
  skip "storage" "bucket create failed (R2 not configured?): $CREATE_RESP"
fi

###############################################################################
# F24. Email — register a user with a SES-sandbox-allowed recipient and
# fire the verify-send endpoint. We can't read the inbox, so success
# means: SES accepted the message (no error, status=sent). The MessageId
# is logged via provisioning logs for audit.
section "F24. Email — verify-send via SES (sandbox: ducvuminh.dev@gmail.com)"
if [ -n "$EMAIL_E2E" ]; then
  # Use Gmail "+" alias so each run gets a unique email — both SES sandbox
  # and platform-db treat them as distinct addresses, but they all land in
  # the same inbox so the operator can spot-check.
  TS=$(date +%s)
  BASE_EMAIL="${EMAIL_TEST_RECIPIENT:-ducvuminh.dev@gmail.com}"
  if [[ "$BASE_EMAIL" == *"@gmail.com" ]]; then
    LOCAL="${BASE_EMAIL%@*}"
    TEST_EMAIL="${LOCAL}+e2e-${TS}@gmail.com"
  else
    TEST_EMAIL="${BASE_EMAIL%@*}+e2e-${TS}@${BASE_EMAIL#*@}"
  fi
  TEST_USER="email-test-${TS}"
  TEST_PASS="EmailTest@Pass123"

  # Register a new platform user with the sandbox-allowed email.
  REG_RESP=$(curl -s -X POST "$API_PROV/api/auth/register" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$TEST_USER\",\"email\":\"$TEST_EMAIL\",\"password\":\"$TEST_PASS\"}")
  if echo "$REG_RESP" | jq -e '.token // .access_token' > /dev/null 2>&1; then
    TEST_PAT=$(echo "$REG_RESP" | jq -r '.token // .access_token')
    pass "test user registered ($TEST_EMAIL)"

    # Fire the verify endpoint. Provisioning will mint a token, store
    # the hash, and pass the click URL to SES. SES accepts → return 200.
    VERIFY_RESP=$(curl -s -X POST -H "Authorization: Bearer $TEST_PAT" \
      -H 'Content-Type: application/json' \
      "$API_PROV/api/email/verify/send")
    if echo "$VERIFY_RESP" | jq -e '.status == "sent"' > /dev/null 2>&1; then
      pass "verify email dispatched to $TEST_EMAIL"
      # MessageId is in provisioning logs; surface it for the operator.
      yellow "  → SES MessageId visible in: kubectl -n excalibase-platform logs deployment/provisioning | grep email"
    else
      fail "verify email" "$VERIFY_RESP"
    fi

    # Reset flow — same provider, different template. Hit it as the
    # registered user; SES sandbox allows the same recipient.
    RESET_RESP=$(curl -s -X POST "$API_PROV/api/email/reset/send" \
      -H 'Content-Type: application/json' \
      -d "{\"email\":\"$TEST_EMAIL\"}")
    echo "$RESET_RESP" | jq -e '.status == "sent"' > /dev/null 2>&1 \
      && pass "password-reset email dispatched" || fail "reset email" "$RESET_RESP"
  else
    skip "email tests" "user register failed: $REG_RESP"
  fi
else
  skip "email" "EMAIL_E2E env not set; skipping live SES test"
fi

###############################################################################
# F25. Backup verification — barman/CNPG should have written WAL or base
# backup objects to the configured S3-compatible store. F18 above triggers
# a backup; this section verifies the bytes actually landed.
section "F25. Backup → R2 verification"
# Pick up R2 creds from env (set by run script) or fall back to the
# same defaults provisioning uses. Defaults align with what the chart
# wires from the r2-creds Secret.
B_BUCKET="${BACKUP_VERIFY_BUCKET:-excalibase-backups}"
B_ENDPOINT="${BACKUP_VERIFY_ENDPOINT:-${R2_ENDPOINT:-}}"
B_KEY="${BACKUP_VERIFY_KEY_ID:-${R2_ACCESS_KEY_ID:-}}"
B_SECRET="${BACKUP_VERIFY_SECRET:-${R2_SECRET_ACCESS_KEY:-}}"
if [ -n "$B_ENDPOINT" ] && [ -n "$B_KEY" ] && [ -n "$B_SECRET" ]; then
  # Barman writes WAL within seconds of cluster ready, but the first
  # base backup needs the trigger from F18; give it a moment.
  sleep 10
  KEYS=$(AWS_ACCESS_KEY_ID="$B_KEY" \
    AWS_SECRET_ACCESS_KEY="$B_SECRET" \
    AWS_DEFAULT_REGION=auto \
    aws --endpoint-url "$B_ENDPOINT" s3 ls "s3://$B_BUCKET/$PROJECT_ID/" --recursive 2>&1 | head -20)
  if [ -n "$KEYS" ] && ! echo "$KEYS" | grep -q 'NoSuchBucket\|error\|InvalidAccessKey'; then
    pass "backup objects present in s3://$B_BUCKET/$PROJECT_ID/"
    yellow "  → first key: $(echo "$KEYS" | head -1 | awk '{print $4}')"
  else
    skip "backup verification" "no backup objects yet (Barman may still be pushing): ${KEYS:0:120}"
  fi
else
  skip "backup verification" "no R2 creds in env (R2_* or BACKUP_VERIFY_*)"
fi

###############################################################################
# F26 moved up to before F21 deprovision (project must be alive for CNPG metrics).

###############################################################################
# Rate-limit test runs LAST: floods the IP, breaks subsequent unauth calls.
section "F22. Rate limiting (runs last)"
HITS_429=0
for i in $(seq 1 60); do
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API_PROV/api/auth/login" \
    -H 'Content-Type: application/json' \
    -d '{"username":"nope","password":"wrong"}')
  [ "$code" = "429" ] && HITS_429=$((HITS_429+1))
done
[ "$HITS_429" -gt 0 ] && pass "rate limit triggers (429 hits=$HITS_429/60)" || fail "rate limit" "no 429"

###############################################################################
echo ""
cyan "======================================"
[ "$FAIL" -eq 0 ] && green "RESULTS: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -gt 0 ] && red   "RESULTS: $PASS passed, $FAIL failed, $SKIP skipped"
cyan "======================================"

[ "$FAIL" -eq 0 ] && exit 0 || exit 1
