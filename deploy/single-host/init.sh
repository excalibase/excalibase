#!/bin/sh
# Writes the .env a single-host install starts from: random secrets, the
# domain the edge serves, and the engine socket the proxy reaches. Refuses to
# overwrite an existing .env, whose secrets the database was created with.
#
#   ./init.sh --domain example.com --admin-email you@example.com [--engine docker|podman] [--manual-unseal]
#             [--apps] [--app-domain apps.example.com] [--app-sandbox runsc|none] [--documentdb] [--host-metrics]
#             [--host-logs] [--log-drains]
#             [--auth-hooks] [--no-host-firewall]
#
# Studio:     https://studio.<domain>
# Data plane: https://api.<domain>   (/{projectId}/graphql, /{projectId}/api/v1, /auth)
# Files:      https://files.<domain> (signed links into the bundled object store)
# --manual-unseal: the vault starts sealed and the admin unseals it after every
# restart (Studio /setup or `excalibase-provisioning vault unseal`); the key is
# never stored on the host.
# --apps: Containers (app hosting) on this host, each app at <app>-<project>.<app domain>
# (default apps.<domain>; point *.<app domain> at this host). Apps run under
# gVisor (runsc must be installed); --app-sandbox none runs them without a sandbox.
# --documentdb: projects may carry DocumentDB, reached by Mongo clients on the host's
# 127.0.0.1 (or over an SSH tunnel) through a gateway container beside the database.
# --host-metrics: Studio's Host page: the host's and every container's CPU, memory, disk,
# network and temperatures, kept 30 days, with alerts by email and webhook. Mounts the
# host's /proc/1/net/dev read-only into provisioning for the network figures.
# --host-logs: stored project log search: the bundled Loki (compose profile hostlogs)
# keeps each project container's output for LOG_RETENTION (168h), searchable in Studio
# after the container is gone; at most HOST_LOGS_DAILY_MB (64) per project per day.
# --auth-hooks: end-user auth hooks, signed calls to each project's own endpoint. The addresses
# the install's names resolve to are refused to them; behind a CDN or proxy, edit AUTH_HOOKS_DENY_CIDRS.
# --log-drains: project Admins forward their database logs to Datadog or an HTTPS endpoint
# (compose profile logdrains). Reads what --host-logs stores, so it turns that on too. The
# forwarder refuses private ranges and the host's own addresses; behind NAT, add the public
# address to LOG_FORWARDER_EXTRA_DENY_CIDRS.
# --no-host-firewall: on Docker, the host-firewall service, which provisioning and the edge wait for,
# drops new connections from the platform's bridges to this host, and forwarded traffic
# from the tenants' bridge to 169.254.0.0/16 (cloud metadata) and private ranges, so a
# tenant database reaches neither a host service nor the LAN. Leave it off to manage that
# yourself, for each bridge (docker network inspect: br-<first 12 of the id>):
# iptables -I INPUT -i <bridge> -m conntrack ! --ctstate ESTABLISHED,RELATED -j DROP,
# and in DOCKER-USER for the tenants' bridge, the same toward those ranges.
# A public domain gets Let's Encrypt certificates for the admin address;
# localhost gets certificates from the edge's own CA.
set -eu

usage() {
  sed -n 's/^#   //p' "$0" >&2
  exit 2
}

env_file=.env
domain=
admin_email=
engine=docker
# Compose waits for healthy services, and only Podman built with systemd, in a
# user session, runs their checks; elsewhere `up -d` waits forever with no error.
# Asked of a real container: nothing Podman reports says so. A probe that cannot
# run learned nothing, and refuses nothing.
check_podman_healthchecks() {
  probe_seconds=${INIT_HEALTH_PROBE_SECONDS:-30}
  probe_name=excalibase-health-probe-$$
  podman_api="podman --url unix://$engine_socket"
  if ! $podman_api run -d --name "$probe_name" --health-cmd true --health-interval 1s --health-retries 1 \
    docker.io/library/alpine:3.20 sleep "$((probe_seconds + 30))" >/dev/null 2>&1; then
    $podman_api rm -f -t 0 "$probe_name" >/dev/null 2>&1 || true
    echo "init.sh: could not check that Podman runs healthchecks (the probe container did not start); continuing. If up -d waits forever, see docs/deployment/podman.md" >&2
    return 0
  fi
  probe_health=
  waited=0
  while [ "$waited" -lt "$probe_seconds" ]; do
    probe_health=$($podman_api inspect --format '{{.State.Health.Status}}' "$probe_name" 2>/dev/null || true)
    [ "$probe_health" = healthy ] && break
    sleep 1
    waited=$((waited + 1))
  done
  $podman_api rm -f -t 0 "$probe_name" >/dev/null 2>&1 || true
  if [ "$probe_health" != healthy ]; then
    echo "init.sh: this Podman does not run healthchecks: a test container stayed '${probe_health:-unknown}' for ${probe_seconds}s, so the install would wait forever at up -d with no error. They run through systemd user timers, which a static Podman build and a host without a systemd user session do not have. Use the distribution's Podman, with lingering on (loginctl enable-linger), or use Docker. See docs/deployment/podman.md" >&2
    exit 1
  fi
}

engine_socket=
engine_socket_uid=
engine_socket_gid=
studio_allow="0.0.0.0/0 ::/0"
manual_unseal=
apps=
documentdb=
host_metrics=
host_logs=
log_drains=
host_firewall=1
app_domain=
app_sandbox=runsc
auth_hooks=

while [ $# -gt 0 ]; do
  case $1 in
    --env-file) env_file=$2; shift 2 ;;
    --domain) domain=$2; shift 2 ;;
    --admin-email) admin_email=$2; shift 2 ;;
    --engine) engine=$2; shift 2 ;;
    --engine-socket) engine_socket=$2; shift 2 ;;
    --engine-socket-gid) engine_socket_gid=$2; shift 2 ;;
    --studio-allow) studio_allow=$2; shift 2 ;;
    --manual-unseal) manual_unseal="VAULT_UNSEAL_PROVIDER=manual"; shift ;;
    --apps) apps="APP_HOSTING_ENABLED=true"; shift ;;
    --documentdb) documentdb="DOCUMENTDB_ENABLED=true"; shift ;;
    --host-metrics) host_metrics="FEATURE_HOSTMETRICS=true
HOST_METRICS_NET_DEV_SOURCE=/proc/1/net/dev"; shift ;;
    --host-logs) host_logs="FEATURE_HOSTLOGS=true"; shift ;;
    --auth-hooks) auth_hooks=1; shift ;;
    --log-drains) log_drains=1; shift ;;
    --no-host-firewall) host_firewall=; shift ;;
    --app-domain) app_domain=$2; shift 2 ;;
    --app-sandbox) app_sandbox=$2; shift 2 ;;
    *) usage ;;
  esac
done

[ -n "$domain" ] && [ -n "$admin_email" ] || usage
if [ -e "$env_file" ]; then
  echo "init.sh: $env_file exists; its secrets are the ones the database was created with. Remove it only together with the volumes." >&2
  exit 1
fi

app_domain=${app_domain:-apps.$domain}
case $app_domain in
  *[!a-z0-9.-]* | .* | *. | *..*) echo "init.sh: --app-domain must be a lowercase DNS name" >&2; exit 2 ;;
esac
case $app_sandbox in
  runsc | none) ;;
  *) echo "init.sh: --app-sandbox must be runsc (gVisor) or none" >&2; exit 2 ;;
esac

case $engine in
  docker)
    engine_socket=${engine_socket:-/var/run/docker.sock}
    engine_socket_uid=1000
    if [ -z "${engine_socket_gid:-}" ]; then
      # ls -n is POSIX; stat -c is GNU only and fails on macOS.
      [ -e "$engine_socket" ] || { echo "init.sh: no engine socket at $engine_socket (set --engine-socket)" >&2; exit 2; }
      engine_socket_gid=$(ls -nd -- "$engine_socket" | awk '{print $4}')
    fi
    ;;
  podman)
    # Rootless: the container's root is this unprivileged user, the socket's owner.
    engine_socket=${engine_socket:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock}
    # Rootful Podman's bridges are on the host and it has no DOCKER-USER chain:
    # nothing would keep a tenant off the host (EXC-683). Fail closed.
    rootless_hint="Run this as an unprivileged user with rootless Podman (systemctl --user enable --now podman.socket), or use Docker."
    init_uid=${INIT_UID:-$(id -u)}
    if [ "$init_uid" = 0 ]; then
      echo "init.sh: rootful Podman is not supported: this is running as root, and nothing would guard the host from tenant containers. $rootless_hint" >&2
      exit 1
    fi
    if [ ! -e "$engine_socket" ]; then
      echo "init.sh: no engine socket at $engine_socket to check for rootless Podman. Start it first (systemctl --user enable --now podman.socket), or set --engine-socket." >&2
      exit 1
    fi
    socket_owner=$(ls -nLd -- "$engine_socket" | awk '{print $3}')
    if [ "$socket_owner" != "$init_uid" ]; then
      echo "init.sh: rootful Podman is not supported: $engine_socket belongs to uid $socket_owner, not this user, and nothing would guard the host from tenant containers. $rootless_hint" >&2
      exit 1
    fi
    # The owner of a file proves nothing alone: ask the engine. The engine proxy asks it again on every start.
    rootless=$(${PODMAN_INFO:-podman --url "unix://$engine_socket" info --format '{{.Host.Security.Rootless}}'} 2>/dev/null || true)
    case $rootless in
      true) ;;
      false)
        echo "init.sh: rootful Podman is not supported: the engine at $engine_socket says it is not rootless, and nothing would guard the host from tenant containers. $rootless_hint" >&2
        exit 1 ;;
      *)
        echo "init.sh: the engine at $engine_socket did not say whether it is rootless. Check it answers (systemctl --user enable --now podman.socket), or set --engine-socket." >&2
        exit 1 ;;
    esac
    check_podman_healthchecks
    engine_socket_uid=0
    engine_socket_gid=0
    ;;
  *) echo "init.sh: --engine must be docker or podman" >&2; exit 2 ;;
esac

secret() { od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; }

case $domain in
  localhost | *.localhost) tls=internal ;;
  *[!0-9.]*) tls=$admin_email ;;
  *) echo "init.sh: Studio and the API are served on subdomains; use a DNS name (for a bare address, $domain.sslip.io)" >&2; exit 2 ;;
esac

# Rootless Podman: tenant limits need the cpu and memory cgroup controllers
# delegated to this user, and ports below ip_unprivileged_port_start are out
# of reach of the edge.
edge_ports=
if [ "$engine" = podman ]; then
  controllers=${CGROUP_CONTROLLERS:-$(cat "/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/cgroup.controllers" 2>/dev/null)}
  for needed in cpu memory; do
    case " $controllers " in
      *" $needed "*) ;;
      *) echo "init.sh: the $needed cgroup controller is not delegated to $(id -un); projects run with CPU and memory limits." >&2
         echo "  As root: mkdir -p /etc/systemd/system/user@.service.d && printf '[Service]\\nDelegate=cpu cpuset io memory pids\\n' > /etc/systemd/system/user@.service.d/delegate.conf && systemctl daemon-reload, then log in again." >&2
         exit 1 ;;
    esac
  done
  port_start=${UNPRIVILEGED_PORT_START:-$(cat /proc/sys/net/ipv4/ip_unprivileged_port_start)}
  if [ "$port_start" -gt 80 ]; then
    if [ "$tls" != internal ]; then
      echo "init.sh: Let's Encrypt needs ports 80 and 443, which rootless Podman cannot open here." >&2
      echo "  As root: echo 'net.ipv4.ip_unprivileged_port_start=80' > /etc/sysctl.d/90-excalibase.conf && sysctl --system" >&2
      exit 1
    fi
    edge_ports="EDGE_HTTP_PORT=8080
EDGE_HTTPS_PORT=8443"
  fi
fi
# On an SELinux host a confined container may not connect to the engine socket.
selinux_label=
if [ "${SELINUX_MODE:-$(getenforce 2>/dev/null || echo Disabled)}" = Enforcing ]; then
  selinux_label="ENGINE_PROXY_SELINUX_LABEL=disable"
fi

# Auth hooks may call only public addresses, and this host's own is one: refuse
# what its names resolve to (a host behind NAT does not hold that address) and
# every global address on its interfaces (behind a CDN the names resolve to the CDN).
hooks=
if [ -n "$auth_hooks" ]; then
  resolved=$(for name in "studio.$domain" "api.$domain" "files.$domain" "$app_domain"; do
    ${RESOLVE_HOSTS:-getent ahosts} "$name" 2>/dev/null | awk '{print $1}'
  done | awk '/:/ || /^[0-9.]+$/' | sort -u)
  if [ -z "$resolved" ]; then
    echo "init.sh: --auth-hooks: no name of this install resolves yet; point api.$domain at this host first, or write the host's public addresses to AUTH_HOOKS_DENY_CIDRS yourself" >&2
    exit 1
  fi
  list_addresses=${LIST_ADDRESSES:-ip -o addr show scope global}
  if command -v "${list_addresses%% *}" >/dev/null 2>&1; then
    local_addresses=$($list_addresses | awk '$3 == "inet" || $3 == "inet6" {sub(/\/.*/, "", $4); print $4}' | sort -u)
    for address in $resolved; do
      if ! printf '%s\n' "$local_addresses" | grep -qxF "$address"; then
        echo "init.sh: WARNING: --auth-hooks: $address (from DNS) is not on this host's interfaces; behind a CDN, proxy or NAT, add the host's public address to AUTH_HOOKS_DENY_CIDRS" >&2
      fi
    done
  else
    local_addresses=
    echo "init.sh: WARNING: --auth-hooks: '${list_addresses%% *}' not found, so this host's interface addresses were not read; add its public addresses to AUTH_HOOKS_DENY_CIDRS yourself" >&2
  fi
  own=$(printf '%s\n%s\n' "$resolved" "$local_addresses" | sort -u \
    | awk '/:/ {printf "%s%s/128", sep, $1; sep=","; next} /^[0-9.]+$/ {printf "%s%s/32", sep, $1; sep=","}')
  hooks="FEATURE_AUTHHOOKS=true
# Addresses the install's names resolved to and the host's interface addresses at init, refused to auth hooks.
AUTH_HOOKS_DENY_CIDRS=$own"
fi

# Log drains read the stored logs, so they bring them; the IPv6 table is
# mounted only where the host has one.
drains=
if [ -n "$log_drains" ]; then
  host_logs="FEATURE_HOSTLOGS=true"
  inet6=${HOST_IF_INET6:-/proc/1/net/if_inet6}
  [ -r "$inet6" ] || inet6=/dev/null
  # A host behind NAT does not hold the address its names point at: refuse those too.
  own=$(for name in "studio.$domain" "api.$domain" "files.$domain" "$app_domain"; do
    ${RESOLVE_HOSTS:-getent ahosts} "$name" 2>/dev/null | awk '{print $1}'
  done | sort -u | awk '/:/ {printf "%s%s/128", sep, $1; sep=","; next} /^[0-9.]+$/ {printf "%s%s/32", sep, $1; sep=","}')
  drains="FEATURE_LOGDRAINS=true
LOG_FORWARDER_TOKEN=$(secret)
# Addresses the install's names resolved to at init, refused to drains. Behind a CDN or proxy, remove its addresses.
LOG_FORWARDER_EXTRA_DENY_CIDRS=$own
HOST_IF_INET6_SOURCE=$inet6"
fi

umask 077
cat >"$env_file" <<ENV
# Written by init.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ). Keep it private and with the volumes.
EXCALIBASE_DOMAIN=$domain
# "internal" (the edge's own CA) or the ACME account address for Let's Encrypt.
EXCALIBASE_TLS=$tls
# Who may open Studio, space-separated CIDRs. Narrow it to your own addresses if you can.
STUDIO_ALLOW_CIDRS=$studio_allow
ADMIN_EMAIL=$admin_email
POSTGRES_PASSWORD=$(secret)
SETUP_TOKEN=$(secret)
# The bundled object store (backups, customer files): its root key and one key per bucket.
OBJECTSTORE_ROOT_SECRET=$(secret)
OBJECTSTORE_BACKUPS_SECRET=$(secret)
OBJECTSTORE_FILES_SECRET=$(secret)
FILES_PUBLIC_URL=https://files.$domain${edge_ports:+:8443}
ENGINE_SOCKET=$engine_socket
ENGINE_SOCKET_UID=$engine_socket_uid
ENGINE_SOCKET_GID=$engine_socket_gid
# Apps are served at <app>-<project>.$app_domain; point *.$app_domain at this host.
APP_DOMAIN=$app_domain
APP_SANDBOX_RUNTIME=$app_sandbox
ENV
# Rootless Podman's bridges are in the user's own namespace: no host firewall to set.
# On Docker, either the firewall or an explicit off, which preflight requires (EXC-683).
profiles=
firewall_env=
if [ "$engine" = docker ] && [ -n "$host_firewall" ]; then
  # The firewall lets tenants' DNS through only to the resolvers Docker forwards to:
  # systemd-resolved's upstream file when /etc/resolv.conf names only its stub, as Docker reads it.
  etc_resolv=${HOST_ETC_RESOLV_CONF:-/etc/resolv.conf}
  resolv=/etc/resolv.conf
  if ! awk '$1 == "nameserver" && $2 !~ /^(127\.|::1$)/ { found = 1 } END { exit !found }' "$etc_resolv" 2>/dev/null &&
    [ -r "${HOST_SYSTEMD_RESOLV_CONF:-/run/systemd/resolve/resolv.conf}" ]; then
    resolv=/run/systemd/resolve/resolv.conf
  fi
  firewall_env="HOST_RESOLV_CONF=$resolv"
elif [ "$engine" = docker ]; then
  firewall_env="HOST_FIREWALL=off"
fi
[ -n "$host_logs" ] && profiles=${profiles:+$profiles,}hostlogs
[ -n "$log_drains" ] && profiles=${profiles:+$profiles,}logdrains
profiles=${profiles:+COMPOSE_PROFILES=$profiles}
for line in "$edge_ports" "$selinux_label" "$manual_unseal" "$apps" "$documentdb" "$host_metrics" "$host_logs" "$profiles" "$firewall_env" "$hooks" "$drains"; do
  [ -n "$line" ] && printf '%s\n' "$line" >>"$env_file"
done
chmod 600 "$env_file"
port=${edge_ports:+:8443}
echo "init.sh: wrote $env_file (Studio: https://studio.$domain$port, API: https://api.$domain$port)"
