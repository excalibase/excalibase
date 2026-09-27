#!/usr/bin/env sh
# Provisioning refuses to start without STUDIO_URL, so the chart refuses to
# render without provisioning.studioUrl and passes it through when set.
#
#   sh charts/provisioning/tests/studio-url.sh
set -eu

CHART=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

if out=$(helm template t "$CHART" --set trustedProxyCIDRs=10.42.0.0/16 2>&1); then
  echo "rendered without studioUrl" >&2
  exit 1
fi
echo "$out" | grep -q studioUrl || { echo "refusal does not name studioUrl: $out" >&2; exit 1; }

helm template t "$CHART" --set studioUrl=https://studio.example.test --set trustedProxyCIDRs=10.42.0.0/16 \
  | grep -A1 'name: STUDIO_URL' | grep -q '"https://studio.example.test"' \
  || { echo "STUDIO_URL is not passed to provisioning" >&2; exit 1; }

echo "studio URL cases passed"
