#!/usr/bin/env sh
# The cluster-wide role holds only what cannot be scoped to one project
# namespace (EXC-521). Secrets, exec, workloads and namespace delete live in a
# tenant role that provisioning binds inside each project namespace it creates,
# and admission keeps those bindings to project namespaces, one subject, and the
# tenant role (EXC-484).
#
#   sh charts/provisioning/tests/scoped-rbac.sh
set -eu

CHART=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
python3 -c 'import yaml' 2>/dev/null || pip3 install --quiet pyyaml

helm template t "$CHART" --set studioUrl=https://studio.example.test --set trustedProxyCIDRs=10.42.0.0/16 --set setupToken.existingSecret=first-admin-token | python3 "$CHART/tests/scoped-rbac.py"
echo "scoped RBAC cases passed"
