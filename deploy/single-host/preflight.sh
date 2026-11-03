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

# Optional: unset means bootstrap generates one and prints it once.
admin=${ADMIN_PASSWORD:-}
if [ -n "$admin" ] && { is_weak "$admin" || [ "${#admin}" -lt 12 ]; }; then
  complain "ADMIN_PASSWORD is weak; use 12+ characters or leave it unset to have one generated"
fi

if [ "$problems" -gt 0 ]; then
  exit 1
fi
echo "preflight: secrets ok"
