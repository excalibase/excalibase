#!/bin/sh
# Where a gVisor container's root filesystem lives, and that a read-only root with a capped
# /tmp holds under it (EXC-732). Needs runsc registered in the engine (install-gvisor.sh).
#
#   sh deploy/single-host/tests/gvisor-rootfs.sh
#
# 1. A writable root is written to and the change in the engine's disk is printed, as a record of where it lands.
# 2. A read-only root refuses writes outside /tmp, and /tmp stops at its size and is not noexec.
# 3. An image's VOLUME is covered by a capped tmpfs that keeps the image's files (tmpcopyup).
# 4. The probe volume is mounted read-only: nothing an app writes there reaches the disk.
set -eu

DOCKER=${DOCKER:-docker}
RUNTIME=${RUNTIME:-runsc}
IMAGE=${GVISOR_TEST_IMAGE:-docker.io/library/busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e}
[ -z "${IMAGE_MIRROR:-}" ] || IMAGE="$IMAGE_MIRROR/${IMAGE#docker.io/}"
NAME=gvisor-rootfs-$$
VOLUME_IMAGE=gvisor-rootfs-volume-$$
PROBE_VOLUME=gvisor-rootfs-probe-$$
PROBE_FILE=$(mktemp)
fail=0
ok() { echo "  ok: $1"; }
ko() { echo "  FAIL: $1"; fail=1; }
cleanup() {
  $DOCKER rm -fv "$NAME" "$NAME-ro" "$NAME-vol" "$NAME-fill" "$NAME-probe" >/dev/null 2>&1 || true
  $DOCKER volume rm "$PROBE_VOLUME" >/dev/null 2>&1 || true
  $DOCKER rmi "$VOLUME_IMAGE" >/dev/null 2>&1 || true
  rm -f "$PROBE_FILE"
}
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

echo "== a read-only root with a capped, executable /tmp under $RUNTIME"
$DOCKER run -d --name "$NAME-ro" --runtime "$RUNTIME" --read-only --tmpfs /tmp:rw,exec,nosuid,nodev,mode=1777,size=8388608 "$IMAGE" sleep 120 >/dev/null
$DOCKER exec "$NAME-ro" sh -c 'touch /rootfs-probe' >/dev/null 2>&1 && ko "a write to the root succeeded under $RUNTIME" || ok "the root refuses writes under $RUNTIME"
$DOCKER exec "$NAME-ro" sh -c 'touch /tmp/ok' >/dev/null 2>&1 && ok "/tmp is writable under $RUNTIME" || ko "/tmp is not writable under $RUNTIME"
$DOCKER exec "$NAME-ro" sh -c 'dd if=/dev/zero of=/tmp/big bs=1M count=16 2>/dev/null' && ko "16 MiB fit in an 8 MiB /tmp under $RUNTIME" || ok "/tmp stops at its size under $RUNTIME"
size=$($DOCKER exec "$NAME-ro" sh -c 'df -k /tmp | awk "NR==2{print \$2}"')
[ "$size" -le 8192 ] && ok "df reports /tmp as ${size} KiB" || ko "df reports /tmp as ${size} KiB, want at most 8192"
# Docker adds noexec to a tmpfs unless exec is passed, and sqlite-jdbc and Netty run natives from /tmp.
$DOCKER exec "$NAME-ro" sh -c 'grep " /tmp " /proc/mounts' | grep -q noexec && ko "/tmp is mounted noexec under $RUNTIME" || ok "/tmp is not noexec under $RUNTIME"
$DOCKER exec "$NAME-ro" sh -c 'rm -f /tmp/big && cp /bin/busybox /tmp/true && /tmp/true' >/dev/null 2>&1 && ok "a binary copied to /tmp runs under $RUNTIME" || ko "a binary copied to /tmp does not run under $RUNTIME"

echo "== an image VOLUME under a read-only root under $RUNTIME"
printf 'FROM %s\nRUN mkdir /v && echo seed > /v/seed\nVOLUME /v\n' "$IMAGE" | $DOCKER build -q -t "$VOLUME_IMAGE" - >/dev/null
# Podman copies the image's files up into a tmpfs (tmpcopyup); Docker rejects the option, and its
# tmpfs starts empty. Both are the engine's behaviour, not gVisor's, so say which this is.
if $DOCKER run -d --name "$NAME-vol" --runtime "$RUNTIME" --read-only --tmpfs /v:rw,exec,nosuid,nodev,tmpcopyup,size=4194304 "$VOLUME_IMAGE" sleep 120 >/dev/null 2>&1; then
  [ "$($DOCKER exec "$NAME-vol" cat /v/seed 2>/dev/null)" = seed ] && ok "tmpcopyup keeps the image's files under $RUNTIME" || ko "tmpcopyup lost the image's files under $RUNTIME"
else
  echo "  finding: this engine rejects tmpcopyup, so an image volume's own files are hidden by its tmpfs"
  $DOCKER rm -f "$NAME-vol" >/dev/null
  $DOCKER run -d --name "$NAME-vol" --runtime "$RUNTIME" --read-only --tmpfs /v:rw,exec,nosuid,nodev,size=4194304 "$VOLUME_IMAGE" sleep 120 >/dev/null
fi
$DOCKER exec "$NAME-vol" sh -c 'dd if=/dev/zero of=/v/big bs=1M count=8 2>/dev/null' && ko "8 MiB fit in a 4 MiB image volume under $RUNTIME" || ok "an image volume stops at its size under $RUNTIME"

echo "== the probe volume is read-only to an app under $RUNTIME"
$DOCKER volume create "$PROBE_VOLUME" >/dev/null
echo hi > "$PROBE_FILE"
# The platform fills the volume in a container it never starts; an app only ever mounts it read-only.
$DOCKER create --name "$NAME-fill" -v "$PROBE_VOLUME:/.excalibase" "$IMAGE" true >/dev/null
$DOCKER cp "$PROBE_FILE" "$NAME-fill:/.excalibase/probe" >/dev/null 2>&1 && ok "the platform fills the probe volume" || ko "the probe volume cannot be filled"
$DOCKER rm "$NAME-fill" >/dev/null
$DOCKER run -d --name "$NAME-probe" --runtime "$RUNTIME" --read-only -v "$PROBE_VOLUME:/.excalibase:ro" "$IMAGE" sleep 120 >/dev/null
[ "$($DOCKER exec "$NAME-probe" cat /.excalibase/probe 2>/dev/null)" = hi ] && ok "an app reads the probe under $RUNTIME" || ko "an app cannot read the probe under $RUNTIME"
$DOCKER exec "$NAME-probe" sh -c 'dd if=/dev/zero of=/.excalibase/filler bs=1M count=1 2>/dev/null' && ko "an app wrote to the probe volume under $RUNTIME" || ok "an app cannot write the probe volume under $RUNTIME"
$DOCKER cp "$PROBE_FILE" "$NAME-probe:/.excalibase/probe2" >/dev/null 2>&1 && ko "a copy into the read-only probe volume worked" || ok "a copy into the read-only probe volume is refused"
$DOCKER cp "$PROBE_FILE" "$NAME-probe:/probe" >/dev/null 2>&1 && ko "a copy into the read-only root worked" || ok "a copy into the read-only root is refused"

[ "$fail" -eq 0 ] && echo "gvisor rootfs checks passed"
exit "$fail"
