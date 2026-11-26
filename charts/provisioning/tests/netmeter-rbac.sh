#!/usr/bin/env sh
# Per-app network metering (EXC-844) reads each node's kubelet summary with the
# service account's token: the role may list nodes and get nodes/stats (never
# nodes/proxy, which reaches far more), the pod mounts the CA that signed the
# kubelet's certificate from a ConfigMap that may be missing (the pod must still
# start), and the pod's egress policy lets it reach the kubelet port: a plain
# NetworkPolicy rule everywhere, plus a CiliumNetworkPolicy naming the node by
# entity where the cilium.io/v2 API exists (an address rule does not match the
# node there), so a cluster on another CNI still installs. All of it exists only
# while netmeter.enabled is set.
#
#   sh charts/provisioning/tests/netmeter-rbac.sh
set -eu

CHART=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
python3 -c 'import yaml' 2>/dev/null || pip3 install --quiet pyyaml
BASE="--set studioUrl=https://studio.example.test --set trustedProxyCIDRs=10.42.0.0/16 --set setupToken.existingSecret=first-admin-token"

render() { # render <check> [helm args...]
  check=$1; shift
  # shellcheck disable=SC2086
  helm template t "$CHART" $BASE "$@" | python3 -c '
import sys, yaml
docs = [d for d in yaml.safe_load_all(sys.stdin) if d]
check, config_map = sys.argv[1], sys.argv[2]
role = next(d for d in docs if d["kind"] == "ClusterRole" and d["metadata"]["name"] == "excalibase-provisioning")
def verbs(group, resource):
    return {v for r in role["rules"] if group in r.get("apiGroups", []) and resource in r.get("resources", []) for v in r["verbs"]}
deploy = next(d for d in docs if d["kind"] == "Deployment")
pod = deploy["spec"]["template"]["spec"]
container = pod["containers"][0]
env = {e["name"]: e.get("value") for e in container["env"]}
mounts = [m for m in container.get("volumeMounts", []) if m["name"] == "kubelet-ca"]
volumes = [v for v in pod.get("volumes", []) if v["name"] == "kubelet-ca"]
policy = next(d for d in docs if d["kind"] == "NetworkPolicy" and d["metadata"]["name"] == "provisioning")
kubelet_rules = [r for r in policy["spec"]["egress"] if any(p.get("port") == 10250 for p in r.get("ports", []))]
failures = []
if any("nodes/proxy" in r.get("resources", []) for r in role["rules"]): failures.append("the role must never get nodes/proxy")
cilium = [d for d in docs if d["kind"] == "CiliumNetworkPolicy"]
if check == "off":
    if verbs("", "nodes/stats"): failures.append("nodes/stats granted while netmeter is off")
    if verbs("", "nodes"): failures.append("nodes readable while netmeter is off")
    if mounts or volumes: failures.append("the kubelet CA is mounted while netmeter is off")
    if "FEATURE_NETMETER" in env: failures.append("FEATURE_NETMETER set while netmeter is off")
    if kubelet_rules: failures.append("egress to the kubelet port while netmeter is off")
    if cilium: failures.append("a Cilium policy rendered while netmeter is off")
else:
    if verbs("", "nodes/stats") != {"get"}: failures.append("nodes/stats verbs %s, want only get" % sorted(verbs("", "nodes/stats")))
    if verbs("", "nodes") != {"list"}: failures.append("nodes verbs %s, want only list" % sorted(verbs("", "nodes")))
    if env.get("FEATURE_NETMETER") != "true": failures.append("FEATURE_NETMETER is %r, want true" % env.get("FEATURE_NETMETER"))
    if len(mounts) != 1 or len(volumes) != 1: failures.append("the kubelet CA is not mounted")
    else:
        cm = volumes[0].get("configMap", {})
        if "hostPath" in volumes[0]: failures.append("the CA must never be a hostPath")
        if cm.get("name") != config_map: failures.append("the CA volume is ConfigMap %r, want %r" % (cm.get("name"), config_map))
        if cm.get("optional") is not True: failures.append("the CA ConfigMap must be optional, or a missing one keeps the pod from starting")
    if len(kubelet_rules) != 1: failures.append("want one egress rule for the kubelet port, got %d" % len(kubelet_rules))
    elif {p["protocol"] for p in kubelet_rules[0]["ports"]} != {"TCP"}: failures.append("kubelet egress is not TCP only")
    if check == "cilium":
        if len(cilium) != 1: failures.append("want one CiliumNetworkPolicy, got %d" % len(cilium))
        else:
            egress = cilium[0]["spec"]["egress"]
            entities = {e for rule in egress for e in rule.get("toEntities", [])}
            ports = {p["port"] for rule in egress for pp in rule.get("toPorts", []) for p in pp["ports"]}
            if entities != {"host", "remote-node"}: failures.append("entities %s, want host and remote-node" % sorted(entities))
            if ports != {"10250"}: failures.append("ports %s, want only 10250" % sorted(ports))
    elif cilium: failures.append("a Cilium policy rendered on a cluster without cilium.io/v2")
if failures: sys.exit("%s: %s" % (check, "; ".join(failures)))
' "$check" "${CA_MAP:-kube-root-ca.crt}"
}

render off
render on --set netmeter.enabled=true
CA_MAP=my-ca render on --set netmeter.enabled=true --set netmeter.kubeletCAConfigMap=my-ca
render cilium --set netmeter.enabled=true --api-versions cilium.io/v2
# Cilium present but the feature off: still nothing.
render off --api-versions cilium.io/v2
echo "net meter chart cases passed"
