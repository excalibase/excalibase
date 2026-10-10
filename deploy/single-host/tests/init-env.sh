#!/usr/bin/env sh
# EXC-489: init.sh writes a private .env of random secrets and refuses to
# overwrite one, so no install starts on a published or reused secret.
#
#   sh deploy/single-host/tests/init-env.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

sh "$DIR/../init.sh" --env-file "$WORK/.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 >/dev/null
[ "$(stat -c %a "$WORK/.env")" = 600 ] || fail ".env is readable by others"

value() { sed -n "s/^$1=//p" "$WORK/.env"; }
for key in POSTGRES_PASSWORD SETUP_TOKEN; do
  echo "$(value $key)" | grep -Eq '^[0-9a-f]{64}$' || fail "$key is not 64 random hex characters"
done
for key in OBJECTSTORE_ROOT_SECRET OBJECTSTORE_BACKUPS_SECRET OBJECTSTORE_FILES_SECRET; do
  echo "$(value $key)" | grep -Eq '^[0-9a-f]{64}$' || fail "$key is not 64 random hex characters"
done
[ "$(for key in POSTGRES_PASSWORD SETUP_TOKEN OBJECTSTORE_ROOT_SECRET OBJECTSTORE_BACKUPS_SECRET OBJECTSTORE_FILES_SECRET; do value $key; done | sort -u | wc -l)" = 5 ] \
  || fail "two secrets are equal"
[ "$(value FILES_PUBLIC_URL)" = https://files.example.com ] || fail "file links are not served at files.<domain>"
! grep -q '^VAULT_UNSEAL_PROVIDER' "$WORK/.env" || fail "manual unseal chosen without --manual-unseal"
sh "$DIR/../init.sh" --env-file "$WORK/manual.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --manual-unseal >/dev/null
grep -q '^VAULT_UNSEAL_PROVIDER=manual$' "$WORK/manual.env" || fail "--manual-unseal not written"
! grep -q '^BACKUP_DEFAULT_' "$WORK/.env" || fail "init.sh wrote a backup target"
[ "$(value EXCALIBASE_DOMAIN)" = example.com ] || fail "domain not written"
[ "$(value ADMIN_EMAIL)" = me@example.com ] || fail "admin email not written"
[ "$(value EXCALIBASE_TLS)" = me@example.com ] || fail "a public domain must get ACME certificates for the admin address"
[ "$(value ENGINE_SOCKET_GID)" = 998 ] || fail "engine socket group not written"

# A second run keeps the secrets the database was initialised with.
first=$(value POSTGRES_PASSWORD)
if sh "$DIR/../init.sh" --env-file "$WORK/.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 >/dev/null 2>&1; then
  fail "init.sh overwrote an existing .env"
fi
[ "$(value POSTGRES_PASSWORD)" = "$first" ] || fail "existing secrets changed"

# localhost and bare addresses cannot get public certificates.
sh "$DIR/../init.sh" --env-file "$WORK/local.env" --domain localhost --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 >/dev/null
grep -q '^EXCALIBASE_TLS=internal$' "$WORK/local.env" || fail "localhost must use the internal CA"


# A bare address has no subdomains for Studio and the API.
if sh "$DIR/../init.sh" --env-file "$WORK/ip.env" --domain 203.0.113.7 --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 >/dev/null 2>&1; then
  fail "init.sh accepted a bare address"
fi

# Rootless Podman: the proxy runs as the container's root, which is the
# unprivileged owner of the socket; low edge ports need the sysctl.
host() { env UNPRIVILEGED_PORT_START=1024 CGROUP_CONTROLLERS="cpuset cpu io memory pids" SELINUX_MODE=Disabled "$@"; }
host sh "$DIR/../init.sh" --env-file "$WORK/pm.env" --domain localhost --admin-email me@example.com \
  --engine podman --engine-socket "$WORK/podman.sock" >/dev/null || fail "podman init refused"
pm() { sed -n "s/^$1=//p" "$WORK/pm.env"; }
[ "$(pm ENGINE_SOCKET_UID):$(pm ENGINE_SOCKET_GID)" = 0:0 ] || fail "podman proxy is not the container root (the socket owner)"
[ "$(pm EDGE_HTTP_PORT):$(pm EDGE_HTTPS_PORT)" = 8080:8443 ] || fail "rootless edge not moved above the privileged ports"
[ "$(pm FILES_PUBLIC_URL)" = https://files.localhost:8443 ] || fail "file links do not carry the moved edge port"
if host sh "$DIR/../init.sh" --env-file "$WORK/pm2.env" --domain example.com --admin-email me@example.com \
  --engine podman --engine-socket "$WORK/podman.sock" >"$WORK/out" 2>&1; then
  fail "public domain accepted with ports 80/443 out of reach"
fi
grep -q ip_unprivileged_port_start "$WORK/out" || fail "refusal does not say how to open ports 80/443"
UNPRIVILEGED_PORT_START=80 CGROUP_CONTROLLERS="memory pids" SELINUX_MODE=Disabled sh "$DIR/../init.sh" --env-file "$WORK/pm3.env" \
  --domain example.com --admin-email me@example.com --engine podman --engine-socket "$WORK/podman.sock" >"$WORK/out" 2>&1 &&
  fail "podman accepted without the cpu controller delegated"
grep -q Delegate "$WORK/out" || fail "refusal does not say how to delegate cpu"
UNPRIVILEGED_PORT_START=80 CGROUP_CONTROLLERS="cpu memory pids" SELINUX_MODE=Enforcing sh "$DIR/../init.sh" --env-file "$WORK/pm4.env" \
  --domain example.com --admin-email me@example.com --engine podman --engine-socket "$WORK/podman.sock" >/dev/null || fail "podman with sysctl refused"
grep -q '^ENGINE_PROXY_SELINUX_LABEL=disable$' "$WORK/pm4.env" || fail "SELinux host: the proxy cannot reach the socket"
! grep -q '^EDGE_HTTP_PORT' "$WORK/pm4.env" || fail "ports moved although 80/443 are reachable"

# Apps (EXC-575): off and at apps.<domain> unless asked; the sandbox is gVisor unless opted out.
[ "$(value APP_DOMAIN)" = apps.example.com ] || fail "apps are not at apps.<domain> by default"
! grep -q '^APP_HOSTING_ENABLED' "$WORK/.env" || fail "apps turned on without --apps"
sh "$DIR/../init.sh" --env-file "$WORK/apps.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 \
  --apps --app-domain run.example.org --app-sandbox none >/dev/null || fail "--apps refused"
apps() { sed -n "s/^$1=//p" "$WORK/apps.env"; }
[ "$(apps APP_HOSTING_ENABLED):$(apps APP_DOMAIN):$(apps APP_SANDBOX_RUNTIME)" = true:run.example.org:none ] \
  || fail "--apps, --app-domain and --app-sandbox not written"
if sh "$DIR/../init.sh" --env-file "$WORK/apps2.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --apps --app-sandbox kata >/dev/null 2>&1; then
  fail "an unknown sandbox was accepted"
fi
if sh "$DIR/../init.sh" --env-file "$WORK/apps3.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --app-domain 'bad domain' >/dev/null 2>&1; then
  fail "an app domain that is not a DNS name was accepted"
fi

# Required arguments.
if sh "$DIR/../init.sh" --env-file "$WORK/none.env" --admin-email me@example.com >/dev/null 2>&1; then
  fail "init.sh ran without a domain"
fi
echo "init.sh ok"
