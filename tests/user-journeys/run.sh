#!/usr/bin/env bash
# The user-journey gate (EXC-611): a fresh stack, then drive.mjs walks every example app on it the
# way a builder does, ending in each example repo's own Playwright journeys at the app's real URL.
#
#   tests/user-journeys/run.sh <docker|podman|k3d>
#
# docker   single host on this machine's Docker (init.sh + compose, https://*.localhost)
# podman   single host on rootful Podman (`podman compose`) inside a privileged container
# k3d      platform-aio on a fresh k3d cluster (excalibase-service's journeys install)
#
# IMAGES names the images under test by digest (CI resolves them once for all legs); unset, TAG
# (default main) is resolved here. PLATFORM_TAG overrides provisioning/Studio with images built
# locally from a branch. EXAMPLES_DIR holds the example repos checked out, one directory per
# example (cloned with gh when unset). SERVICE_DIR is an excalibase-service checkout (k3d only).
# ARTIFACTS receives logs and the verdicts. Destroys this leg's stack and volumes: never run it on
# an install you keep. Locally it waits for the heavy-suite lock, so it never runs beside another.
set -euo pipefail
LEG=${1:?usage: run.sh <docker|podman|k3d>}
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
BUNDLE=$ROOT/deploy/single-host
LOCK=${HEAVY_LOCK:-${TMPDIR:-/tmp}/excalibase-heavy.lock}
if [ -z "${GATE_LOCKED:-}" ]; then
  mkdir -p "$(dirname "$LOCK")"
  GATE_LOCKED=1 exec flock -o "$LOCK" "$0" "$@"
fi
export TAG=${TAG:-main}
W=$(mktemp -d)
ARTIFACTS=${ARTIFACTS:-$(mktemp -d -t "user-journeys-$LEG.XXXX")}
mkdir -p "$ARTIFACTS"
echo "artifacts: $ARTIFACTS"
REGISTRY_IMAGE=docker.io/library/registry@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373
PIND_IMAGE=quay.io/podman/stable:v5.4.2@sha256:52536d900c9e7e7cc924bb737eb8c570eeaa3c50f6a3b0b563eb0ec9c48807e4
PIND=excalibase-gate-podman
SERVICES="provisioning studio excalibase-auth excalibase-graphql"

# The images under test, pulled by digest and tagged $RUN_TAG for the stack, listed by digest in
# $ARTIFACTS/images.txt. Branch builds (PLATFORM_TAG) have no registry digest and are listed as such.
RUN_TAG=gate-under-test
pin_images() {
  local images=${IMAGES:-}
  if [ -z "$images" ]; then
    for service in $SERVICES; do
      local digest
      digest=$(docker buildx imagetools inspect "docker.io/excalibase/$service:$TAG" | awk '/^Digest:/ { print $2; exit }')
      images="$images docker.io/excalibase/$service@$digest"
    done
  fi
  : > "$ARTIFACTS/images.txt"
  for image in $images; do
    local repository=${image%@*} digest=${image#*@}
    case $digest in sha256:*) ;; *) echo "not pinned by digest: $image" >&2; return 1 ;; esac
    case ${repository##*/} in
      provisioning|studio) if [ -n "${PLATFORM_TAG:-}" ]; then
        echo "$repository:$PLATFORM_TAG (built locally)" >> "$ARTIFACTS/images.txt"; continue
      fi ;;
    esac
    docker pull -q "$image" >/dev/null
    docker tag "$image" "$repository:$RUN_TAG"
    echo "$image" >> "$ARTIFACTS/images.txt"
  done
  export TAG=$RUN_TAG
  cat "$ARTIFACTS/images.txt"
}

# Logs go to artifacts a public repo shows: the bootstrap's admin password never does.
redact() {
  sed -E 's/(password:)[[:space:]]*[^[:space:]]+/\1 [redacted]/Ig'
}

examples_checkout() {
  if [ -n "${EXAMPLES_DIR:-}" ]; then return; fi
  export EXAMPLES_DIR=$W/examples
  for pair in lecture-search:example-lecture-search live-sports:example-live-sports-dashboard bookshelf:example-nestjs-api; do
    gh repo clone "excalibase/${pair#*:}" "$EXAMPLES_DIR/${pair%%:*}" -- -q --depth 1
  done
}

# --- single host ------------------------------------------------------------
single_host_env() {
  local engine=$1 env_file=$2
  shift 2
  (cd "$BUNDLE" && ./init.sh --env-file "$env_file" --domain localhost --admin-email gate@example.com \
    --engine "$engine" --apps --app-sandbox "${APP_SANDBOX:-none}" "$@" >/dev/null)
  cat >> "$env_file" <<ENV
TAG=$TAG
PLATFORM_TAG=${PLATFORM_TAG:-$TAG}
FEATURE_MCP=true
APP_LOCAL_REGISTRIES=localhost:5000
EDGE_BIND_ADDRESS=127.0.0.1
ENV
}

# The bundle names its volumes without the compose project, so this machine's own single host would
# share them: refuse unless every platform volume is a gate run's leftover and no project exists.
docker_guard() {
  for name in excalibase-edge excalibase-provisioning excalibase-gate-registry; do
    if docker inspect "$name" >/dev/null 2>&1; then echo "$name exists; tear that stack down first" >&2; exit 2; fi
  done
  local foreign managed
  foreign=$(docker volume ls -q --filter name=excalibase-platform- | while read -r volume; do
    [ "$(docker volume inspect "$volume" -f '{{index .Labels "com.docker.compose.project"}}')" = excalibase-gate ] || echo "$volume"
  done)
  if [ -n "$foreign" ]; then echo "volumes of another single host exist: $foreign" >&2; exit 2; fi
  managed=$(docker ps -aq --filter label=excalibase.managed=true; docker volume ls -q --filter label=excalibase.managed=true)
  if [ -n "$managed" ]; then
    echo "project containers or volumes exist (label excalibase.managed=true); remove them first" >&2; exit 2
  fi
}

# Runs on this machine with Docker: the edge on 127.0.0.1:443, a registry on 127.0.0.1:5000.
docker_up() {
  single_host_env docker "$W/.env"
  # Fresh state: what an earlier gate run left would answer instead (docker_guard proved it is the gate's).
  "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  docker run -d --name excalibase-gate-registry -p 127.0.0.1:5000:5000 "$REGISTRY_IMAGE" >/dev/null
  "${COMPOSE[@]}" up -d --quiet-pull
  ENGINE_EXEC=(docker)
  PUSH_REGISTRY=localhost:5000
}

COMPOSE=(docker compose -p excalibase-gate --env-file "$W/.env" -f "$BUNDLE/compose.yaml" -f "$BUNDLE/tests/boot-smoke.override.yml")
docker_down() {
  "${COMPOSE[@]}" logs --no-color 2>&1 | redact > "$ARTIFACTS/stack.log" || true
  "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  wipe_managed docker
  docker rm -f excalibase-gate-registry >/dev/null 2>&1 || true
}

wipe_managed() {
  local engine=("$@")
  "${engine[@]}" ps -aq --filter label=excalibase.managed=true | xargs -r "${engine[@]}" rm -f >/dev/null 2>&1 || true
  "${engine[@]}" network ls -q --filter label=excalibase.managed=true | xargs -r "${engine[@]}" network rm >/dev/null 2>&1 || true
  "${engine[@]}" volume ls -q --filter label=excalibase.managed=true | xargs -r "${engine[@]}" volume rm >/dev/null 2>&1 || true
}

# Rootful Podman inside a privileged container: the stack, its registry and `podman compose` live
# there; the edge and the registry are published to this machine as the Docker leg's are.
podman_guard() {
  if docker inspect "$PIND" >/dev/null 2>&1; then echo "$PIND exists; tear it down first" >&2; exit 2; fi
}

podman_up() {
  docker run -d --privileged --name "$PIND" -p 127.0.0.1:443:443 -p 127.0.0.1:80:80 -p 127.0.0.1:5001:5000 \
    -v "$ROOT:/src:ro" -v "$W:/gate" -v /usr/libexec/docker/cli-plugins:/host-compose:ro \
    -e TAG -e PLATFORM_TAG -e APP_SANDBOX "$PIND_IMAGE" sleep infinity >/dev/null
  # The same images the Docker leg ran, by name: Podman gets them from this machine, not the registry.
  local images=""
  for service in $SERVICES; do
    case $service in provisioning|studio) images="$images docker.io/excalibase/$service:${PLATFORM_TAG:-$TAG}" ;;
      *) images="$images docker.io/excalibase/$service:$TAG" ;; esac
  done
  # shellcheck disable=SC2086
  docker save -o "$W/images.tar" $images
  docker exec "$PIND" bash /src/tests/user-journeys/podman-stack.sh
  ENGINE_EXEC=(docker exec "$PIND" podman)
  PUSH_REGISTRY=localhost:5001
}

podman_down() {
  docker exec "$PIND" bash -c 'for c in $(podman ps -a --format "{{.Names}}"); do echo "== $c"; podman logs --tail 300 "$c" 2>&1; done' \
    2>&1 | redact > "$ARTIFACTS/stack.log" || true
  redact < "$W/podman-service.log" > "$ARTIFACTS/podman-service.log" 2>/dev/null || true
  # The container wrote the run's .env and logs as root; hand them back so $W can be removed.
  docker exec "$PIND" chown -R "$(id -u):$(id -g)" /gate >/dev/null 2>&1 || true
  docker rm -f "$PIND" >/dev/null 2>&1 || true
}

single_host_ready() {
  for _ in $(seq 120); do
    [ "$(curl -sk -o /dev/null -w '%{http_code}' https://studio.localhost/api/auth/me)" = 401 ] && break
    sleep 3
  done
  for _ in $(seq 60); do
    [ "$("${ENGINE_EXEC[@]}" inspect excalibase-bootstrap --format '{{.State.Status}}' 2>/dev/null)" = exited ] && break
    sleep 2
  done
  "${ENGINE_EXEC[@]}" logs excalibase-bootstrap 2>&1 | sed -n 's/.*password: //p' | tail -1 > "$W/admin-pass"
  test -s "$W/admin-pass" || { echo "bootstrap printed no admin password"; return 1; }
  # Apps and this machine trust the edge's own CA, as a user's browser would after installing it.
  "${ENGINE_EXEC[@]}" exec excalibase-edge cat /data/caddy/pki/authorities/local/root.crt > "$W/edge-ca.crt"
  export NODE_EXTRA_CA_CERTS=$W/edge-ca.crt CURL_CA_BUNDLE=$W/edge-ca.crt
  cat > "$W/app-exec.sh" <<EXEC
#!/usr/bin/env bash
id=\$1; shift
container=\$(${ENGINE_EXEC[*]} ps -q --filter label=excalibase.app=\$id --filter label=excalibase.component=app | head -1)
exec ${ENGINE_EXEC[*]} exec "\$container" "\$@"
EXEC
  chmod +x "$W/app-exec.sh"
  # A single host runs no CDC watcher, so subscriptions deliver no table changes yet: reported, not hidden.
  export E2E_KNOWN_GAPS=realtime
  export STUDIO_URL=https://studio.localhost API_URL=https://api.localhost APP_SCHEME=https: \
    ADMIN_PASSWORD_FILE=$W/admin-pass APP_EXEC=$W/app-exec.sh PUSH_REGISTRY PULL_REGISTRY=localhost:5000
}

# --- Kubernetes ----------------------------------------------------------------
# excalibase-service's journeys install (ingress on :80, *.127.0.0.1.nip.io), plus a k3d registry
# the nodes pull the apps from. Apps are deployed by digest, which the platform never resolves, so
# it never dials this registry; only the nodes do.
k3d_guard() {
  : "${SERVICE_DIR:?SERVICE_DIR must name an excalibase-service checkout}"
  if k3d cluster get gate >/dev/null 2>&1 || k3d registry get k3d-gate-registry.localhost >/dev/null 2>&1; then
    echo "a k3d cluster or registry named gate exists; delete it first" >&2; exit 2
  fi
}

k3d_up() {
  k3d registry create gate-registry.localhost --port 127.0.0.1:5002 >/dev/null
  local pinned=() import=""
  for image in $(cat "$ARTIFACTS/images.txt"); do
    case $image in
      */provisioning@*) pinned+=(IMAGE_PROVISIONING="$image") ;;
      */studio@*) pinned+=(IMAGE_STUDIO="$image") ;;
      */excalibase-auth@*) pinned+=(IMAGE_AUTH="$image") ;;
      */excalibase-graphql@*) pinned+=(IMAGE_GRAPHQL="$image") ;;
    esac
  done
  # Branch builds exist only on this machine: the cluster gets them by import.
  if [ -n "${PLATFORM_TAG:-}" ]; then
    import="docker.io/excalibase/provisioning:$PLATFORM_TAG docker.io/excalibase/studio:$PLATFORM_TAG"
    pinned+=(IMAGE_PROVISIONING="docker.io/excalibase/provisioning:$PLATFORM_TAG" IMAGE_STUDIO="docker.io/excalibase/studio:$PLATFORM_TAG")
  fi
  (cd "$SERVICE_DIR" && env "${pinned[@]}" IMPORT_IMAGES="$import" CLUSTER=gate K3D_REGISTRY=k3d-gate-registry.localhost:5000 \
    aio-e2e/journeys/up.sh "$W/admin-pass" 2>&1 | redact > "$ARTIFACTS/install.log")
  cat > "$W/app-exec.sh" <<'EXEC'
#!/usr/bin/env bash
id=$1; shift
pod=$(kubectl get pods -A -l "excalibase.io/app=$id,app.kubernetes.io/managed-by" --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.namespace}/{.items[0].metadata.name}')
exec kubectl exec -n "${pod%%/*}" "${pod#*/}" -- "$@"
EXEC
  chmod +x "$W/app-exec.sh"
  export STUDIO_URL=http://localhost API_URL=http://api.127.0.0.1.nip.io APP_SCHEME=http: \
    ADMIN_PASSWORD_FILE=$W/admin-pass APP_EXEC=$W/app-exec.sh \
    PUSH_REGISTRY=localhost:5002 PULL_REGISTRY=k3d-gate-registry.localhost:5000
}

k3d_down() {
  kubectl get pods -A -o wide > "$ARTIFACTS/pods.txt" 2>&1 || true
  for d in provisioning auth graphql studio; do
    kubectl logs -n excalibase-platform "deploy/$d" --tail=-1 2>&1 | redact > "$ARTIFACTS/$d.log" || true
  done
  k3d cluster delete gate >/dev/null 2>&1 || true
  k3d registry delete k3d-gate-registry.localhost >/dev/null 2>&1 || true
}

# Room for every example: one project and app each, sized for a 4-vCPU runner.
size_free_tier() {
  local token
  token=$(curl -sf -X POST "$STUDIO_URL/api/auth/login" -H 'Content-Type: application/json' \
    -d "$(jq -n --rawfile p "$ADMIN_PASSWORD_FILE" '{username:"admin",password:($p|rtrimstr("\n"))}')" | jq -r .token)
  curl -sf -o /dev/null -X PUT "$STUDIO_URL/api/admin/tiers/FREE" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -d '{"maxProjects":4,"instances":1,"storageSize":"2Gi","maxStorageSize":"5Gi","maxAppDiskSize":"1Gi","maxApps":2,"memory":"512Mi","cpu":"0.5","backupEnabled":false,"autoPauseAfterDays":7}'
}

# The example images this run built and the stack's local tags go with the stack.
forget_images() {
  docker images --format '{{.Repository}}:{{.Tag}}' \
    | grep -E "^localhost:500[0-9]/[a-z-]+:gate-|^docker.io/excalibase/[a-z-]+:$RUN_TAG\$|^excalibase/[a-z-]+:$RUN_TAG\$" \
    | xargs -r docker rmi -f >/dev/null 2>&1 || true
  rm -rf "$W"
}

case $LEG in
  docker) docker_guard; trap 'docker_down; forget_images' EXIT; pin_images; docker_up; single_host_ready ;;
  podman) podman_guard; trap 'podman_down; forget_images' EXIT; pin_images; podman_up; single_host_ready ;;
  k3d) k3d_guard; trap 'k3d_down; forget_images' EXIT; pin_images; k3d_up ;;
  *) echo "unknown leg $LEG" >&2; exit 2 ;;
esac
examples_checkout
size_free_tier
REPORT=$ARTIFACTS/verdicts.json node "$HERE/drive.mjs"
