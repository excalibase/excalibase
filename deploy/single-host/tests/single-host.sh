#!/usr/bin/env sh
# EXC-489: on a single host only the HTTPS edge is published, only the engine
# proxy holds the Docker/Podman socket, and the stack refuses to render
# without its generated secrets. Pure `docker compose config`, nothing started.
#
#   sh deploy/single-host/tests/single-host.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
COMPOSE="$DIR/../compose.yaml"
python3 -c 'import yaml' 2>/dev/null || pip3 install --quiet pyyaml

hex=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
render() {
  env -i PATH="$PATH" HOME="$HOME" \
    POSTGRES_PASSWORD=$hex SETUP_TOKEN=$hex \
    EXCALIBASE_DOMAIN=example.test EXCALIBASE_TLS=internal STUDIO_ALLOW_CIDRS="0.0.0.0/0 ::/0" \
    ADMIN_EMAIL=a@example.test ENGINE_SOCKET=/var/run/docker.sock ENGINE_SOCKET_GID=999 \
    OBJECTSTORE_ROOT_SECRET=${hex%?}1 OBJECTSTORE_BACKUPS_SECRET=${hex%?}2 OBJECTSTORE_FILES_SECRET=${hex%?}3 \
    FILES_PUBLIC_URL=https://files.example.test \
    "$@" docker compose --project-directory "$DIR/.." -f "$COMPOSE" config
}

render | python3 "$DIR/single_host_check.py"

# Every generated secret is required: leaving one out fails the render.
for missing in POSTGRES_PASSWORD SETUP_TOKEN EXCALIBASE_DOMAIN EXCALIBASE_TLS STUDIO_ALLOW_CIDRS ENGINE_SOCKET_GID \
  OBJECTSTORE_ROOT_SECRET OBJECTSTORE_BACKUPS_SECRET OBJECTSTORE_FILES_SECRET FILES_PUBLIC_URL; do
  if render "$missing=" >/dev/null 2>&1; then
    echo "rendered without $missing" >&2
    exit 1
  fi
done
# An outside bucket still wins over the bundled store (R2, S3, B2).
render BACKUP_DEFAULT_ENDPOINT=https://acct.r2.cloudflarestorage.com BACKUP_DEFAULT_ACCESS_KEY_ID=r2key \
  BACKUP_DEFAULT_SECRET_ACCESS_KEY=r2secret | grep -q 'BACKUP_DEFAULT_ENDPOINT: https://acct.r2.cloudflarestorage.com' \
  || { echo "an outside backup bucket does not override the bundled store" >&2; exit 1; }
# Manual unseal (EXC-579) reaches provisioning when chosen.
render VAULT_UNSEAL_PROVIDER=manual | grep -q 'VAULT_UNSEAL_PROVIDER: manual' \
  || { echo "VAULT_UNSEAL_PROVIDER=manual does not reach provisioning" >&2; exit 1; }
echo "single-host required settings ok"
