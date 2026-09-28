#!/usr/bin/env sh
# A disk resize reads the project's volumes and their storage class before it
# asks for more (EXC-492), so the role may list claims and get storage classes,
# read only.
#
#   sh charts/provisioning/tests/disk-resize-rbac.sh
set -eu

CHART=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
python3 -c 'import yaml' 2>/dev/null || pip3 install --quiet pyyaml

helm template t "$CHART" --set studioUrl=https://studio.example.test --set trustedProxyCIDRs=10.42.0.0/16 | python3 -c '
import sys, yaml
role = next(d for d in yaml.safe_load_all(sys.stdin) if d and d.get("kind") == "ClusterRole" and d["metadata"]["name"] == "excalibase-provisioning")
def verbs(group, resource):
    return {v for r in role["rules"] if group in r.get("apiGroups", []) and resource in r.get("resources", []) for v in r["verbs"]}
failures = []
if "list" not in verbs("", "persistentvolumeclaims"): failures.append("cannot list persistentvolumeclaims")
if verbs("storage.k8s.io", "storageclasses") != {"get"}: failures.append("storageclasses verbs %s, want only get" % sorted(verbs("storage.k8s.io", "storageclasses")))
if verbs("", "persistentvolumeclaims") - {"get", "list"}: failures.append("persistentvolumeclaims writable")
if failures: sys.exit("; ".join(failures))
'
echo "disk resize RBAC cases passed"
