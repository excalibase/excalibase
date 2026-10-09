#!/bin/sh
# Refuses to start the stack on a known, weak or reused secret. Runs before
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
check_secret APP_DB_PASSWORD "${APP_DB_PASSWORD:-}"
check_secret SETUP_TOKEN "${SETUP_TOKEN:-}"

if [ "${POSTGRES_PASSWORD:-}" = "${APP_DB_PASSWORD:-}" ] || [ "${POSTGRES_PASSWORD:-}" = "${SETUP_TOKEN:-}" ] ||
  [ "${APP_DB_PASSWORD:-}" = "${SETUP_TOKEN:-}" ]; then
  complain "POSTGRES_PASSWORD, APP_DB_PASSWORD and SETUP_TOKEN must all differ"
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
