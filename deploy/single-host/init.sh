#!/bin/sh
# Writes the .env a single-host install starts from: random secrets, the
# domain the edge serves, and the engine socket the proxy reaches. Refuses to
# overwrite an existing .env, whose secrets the database was created with.
#
#   ./init.sh --domain example.com --admin-email you@example.com [--engine docker|podman]
#
# Studio:     https://studio.<domain>
# Data plane: https://api.<domain>   (/{projectId}/graphql, /{projectId}/api/v1, /auth)
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
engine_socket=
engine_socket_uid=
engine_socket_gid=
studio_allow="0.0.0.0/0 ::/0"

while [ $# -gt 0 ]; do
  case $1 in
    --env-file) env_file=$2; shift 2 ;;
    --domain) domain=$2; shift 2 ;;
    --admin-email) admin_email=$2; shift 2 ;;
    --engine) engine=$2; shift 2 ;;
    --engine-socket) engine_socket=$2; shift 2 ;;
    --engine-socket-gid) engine_socket_gid=$2; shift 2 ;;
    --studio-allow) studio_allow=$2; shift 2 ;;
    *) usage ;;
  esac
done

[ -n "$domain" ] && [ -n "$admin_email" ] || usage
if [ -e "$env_file" ]; then
  echo "init.sh: $env_file exists; its secrets are the ones the database was created with. Remove it only together with the volumes." >&2
  exit 1
fi

case $engine in
  docker)
    engine_socket=${engine_socket:-/var/run/docker.sock}
    engine_socket_uid=1000
    engine_socket_gid=${engine_socket_gid:-$(stat -c %g "$engine_socket")}
    ;;
  podman)
    # Rootless: the container's root is this unprivileged user, the socket's owner.
    engine_socket=${engine_socket:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock}
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
ENGINE_SOCKET=$engine_socket
ENGINE_SOCKET_UID=$engine_socket_uid
ENGINE_SOCKET_GID=$engine_socket_gid
ENV
for line in "$edge_ports" "$selinux_label"; do
  [ -n "$line" ] && printf '%s\n' "$line" >>"$env_file"
done
chmod 600 "$env_file"
port=${edge_ports:+:8443}
echo "init.sh: wrote $env_file (Studio: https://studio.$domain$port, API: https://api.$domain$port)"
