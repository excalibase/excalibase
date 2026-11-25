#!/usr/bin/env sh
# EXC-752: provisioning pulls through the Docker API with no registry credential (ensureImage), so
# a runner's `docker login` does not reach those pulls and Docker Hub counts them as anonymous.
# Pulling the same references here with the CLI, which uses the login, leaves the daemon an image
# it finds local. The references are read from the sources provisioning reads them from.
#
#   sh deploy/single-host/tests/prepull-provisioning-images.sh
#   PREPULL_LIST=1 sh deploy/single-host/tests/prepull-provisioning-images.sh   # print, pull nothing
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
CFG="$ROOT/server-go/internal"

# postgres:<major>, as config.DockerPostgresImage names it, for each major the journeys provision.
majors=$(grep -rhoE 'postgresVersion\\*":\\*"[0-9]+' "$ROOT/deploy/single-host/tests" | grep -oE '[0-9]+$' | sort -u)
node=$(grep -oE '"node:[0-9]+-bookworm@sha256:[0-9a-f]{64}"' "$CFG/sitebuilds/settings.go" | tr -d '"')
busybox=$(grep -oE 'DefaultSiteBuildPrepareImage = "[^"]+"' "$CFG/config/sites_git.go" | cut -d'"' -f2)
paketo=$(grep -oE 'DefaultAppRunImage = "[^"]+"' "$CFG/config/app_builds.go" | cut -d'"' -f2)

refs=$(for major in $majors; do echo "postgres:$major"; done; echo "$node"; echo "$busybox"; echo "$paketo")
refs=$(echo "$refs" | sed '/^$/d')
# A moved source must not turn into a silent no-op.
[ "$(echo "$refs" | grep -c .)" -ge 7 ] || { echo "prepull: found too few image references:" >&2; echo "$refs" >&2; exit 1; }

if [ -n "${PREPULL_LIST:-}" ]; then
  echo "$refs"
  exit 0
fi

# A failed pull is a warning: the daemon then pulls it itself, as before this step existed.
for ref in $refs; do
  docker pull -q "$ref" >/dev/null || echo "prepull: could not pull $ref; provisioning will try itself" >&2
done
echo "prepull: done"
