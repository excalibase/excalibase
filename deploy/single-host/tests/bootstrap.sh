#!/usr/bin/env sh
# Runs deploy/single-host/bootstrap.sh against a stubbed control plane inside a
# throwaway alpine container (the image the bootstrap service runs), so the
# shell under test is the same busybox ash. Nothing else is started.
#
#   sh deploy/single-host/tests/bootstrap.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ENGINE=${1:-docker}
exec "$ENGINE" run --rm \
  -v "$DIR/../bootstrap.sh:/bootstrap.sh:ro" \
  -v "$DIR/bootstrap:/suite:ro" \
  docker.io/library/alpine:3.20 sh /suite/suite.sh
