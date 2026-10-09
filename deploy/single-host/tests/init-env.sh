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
[ "$(value POSTGRES_PASSWORD)" != "$(value SETUP_TOKEN)" ] || fail "two secrets are equal"
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

# Required arguments.
if sh "$DIR/../init.sh" --env-file "$WORK/none.env" --admin-email me@example.com >/dev/null 2>&1; then
  fail "init.sh ran without a domain"
fi
echo "init.sh ok"
