#!/usr/bin/env sh
# Edge functions and apps run under the gVisor RuntimeClass, and provisioning
# checks the class's handler before it starts a function runtime (EXC-715), so
# the role may get runtime classes, read only, whatever else is switched on.
#
#   sh charts/provisioning/tests/runtimeclass-rbac.sh
set -eu

CHART=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
python3 -c 'import yaml' 2>/dev/null || pip3 install --quiet pyyaml

helm template t "$CHART" --set studioUrl=https://studio.example.test --set trustedProxyCIDRs=10.42.0.0/16 --set setupToken.existingSecret=first-admin-token | python3 -c '
import sys, yaml
role = next(d for d in yaml.safe_load_all(sys.stdin) if d and d.get("kind") == "ClusterRole" and d["metadata"]["name"] == "excalibase-provisioning")
got = {v for r in role["rules"] if "node.k8s.io" in r.get("apiGroups", []) and "runtimeclasses" in r.get("resources", []) for v in r["verbs"]}
if got != {"get"}: sys.exit("runtimeclasses verbs %s, want only get" % sorted(got))
'
echo "runtime class RBAC cases passed"
