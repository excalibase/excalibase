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
    engine_socket=${engine_socket:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock}
    engine_socket_uid=$(id -u)
    engine_socket_gid=${engine_socket_gid:-$(id -g)}
    ;;
  *) echo "init.sh: --engine must be docker or podman" >&2; exit 2 ;;
esac

secret() { od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; }

case $domain in
  localhost | *.localhost) tls=internal ;;
  *[!0-9.]*) tls=$admin_email ;;
  *) echo "init.sh: Studio and the API are served on subdomains; use a DNS name (for a bare address, $domain.sslip.io)" >&2; exit 2 ;;
esac

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
chmod 600 "$env_file"
echo "init.sh: wrote $env_file (Studio: https://studio.$domain, API: https://api.$domain)"
