#!/usr/bin/env sh
# EXC-489: the stack refuses to start on a known or weak secret.
#
#   sh deploy/single-host/tests/preflight.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
fail() { echo "FAIL: $*" >&2; exit 1; }
hex=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
good() { env -i POSTGRES_PASSWORD=$hex APP_DB_PASSWORD=${hex%?}e SETUP_TOKEN=${hex%?}d "$@" sh "$DIR/../preflight.sh"; }

good >/dev/null || fail "generated secrets refused"
for bad in POSTGRES_PASSWORD=password123 POSTGRES_PASSWORD= APP_DB_PASSWORD=changeme SETUP_TOKEN=abc123 \
  "SETUP_TOKEN=$(printf 'a%.0s' $(seq 64))" POSTGRES_PASSWORD="${hex%?}e" ADMIN_PASSWORD=password123 ADMIN_PASSWORD=short; do
  if good "$bad" >/dev/null 2>&1; then
    fail "preflight accepted $bad"
  fi
done
good ADMIN_PASSWORD='a-long-passphrase-of-mine' >/dev/null || fail "a strong admin password refused"
echo "preflight ok"
