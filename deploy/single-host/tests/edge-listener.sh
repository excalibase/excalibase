#!/usr/bin/env sh
# The single-host edge (Caddy) and Studio's nginx sit alone on the edge network
# with provisioning, so provisioning's public listener believes X-Forwarded-For
# only from that subnet; tenant containers join their own network and cannot
# forge it. Pure `docker compose config`, nothing started.
#
#   sh deploy/single-host/tests/edge-listener.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
python3 -c 'import yaml' 2>/dev/null || pip3 install --quiet pyyaml

POSTGRES_PASSWORD=x APP_DB_PASSWORD=x SETUP_TOKEN=x EXCALIBASE_DOMAIN=example.test EXCALIBASE_TLS=internal \
STUDIO_ALLOW_CIDRS=0.0.0.0/0 ADMIN_EMAIL=a@example.test GRAPHQL_PROJECT_ID=x ENGINE_SOCKET=/var/run/docker.sock ENGINE_SOCKET_GID=999 \
  docker compose -f "$DIR/../compose.yaml" config | python3 "$DIR/edge_listener_check.py"
