#!/usr/bin/env sh
# EXC-683 on a real engine and Compose: a service gated as compose.yaml gates
# provisioning and the edge does not start while the host firewall is
# unhealthy, here the published 1.6.2 image under the floating latest tag
# (no iptables in it), and starts when the firewall is off on purpose
# (HOST_FIREWALL=off). An optional (required: false) dependency would not do:
# Compose skips one that fails and starts the service anyway.
#
#   sh deploy/single-host/tests/host-firewall-gate.sh [docker|podman]
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ENGINE=${1:-docker}
OLD=${HOST_FIREWALL_OLD_IMAGE:-docker.io/excalibase/provisioning:latest}
WORK=$(mktemp -d)
PROJECT=exc683-gate-$$
fail() { echo "FAIL: $*" >&2; exit 1; }
compose() { $ENGINE compose -p "$PROJECT" -f "$WORK/compose.yaml" "$@"; }
trap 'compose down -t 1 >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT

# The gate must be the one compose.yaml puts on provisioning and the edge.
gate=$(sed -n '/^  provisioning:$/,/^  [a-z-]*:$/p' "$DIR/../compose.yaml" | sed -n '/^      host-firewall:$/,/condition:/p')
[ "$(echo "$gate" | tr -d ' ' | tr '\n' ' ')" = "host-firewall: condition:service_healthy " ] \
  || fail "provisioning's gate in compose.yaml changed: $gate"

cp "$DIR/../host-firewall.sh" "$WORK/"
cat >"$WORK/compose.yaml" <<EOF
services:
  host-firewall:
    image: $OLD
    entrypoint: ["sh", "/host-firewall.sh"]
    user: "0:0"
    environment:
      HOST_FIREWALL_NETWORKS: excalibase-tenants
      HOST_FIREWALL_TENANT_NETWORKS: excalibase-tenants
      HOST_FIREWALL_WAIT: "0"
      HOST_FIREWALL: \${HOST_FIREWALL:-}
    volumes:
      - ./host-firewall.sh:/host-firewall.sh:ro
    healthcheck:
      test: ["CMD", "sh", "/host-firewall.sh", "check"]
      interval: 2s
      timeout: 5s
      retries: 1
    restart: unless-stopped
  provisioning:
    image: $OLD
    entrypoint: ["sleep", "300"]
    depends_on:
$gate
EOF

$ENGINE pull -q "$OLD" >/dev/null
if HOST_FIREWALL= timeout 120 $ENGINE compose -p "$PROJECT" -f "$WORK/compose.yaml" up -d >"$WORK/up.log" 2>&1; then
  fail "up succeeded with the host firewall unhealthy: $(cat "$WORK/up.log")"
fi
running=$($ENGINE ps --filter "name=$PROJECT-provisioning" --filter status=running -q)
[ -z "$running" ] || fail "a gated service runs while the host firewall is unhealthy"
echo "ok: an unhealthy host firewall ($OLD) keeps the gated service from starting"
compose down -t 1 >/dev/null 2>&1

HOST_FIREWALL=off compose up -d >"$WORK/up.log" 2>&1 || fail "HOST_FIREWALL=off: $(cat "$WORK/up.log")"
[ -n "$($ENGINE ps --filter "name=$PROJECT-provisioning" --filter status=running -q)" ] || fail "HOST_FIREWALL=off: the gated service did not start"
echo "ok: with HOST_FIREWALL=off the gated service starts"
echo "host-firewall-gate: ok"
