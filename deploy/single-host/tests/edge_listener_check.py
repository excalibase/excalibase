import ipaddress
import sys

import yaml

cfg = yaml.safe_load(sys.stdin)
services, networks = cfg["services"], cfg["networks"]
failures = []

subnets = [c["subnet"] for c in (networks.get("edge", {}).get("ipam") or {}).get("config", [])]
if len(subnets) != 1:
    sys.exit(f"edge network has subnets {subnets}, want exactly one")
edge = subnets[0]
# Docker hands these out to other networks; a fixed subnet inside them clashes.
docker_pools = [ipaddress.ip_network("172.16.0.0/12"), ipaddress.ip_network("192.168.0.0/16")]
if any(ipaddress.ip_network(edge).overlaps(pool) for pool in docker_pools):
    failures.append(f"edge subnet {edge} is inside Docker's default address pools and can clash with another network")

env = services["provisioning"]["environment"]
if env.get("PUBLIC_PORT") != "24006":
    failures.append(f"PUBLIC_PORT={env.get('PUBLIC_PORT')!r}, want 24006")
if env.get("TRUSTED_PROXY_CIDRS") != edge:
    failures.append(f"TRUSTED_PROXY_CIDRS={env.get('TRUSTED_PROXY_CIDRS')!r}, want the edge subnet {edge}")
if set(services["provisioning"]["networks"]) != {"platform", "engine", "tenants", "edge"}:
    failures.append(f"provisioning networks {sorted(services['provisioning']['networks'])}")
if set(services["studio"]["networks"]) != {"edge"}:
    failures.append(f"studio networks {sorted(services['studio']['networks'])}, want only edge")
for name, svc in services.items():
    if name not in ("provisioning", "studio", "edge") and "edge" in (svc.get("networks") or {}):
        failures.append(f"{name} joined the edge network; its X-Forwarded-For would be believed")

if failures:
    sys.exit("\n".join(failures))
print("single-host edge listener ok")
