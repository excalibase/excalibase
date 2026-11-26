#!/bin/sh
# Where a gVisor container's root filesystem lives, and that a read-only root with a capped
# /tmp holds under it (EXC-732). Needs runsc registered in the engine (install-gvisor.sh).
#
#   sh deploy/single-host/tests/gvisor-rootfs.sh
#
# 1. A writable root is written to and the change in the engine's disk is printed, as a record of where it lands.
# 2. A read-only root refuses writes outside /tmp, and /tmp stops at its size.
set -eu

DOCKER=${DOCKER:-docker}
RUNTIME=${RUNTIME:-runsc}
IMAGE=${GVISOR_TEST_IMAGE:-docker.io/library/busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e}
[ -z "${IMAGE_MIRROR:-}" ] || IMAGE="$IMAGE_MIRROR/${IMAGE#docker.io/}"
NAME=gvisor-rootfs-$$
fail=0
ok() { echo "  ok: $1"; }
ko() { echo "  FAIL: $1"; fail=1; }
cleanup() { $DOCKER rm -fv "$NAME" "$NAME-ro" >/dev/null 2>&1 || true; }
trap cleanup EXIT

$DOCKER info --format "{{json .Runtimes}}" | grep -q "$RUNTIME" || { echo "$RUNTIME is not registered in this engine"; exit 2; }
$DOCKER image inspect "$IMAGE" >/dev/null 2>&1 || $DOCKER pull -q "$IMAGE" >/dev/null

echo "== a writable root under $RUNTIME: where do 64 MiB go? (informational: other writers share the disk)"
root=$($DOCKER info --format '{{.DockerRootDir}}')
used_before=$(df -Pk "$root" | awk 'NR==2{print $3}')
$DOCKER run -d --name "$NAME" --runtime "$RUNTIME" "$IMAGE" sh -c 'dd if=/dev/zero of=/filled bs=1M count=64 2>/dev/null; sync; sleep 120' >/dev/null
for _ in $(seq 1 30); do
  $DOCKER exec "$NAME" test -s /filled 2>/dev/null && break
  sleep 1
done
sleep 2
used_after=$(df -Pk "$root" | awk 'NR==2{print $3}')
echo "  finding: the disk under $root changed by $(( (used_after - used_before) / 1024 )) MiB while the container held 64 MiB in its writable root"
$DOCKER rm -fv "$NAME" >/dev/null

echo "== a read-only root with a capped /tmp under $RUNTIME"
$DOCKER run -d --name "$NAME-ro" --runtime "$RUNTIME" --read-only --tmpfs /tmp:rw,nosuid,nodev,mode=1777,size=8388608 "$IMAGE" sleep 120 >/dev/null
$DOCKER exec "$NAME-ro" sh -c 'touch /rootfs-probe' >/dev/null 2>&1 && ko "a write to the root succeeded under $RUNTIME" || ok "the root refuses writes under $RUNTIME"
$DOCKER exec "$NAME-ro" sh -c 'touch /tmp/ok' >/dev/null 2>&1 && ok "/tmp is writable under $RUNTIME" || ko "/tmp is not writable under $RUNTIME"
$DOCKER exec "$NAME-ro" sh -c 'dd if=/dev/zero of=/tmp/big bs=1M count=16 2>/dev/null' && ko "16 MiB fit in an 8 MiB /tmp under $RUNTIME" || ok "/tmp stops at its size under $RUNTIME"
size=$($DOCKER exec "$NAME-ro" sh -c 'df -k /tmp | awk "NR==2{print \$2}"')
[ "$size" -le 8192 ] && ok "df reports /tmp as ${size} KiB" || ko "df reports /tmp as ${size} KiB, want at most 8192"
# The probe a deploy copies in lives on a volume, because the engine refuses a copy into a read-only root.
$DOCKER create --name "$NAME" --runtime "$RUNTIME" --read-only -v /excalibase-probe "$IMAGE" sleep 1 >/dev/null
echo hi > /tmp/gvisor-rootfs-probe.$$
$DOCKER cp /tmp/gvisor-rootfs-probe.$$ "$NAME:/excalibase-probe/probe" >/dev/null 2>&1 && ok "a copy into the probe volume of a read-only container works" || ko "the probe cannot be copied into a read-only container"
$DOCKER cp /tmp/gvisor-rootfs-probe.$$ "$NAME:/probe" >/dev/null 2>&1 && ko "a copy into the read-only root worked" || ok "a copy into the read-only root is refused"
rm -f /tmp/gvisor-rootfs-probe.$$

[ "$fail" -eq 0 ] && echo "gvisor rootfs checks passed"
exit "$fail"
