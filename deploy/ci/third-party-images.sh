#!/usr/bin/env sh
# EXC-880: every CI shard used to pull the same third-party images itself, so a run of many shards
# hit the registries' rate limits. One job pulls each image once (with retries), saves them into an
# artifact, and the shards load that instead.
#
#   sh deploy/ci/third-party-images.sh list <set>          print the references of a set
#   sh deploy/ci/third-party-images.sh save <set> <file>   pull each once, write <file> (tar.gz)
#   sh deploy/ci/third-party-images.sh load <set> <file>   load <file>; pull what is still missing
#   sh deploy/ci/third-party-images.sh check               fail when an integration test names an
#                                                          image the go-integration set lacks
#
# Sets: go-integration (testcontainers in the Go Integration shards) and, for the single-host boot
# shards, single-host-fence, single-host-smoke (every smoke shard), single-host-gvisor and
# single-host-mysql (the extras only that shard needs). References carry the same digests the
# code pins; the lists are read from those sources.
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

# Images the integration tests start. `check` fails when a test names one that is not here. Images a
# test builds in code ("postgres:" + major) are listed by hand: the majors 14 to 18 are all here.
list_go_integration() {
  cnpg_base 17
  cat <<'REFS'
busybox:1.36
ghcr.io/documentdb/documentdb/documentdb-local:latest
ghcr.io/project-zot/zot-minimal-linux-amd64:v2.1.22@sha256:27729292c45f1dc0a323b69292d15cc13a5ca4b8bf05f81106e7f89a2b1abd08
docker.io/grafana/loki:3.7.8@sha256:1107dd5274e0ada47e42472b7a7e71f3b2a2fe878878108f3e2f9e51528f0193
localstack/localstack:3.7
mysql:8.4
nats:2.10-alpine
nginx:1.27
node:22-bookworm
pgvector/pgvector:pg16
postgres:14
postgres:14-alpine
postgres:15
postgres:15-alpine
postgres:16
postgres:16-alpine
postgres:17
postgres:17-alpine
postgres:18
rancher/k3s:v1.31.6-k3s1
testcontainers/ryuk:0.13.0
REFS
}

# Reboot-order and bridge-fence checks: Docker in Docker and a python fake.
list_single_host_fence() {
  cat <<'REFS'
docker.io/library/alpine:3.20
docker.io/library/docker:27-dind
docker.io/library/docker:28-dind
docker.io/library/docker:29-dind
docker.io/library/python:3.12-alpine
REFS
}

# What every fresh install pulls: the compose file's own images and the tenant databases.
list_single_host_smoke() {
  PREPULL_LIST=1 sh "$TESTS/prepull-provisioning-images.sh" | grep '^postgres:'
  # Literal references in the compose file (the ${TAG} ones are the platform's own images,
  # built or pulled by the shard itself).
  sed -n 's/^ *image: \([^$ ]*\)$/\1/p' "$COMPOSE"
  sed -n 's/^x-watcher-image: &watcher_image \(.*\)$/\1/p' "$COMPOSE"
  echo docker.io/library/python:3.12-alpine
  # The realtime journey runs this one in every smoke run.
  PREPULL_LIST=1 sh "$TESTS/prepull-provisioning-images.sh" | grep '^busybox:'
}

# Apps, site builds and the DNS stand-in: only the gvisor shard.
list_single_host_gvisor() {
  PREPULL_LIST=1 sh "$TESTS/prepull-provisioning-images.sh" | grep -v '^postgres:\|^busybox:'
  cat <<'REFS'
docker.io/coredns/coredns:1.11.3
docker.io/library/python:3.13-alpine
REFS
}

# Every catalog version: the journey provisions one that is not the newest.
list_single_host_mysql() {
  sed -n 's/^ *image: \([^$ ]*\)$/\1/p' "$MYSQL_CATALOG"
}

refs_of() {
  case "$1" in
    go-integration) list_go_integration ;;
    single-host-fence) list_single_host_fence ;;
    single-host-smoke) list_single_host_smoke ;;
    single-host-gvisor) list_single_host_gvisor ;;
    single-host-mysql) list_single_host_mysql ;;
    *) echo "unknown set: $1" >&2; exit 2 ;;
  esac | sed '/^$/d' | sort -u
}

# Three tries with a growing pause: a rate limit lifts, a network blip passes. No pause after the last.
pull_with_retry() {
  attempt=1
  while :; do
    docker pull -q "$1" >/dev/null && return 0
    echo "pull of $1 failed (try $attempt of 3)" >&2
    [ "$attempt" -lt 3 ] || return 1
    sleep $((attempt * 15))
    attempt=$((attempt + 1))
  done
}

# Go files that carry the integration build tag: tests, and the helpers they share.
integration_files() {
  grep -rlE '^//go:build .*integration' --include='*.go' "$ROOT/server-go"
}

# Image literals the integration tests carry: name:tag, not host:port or a scope like metrics:read,
# and not the made-up ghcr.io/acme/... references the tests only parse. A tag counts when it starts
# with a digit or is latest/main/pgNN, or when the name is an image the list already holds
# (nginx:alpine).
test_image_literals() {
  known_repos=$(refs_of go-integration | sed 's/[:@].*//' | sort -u)
  all=$(
    # shellcheck disable=SC2046 # one word per file
    grep -ohE '"[a-z0-9][a-z0-9._/-]*:[A-Za-z0-9][A-Za-z0-9._-]*(@sha256:[0-9a-f]{64})?"' $(integration_files) |
      tr -d '"' |
      grep -vE '^[^/]*\.[^/]*:[0-9]+$' |
      grep -vE '^ghcr\.io/(acme|a|x)/' |
      sort -u
  )
  echo "$all" | while read -r ref; do
    repo=${ref%%[:@]*}
    if echo "$ref" | grep -qE ':(v?[0-9][A-Za-z0-9._-]*|latest|main|pg[0-9]+)(@|$)' ||
       echo "$known_repos" | grep -qxF "$repo"; then
      echo "$ref"
    fi
  done
}

# Images built in code ("postgres:" + major, "postgres:%s"): the name must be one the list holds.
test_image_prefixes() {
  # shellcheck disable=SC2046 # one word per file
  grep -ohE '"[a-z][a-z0-9/._-]*:(%[sdv])?" *\+|"[a-z][a-z0-9/._-]*:%[sdv]' $(integration_files) |
    sed -E 's/^"//; s/:.*//' | grep -vxE 'localhost|sha256' | sort -u
}

check_go_integration() {
  known=$(refs_of go-integration)
  missing=$(test_image_literals | while read -r ref; do
    echo "$known" | grep -qxF "$ref" || echo "$ref"
  done)
  for repo in $(test_image_prefixes); do
    echo "$known" | grep -qE "^$repo[:@]" || missing="$missing
$repo:<built in code>"
  done
  [ -z "$(echo "$missing" | sed '/^$/d')" ] || {
    echo "integration tests name images that deploy/ci/third-party-images.sh (list_go_integration) lacks:" >&2
    echo "$missing" >&2
    exit 1
  }
  echo "every integration-test image is in the go-integration set"
}

cmd=${1:-}
set_name=${2:-}
case "$cmd" in
  list)
    refs_of "$set_name"
    ;;
  check)
    check_go_integration
    ;;
  save)
    out=${3:?usage: save <set> <file>}
    refs=$(refs_of "$set_name")
    [ "$(echo "$refs" | grep -c .)" -ge 1 ] || { echo "too few references for $set_name:" >&2; echo "$refs" >&2; exit 1; }
    for ref in $refs; do pull_with_retry "$ref" || { echo "could not pull $ref" >&2; exit 1; }; done
    # Not a pipe: dash has no pipefail, so a failed save must stop here, not become a green artifact.
    tar_file=${out%.gz}
    # shellcheck disable=SC2086 # one word per reference
    docker save -o "$tar_file" $refs
    gzip -1 -f "$tar_file"
    ls -l "$out"
    ;;
  load)
    file=${3:?usage: load <set> <file>}
    tar_file=${file%.gz}
    gunzip -c "$file" > "$tar_file"
    docker load -i "$tar_file" >/dev/null
    rm -f "$tar_file"
    # `docker save` and `load` drop a name@sha256 reference on every Docker storage driver, so those
    # always come back unnamed. Their layers are loaded, so this pull only fetches the manifest.
    # A tagged reference that is missing is a real miss: said out loud, then pulled as before.
    for ref in $(refs_of "$set_name"); do
      case "$ref" in
        *@sha256:*) pull_with_retry "$ref" || echo "::warning::could not pull $ref; the test will try itself" ;;
        *)
          docker image inspect "$ref" >/dev/null 2>&1 && continue
          echo "::warning::$ref not found after load; pulling it"
          pull_with_retry "$ref" || echo "::warning::could not pull $ref; the test will try itself"
          ;;
      esac
    done
    ;;
  *)
    echo "usage: $0 list|save|load <set> [file] | check" >&2
    exit 2
    ;;
esac
