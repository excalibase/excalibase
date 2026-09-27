#!/usr/bin/env sh
# Provisioning serves the edge on its own port and refuses to start without the
# edge addresses it believes, so the chart requires them, passes PUBLIC_PORT,
# and points the Service's public port and the ingress at that listener.
#
#   sh charts/provisioning/tests/public-listener.sh
set -eu

CHART=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BASE="--set studioUrl=https://studio.example.test"

if out=$(helm template t "$CHART" $BASE 2>&1); then
  echo "rendered without trustedProxyCIDRs" >&2
  exit 1
fi
echo "$out" | grep -q trustedProxyCIDRs || { echo "refusal does not name trustedProxyCIDRs: $out" >&2; exit 1; }

out=$(helm template t "$CHART" $BASE --set trustedProxyCIDRs=10.42.0.0/16)
echo "$out" | grep -A1 'name: PUBLIC_PORT' | grep -q '"24006"' || { echo "PUBLIC_PORT is not passed" >&2; exit 1; }
echo "$out" | grep -A1 'name: TRUSTED_PROXY_CIDRS' | grep -q '"10.42.0.0/16"' || { echo "TRUSTED_PROXY_CIDRS is not passed" >&2; exit 1; }
echo "$out" | grep -B1 -A2 'name: public' | grep -q 'targetPort: 24006' || { echo "Service has no public port" >&2; exit 1; }
ingress=$(echo "$out" | awk '/^kind: Ingress/,/^---/')
echo "$ingress" | grep -q 'number: 24006' || { echo "ingress does not reach the public listener" >&2; exit 1; }
if echo "$ingress" | grep -q 'number: 24005'; then
  echo "ingress still reaches the internal listener" >&2
  exit 1
fi

echo "public listener cases passed"
