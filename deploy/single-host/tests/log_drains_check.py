# EXC-658: log drains on a single host. Reads `docker compose config` on stdin.
#   log_drains_check.py off   the default render: no forwarder, the flag off
#   log_drains_check.py on    rendered with COMPOSE_PROFILES=hostlogs,logdrains
import sys

import yaml

mode = sys.argv[1]
doc = yaml.safe_load(sys.stdin)
services, networks = doc["services"], doc["networks"]
penv = services["provisioning"]["environment"]
failures = []


def joined(network):
    return sorted(name for name, svc in services.items() if network in (svc.get("networks") or {}))


if mode == "off":
    if "log-forwarder" in services:
        failures.append("the log forwarder runs without the logdrains profile")
    if penv.get("FEATURE_LOGDRAINS") != "false":
        failures.append(f"FEATURE_LOGDRAINS={penv.get('FEATURE_LOGDRAINS')!r} by default, want false")
else:
    fwd = services.get("log-forwarder") or sys.exit("no log-forwarder with the logdrains profile")
    fenv = fwd.get("environment") or {}
    if penv.get("FEATURE_LOGDRAINS") != "true" or penv.get("FEATURE_HOSTLOGS") != "true":
        failures.append("FEATURE_LOGDRAINS and FEATURE_HOSTLOGS do not both reach provisioning")
    if not penv.get("LOG_FORWARDER_TOKEN") or penv.get("LOG_FORWARDER_TOKEN") != fenv.get("LOG_FORWARDER_TOKEN"):
        failures.append("provisioning and the forwarder do not share the forwarder token")
    if fenv.get("PROVISIONING_URL") != "http://provisioning:24005":
        failures.append(f"the forwarder pulls from {fenv.get('PROVISIONING_URL')!r}, want provisioning's internal port")
    if set(fwd.get("networks") or {}) != {"drains", "drains-out"}:
        failures.append(f"forwarder networks {sorted(fwd.get('networks') or {})}, want drains and drains-out")
    if not networks["drains"].get("internal") or joined("drains") != ["log-forwarder", "provisioning"]:
        failures.append(f"the drains network must be internal and joined by provisioning and the forwarder only: {joined('drains')}")
    if networks["drains-out"].get("internal") or joined("drains-out") != ["log-forwarder"]:
        failures.append(f"drains-out must reach out and hold the forwarder alone: {joined('drains-out')}")
    if "loki" not in services:
        failures.append("log drains render without the Loki they read")
    mounts = {v.get("target"): v for v in fwd.get("volumes") or []}
    for target, source in (("/host/fib_trie", "/proc/1/net/fib_trie"), ("/host/if_inet6", "/proc/1/net/if_inet6")):
        mount = mounts.get(target) or {}
        if mount.get("source") != source or not mount.get("read_only"):
            failures.append(f"{target} is {mount}, want {source} read-only")
    if fenv.get("LOG_FORWARDER_HOST_FIB_TRIE") != "/host/fib_trie" or fenv.get("LOG_FORWARDER_HOST_IF_INET6") != "/host/if_inet6":
        failures.append("the forwarder is not told where the host's tables are")
    if set(mounts) != {"/host/fib_trie", "/host/if_inet6"}:
        failures.append(f"the forwarder mounts {sorted(mounts)}; it needs the host's tables only")
    if networks["drains-out"].get("enable_ipv6") is not False:
        failures.append("drains-out must not hand the forwarder IPv6")
    if fwd.get("ports") or not fwd.get("read_only") or "ALL" not in (fwd.get("cap_drop") or []):
        failures.append("the forwarder is published or not hardened")
    if "no-new-privileges:true" not in (fwd.get("security_opt") or []):
        failures.append("the forwarder may gain privileges")
    if not fwd.get("mem_limit") or not fwd.get("pids_limit") or not fwd.get("cpus"):
        failures.append("the forwarder has no memory, process or CPU limit")
    if fwd.get("entrypoint") != ["/bin/excalibase-log-forwarder"]:
        failures.append(f"the forwarder runs {fwd.get('entrypoint')}")
    if fwd.get("image") != services["provisioning"].get("image"):
        failures.append("the forwarder is not provisioning's image")

if failures:
    sys.exit("\n".join(failures))
print(f"single-host log drains ({mode}) ok")
