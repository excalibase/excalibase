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
# DocumentDB (EXC-576) reaches provisioning, and the proxy admits exactly the
# catalogue's gateway, so the two cannot drift apart.
gateway=$(sed -n 's/^documentDBGatewayImage: //p' "$DIR/../../../server-go/internal/config/postgres_catalog.yaml")
[ -n "$gateway" ] || { echo "no gateway image in the catalogue" >&2; exit 1; }
documentdb=$(render DOCUMENTDB_ENABLED=true)
echo "$documentdb" | grep -q 'DOCUMENTDB_ENABLED: "true"' \
  || { echo "DOCUMENTDB_ENABLED=true does not reach provisioning" >&2; exit 1; }
echo "$documentdb" | grep -q "PROXY_NETNS_JOIN_IMAGES: $gateway\$" \
  || { echo "the proxy does not admit the catalogue's gateway $gateway" >&2; exit 1; }
echo "single-host required settings ok"
# Email through the operator's own relay (EXC-580) or SES: every setting in .env reaches provisioning.
out=$(render EMAIL_PROVIDER=smtp SMTP_HOST=mail.example.test SMTP_PORT=2587 SMTP_TLS=starttls \
  SMTP_USERNAME=relay-user SMTP_PASSWORD=relay-pass EMAIL_FROM_ADDRESS=noreply@example.test EMAIL_FROM_NAME=Acme \
  SES_ACCESS_KEY_ID=ses-id SES_SECRET_ACCESS_KEY=ses-secret SES_REGION=eu-west-1)
for want in 'EMAIL_PROVIDER: smtp' 'SMTP_HOST: mail.example.test' 'SMTP_PORT: "2587"' 'SMTP_TLS: starttls' \
  'SMTP_USERNAME: relay-user' 'SMTP_PASSWORD: relay-pass' 'EMAIL_FROM_NAME: Acme' \
  'SES_ACCESS_KEY_ID: ses-id' 'SES_SECRET_ACCESS_KEY: ses-secret' 'SES_REGION: eu-west-1'; do
  printf '%s\n' "$out" | grep -qF "$want" || { echo "email setting does not reach provisioning: $want" >&2; exit 1; }
done
