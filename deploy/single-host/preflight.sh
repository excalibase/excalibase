#!/bin/sh
# Refuses to start the stack on a known, weak or reused secret, or with auth
# hooks able to reach the host itself. Runs before
# Postgres initialises, so a bad .env never becomes a database password.
# init.sh generates values that pass.
set -eu

weak="password password123 changeme secret admin postgres hana001 example test"
problems=0

complain() {
  echo "preflight: $*" >&2
  problems=$((problems + 1))
}

is_weak() {
  for known in $weak; do
    [ "$1" = "$known" ] && return 0
  done
  return 1
}

# A generated secret: at least 32 hex characters, more than one distinct.
check_secret() {
  name=$1
  value=$2
  if [ -z "$value" ]; then
    complain "$name is empty; run ./init.sh"
  elif is_weak "$value"; then
    complain "$name is a known default; run ./init.sh"
  elif ! printf '%s' "$value" | grep -Eq '^[0-9a-f]{32,}$'; then
    complain "$name must be at least 32 hex characters (openssl rand -hex 32)"
  elif [ "$(printf '%s' "$value" | fold -w1 | sort -u | wc -l)" -lt 8 ]; then
    complain "$name is not random"
  fi
}

check_secret POSTGRES_PASSWORD "${POSTGRES_PASSWORD:-}"
check_secret SETUP_TOKEN "${SETUP_TOKEN:-}"
check_secret OBJECTSTORE_ROOT_SECRET "${OBJECTSTORE_ROOT_SECRET:-}"
check_secret OBJECTSTORE_BACKUPS_SECRET "${OBJECTSTORE_BACKUPS_SECRET:-}"
check_secret OBJECTSTORE_FILES_SECRET "${OBJECTSTORE_FILES_SECRET:-}"
distinct=$(printf '%s\n' "${POSTGRES_PASSWORD:-}" "${SETUP_TOKEN:-}" "${OBJECTSTORE_ROOT_SECRET:-}" \
  "${OBJECTSTORE_BACKUPS_SECRET:-}" "${OBJECTSTORE_FILES_SECRET:-}" | sort -u | wc -l)
if [ "$distinct" -ne 5 ]; then
  complain "every secret in .env must differ from the others"
fi

# Auth hooks (EXC-662): the host's own public address passes the built-in check,
# so hooks start only with it refused, as the Helm chart requires.
# Read as the chart's platform-aio.denyCIDRs does: comma-separated, blanks
# skipped, every entry a CIDR, at least one entry.
if [ "${FEATURE_AUTHHOOKS:-}" = true ]; then
  ipv4='^((25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])[.]){3}(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])/([1-9]|[12][0-9]|3[0-2])$'
  ipv6='^[0-9a-fA-F:]*:[0-9a-fA-F:]*/([1-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$'
  entries=$(printf '%s' "${AUTH_HOOKS_DENY_CIDRS:-}" | tr ',' '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | grep -v '^$' || true)
  invalid=$(printf '%s\n' "$entries" | grep -v '^$' | grep -Ev "$ipv4" | grep -Ev "$ipv6" || true)
  if [ -z "$entries" ]; then
    complain "FEATURE_AUTHHOOKS needs AUTH_HOOKS_DENY_CIDRS: the host's own public addresses, comma-separated (init.sh --auth-hooks fills it)"
  elif [ -n "$invalid" ]; then
    complain "AUTH_HOOKS_DENY_CIDRS must be comma-separated CIDRs such as 203.0.113.7/32,2001:db8::7/128; not a CIDR: $(printf '%s' "$invalid" | tr '\n' ' ')"
  fi
fi

# Log drains (EXC-658): the forwarder's token unlocks every drain's key.
if [ "${FEATURE_LOGDRAINS:-}" = true ]; then
  check_secret LOG_FORWARDER_TOKEN "${LOG_FORWARDER_TOKEN:-}"
  for other in "${POSTGRES_PASSWORD:-}" "${SETUP_TOKEN:-}" "${OBJECTSTORE_ROOT_SECRET:-}" \
    "${OBJECTSTORE_BACKUPS_SECRET:-}" "${OBJECTSTORE_FILES_SECRET:-}"; do
    if [ "${LOG_FORWARDER_TOKEN:-}" = "$other" ]; then
      complain "LOG_FORWARDER_TOKEN must differ from the other secrets"
    fi
  done
fi

# Host firewall (EXC-683). On Docker the platform's bridges reach the host and
# the tenants' bridge the LAN unless it runs; it is on unless HOST_FIREWALL=off,
# and provisioning and the edge wait for it healthy. Here, on every up, an
# install missing what it needs is told so, rather than left waiting on it.
# Rootless Podman (uid 0, which init.sh writes for it alone) is exempt: its
# bridges live in the user's own namespace and its engine has no DOCKER-USER chain.
if [ "${ENGINE_SOCKET_UID:-1000}" != 0 ]; then
  case "${HOST_FIREWALL:-}" in
    off) firewall= ;;
    "") firewall=on ;;
    *) firewall=; complain "HOST_FIREWALL must be off or unset, not ${HOST_FIREWALL}" ;;
  esac
  if [ -n "$firewall" ]; then
    # The firewall runs the platform image, and 1.6.2 and before lack iptables and the
    # proxy's bridges file. Fail closed: only a plain release after 1.6.2, a trunk
    # build or CI's own; floating tags (1.6, 1, latest) name 1.6.2 today.
    tag=${PLATFORM_TAG:-}
    case $tag in
      ci) ;;
      main-*) printf '%s' "${tag#main-}" | grep -Eq '^[0-9a-f]{7,40}$' || tag_refused=1 ;;
      *)
        if printf '%s' "$tag" | grep -Eq '^[0-9]+[.][0-9]+[.][0-9]+$'; then
          [ "$(printf '%s\n%s\n' "$tag" 1.6.2 | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)" != 1.6.2 ] || tag_refused=1
        else
          tag_refused=1
        fi
        ;;
    esac
    [ -z "${tag_refused:-}" ] || complain "TAG '${tag}' does not run the host firewall: set TAG in .env to a trunk build (main-<sha>) until a release after 1.6.2 exists, or HOST_FIREWALL=off"
    # The firewall would refuse these words at the rule; say so before anything starts.
    octet='(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])'
    denied=$(printf '%s' "${APP_EGRESS_EXTRA_DENY_CIDRS:-}" | tr ',' '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | grep -v '^$' \
      | grep -Ev "^($octet[.]){3}$octet/([0-9]|[12][0-9]|3[0-2])\$" | grep -Ev '^[0-9a-fA-F:]*:[0-9a-fA-F:]*/([0-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$' || true)
    [ -z "$denied" ] || complain "APP_EGRESS_EXTRA_DENY_CIDRS must be comma-separated CIDRs; not a CIDR: $(printf '%s' "$denied" | tr '\n' ' ')"
    [ -n "${HOST_RESOLV_CONF:-}" ] || complain "HOST_RESOLV_CONF is unset: the resolv.conf Docker forwards lookups to (/run/systemd/resolve/resolv.conf when /etc/resolv.conf names only 127.0.0.53, else /etc/resolv.conf)"
  fi
fi

# Rootless Podman's egress leaves as this user's own connections, which nothing here can
# guard (EXC-687): it takes a yes, here before provisioning fails on it.
if [ "${ENGINE_SOCKET_UID:-1000}" = 0 ] && [ "${APP_EGRESS:-none}" = internet ] && [ "${APP_EGRESS_UNGUARDED:-}" != accept ]; then
  complain "APP_EGRESS=internet on rootless Podman reaches the LAN and cloud metadata, unguarded: keep APP_EGRESS=none, use Docker, or set APP_EGRESS_UNGUARDED=accept"
fi

# Optional: unset means bootstrap generates one and prints it once.
admin=${ADMIN_PASSWORD:-}
if [ -n "$admin" ] && { is_weak "$admin" || [ "${#admin}" -lt 12 ]; }; then
  complain "ADMIN_PASSWORD is weak; use 12+ characters or leave it unset to have one generated"
fi

if [ "$problems" -gt 0 ]; then
  exit 1
fi
echo "preflight: ok"
