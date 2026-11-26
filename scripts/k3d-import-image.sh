#!/usr/bin/env bash
# EXC-767: load a local image into every node of a k3d cluster and prove it arrived.
#
# `k3d image import` sometimes prints "Successfully imported image(s)" and exits 0 after every node
# failed with "ctr: open /k3d/images/...tar: no such file or directory". The smoke then waits three
# minutes on a pod stuck in ImagePullBackOff. This streams `docker save` into each node's containerd
# instead and checks the node lists the image afterwards, retrying the nodes that still lack it.
#
#   scripts/k3d-import-image.sh <cluster> <image>
set -eu

cluster=${1:?usage: k3d-import-image.sh <cluster> <image>}
image=${2:?usage: k3d-import-image.sh <cluster> <image>}
docker_cmd=${DOCKER:-docker}
attempts=${IMPORT_ATTEMPTS:-3}
pause=${RETRY_SLEEP:-5}

tarball=$(mktemp)
trap 'rm -f "$tarball"' EXIT
"$docker_cmd" save "$image" > "$tarball"

nodes=$("$docker_cmd" ps --filter "name=^k3d-${cluster}-(server|agent)-[0-9]+$" --format '{{.Names}}')
[ -n "$nodes" ] || { echo "no k3d nodes found for cluster $cluster" >&2; exit 1; }

has_image() {
  "$docker_cmd" exec "$1" ctr -n k8s.io images ls -q |
    awk -v ref="$image" '$0 == ref || $0 == "docker.io/" ref || $0 == "docker.io/library/" ref { found = 1 } END { exit !found }'
}

pending=$nodes
for attempt in $(seq 1 "$attempts"); do
  still=""
  for node in $pending; do
    "$docker_cmd" exec -i "$node" ctr -n k8s.io images import - < "$tarball" || true
    if has_image "$node"; then
      echo "$image is on $node"
    else
      still="$still $node"
    fi
  done
  pending=${still# }
  [ -z "$pending" ] && exit 0
  echo "attempt $attempt: $image still missing on: $pending" >&2
  sleep "$pause"
done
echo "$image never reached: $pending" >&2
exit 1
