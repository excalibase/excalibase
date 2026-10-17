import re
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
if nets("postgres") != {"platform"}:
    failures.append(f"postgres networks {sorted(nets('postgres'))}, want only platform")
if nets("edge") != {"edge", "dataplane"}:
    failures.append(f"edge networks {sorted(nets('edge'))}, want edge and dataplane")

# Our own images run without capabilities and cannot gain privileges.
for name in ("provisioning", "engine-proxy", "auth", "graphql"):
    svc = services[name]
    if "ALL" not in (svc.get("cap_drop") or []):
        failures.append(f"{name} keeps its capabilities")
if "label=type:container_t" not in (proxy.get("security_opt") or []):
    failures.append("engine-proxy keeps the default SELinux type unless init.sh says otherwise")
# Unpinned, an install follows whatever was published last: the platform's
# images default to a released version, never latest or a trunk build.
for name, svc in services.items():
    image = svc.get("image", "")
    if image.startswith("docker.io/excalibase/") and not re.search(r":[0-9]+\.[0-9]+\.[0-9]+$", image):
        failures.append(f"{name}: image {image!r} is not pinned to a released version")
# Podman refuses short image names without a terminal to ask on (Fedora,
# RHEL): every image names its registry.
for name, svc in services.items():
    image = svc.get("image", "")
    if "/" not in image or "." not in image.split("/")[0]:
        failures.append(f"{name}: image {image!r} does not name its registry")
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

# EXC-578: the bundled object store holds backups and customer files. It is
# never published, tenant containers cannot reach it, and the edge reaches
# only its file bucket (on the internal dataplane network).
store = services.get("objectstore")
if not store:
    failures.append("no bundled object store")
else:
    if "@sha256:" not in store["image"]:
        failures.append(f"objectstore image {store['image']!r} is not pinned by digest")
    if nets("objectstore") != {"platform", "dataplane"}:
        failures.append(f"objectstore networks {sorted(nets('objectstore'))}, want platform and dataplane")
    if "ALL" not in (store.get("cap_drop") or []):
        failures.append("objectstore keeps its capabilities")
    if store["environment"].get("RUSTFS_CONSOLE_ENABLE") != "false":
        failures.append("objectstore serves its web console")
setup = services.get("objectstore-setup")
if not setup:
    failures.append("no objectstore-setup one-shot")
elif nets("objectstore-setup") != {"platform"}:
    failures.append(f"objectstore-setup networks {sorted(nets('objectstore-setup'))}, want only platform")
penv = services["provisioning"]["environment"]
want = {
    "BACKUP_DEFAULT_ENDPOINT": "http://objectstore:9000",
    "BACKUP_DEFAULT_BUCKET": "excalibase-backups",
    "BACKUP_DEFAULT_ACCESS_KEY_ID": "excalibase-backups",
    "STORAGE_ENDPOINT": "https://files.example.test",
    "STORAGE_BUCKET": "excalibase-storage",
    "STORAGE_ACCESS_KEY_ID": "excalibase-files",
    "STORAGE_INTERNAL_ENDPOINT": "http://objectstore:9000",
}
for key, value in want.items():
    if penv.get(key) != value:
        failures.append(f"provisioning {key}={penv.get(key)!r}, want {value!r} by default")
# Sites (EXC-353): off, with no Cloudflare settings, unless .env names them.
sites = {k: v for k, v in penv.items() if k.startswith("SITES_")}
want_sites = {"SITES_ENABLED": "false", "SITES_CLOUDFLARE_ACCOUNT_ID": "", "SITES_CLOUDFLARE_API_TOKEN": "",
              "SITES_DISPATCH_NAMESPACE": "", "SITES_HOSTS_KV_NAMESPACE_ID": "", "SITES_BASE_DOMAIN": "",
              "SITES_ZONE_ID": "", "SITES_CUSTOM_DOMAIN_TARGET": "", "SITES_MAX_ZONE_HOSTNAMES": ""}
if sites != want_sites:
    failures.append(f"provisioning SITES_* = {sites}, want {want_sites} by default")
secrets = [penv.get("BACKUP_DEFAULT_SECRET_ACCESS_KEY"), penv.get("STORAGE_SECRET_ACCESS_KEY"),
           store and store["environment"].get("RUSTFS_SECRET_KEY")]
if not all(secrets) or len(set(secrets)) != 3:
    failures.append("the root, backups and files keys are not three distinct secrets")

# EXC-579: manual unseal is selectable; the default keeps the key file.
if penv.get("VAULT_UNSEAL_PROVIDER", "") != "":
    failures.append(f"VAULT_UNSEAL_PROVIDER={penv.get('VAULT_UNSEAL_PROVIDER')!r} by default, want unset")

# EXC-575: apps are off by default; when on, they get per-project networks and
# disks under prefixes the platform's own never match, and the edge reads the
# routes provisioning writes from a volume it cannot write.
if penv.get("APP_HOSTING_ENABLED") != "false":
    failures.append(f"APP_HOSTING_ENABLED={penv.get('APP_HOSTING_ENABLED')!r} by default, want false")
if penv.get("APP_DOMAIN") != "apps.example.test":
    failures.append(f"APP_DOMAIN={penv.get('APP_DOMAIN')!r}, want apps.<domain> by default")
if penv.get("APP_SANDBOX_RUNTIME") != "runsc" or penv.get("APP_EGRESS") != "none":
    failures.append("apps default to gVisor and no egress")
penv_proxy = proxy["environment"]
for app_key, proxy_key in (("APP_NETWORK_PREFIX", "PROXY_NETWORK_PREFIX"), ("APP_VOLUME_PREFIX", "PROXY_VOLUME_PREFIX"),
                           ("APP_EDGE_CONTAINER", "PROXY_EDGE_CONTAINER")):
    if not penv.get(app_key) or penv.get(app_key) != penv_proxy.get(proxy_key):
        failures.append(f"{app_key}={penv.get(app_key)!r} and the proxy's {proxy_key}={penv_proxy.get(proxy_key)!r} differ")
if penv.get("APP_EDGE_CONTAINER") != services["edge"].get("container_name"):
    failures.append("the edge the proxy admits is not the edge container")
for prefix in (penv.get("APP_NETWORK_PREFIX", ""), penv.get("APP_VOLUME_PREFIX", "")):
    if not prefix.startswith("excalibase-") or prefix.startswith("excalibase-platform"):
        failures.append(f"app prefix {prefix!r} could match the platform's own names")
if penv_proxy.get("PROXY_RUNTIMES") != penv.get("APP_SANDBOX_RUNTIME"):
    failures.append("the proxy does not admit the apps' sandbox runtime")
edge_mounts = {v.get("target"): v for v in services["edge"].get("volumes") or []}
routes = edge_mounts.get("/etc/caddy/apps")
if not routes or not routes.get("read_only"):
    failures.append("the edge does not read app routes read-only")
prov_mounts = {v.get("target"): v.get("source") for v in services["provisioning"].get("volumes") or []}
if routes and prov_mounts.get(penv.get("APP_EDGE_ROUTES_DIR")) != routes.get("source"):
    failures.append("provisioning does not write the routes the edge reads")
if "--watch" not in (services["edge"].get("command") or []):
    failures.append("the edge does not reload app routes")

# EXC-576: DocumentDB is off by default, and the proxy admits no namespace join then.
if penv.get("DOCUMENTDB_ENABLED") != "false":
    failures.append(f"DOCUMENTDB_ENABLED={penv.get('DOCUMENTDB_ENABLED')!r} by default, want false")
if penv_proxy.get("PROXY_NETNS_JOIN_IMAGES"):
    failures.append("the proxy admits namespace joins with DocumentDB off")

# EXC-592: a project's fixed host port comes from the one window both the
# allocator and the proxy use, below the engine's random ports (32768 up).
tenant_ports = penv_proxy.get("PROXY_HOST_PORTS", "")
if tenant_ports != penv.get("EXCALIBASE_DB_ENDPOINT_PORT_RANGE") or tenant_ports != "30000-30999":
    failures.append(f"tenant port window: proxy {tenant_ports!r}, allocator {penv.get('EXCALIBASE_DB_ENDPOINT_PORT_RANGE')!r}")

# EXC-592: a MySQL project's data volume is named under this prefix.
if penv_proxy.get("PROXY_VOLUME_PREFIX") != "excalibase-vol-":
    failures.append(f"PROXY_VOLUME_PREFIX={penv_proxy.get('PROXY_VOLUME_PREFIX')!r}; MySQL data volumes are excalibase-vol-<project>-mysql")

# EXC-612: every Postgres project's watcher publishes its changes to NATS over
# an internal bus network that only NATS and watchers share: the proxy admits
# it for the watcher image alone, the very image provisioning runs, pinned.
# Tenant databases and apps never join it, and NATS joins nothing else.
bus = networks.get("cdc") or {}
if bus.get("name") != "excalibase-cdc" or not bus.get("internal"):
    failures.append("no internal excalibase-cdc network")
if nets("nats") != {"platform", "cdc"}:
    failures.append(f"nats networks {sorted(nets('nats'))}, want platform and cdc")
if {n for n, s in services.items() if "cdc" in (s.get("networks") or {})} != {"nats"}:
    failures.append("only NATS joins the bus network among the platform's services")
watcher_image = penv.get("WATCHER_IMAGE", "")
if not re.fullmatch(r"docker\.io/excalibase/excalibase-watcher-go@sha256:[0-9a-f]{64}", watcher_image):
    failures.append(f"WATCHER_IMAGE={watcher_image!r}, want the watcher pinned by digest")
if penv_proxy.get("PROXY_CDC_IMAGES") != watcher_image or penv_proxy.get("PROXY_CDC_NETWORK") != bus.get("name"):
    failures.append("the proxy does not admit exactly provisioning's watcher image to the bus network")
if penv.get("DOCKER_CDC_NETWORK") != bus.get("name") or penv.get("WATCHER_NATS_URL") != "nats://nats:4222":
    failures.append("provisioning does not point watchers at NATS on the bus network")
if bus.get("name") in penv_proxy.get("PROXY_NETWORKS", "").split(","):
    failures.append("the bus network is one every container may join")
nats = services["nats"]
if "/data" not in (nats.get("command") or []) or not any(v.get("target") == "/data" for v in nats.get("volumes") or []):
    failures.append("NATS keeps its JetStream store on no volume: a restart loses the CDC stream")
init = services.get("nats-stream-init") or {}
if not init or "@sha256:" not in init.get("image", "") or nets("nats-stream-init") != {"platform"}:
    failures.append("no digest-pinned nats-stream-init on the platform network")
else:
    ienv = init.get("environment") or {}
    if ienv.get("CDC_STREAM_MAX_AGE") != "24h" or str(ienv.get("CDC_STREAM_MAX_BYTES")) != "1073741824":
        failures.append(f"CDC stream limits {ienv.get('CDC_STREAM_MAX_AGE')!r}/{ienv.get('CDC_STREAM_MAX_BYTES')!r}, want 24h/1 GiB by default")
waits = (services["graphql"].get("depends_on") or {}).get("nats-stream-init", {})
if waits.get("condition") != "service_completed_successfully":
    failures.append("graphql starts before the CDC stream exists")

if failures:
    sys.exit("\n".join(failures))
print("single-host hardening ok")
