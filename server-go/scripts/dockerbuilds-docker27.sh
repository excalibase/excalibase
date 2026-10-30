#!/usr/bin/env bash
# Runs the single-host build runner's real-engine tests against Docker 27 (docker:27.5.1-dind), which
# refuses the isolated gateway with a 400 (EXC-671): the startup check must fall back, warn, and a static
# site must still build on a plain internal network. Opt-in and heavy; run it under the shared lock:
#   flock /tmp/excalibase-heavy.lock server-go/scripts/dockerbuilds-docker27.sh
# RUN narrows the tests (default: the static-site build). Removes only what it starts.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
name=excalibase-it-docker27-$$
cleanup() {
  docker rm -f "$name-go" "$name" >/dev/null 2>&1 || true
  docker volume rm -f "$name-run" "$name-lib" "$name-gomod" "$name-gocache" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --privileged --name "$name" -e DOCKER_TLS_CERTDIR= \
  -v "$name-run:/var/run" -v "$name-lib:/var/lib/docker" docker.io/library/docker:27.5.1-dind >/dev/null
for _ in $(seq 60); do docker exec "$name" docker version >/dev/null 2>&1 && break; sleep 2; done
echo "engine: Docker $(docker exec "$name" docker version --format '{{.Server.Version}}')"

# The tests run in the engine's network namespace: the fixture git server they start listens on the
# address builds see as the host, as it does beside a real engine.
toolchain=$(sed -n 's/^go //p' "$root/go.mod")
docker run --rm --name "$name-go" --network "container:$name" -v "$name-run:/var/run" \
  -v "$root:/src:ro" -w /src -v "$name-gomod:/go/pkg/mod" -v "$name-gocache:/root/.cache/go-build" \
  -e DOCKER_HOST=unix:///var/run/docker.sock -e GOFLAGS=-buildvcs=false "docker.io/library/golang:$toolchain" \
  go test -tags=integration ./internal/dockerbuilds/ -run "${RUN:-TestRealEngineBuildsAStaticSite}" -v -count=1 -timeout 25m
