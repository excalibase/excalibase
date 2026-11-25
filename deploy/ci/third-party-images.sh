#!/usr/bin/env sh
# EXC-880: every CI shard used to pull the same third-party images itself, so a run of many shards
# hit the registries' rate limits. One job pulls each image once (with retries), saves them into an
# artifact, and the shards load that instead.
#
#   sh deploy/ci/third-party-images.sh list <set>          print the references of a set
#   sh deploy/ci/third-party-images.sh save <set> <file>   pull each once, write <file> (tar.gz)
#   sh deploy/ci/third-party-images.sh load <set> <file>   load <file>; pull what is still missing
#
# Sets: go-integration (testcontainers in the Go Integration shards) and single-host (the boot
# shards). References carry the same digests the code pins; the lists are read from those sources.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
CATALOG="$ROOT/server-go/internal/config/postgres_catalog.yaml"
MYSQL_CATALOG="$ROOT/server-go/internal/config/mysql_catalog.yaml"
COMPOSE="$ROOT/deploy/single-host/compose.yaml"
TESTS="$ROOT/deploy/single-host/tests"

# The CNPG operand a major's tests start (an anonymous ghcr pull before this script existed).
cnpg_base() {
  awk -v major="$1" '$1 == "-" && $2 == "major:" { current = $3; gsub(/"/, "", current) }
    $1 == "baseImage:" && current == major { print $2 }' "$CATALOG"
}

list_go_integration() {
  cnpg_base 17
  cat <<'REFS'
postgres:14
postgres:15-alpine
postgres:16-alpine
postgres:17
postgres:17-alpine
postgres:18
nats:2.10-alpine
busybox:1.36
testcontainers/ryuk:0.13.0
REFS
}

list_single_host() {
  PREPULL_LIST=1 sh "$TESTS/prepull-provisioning-images.sh"
  # Literal references in the compose file (the ${TAG} ones are the platform's own images,
  # built or pulled by the shard itself).
  sed -n 's/^ *image: \([^$ ]*\)$/\1/p' "$COMPOSE"
  sed -n 's/^x-watcher-image: &watcher_image \(.*\)$/\1/p' "$COMPOSE"
  sed -n 's/^ *image: \([^$ ]*\)$/\1/p' "$MYSQL_CATALOG" | head -1
  cat <<'REFS'
docker.io/coredns/coredns:1.11.3
docker.io/library/python:3.12-alpine
docker.io/library/python:3.13-alpine
docker.io/library/docker:27-dind
docker.io/library/docker:28-dind
docker.io/library/docker:29-dind
REFS
}

refs_of() {
  case "$1" in
    go-integration) list_go_integration ;;
    single-host) list_single_host ;;
    *) echo "unknown set: $1" >&2; exit 2 ;;
  esac | sed '/^$/d' | sort -u
}

# Three tries with a growing pause: a rate limit lifts, a network blip passes.
pull_with_retry() {
  attempt=1
  while [ "$attempt" -le 3 ]; do
    docker pull -q "$1" >/dev/null && return 0
    echo "pull of $1 failed (try $attempt of 3)" >&2
    sleep $((attempt * 15))
    attempt=$((attempt + 1))
  done
  return 1
}

cmd=${1:-}
set_name=${2:-}
case "$cmd" in
  list)
    refs_of "$set_name"
    ;;
  save)
    out=${3:?usage: save <set> <file>}
    refs=$(refs_of "$set_name")
    [ "$(echo "$refs" | grep -c .)" -ge 5 ] || { echo "too few references for $set_name:" >&2; echo "$refs" >&2; exit 1; }
    for ref in $refs; do pull_with_retry "$ref" || { echo "could not pull $ref" >&2; exit 1; }; done
    # shellcheck disable=SC2086 # one word per reference
    docker save $refs | gzip -1 > "$out"
    ls -l "$out"
    ;;
  load)
    file=${3:?usage: load <set> <file>}
    gunzip -c "$file" | docker load >/dev/null
    # An image saved by digest can come back without that name on some Docker storage drivers:
    # pull what is missing, as the shard did before this script existed.
    for ref in $(refs_of "$set_name"); do
      docker image inspect "$ref" >/dev/null 2>&1 && continue
      echo "::warning::$ref not found after load; pulling it"
      pull_with_retry "$ref" || echo "::warning::could not pull $ref; the test will try itself"
    done
    ;;
  *)
    echo "usage: $0 list|save|load <set> [file]" >&2
    exit 2
    ;;
esac
