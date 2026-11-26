#!/usr/bin/env sh
# Runs deploy/single-host/bootstrap.sh against a stubbed control plane inside a
# throwaway alpine container (the image the bootstrap service runs), so the
# shell under test is the same busybox ash. Nothing else is started.
#
#   sh deploy/single-host/tests/bootstrap.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# EXC-881: CI pulls Docker Hub images from its mirror (IMAGE_MIRROR); unset, the names are unchanged.
# shellcheck disable=SC1091
. "$DIR/../../ci/mirror-images.sh"
ENGINE=${1:-docker}
exec "$ENGINE" run --rm \
  -v "$DIR/../bootstrap.sh:/bootstrap.sh:ro" \
  -v "$DIR/bootstrap:/suite:ro" \
  "$(mirror_ref docker.io/library/alpine:3.20)" sh /suite/suite.sh
