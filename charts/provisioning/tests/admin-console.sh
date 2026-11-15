#!/usr/bin/env sh
# The operator console's host and Cloudflare Access fence are off by default and
# rendered only when asked for (EXC-757).
#
#   sh charts/provisioning/tests/admin-console.sh
set -eu

CHART=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BASE="--set studioUrl=https://studio.example.test --set setupToken.existingSecret=first-admin-token --set trustedProxyCIDRs=10.42.0.0/16"

# Off by default: no admin ingress, no admin environment.
out=$(helm template t "$CHART" $BASE)
if echo "$out" | grep -qE 'ADMIN_HOST|ADMIN_ACCESS|name: provisioning-admin'; then
  echo "the admin console renders by default" >&2
  exit 1
fi

# A host alone: its own ingress and ADMIN_HOST, no Access settings.
out=$(helm template t "$CHART" $BASE --set adminConsole.host=admin.example.test)
echo "$out" | grep -A1 'name: ADMIN_HOST' | grep -q '"admin.example.test"' || { echo "ADMIN_HOST is not passed" >&2; exit 1; }
if echo "$out" | grep -q 'ADMIN_ACCESS'; then
  echo "Access settings rendered without being set" >&2
  exit 1
fi
admin=$(echo "$out" | awk '/name: provisioning-admin/,/^---/')
echo "$admin" | grep -q 'host: admin.example.test' || { echo "admin ingress has no host" >&2; exit 1; }
echo "$admin" | grep -q 'number: 24006' || { echo "admin ingress /api does not reach the public listener" >&2; exit 1; }
if echo "$admin" | grep -q 'cert-manager.io'; then
  echo "admin ingress asks cert-manager for a certificate that a standalone Certificate already manages" >&2
  exit 1
fi
echo "$admin" | grep -q 'ingressClassName: haproxy' || { echo "admin ingress is not on haproxy" >&2; exit 1; }
echo "$admin" | grep -q 'secretName: admin-excalibase-io-tls' || { echo "admin ingress does not use the issued certificate secret" >&2; exit 1; }

# Access settings come as a pair, and need the host.
if helm template t "$CHART" $BASE --set adminConsole.host=admin.example.test --set adminAccess.teamDomain=team.cloudflareaccess.com >/dev/null 2>&1; then
  echo "rendered a team domain without an AUD" >&2
  exit 1
fi
if helm template t "$CHART" $BASE --set adminAccess.teamDomain=team.cloudflareaccess.com --set adminAccess.audience=aud >/dev/null 2>&1; then
  echo "rendered Access without an admin host" >&2
  exit 1
fi
out=$(helm template t "$CHART" $BASE --set adminConsole.host=admin.example.test --set adminAccess.teamDomain=team.cloudflareaccess.com --set adminAccess.audience=aud-tag)
echo "$out" | grep -A1 'name: ADMIN_ACCESS_TEAM_DOMAIN' | grep -q '"team.cloudflareaccess.com"' || { echo "ADMIN_ACCESS_TEAM_DOMAIN is not passed" >&2; exit 1; }
echo "$out" | grep -A1 'name: ADMIN_ACCESS_AUD' | grep -q '"aud-tag"' || { echo "ADMIN_ACCESS_AUD is not passed" >&2; exit 1; }

# The ingress can be left to the operator.
out=$(helm template t "$CHART" $BASE --set adminConsole.host=admin.example.test --set adminConsole.ingress.enabled=false)
if echo "$out" | grep -q 'name: provisioning-admin'; then
  echo "the admin ingress rendered though disabled" >&2
  exit 1
fi
echo "$out" | grep -q 'name: ADMIN_HOST' || { echo "ADMIN_HOST must still be passed" >&2; exit 1; }

echo "admin console cases passed"
