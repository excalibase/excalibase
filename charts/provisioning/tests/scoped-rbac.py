import sys, yaml
docs = [d for d in yaml.safe_load_all(sys.stdin) if d]
def role(name):
    return next((d for d in docs if d["kind"] == "ClusterRole" and d["metadata"]["name"] == name), None)
def verbs(r, group, resource):
    return {v for x in r["rules"] if group in x.get("apiGroups", []) and resource in x.get("resources", []) for v in x["verbs"]}
failures = []
wide = role("excalibase-provisioning")
tenant = role("excalibase-provisioning-project")
if wide is None: sys.exit("no cluster role excalibase-provisioning")
if verbs(wide, "", "secrets"): failures.append("cluster-wide role reaches secrets")
if verbs(wide, "", "pods/exec"): failures.append("cluster-wide role reaches pods/exec")
if "delete" in verbs(wide, "", "namespaces"): failures.append("cluster-wide role deletes namespaces")
if verbs(wide, "postgresql.cnpg.io", "clusters"): failures.append("cluster-wide role reaches CNPG clusters")
if tenant is None: failures.append("no tenant role excalibase-provisioning-project")
else:
    if not {"get", "create"} <= verbs(tenant, "", "secrets"): failures.append("tenant role cannot manage secrets")
    if "delete" not in verbs(tenant, "", "namespaces"): failures.append("tenant role cannot delete its namespace")
def policy(name):
    return next((d for d in docs if d["kind"] == "ValidatingAdmissionPolicy" and d["metadata"]["name"] == name), None)
def expression(pol):
    return " ".join(v["expression"] for v in pol["spec"]["validations"]) if pol else ""
bind = [x for x in wide["rules"] if "bind" in x.get("verbs", []) and "clusterroles" in x.get("resources", [])]
if len(bind) != 1 or bind[0].get("resourceNames") != ["excalibase-provisioning-project"]:
    failures.append("bind is not limited by resourceNames to the tenant role: %s" % bind)
bindings = policy("excalibase-provisioning-project-bindings")
if bindings is None: failures.append("no admission policy limiting bindings to project namespaces")
text = expression(bindings)
for needle, what in [
    ("object.roleRef.name == 'excalibase-provisioning-project'", "the roleRef check"),
    ("object.roleRef.kind == 'ClusterRole'", "the roleRef kind check"),
    ("namespaceObject.metadata.labels[?'excalibase.io/type'].orValue('') == 'project'", "the project-label check"),
    ("object.subjects.size() == 1", "the single-subject check"),
    ("object.subjects[0].name == 'provisioning-sa'", "the subject name check"),
    ("object.subjects[0].namespace == 'excalibase-platform'", "the subject namespace check"),
]:
    if needle not in text: failures.append("bindings policy lacks " + what)
podsec = policy("excalibase-provisioning-namespace-pod-security")
if podsec is None: failures.append("no admission policy on namespace creation")
elif "in ['baseline', 'restricted']" not in expression(podsec): failures.append("namespace policy does not require baseline or restricted")
check = next((d for d in docs if d["kind"] == "Role" and d["metadata"]["name"] == "excalibase-provisioning-operator-check"), None)
if check is None or check["metadata"]["namespace"] != "cnpg-system" or check["rules"][0].get("resourceNames") != ["cnpg-controller-manager"]:
    failures.append("no operator-check role on cnpg-controller-manager in cnpg-system")
if not any(d["kind"] == "RoleBinding" and d["metadata"]["name"] == "excalibase-provisioning-operator-check" for d in docs): failures.append("operator-check role is not bound")
dep = next(d for d in docs if d["kind"] == "Deployment")
env = {e["name"]: e for c in dep["spec"]["template"]["spec"]["containers"] for e in c.get("env", [])}
if env.get("PROJECT_NAMESPACE_ROLE", {}).get("value") != "excalibase-provisioning-project": failures.append("PROJECT_NAMESPACE_ROLE not set")
if failures: sys.exit("; ".join(failures))
