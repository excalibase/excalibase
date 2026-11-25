#!/usr/bin/env sh
# EXC-881: the mirror's naming, the rewrite of compose files and the guard.
#   sh deploy/ci/tests/mirror-images-test.sh
set -u

DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TOOL="$DIR/mirror-images.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
failed=0

expect() { # <name> <want> <got>
  if [ "$2" = "$3" ]; then echo "PASS $1"; else echo "FAIL $1: want '$2', got '$3'"; failed=$((failed + 1)); fi
}

# shellcheck disable=SC1090
. "$TOOL"

expect "library image loses its host and library/" "postgres:16" "$(hub_path docker.io/library/postgres:16)"
expect "implicit library image" "postgres:16" "$(hub_path postgres:16)"
expect "user image keeps its path" "grafana/loki:3.7.8" "$(hub_path docker.io/grafana/loki:3.7.8)"
expect "implicit user image" "grafana/loki:3.7.8" "$(hub_path grafana/loki:3.7.8)"
expect "ghcr image is not Docker Hub" "" "$(hub_path ghcr.io/cloudnative-pg/postgresql@sha256:abc)"
expect "registry with a port is not Docker Hub" "" "$(hub_path localhost:5000/app:1)"

unset IMAGE_MIRROR
expect "without a mirror a reference stays" "docker.io/library/postgres:16" "$(mirror_ref docker.io/library/postgres:16)"
IMAGE_MIRROR=ghcr.io/excalibase/mirror
expect "library image in the mirror" "ghcr.io/excalibase/mirror/postgres:16" "$(mirror_ref docker.io/library/postgres:16)"
expect "digest kept in the mirror" "ghcr.io/excalibase/mirror/grafana/loki:3@sha256:aa" "$(mirror_ref docker.io/grafana/loki:3@sha256:aa)"
expect "other registries are untouched" "ghcr.io/x/y:1" "$(mirror_ref ghcr.io/x/y:1)"
IMAGE_MIRROR=ghcr.io/excalibase/mirror/
expect "a trailing slash is tolerated" "ghcr.io/excalibase/mirror/nats:2.10" "$(mirror_ref nats:2.10)"
unset IMAGE_MIRROR

expect "entry repo" "grafana/loki" "$(entry_repo grafana/loki:3.7.8@sha256:aa)"
expect "entry tag" "3.7.8" "$(entry_tag grafana/loki:3.7.8@sha256:aa)"
expect "digest-only entry gets a derived tag" "d-0123456789ab" "$(entry_tag node@sha256:0123456789abcdef)"
expect "digest-only entry repo" "node" "$(entry_repo node@sha256:0123456789abcdef)"
expect "source repo of a library image" "library/postgres" "$(source_repo postgres)"
expect "source repo of a user image" "grafana/loki" "$(source_repo grafana/loki)"

# rewrite: only listed images move; the platform's own build keeps its name.
printf 'postgres:16\ngrafana/loki:3.7.8@sha256:aa\nexcalibase/excalibase-graphql:1.5.0\nexcalibase/provisioning:latest\n' > "$WORK/list.txt"
cat > "$WORK/compose.yaml" <<'YAML'
    image: docker.io/library/postgres:16
    image: docker.io/grafana/loki:3.7.8@sha256:aa
    image: docker.io/excalibase/provisioning:ci
    image: ${PLATFORM_TAG:-docker.io/excalibase/provisioning:${TAG:-latest}}
    image: ${GRAPHQL_IMAGE:-docker.io/excalibase/excalibase-graphql:${TAG:-1.5.0}}
    image: ghcr.io/x/y:1
YAML
MIRROR_LIST="$WORK/list.txt" IMAGE_MIRROR=ghcr.io/m sh "$TOOL" rewrite "$WORK/compose.yaml"
expect "rewrite" "    image: ghcr.io/m/postgres:16
    image: ghcr.io/m/grafana/loki:3.7.8@sha256:aa
    image: docker.io/excalibase/provisioning:ci
    image: \${PLATFORM_TAG:-docker.io/excalibase/provisioning:\${TAG:-latest}}
    image: \${GRAPHQL_IMAGE:-ghcr.io/m/excalibase/excalibase-graphql:\${TAG:-1.5.0}}
    image: ghcr.io/x/y:1" "$(cat "$WORK/compose.yaml")"
printf '    image: docker.io/library/postgres:16\n' > "$WORK/local.yaml"
MIRROR_LIST="$WORK/list.txt" sh "$TOOL" rewrite "$WORK/local.yaml"
expect "rewrite without a mirror changes nothing" "    image: docker.io/library/postgres:16" "$(cat "$WORK/local.yaml")"

# guard: the shipped list passes; a list without an image CI uses fails and says how to add it.
sh "$TOOL" guard >/dev/null 2>&1 && echo "PASS guard passes on the shipped list" || { echo "FAIL guard fails on the shipped list"; failed=$((failed + 1)); }
grep -v '^caddy:' "$DIR/mirror-images.txt" > "$WORK/short.txt"
if MIRROR_LIST="$WORK/short.txt" sh "$TOOL" guard >"$WORK/out" 2>&1; then
  echo "FAIL guard passed with caddy missing"; failed=$((failed + 1))
else
  grep -q 'caddy:2.11' "$WORK/out" && grep -q 'mirror-images.txt' "$WORK/out" \
    && echo "PASS guard names the missing image and the file" || { echo "FAIL guard message: $(cat "$WORK/out")"; failed=$((failed + 1)); }
fi

# sync: an image the mirror already holds is not copied again; only a rate limit stops the run.
mkdir "$WORK/bin"
cat > "$WORK/bin/crane" <<'STUB'
#!/usr/bin/env sh
echo "$*" >> "$CRANE_CALLS"
case "$1" in
  digest) case "$2" in ghcr.io/*) [ -z "${MIRROR_HAS:-}" ] || echo "$PINNED" ;; *) echo sha256:source ;; esac ;;
  copy) [ -z "${COPY_ERROR:-}" ] || { echo "$COPY_ERROR" >&2; exit 1; } ;;
esac
STUB
chmod +x "$WORK/bin/crane"
export CRANE_CALLS="$WORK/crane.calls" PINNED=sha256:0123abcd
printf 'busybox:1.36\nnats:2.10\n' > "$WORK/two.txt"
: > "$CRANE_CALLS"
PATH="$WORK/bin:$PATH" COPY_ERROR='toomanyrequests: You have reached your pull rate limit' sh "$TOOL" sync "$WORK/two.txt" >"$WORK/out" 2>&1 && echo "FAIL rate limit did not fail the run" || echo "PASS a rate limit fails the run"
expect "a rate limit stops at the first image" "1" "$(grep -c '^copy' "$CRANE_CALLS")"
grep -q 'rate limiting' "$WORK/out"; expect "a rate limit says so" "0" "$?"
: > "$CRANE_CALLS"
PATH="$WORK/bin:$PATH" COPY_ERROR='manifest list must be converted, digest sha256:aaa429bbb' sh "$TOOL" sync "$WORK/two.txt" >"$WORK/out" 2>&1 && echo "FAIL a failure passed" || echo "PASS another failure fails"
expect "another failure does not stop the run" "2" "$(grep -c '^copy' "$CRANE_CALLS")"
grep -q 'rate limiting' "$WORK/out"; expect "another failure is not called a rate limit" "1" "$?"
grep -q 'manifest list must be converted' "$WORK/out"; expect "the real error is shown" "0" "$?"
printf 'busybox:1.37.0@%s\n' "$PINNED" > "$WORK/pinned.txt"
: > "$CRANE_CALLS"
PATH="$WORK/bin:$PATH" MIRROR_HAS=1 sh "$TOOL" sync "$WORK/pinned.txt" >/dev/null 2>&1
expect "a digest the mirror holds is not copied" "0" "$(grep -c '^copy' "$CRANE_CALLS")"
: > "$CRANE_CALLS"
PATH="$WORK/bin:$PATH" sh "$TOOL" sync "$WORK/pinned.txt" >/dev/null 2>&1
grep -q "^copy docker.io/library/busybox@$PINNED ghcr.io/excalibase/mirror/busybox:1.37.0" "$CRANE_CALLS"; expect "a pinned image is copied by digest" "0" "$?"

# guard on a throwaway repository: each way of naming a Docker Hub image is caught, one at a time.
REPO="$WORK/repo"
mkdir -p "$REPO/deploy/ci" "$REPO/.github/workflows" "$REPO/deploy/single-host/tests" "$REPO/server-go/internal/x"
cp "$TOOL" "$REPO/deploy/ci/"
printf 'busybox:1.36\n' > "$REPO/deploy/ci/mirror-images.txt"
printf '#!/bin/sh\ntrue\n' > "$REPO/deploy/ci/third-party-images.sh"
git -C "$REPO" init -q && git -C "$REPO" add -A >/dev/null
inject() { # <name> <file> <content> <image the guard must name>
  printf '%s\n' "$3" > "$REPO/$2"
  git -C "$REPO" add -A >/dev/null
  if sh "$REPO/deploy/ci/mirror-images.sh" guard >"$WORK/guard.out" 2>&1; then
    echo "FAIL guard missed $1"; failed=$((failed + 1))
  elif grep -q "$4" "$WORK/guard.out"; then echo "PASS guard catches $1"
  else echo "FAIL guard named the wrong image for $1: $(cat "$WORK/guard.out")"; failed=$((failed + 1)); fi
  git -C "$REPO" rm -qf --cached "$2" >/dev/null; rm -f "$REPO/$2"
}
inject "docker run in a script" deploy/single-host/tests/a.sh 'docker run --rm -e A=b --name x redis:7 sleep 1' 'redis:7'
inject "docker run over several lines" deploy/single-host/tests/b.sh 'docker run -d \
  --network n \
  memcached:1.6 serve' 'memcached:1.6'
inject "docker pull in a workflow run" .github/workflows/w.yml '      - run: docker pull -q valkey/valkey:8' 'valkey/valkey:8'
inject "docker create with an engine variable" deploy/single-host/tests/c.sh '$ENGINE create --name a haproxy:3.0' 'haproxy:3.0'
inject "an image in a workflow env var" .github/workflows/e.yml '      K3S_IMAGE: rancher/k3s:v9.9.9-k3s1' 'rancher/k3s:v9.9.9-k3s1'
inject "an image in a script variable" deploy/single-host/tests/d.sh 'POSTGRES_IMAGE=postgres:99' 'postgres:99'
inject "COPY --from an image" server-go/Dockerfile 'FROM busybox:1.36
COPY --from=tianon/gosu:1.17 /gosu /gosu' 'tianon/gosu:1.17'
inject "an unqualified image in a live Go test" server-go/internal/x/a_test.go '//go:build live

package x

const img = "redis:7-alpine"' 'redis:7-alpine'
inject "a flag the list did not know" deploy/single-host/tests/f.sh 'docker run --shm-size 1g --pids-limit 5 --rm tomcat:10 true' 'tomcat:10'
inject "k3d cluster create --image" .github/workflows/k.yml '      - run: k3d cluster create c --image rancher/k3s:v8.8.8-k3s1' 'rancher/k3s:v8.8.8-k3s1'
inject_release() { # <name> <content>
  printf '%s\n' "$2" > "$REPO/.github/workflows/pub.yml"
  git -C "$REPO" add -A >/dev/null
  if sh "$REPO/deploy/ci/mirror-images.sh" guard >"$WORK/guard.out" 2>&1; then echo "FAIL guard missed $1"; failed=$((failed + 1))
  else grep -q 'pub.yml' "$WORK/guard.out" && echo "PASS guard catches $1" || { echo "FAIL release guard output for $1: $(cat "$WORK/guard.out")"; failed=$((failed + 1)); }; fi
  git -C "$REPO" rm -qf --cached .github/workflows/pub.yml >/dev/null; rm -f "$REPO/.github/workflows/pub.yml"
}
inject_release "a push: true build with HUB_MIRROR" 'jobs:
  b:
    steps:
      - uses: docker/build-push-action@x
        with:
          push: true
          build-args: HUB_MIRROR=${{ env.HUB_MIRROR }}'
inject_release "a docker push job using IMAGE_MIRROR" '      - run: docker push x:1
      - run: echo $IMAGE_MIRROR'
inject_release "a pushing job naming excalibase/mirror" '      - run: docker buildx build --push --build-arg B=ghcr.io/excalibase/mirror/golang:1 .'
for bad in '../../owner/repo/main' '/etc' 'a//b' 'a/../b' 'feat x' ''; do
  sh "$TOOL" check-ref "$bad" >/dev/null 2>&1 && { echo "FAIL check-ref accepted '$bad'"; failed=$((failed + 1)); } || echo "PASS check-ref rejects '$bad'"
done
for good in main exc-881-ci-mirror release/1.7 v1.2.3; do
  sh "$TOOL" check-ref "$good" >/dev/null 2>&1 && echo "PASS check-ref accepts $good" || { echo "FAIL check-ref rejected $good"; failed=$((failed + 1)); }
done

[ "$failed" -eq 0 ] || { echo "$failed check(s) failed"; exit 1; }
echo "all mirror checks passed"
