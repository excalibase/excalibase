#!/usr/bin/env sh
# Builds the runtime image and deploys v2 functions into it with no network
# (EXC-518): what a function imports must already be in the image.
#
#   sh deno-server/scripts/image-offline-test.sh
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
IMAGE=${IMAGE:-excalibase/deno-runtime:offline-test}
NAME=deno-offline-test-$$
docker build -q -t "$IMAGE" "$DIR" > /dev/null
trap 'docker rm -f "$NAME" > /dev/null 2>&1 || true' EXIT
docker run -d --name "$NAME" --network none -e RUNTIME_SECRET=offline -e EXCALIBASE_FUNCTIONS_V2=1 \
  -v "$DIR/test/image/offline-functions.ts:/tmp/offline-functions.ts:ro" "$IMAGE" > /dev/null
for _ in $(seq 1 30); do
  docker exec "$NAME" deno eval 'await fetch("http://localhost:8000/health")' > /dev/null 2>&1 && break
  sleep 1
done
docker exec -e RUNTIME_SECRET=offline "$NAME" deno run -A /tmp/offline-functions.ts
