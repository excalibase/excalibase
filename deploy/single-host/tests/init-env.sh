#!/usr/bin/env sh
# EXC-489: init.sh writes a private .env of random secrets and refuses to
# overwrite one, so no install starts on a published or reused secret.
#
#   sh deploy/single-host/tests/init-env.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
# The engine's own answer on whether it is rootless; a refusal below sets its own.
export PODMAN_INFO="echo true"
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
! grep -q '^FEATURE_HOSTMETRICS' "$WORK/.env" || fail "host monitoring on without --host-metrics"
sh "$DIR/../init.sh" --env-file "$WORK/metrics.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --host-metrics >/dev/null
grep -q '^FEATURE_HOSTMETRICS=true$' "$WORK/metrics.env" && grep -q '^HOST_METRICS_NET_DEV_SOURCE=/proc/1/net/dev$' "$WORK/metrics.env" \
  || fail "--host-metrics not written"
! grep -q '^FEATURE_HOSTLOGS' "$WORK/.env" || fail "stored logs on without --host-logs"
# EXC-683: Docker installs guard the host from the platform's bridges unless told not to.
! grep -q '^COMPOSE_PROFILES' "$WORK/.env" || fail "the firewall is a profile again: off profiles, a gated service would start unguarded"
sh "$DIR/../init.sh" --env-file "$WORK/nofw.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --no-host-firewall >/dev/null
! grep -q '^COMPOSE_PROFILES' "$WORK/nofw.env" || fail "--no-host-firewall still runs it"
# Preflight stops an install with neither the firewall nor this explicit opt-out.
grep -qx 'HOST_FIREWALL=off' "$WORK/nofw.env" || fail "--no-host-firewall is not recorded as a choice"
! grep -q '^HOST_FIREWALL=' "$WORK/.env" || fail "a firewalled install says HOST_FIREWALL"
# The firewall allows DNS to the resolvers Docker forwards to: the file Docker reads.
grep -q '^HOST_RESOLV_CONF=/' "$WORK/.env" || fail "a firewalled install names no resolv.conf"
printf 'nameserver 10.0.0.2\n' >"$WORK/plain-resolv.conf"
HOST_ETC_RESOLV_CONF="$WORK/plain-resolv.conf" sh "$DIR/../init.sh" --env-file "$WORK/plain.env" --domain example.com \
  --admin-email me@example.com --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 >/dev/null
grep -qx 'HOST_RESOLV_CONF=/etc/resolv.conf' "$WORK/plain.env" || fail "HOST_RESOLV_CONF: $(grep HOST_RESOLV "$WORK/plain.env")"
printf 'nameserver 127.0.0.53\noptions edns0\n' >"$WORK/stub-resolv.conf"
printf 'nameserver 192.168.1.1\n' >"$WORK/upstream-resolv.conf"
HOST_ETC_RESOLV_CONF="$WORK/stub-resolv.conf" HOST_SYSTEMD_RESOLV_CONF="$WORK/upstream-resolv.conf" sh "$DIR/../init.sh" \
  --env-file "$WORK/resolved.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 >/dev/null
grep -qx 'HOST_RESOLV_CONF=/run/systemd/resolve/resolv.conf' "$WORK/resolved.env" \
  || fail "systemd-resolved's stub: Docker reads the upstream file, $(grep HOST_RESOLV "$WORK/resolved.env")"
sh "$DIR/../init.sh" --env-file "$WORK/logs.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --host-logs >/dev/null
grep -q '^FEATURE_HOSTLOGS=true$' "$WORK/logs.env" && grep -q '^COMPOSE_PROFILES=hostlogs$' "$WORK/logs.env" \
  || fail "--host-logs not written"
! grep -q '^FEATURE_LOGDRAINS\|^LOG_FORWARDER' "$WORK/.env" "$WORK/logs.env" || fail "log drains on without --log-drains"
printf '#!/bin/sh\ncase $1 in api.*) echo "203.0.113.7 STREAM $1"; echo "2001:db8::7 STREAM $1" ;; esac\n' > "$WORK/resolve"; chmod +x "$WORK/resolve"
RESOLVE_HOSTS="$WORK/resolve" HOST_IF_INET6="$WORK/no-ipv6" sh "$DIR/../init.sh" --env-file "$WORK/drains.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --log-drains >/dev/null
for line in FEATURE_LOGDRAINS=true FEATURE_HOSTLOGS=true COMPOSE_PROFILES=hostlogs,logdrains HOST_IF_INET6_SOURCE=/dev/null; do
  grep -qx "$line" "$WORK/drains.env" || fail "--log-drains did not write $line"
done
grep -qx 'LOG_FORWARDER_EXTRA_DENY_CIDRS=2001:db8::7/128,203.0.113.7/32' "$WORK/drains.env" || fail "--log-drains did not refuse the addresses the install's names resolve to"
sed -n 's/^LOG_FORWARDER_TOKEN=//p' "$WORK/drains.env" | grep -Eq '^[0-9a-f]{64}$' || fail "the forwarder token is not 64 random hex characters"
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

# EXC-683: rootful Podman has neither the rootless exemption nor Docker's DOCKER-USER chain,
# so nothing would guard the host. A root-owned socket, or running as root, is refused.
refused_rootful() {
  if env CGROUP_CONTROLLERS="cpu memory" UNPRIVILEGED_PORT_START=80 "$@" sh "$DIR/../init.sh" --env-file "$WORK/rootful.env" --domain example.com \
    --admin-email me@example.com --engine podman $engine_socket_arg >"$WORK/out" 2>&1; then
    fail "rootful Podman accepted ($*, socket: $engine_socket_arg)"
  fi
  grep -q "rootful Podman" "$WORK/out" || fail "rootful Podman refused without saying so: $(cat "$WORK/out")"
  grep -q "rootless" "$WORK/out" || fail "the refusal does not say what to do: $(cat "$WORK/out")"
  [ ! -e "$WORK/rootful.env" ] || fail "rootful Podman refused but .env written"
}
: >"$WORK/podman.sock"
engine_socket_arg="--engine-socket /etc/passwd" refused_rootful
engine_socket_arg="--engine-socket $WORK/podman.sock" refused_rootful INIT_UID=0
# The file's owner proves nothing alone: a chowned rootful socket, or an engine that does not answer.
engine_socket_arg="--engine-socket $WORK/podman.sock" refused_rootful PODMAN_INFO="echo false"
if env PODMAN_INFO=false CGROUP_CONTROLLERS="cpu memory" UNPRIVILEGED_PORT_START=80 sh "$DIR/../init.sh" --env-file "$WORK/mute.env" --domain example.com \
  --admin-email me@example.com --engine podman --engine-socket "$WORK/podman.sock" >"$WORK/out" 2>&1; then
  fail "podman accepted with an engine that does not say it is rootless"
fi
grep -q "podman.socket" "$WORK/out" || fail "the refusal does not say how to start the socket: $(cat "$WORK/out")"
# A Podman that never runs healthchecks (a static build, no systemd timers) would leave
# `up -d` waiting forever with no error. Probed with a real container, through a stand-in here.
fake_podman() {
  mkdir -p "$WORK/fakebin"
  cat >"$WORK/fakebin/podman" <<FAKE
#!/bin/sh
for arg in "\$@"; do
  case "\$arg" in
    run) echo "\$*" >>"$WORK/probe.log"; [ "${2:-}" = no-run ] && exit 125; echo probe-id; exit 0 ;;
    inspect) echo "$1"; exit 0 ;;
    rm) echo removed >>"$WORK/probe.log"; exit 0 ;;
  esac
done
exit 0
FAKE
  chmod +x "$WORK/fakebin/podman"
  : >"$WORK/probe.log"
}
probed_init() {
  rm -f "$WORK/probed.env"
  env PATH="$WORK/fakebin:$PATH" INIT_HEALTH_PROBE_SECONDS=2 CGROUP_CONTROLLERS="cpu memory" UNPRIVILEGED_PORT_START=80 \
    sh "$DIR/../init.sh" --env-file "$WORK/probed.env" --domain example.com --admin-email me@example.com \
    --engine podman --engine-socket "$WORK/podman.sock" >"$WORK/out" 2>&1
}
fake_podman starting
if probed_init; then fail "a Podman that runs no healthchecks was accepted"; fi
grep -q "healthchecks" "$WORK/out" || fail "the refusal does not name healthchecks: $(cat "$WORK/out")"
grep -q "static" "$WORK/out" || fail "the refusal does not name the static build: $(cat "$WORK/out")"
[ ! -e "$WORK/probed.env" ] || fail "a refused engine still wrote .env"
grep -q removed "$WORK/probe.log" || fail "the probe container was left behind"
grep -q -- "--health-cmd" "$WORK/probe.log" || fail "the probe asked for no healthcheck: $(cat "$WORK/probe.log")"
fake_podman healthy
probed_init || fail "a Podman that runs healthchecks was refused: $(cat "$WORK/out")"
grep -q removed "$WORK/probe.log" || fail "the probe container was left behind on success"
# Nothing learned (no network for the image): not a reason to refuse a working Podman.
fake_podman unused no-run
probed_init || fail "a probe that could not run refused the install: $(cat "$WORK/out")"
grep -q "could not check" "$WORK/out" || fail "a probe that could not run said nothing: $(cat "$WORK/out")"
# No socket to check is no proof of a rootless engine.
if env CGROUP_CONTROLLERS="cpu memory" UNPRIVILEGED_PORT_START=80 sh "$DIR/../init.sh" --env-file "$WORK/nosock.env" --domain example.com \
  --admin-email me@example.com --engine podman --engine-socket "$WORK/absent.sock" >"$WORK/out" 2>&1; then
  fail "podman accepted with no socket to check"
fi
grep -q "podman.socket" "$WORK/out" || fail "the refusal does not say how to start the socket: $(cat "$WORK/out")"
# Rootless Podman: the proxy runs as the container's root, which is the
# unprivileged owner of the socket; low edge ports need the sysctl.
host() { env UNPRIVILEGED_PORT_START=1024 CGROUP_CONTROLLERS="cpuset cpu io memory pids" SELINUX_MODE=Disabled "$@"; }
host sh "$DIR/../init.sh" --env-file "$WORK/pm.env" --domain localhost --admin-email me@example.com \
  --engine podman --engine-socket "$WORK/podman.sock" >/dev/null || fail "podman init refused"
pm() { sed -n "s/^$1=//p" "$WORK/pm.env"; }
[ "$(pm ENGINE_SOCKET_UID):$(pm ENGINE_SOCKET_GID)" = 0:0 ] || fail "podman proxy is not the container root (the socket owner)"
[ "$(pm EDGE_HTTP_PORT):$(pm EDGE_HTTPS_PORT)" = 8080:8443 ] || fail "rootless edge not moved above the privileged ports"
! grep -q '^COMPOSE_PROFILES' "$WORK/pm.env" || fail "rootless Podman runs a host firewall it cannot set"
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

# DocumentDB (EXC-576): off unless asked.
! grep -q '^DOCUMENTDB_ENABLED' "$WORK/.env" || fail "DocumentDB turned on without --documentdb"
sh "$DIR/../init.sh" --env-file "$WORK/docdb.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --documentdb >/dev/null || fail "--documentdb refused"
grep -qx 'DOCUMENTDB_ENABLED=true' "$WORK/docdb.env" || fail "--documentdb not written"

# Auth hooks (EXC-662): off unless asked; on, the addresses the install's names resolve to are refused to them.
! grep -q '^FEATURE_AUTHHOOKS' "$WORK/.env" || fail "auth hooks turned on without --auth-hooks"
printf '#!/bin/sh\ncase $1 in api.*) echo "203.0.113.7 STREAM $1"; echo "2001:db8::7 STREAM $1" ;; esac\n' > "$WORK/resolve"; chmod +x "$WORK/resolve"
# The host's own interfaces count too: behind a CDN the names resolve to the CDN.
cat > "$WORK/addrs" <<'ADDRS'
#!/bin/sh
echo "2: eth0    inet 198.51.100.9/24 brd 198.51.100.255 scope global eth0\       valid_lft forever preferred_lft forever"
echo "2: eth0    inet 203.0.113.7/24 brd 203.0.113.255 scope global secondary eth0\       valid_lft forever preferred_lft forever"
echo "2: eth0    inet6 2001:db8::9/64 scope global dynamic \       valid_lft 86000sec preferred_lft 14000sec"
ADDRS
chmod +x "$WORK/addrs"
RESOLVE_HOSTS="$WORK/resolve" LIST_ADDRESSES="$WORK/addrs" sh "$DIR/../init.sh" --env-file "$WORK/hooks.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --auth-hooks >/dev/null 2>"$WORK/out" || fail "--auth-hooks refused"
grep -qx 'FEATURE_AUTHHOOKS=true' "$WORK/hooks.env" || fail "--auth-hooks not written"
grep -qx 'AUTH_HOOKS_DENY_CIDRS=198.51.100.9/32,2001:db8::7/128,2001:db8::9/128,203.0.113.7/32' "$WORK/hooks.env" \
  || fail "--auth-hooks did not refuse the resolved and the host's own addresses, once each"
grep 'WARNING' "$WORK/out" | grep -q '2001:db8::7' || fail "no warning that a resolved address is not on this host"
! grep 'WARNING' "$WORK/out" | grep -q '203.0.113.7' || fail "warned about a resolved address the host holds"
# Without the ip tool (macOS) the resolved addresses still apply, with a warning.
RESOLVE_HOSTS="$WORK/resolve" LIST_ADDRESSES="$WORK/no-such-ip" sh "$DIR/../init.sh" --env-file "$WORK/hooks3.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --auth-hooks >/dev/null 2>"$WORK/out" || fail "--auth-hooks refused without ip"
grep -qx 'AUTH_HOOKS_DENY_CIDRS=2001:db8::7/128,203.0.113.7/32' "$WORK/hooks3.env" || fail "resolved addresses lost without ip"
grep 'WARNING' "$WORK/out" | grep -q 'interface' || fail "no warning that the host's own addresses were not read"
printf '#!/bin/sh\nexit 2\n' > "$WORK/unresolved"; chmod +x "$WORK/unresolved"
if RESOLVE_HOSTS="$WORK/unresolved" sh "$DIR/../init.sh" --env-file "$WORK/hooks2.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/docker.sock" --engine-socket-gid 998 --auth-hooks >"$WORK/out" 2>&1; then
  fail "--auth-hooks accepted with no address to refuse"
fi
grep -q AUTH_HOOKS_DENY_CIDRS "$WORK/out" || fail "refusal does not name AUTH_HOOKS_DENY_CIDRS"

# macOS (EXC-629): the socket's group is read without GNU-only stat flags. A BSD
# stat on PATH refuses -c, as macOS's does.
mkdir "$WORK/bsd"
cat > "$WORK/bsd/stat" <<'STAT'
#!/bin/sh
case $1 in -c|--format*|--printf*) echo "stat: illegal option -- c" >&2; exit 1 ;; esac
echo "stat: unsupported in this test" >&2; exit 1
STAT
chmod +x "$WORK/bsd/stat"
: > "$WORK/owned.sock"
for path in "$WORK/bsd:$PATH" "$PATH"; do
  rm -f "$WORK/gid.env"
  PATH=$path sh "$DIR/../init.sh" --env-file "$WORK/gid.env" --domain example.com --admin-email me@example.com \
    --engine docker --engine-socket "$WORK/owned.sock" >/dev/null || fail "init.sh failed to read the socket's group (PATH=$path)"
  [ "$(sed -n 's/^ENGINE_SOCKET_GID=//p' "$WORK/gid.env")" = "$(id -g)" ] || fail "wrong engine socket group (PATH=$path)"
done
if sh "$DIR/../init.sh" --env-file "$WORK/nosock.env" --domain example.com --admin-email me@example.com \
  --engine docker --engine-socket "$WORK/missing.sock" >/dev/null 2>&1; then
  fail "init.sh accepted an engine socket that does not exist"
fi

# Required arguments.
if sh "$DIR/../init.sh" --env-file "$WORK/none.env" --admin-email me@example.com >/dev/null 2>&1; then
  fail "init.sh ran without a domain"
fi
echo "init.sh ok"
