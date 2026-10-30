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
  SITES_ZONE_ID=0123456789abcdef0123456789abcdef SITES_CUSTOM_DOMAIN_TARGET=cname.example.site SITES_MAX_ZONE_HOSTNAMES=40)
for want in 'SITES_ENABLED: "true"' 'SITES_CLOUDFLARE_ACCOUNT_ID: acct-9f3e' 'SITES_CLOUDFLARE_API_TOKEN: cf-token' \
  'SITES_DISPATCH_NAMESPACE: excalibase-sites' 'SITES_HOSTS_KV_NAMESPACE_ID: kv-7c21' 'SITES_BASE_DOMAIN: example.site' \
  'SITES_ZONE_ID: 0123456789abcdef0123456789abcdef' 'SITES_CUSTOM_DOMAIN_TARGET: cname.example.site' 'SITES_MAX_ZONE_HOSTNAMES: "40"'; do
  printf '%s\n' "$out" | grep -qF "$want" || { echo "sites setting does not reach provisioning: $want" >&2; exit 1; }
done
