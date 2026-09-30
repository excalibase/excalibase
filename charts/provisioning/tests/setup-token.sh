#!/usr/bin/env sh
# On Kubernetes provisioning refuses to start without SETUP_TOKEN until the
# first admin exists (EXC-451), so the chart refuses to render without
# setupToken.existingSecret and passes the Secret's key through when set.
#
#   sh charts/provisioning/tests/setup-token.sh
set -eu

CHART=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
BASE="--set studioUrl=https://studio.example.test --set trustedProxyCIDRs=10.42.0.0/16"

# shellcheck disable=SC2086
if out=$(helm template t "$CHART" $BASE 2>&1); then
  echo "rendered without setupToken.existingSecret" >&2
  exit 1
fi
echo "$out" | grep -q setupToken.existingSecret || { echo "refusal does not name setupToken.existingSecret: $out" >&2; exit 1; }

# shellcheck disable=SC2086
out=$(helm template t "$CHART" $BASE --set setupToken.existingSecret=first-admin-token)
echo "$out" | grep -A4 'name: SETUP_TOKEN' | grep -q 'name: first-admin-token' \
  || { echo "SETUP_TOKEN does not come from the named Secret" >&2; exit 1; }
echo "$out" | grep -A5 'name: SETUP_TOKEN' | grep -q 'key: token' \
  || { echo "SETUP_TOKEN does not read the Secret's token key" >&2; exit 1; }

echo "setup token cases passed"
