import sys

import yaml

cfg = yaml.safe_load(sys.stdin)
services, networks = cfg["services"], cfg["networks"]
failures = []

ENGINE_SOCKET = "/var/run/docker.sock"


def nets(name):
    return set(services[name].get("networks") or {})


# Only the edge is published, and only HTTP(S).
for name, svc in services.items():
    ports = svc.get("ports") or []
    if name == "edge":
        targets = sorted(int(p["target"]) for p in ports)
        if targets != [80, 443]:
            failures.append(f"edge publishes {targets}, want [80, 443]")
    elif ports:
        failures.append(f"{name} publishes ports {ports}; only the edge may")

# Only the engine proxy holds the engine socket, and it holds nothing else.
for name, svc in services.items():
    sources = [v.get("source") for v in svc.get("volumes") or []]
    if ENGINE_SOCKET in sources and name != "engine-proxy":
        failures.append(f"{name} mounts the engine socket")
    if svc.get("group_add") and name != "engine-proxy":
        failures.append(f"{name} joins a host group {svc['group_add']}")
proxy = services["engine-proxy"]
if ENGINE_SOCKET not in [v.get("source") for v in proxy.get("volumes") or []]:
    failures.append("engine-proxy does not mount the engine socket")
if nets("engine-proxy") != {"engine"}:
    failures.append(f"engine-proxy networks {sorted(nets('engine-proxy'))}, want only engine")
if {n for n, s in services.items() if "engine" in (s.get("networks") or {})} != {"engine-proxy", "provisioning"}:
    failures.append("only provisioning may share the engine network with the proxy")

prov = services["provisioning"]["environment"]
if prov.get("DOCKER_HOST") != "tcp://engine-proxy:2375":
    failures.append(f"provisioning DOCKER_HOST={prov.get('DOCKER_HOST')!r}, want the proxy")
tenants = networks["tenants"]["name"]
if prov.get("DOCKER_NETWORK") != tenants:
    failures.append(f"DOCKER_NETWORK={prov.get('DOCKER_NETWORK')!r}, want the tenants network {tenants!r}")
if tenants not in proxy["environment"].get("PROXY_NETWORKS", "").split(","):
    failures.append("the proxy does not admit the tenants network")
if prov.get("CORS_ORIGINS") == "*":
    failures.append("provisioning answers every origin; Studio is its only browser origin")
if prov.get("REGISTRATION_MODE") != "invite":
    failures.append(f"REGISTRATION_MODE={prov.get('REGISTRATION_MODE')!r}, want invite on a self-hosted box")

# Internal networks: no route out, and nothing on them is reachable from outside.
for name in ("platform", "engine", "dataplane"):
    if not networks[name].get("internal"):
        failures.append(f"network {name} is not internal")
for name in ("postgres", "nats"):
    if nets(name) != {"platform"}:
        failures.append(f"{name} networks {sorted(nets(name))}, want only platform")
if nets("edge") != {"edge", "dataplane"}:
    failures.append(f"edge networks {sorted(nets('edge'))}, want edge and dataplane")

# Our own images run without capabilities and cannot gain privileges.
for name in ("provisioning", "engine-proxy", "auth", "graphql"):
    svc = services[name]
    if "ALL" not in (svc.get("cap_drop") or []):
        failures.append(f"{name} keeps its capabilities")
for name, svc in services.items():
    if "no-new-privileges:true" not in (svc.get("security_opt") or []):
        failures.append(f"{name} may gain privileges (no no-new-privileges)")

# Platform volumes keep names a tenant volume prefix can never match.
for key, volume in cfg.get("volumes", {}).items():
    if not (volume or {}).get("name", "").startswith("excalibase-platform-"):
        failures.append(f"volume {key} is not named excalibase-platform-*")

# EXC-573: graphql serves the projects Studio creates, not a bundled database.
gql = services["graphql"]["environment"]
for key in ("SPRING_DATASOURCE_URL", "APP_PROJECT_ID"):
    if key in gql:
        failures.append(f"graphql still sets {key}; it must run multi-tenant")
for key in ("APP_SECURITY_MULTI_TENANT_PROVISIONING_URL", "APP_SECURITY_RLS_POLICY_URL"):
    if gql.get(key) != "http://provisioning:24005/api":
        failures.append(f"graphql {key}={gql.get(key)!r}, want provisioning")
if gql.get("APP_NATS_TENANT_IN_SUBJECT") != "true":
    failures.append("graphql does not scope change events by tenant")
for gone in ("rest", "watcher"):
    if gone in services:
        failures.append(f"{gone} is still in the stack")
if services["postgres"]["environment"].get("POSTGRES_DB") != "excalibase_platform":
    failures.append("postgres holds more than the platform's own database")
if any("initdb" in (v.get("source") or "") for v in services["postgres"].get("volumes") or []):
    failures.append("postgres still runs init scripts for a data database")

# The opt-in local backup store is internal and unpublished.
if any(k.startswith("R2_") for k in services["provisioning"]["environment"]):
    failures.append("provisioning still reads R2_*; backups use BACKUP_DEFAULT_*")

if failures:
    sys.exit("\n".join(failures))
print("single-host hardening ok")
