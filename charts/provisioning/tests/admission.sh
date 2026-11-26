#!/usr/bin/env sh
# On a real cluster with the chart installed: the admission policies refuse what
# the RBAC alone would let provisioning do (EXC-484). Each refusal has a matching
# case that is admitted, so a policy that refuses everything cannot pass.
#
#   sh charts/provisioning/tests/admission.sh <platform-namespace> [project-namespace]
set -eu

NS=${1:?platform namespace}
PROJECT=${2:-org-smoke-proj}
AS="system:serviceaccount:$NS:provisioning-sa"
fail=0
as_prov() { kubectl --as="$AS" "$@"; }
bad() { echo "FAIL $1"; fail=1; }

binding() { # binding <namespace> <name> <subjects-yaml>
  cat <<YAML
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: $2, namespace: $1}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: excalibase-provisioning-project}
subjects: $3
YAML
}
SELF="[{kind: ServiceAccount, name: provisioning-sa, namespace: $NS}]"
TWO="[{kind: ServiceAccount, name: provisioning-sa, namespace: $NS}, {kind: ServiceAccount, name: other, namespace: $NS}]"
OTHER="[{kind: ServiceAccount, name: other, namespace: $NS}]"

# Admission policies take a moment to take effect: wait for the first refusal.
for _ in $(seq 1 30); do
  binding default probe "$SELF" | as_prov create --dry-run=server -f - >/dev/null 2>&1 || break
  sleep 2
done

binding "$PROJECT" probe "$SELF" | as_prov create --dry-run=server -f - >/dev/null 2>&1 || bad "provisioning's own binding in a project namespace was refused"
binding default probe "$SELF" | as_prov create --dry-run=server -f - >/dev/null 2>&1 && bad "a binding in a namespace that is not a project was admitted"
binding "$PROJECT" probe "$TWO" | as_prov create --dry-run=server -f - >/dev/null 2>&1 && bad "a binding with a second subject was admitted"
binding "$PROJECT" probe "$OTHER" | as_prov create --dry-run=server -f - >/dev/null 2>&1 && bad "a binding for another subject was admitted"

namespace() { # namespace <name> [enforce]
  if [ -n "${2:-}" ]; then
    printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n  labels: {"pod-security.kubernetes.io/enforce": "%s"}\n' "$1" "$2"
  else
    printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n' "$1"
  fi
}
namespace admission-probe baseline | as_prov create --dry-run=server -f - >/dev/null 2>&1 || bad "a baseline namespace was refused"
namespace admission-probe restricted | as_prov create --dry-run=server -f - >/dev/null 2>&1 || bad "a restricted namespace was refused"
namespace admission-probe privileged | as_prov create --dry-run=server -f - >/dev/null 2>&1 && bad "a privileged namespace was admitted"
namespace admission-probe | as_prov create --dry-run=server -f - >/dev/null 2>&1 && bad "a namespace with no pod security label was admitted"

as_prov auth can-i get deployments/cnpg-controller-manager -n cnpg-system >/dev/null 2>&1 || bad "the operator check cannot read the CloudNativePG deployment"
as_prov auth can-i get secrets -n default >/dev/null 2>&1 && bad "provisioning reads secrets outside a project namespace"

[ "$fail" -eq 0 ] && echo "admission cases passed"
exit "$fail"
