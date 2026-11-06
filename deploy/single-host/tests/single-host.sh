#!/usr/bin/env sh
# EXC-489: on a single host only the HTTPS edge is published, only the engine
# proxy holds the Docker/Podman socket, and the stack refuses to render
# without its generated secrets. Pure `docker compose config`, nothing started.
#
#   sh deploy/single-host/tests/single-host.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
COMPOSE="$DIR/../compose.yaml"
python3 -c 'import yaml' 2>/dev/null || pip3 install --quiet pyyaml

hex=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
render() {
  env -i PATH="$PATH" HOME="$HOME" \
    POSTGRES_PASSWORD=$hex SETUP_TOKEN=$hex \
    EXCALIBASE_DOMAIN=example.test EXCALIBASE_TLS=internal STUDIO_ALLOW_CIDRS="0.0.0.0/0 ::/0" \
    ADMIN_EMAIL=a@example.test ENGINE_SOCKET=/var/run/docker.sock ENGINE_SOCKET_GID=999 \
    OBJECTSTORE_ROOT_SECRET=${hex%?}1 OBJECTSTORE_BACKUPS_SECRET=${hex%?}2 OBJECTSTORE_FILES_SECRET=${hex%?}3 \
    FILES_PUBLIC_URL=https://files.example.test \
    "$@" docker compose --project-directory "$DIR/.." -f "$COMPOSE" config
}

render | python3 "$DIR/single_host_check.py"

# Every generated secret is required: leaving one out fails the render.
for missing in POSTGRES_PASSWORD SETUP_TOKEN EXCALIBASE_DOMAIN EXCALIBASE_TLS STUDIO_ALLOW_CIDRS ENGINE_SOCKET_GID \
  OBJECTSTORE_ROOT_SECRET OBJECTSTORE_BACKUPS_SECRET OBJECTSTORE_FILES_SECRET FILES_PUBLIC_URL; do
  if render "$missing=" >/dev/null 2>&1; then
    echo "rendered without $missing" >&2
    exit 1
  fi
done
# An outside bucket still wins over the bundled store (R2, S3, B2).
render BACKUP_DEFAULT_ENDPOINT=https://acct.r2.cloudflarestorage.com BACKUP_DEFAULT_ACCESS_KEY_ID=r2key \
  BACKUP_DEFAULT_SECRET_ACCESS_KEY=r2secret | grep -q 'BACKUP_DEFAULT_ENDPOINT: https://acct.r2.cloudflarestorage.com' \
  || { echo "an outside backup bucket does not override the bundled store" >&2; exit 1; }
# Manual unseal (EXC-579) reaches provisioning when chosen.
render VAULT_UNSEAL_PROVIDER=manual | grep -q 'VAULT_UNSEAL_PROVIDER: manual' \
  || { echo "VAULT_UNSEAL_PROVIDER=manual does not reach provisioning" >&2; exit 1; }
# DocumentDB (EXC-576) reaches provisioning, and the proxy admits exactly the
# catalogue's gateway, so the two cannot drift apart.
gateway=$(sed -n 's/^documentDBGatewayImage: //p' "$DIR/../../../server-go/internal/config/postgres_catalog.yaml")
[ -n "$gateway" ] || { echo "no gateway image in the catalogue" >&2; exit 1; }
documentdb=$(render DOCUMENTDB_ENABLED=true)
echo "$documentdb" | grep -q 'DOCUMENTDB_ENABLED: "true"' \
  || { echo "DOCUMENTDB_ENABLED=true does not reach provisioning" >&2; exit 1; }
echo "$documentdb" | grep -q "PROXY_NETNS_JOIN_IMAGES: $gateway\$" \
  || { echo "the proxy does not admit the catalogue's gateway $gateway" >&2; exit 1; }
# Host monitoring (EXC-670): the flag and the one read-only host file reach provisioning.
hostmetrics=$(render FEATURE_HOSTMETRICS=true HOST_METRICS_NET_DEV_SOURCE=/proc/1/net/dev)
echo "$hostmetrics" | grep -q 'FEATURE_HOSTMETRICS: "true"' \
  || { echo "FEATURE_HOSTMETRICS=true does not reach provisioning" >&2; exit 1; }
echo "$hostmetrics" | grep -q 'source: /proc/1/net/dev' \
  || { echo "the host's network counters are not mounted with --host-metrics" >&2; exit 1; }
# EXC-683: no network sets driver options (a changed option makes compose
# re-create a network tenants are attached to, which fails on upgrade); the
# firewall guards the networks the engine proxy names the bridges of, runs only
# with its profile, from the release's image (no download at start), alone in
# the host's network namespace with nothing but the capabilities iptables needs,
# and is healthy only while its rules hold.
render | python3 -c '
import sys, yaml
doc = yaml.safe_load(sys.stdin)
for key, net in doc["networks"].items():
    opts = net.get("driver_opts")
    if opts:
        sys.exit(f"network {key} sets driver_opts {opts}: an upgrade would re-create it under its tenants")
if doc["services"].get("host-firewall", {}).get("profiles"):
    sys.exit("the host firewall is behind a profile: off it, gated services fail open")
names = {net["name"] for net in doc["networks"].values()}
listed = set(doc["services"]["engine-proxy"]["environment"]["PROXY_BRIDGE_NETWORKS"].split(","))
if not names <= listed:
    sys.exit(f"the proxy does not name the bridges of {sorted(names - listed)}")
'
render TAG=1.9.9 HOST_RESOLV_CONF=/run/systemd/resolve/resolv.conf | python3 -c '
import sys, yaml
doc = yaml.safe_load(sys.stdin)
fw = doc["services"].get("host-firewall") or sys.exit("no host firewall")
proxy = doc["services"]["engine-proxy"]
if fw["image"] != proxy["image"]:
    sys.exit("host firewall image %s, want the release image %s" % (fw["image"], proxy["image"]))
if "apk" in str(fw.get("command")) + str(fw.get("entrypoint")):
    sys.exit("the host firewall downloads packages when it starts")
if fw.get("network_mode") != "host":
    sys.exit("host firewall must run in the host network namespace, got %r" % fw.get("network_mode"))
if sorted(fw.get("cap_add") or []) != ["NET_ADMIN", "NET_RAW"] or fw.get("cap_drop") != ["ALL"]:
    sys.exit("host firewall capabilities %r / %r" % (fw.get("cap_add"), fw.get("cap_drop")))
if fw.get("privileged") or fw.get("ports") or not fw.get("read_only"):
    sys.exit("host firewall is privileged, published or writable")
mounts = {v.get("target"): v for v in fw.get("volumes") or []}
if set(mounts) != {"/host-firewall.sh", "/var/lib/excalibase-bridges", "/etc/host-resolv.conf"} or not all(v.get("read_only") for v in mounts.values()):
    sys.exit(f"host firewall mounts {sorted(mounts)}, want its script, the bridges and the host resolv.conf, read-only")
if mounts["/etc/host-resolv.conf"].get("source") != "/run/systemd/resolve/resolv.conf":
    sys.exit("the firewall does not read the resolv.conf HOST_RESOLV_CONF names")
pre = doc["services"]["preflight"]["environment"]
want = {"HOST_FIREWALL": "", "PLATFORM_TAG": "1.9.9", "ENGINE_SOCKET_UID": "1000", "HOST_RESOLV_CONF": "/run/systemd/resolve/resolv.conf"}
for key, value in want.items():
    if pre.get(key) != value:
        sys.exit(f"preflight sees {key}={pre.get(key)!r}, want {value!r}: it cannot refuse an unguarded start")
if "/var/run/engine.sock" in str(fw):
    sys.exit("the host firewall holds the engine socket")
if fw["environment"]["HOST_FIREWALL_NETWORKS"] != proxy["environment"]["PROXY_BRIDGE_NETWORKS"]:
    sys.exit("the firewall guards other networks than the proxy names")
for key in ("HOST_FIREWALL", "ENGINE_SOCKET_UID"):
    if key not in fw["environment"]:
        sys.exit(f"the firewall cannot tell it is off: no {key}")
proxy_env = proxy["environment"]
if proxy_env.get("PROXY_FIREWALL_CONTAINER") != fw.get("container_name"):
    sys.exit("the proxy holds tenants to another container than the host firewall")
if not proxy_env.get("PROXY_STOPPED_FILE", "").startswith("/var/lib/excalibase-bridges/") or "ENGINE_SOCKET_UID" not in proxy_env:
    sys.exit("the proxy cannot record stops on its volume, or cannot tell rootless Podman")
if fw["environment"]["HOST_FIREWALL_TENANT_NETWORKS"] != "excalibase-tenants":
    sys.exit("the tenant network is not the one guarded from metadata and the LAN")
if fw.get("healthcheck", {}).get("test") != ["CMD", "sh", "/host-firewall.sh", "check"]:
    sys.exit("the host firewall is healthy without checking its rules")
if "no-new-privileges:true" not in (fw.get("security_opt") or []):
    sys.exit("host firewall may gain privileges")
# What must not run unguarded waits for the firewall healthy; without the profile it starts anyway.
for name in ("provisioning", "edge"):
    gate = (doc["services"][name].get("depends_on") or {}).get("host-firewall")
    # required stays true: an optional dependency that fails is skipped, and the service starts unguarded.
    if (gate or {}).get("condition") != "service_healthy" or (gate or {}).get("required", True) is not True:
        sys.exit(f"{name} does not wait for a healthy host firewall: {gate}")
'
# Stored log search (EXC-672): Loki only with the profile, on a network only provisioning shares.
render | python3 -c 'import sys,yaml; sys.exit("loki runs without the hostlogs profile" if "loki" in yaml.safe_load(sys.stdin)["services"] else 0)'
render COMPOSE_PROFILES=hostlogs FEATURE_HOSTLOGS=true | python3 -c '
import sys, yaml
doc = yaml.safe_load(sys.stdin)
services, networks = doc["services"], doc["networks"]
loki = services.get("loki") or sys.exit("no loki with the hostlogs profile")
prov = services["provisioning"]
if prov["environment"].get("FEATURE_HOSTLOGS") != "true":
    sys.exit("FEATURE_HOSTLOGS=true does not reach provisioning")
if prov["environment"].get("HOST_LOGS_LOKI_URL") != "http://loki:3100":
    sys.exit("provisioning is not pointed at the bundled loki")
if set(loki.get("networks") or {}) != {"logs"} or not networks["logs"].get("internal"):
    sys.exit("loki must sit alone on the internal logs network")
joined = sorted(n for n, s in services.items() if "logs" in (s.get("networks") or {}))
if joined != ["loki", "provisioning"]:
    sys.exit(f"the logs network is joined by {joined}")
if loki.get("ports") or not loki.get("read_only") or "ALL" not in (loki.get("cap_drop") or []):
    sys.exit("loki is published or not hardened")
if not loki.get("mem_limit"):
    sys.exit("loki has no memory limit")
env = prov["environment"]
if env.get("FEATURE_LOGDELETION") != "false":
    sys.exit("log deletion is not off by default")
if env.get("HOST_LOGS_DAILY_MB_BY_PLAN") != "free=64,standard=256,enterprise=1024" or env.get("HOST_LOGS_MIN_FREE_PERCENT") != "10":
    sys.exit("the per-plan budget or the disk floor does not reach provisioning")
'
# EXC-678: the deletion flag reaches provisioning, and Loki states deletion as the chart does.
render COMPOSE_PROFILES=hostlogs FEATURE_HOSTLOGS=true FEATURE_LOGDELETION=true | grep -q 'FEATURE_LOGDELETION: "true"' \
  || { echo "FEATURE_LOGDELETION=true does not reach provisioning" >&2; exit 1; }
python3 - "$DIR/../loki/loki.yaml" <<'PY' || exit 1
import sys, yaml
config = yaml.safe_load(open(sys.argv[1]))
if config["limits_config"].get("deletion_mode") != "filter-and-delete":
    sys.exit("loki.yaml does not state deletion_mode: filter-and-delete")
compactor = config["compactor"]
if not compactor.get("retention_enabled") or compactor.get("delete_request_store") != "filesystem" \
        or compactor.get("delete_request_cancel_period") != "24h":
    sys.exit(f"loki.yaml compactor {compactor} does not take delete requests as the chart does")
PY
# Log drains (EXC-658): the forwarder only with the profile; it reaches provisioning and the internet only.
render | python3 "$DIR/log_drains_check.py" off
render COMPOSE_PROFILES=hostlogs,logdrains FEATURE_HOSTLOGS=true FEATURE_LOGDRAINS=true LOG_FORWARDER_TOKEN=$hex \
  | python3 "$DIR/log_drains_check.py" on
# Previews (EXC-660): the flag reaches provisioning.
render FEATURE_SITEPREVIEWS=true | grep -q 'FEATURE_SITEPREVIEWS: "true"' \
  || { echo "FEATURE_SITEPREVIEWS=true does not reach provisioning" >&2; exit 1; }
echo "single-host required settings ok"
# Email through the operator's own relay (EXC-580) or SES: every setting in .env reaches provisioning.
out=$(render EMAIL_PROVIDER=smtp SMTP_HOST=mail.example.test SMTP_PORT=2587 SMTP_TLS=starttls \
  SMTP_USERNAME=relay-user SMTP_PASSWORD=relay-pass EMAIL_FROM_ADDRESS=noreply@example.test EMAIL_FROM_NAME=Acme \
  SES_ACCESS_KEY_ID=ses-id SES_SECRET_ACCESS_KEY=ses-secret SES_REGION=eu-west-1)
for want in 'EMAIL_PROVIDER: smtp' 'SMTP_HOST: mail.example.test' 'SMTP_PORT: "2587"' 'SMTP_TLS: starttls' \
  'SMTP_USERNAME: relay-user' 'SMTP_PASSWORD: relay-pass' 'EMAIL_FROM_NAME: Acme' \
  'SES_ACCESS_KEY_ID: ses-id' 'SES_SECRET_ACCESS_KEY: ses-secret' 'SES_REGION: eu-west-1'; do
  printf '%s\n' "$out" | grep -qF "$want" || { echo "email setting does not reach provisioning: $want" >&2; exit 1; }
done
# Sites (EXC-353): every Cloudflare setting in .env reaches provisioning.
out=$(render SITES_ENABLED=true SITES_CLOUDFLARE_ACCOUNT_ID=acct-9f3e SITES_CLOUDFLARE_API_TOKEN=cf-token \
  SITES_DISPATCH_NAMESPACE=excalibase-sites SITES_HOSTS_KV_NAMESPACE_ID=kv-7c21 SITES_BASE_DOMAIN=example.site \
  SITES_ZONE_ID=0123456789abcdef0123456789abcdef SITES_CUSTOM_DOMAIN_TARGET=cname.example.site SITES_MAX_ZONE_HOSTNAMES=40 \
  SITES_OUTBOUND_WORKER=excalibase-sites-outbound)
for want in 'SITES_ENABLED: "true"' 'SITES_CLOUDFLARE_ACCOUNT_ID: acct-9f3e' 'SITES_CLOUDFLARE_API_TOKEN: cf-token' \
  'SITES_DISPATCH_NAMESPACE: excalibase-sites' 'SITES_HOSTS_KV_NAMESPACE_ID: kv-7c21' 'SITES_BASE_DOMAIN: example.site' \
  'SITES_ZONE_ID: 0123456789abcdef0123456789abcdef' 'SITES_CUSTOM_DOMAIN_TARGET: cname.example.site' 'SITES_MAX_ZONE_HOSTNAMES: "40"' \
  'SITES_OUTBOUND_WORKER: excalibase-sites-outbound'; do
  printf '%s\n' "$out" | grep -qF "$want" || { echo "sites setting does not reach provisioning: $want" >&2; exit 1; }
done
# Git builds of sites on this host (EXC-671): off by default, and every setting in .env reaches provisioning.
out=$(render)
for want in 'SITES_GIT_BUILDS_ENABLED: "false"' 'FEATURE_SINGLEHOSTBUILDS: "false"' 'SITES_BUILD_SLOTS: "1"'; do
  printf '%s\n' "$out" | grep -qF "$want" || { echo "site builds default does not reach provisioning: $want" >&2; exit 1; }
done
out=$(render TAG=9.9.9 SITES_GIT_BUILDS_ENABLED=true FEATURE_SINGLEHOSTBUILDS=true SITES_GITHUB_APP_ID=4242 SITES_GITHUB_APP_SLUG=acme-sites \
  SITES_GITHUB_CLIENT_ID=Iv1.abc SITES_GITHUB_CLIENT_SECRET=gh-secret SITES_GITHUB_APP_PRIVATE_KEY_FILE=/run/app.pem \
  SITES_GITHUB_WEBHOOK_SECRET=hook-secret SITES_BUILD_SLOTS=2 SITES_BUILD_EGRESS_ALLOW=git.lan:8080 SITES_CLOUDFLARE_API_URL=http://cf.test)
for want in 'SITES_GIT_BUILDS_ENABLED: "true"' 'FEATURE_SINGLEHOSTBUILDS: "true"' 'SITES_GITHUB_APP_ID: "4242"' \
  'SITES_GITHUB_APP_SLUG: acme-sites' 'SITES_GITHUB_CLIENT_ID: Iv1.abc' 'SITES_GITHUB_CLIENT_SECRET: gh-secret' \
  'SITES_GITHUB_APP_PRIVATE_KEY_FILE: /run/app.pem' 'SITES_GITHUB_WEBHOOK_SECRET: hook-secret' 'SITES_BUILD_SLOTS: "2"' \
  'SITES_BUILD_EGRESS_ALLOW: git.lan:8080' 'SITES_CLOUDFLARE_API_URL: http://cf.test' \
  'SITES_BUILD_EGRESS_IMAGE: docker.io/excalibase/provisioning:9.9.9' 'SITES_GITHUB_API_URL: https://api.github.com'; do
  printf '%s\n' "$out" | grep -qF "$want" || { echo "site build setting does not reach provisioning: $want" >&2; exit 1; }
done
echo "single-host site build settings ok"
