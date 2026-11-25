#!/usr/bin/env sh
# EXC-881: save pulls from the mirror and keeps the original names; load fills what is missing from it.
#   sh deploy/ci/tests/third-party-images-test.sh
set -u

DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir "$WORK/bin"
failed=0

# A docker that records its calls. `image inspect` finds nothing unless FOUND lists the name.
cat > "$WORK/bin/docker" <<'STUB'
#!/usr/bin/env sh
echo "$*" >> "$CALLS"
case "$1" in
  save) shift; [ "$1" = -o ] && : > "$2" ;;
  image) case " ${FOUND:-} " in *" $3 "*) exit 0 ;; *) exit 1 ;; esac ;;
esac
exit 0
STUB
chmod +x "$WORK/bin/docker"
CALLS="$WORK/calls"; export CALLS
PATH="$WORK/bin:$PATH"; export PATH
IMAGE_MIRROR=ghcr.io/excalibase/mirror; export IMAGE_MIRROR

check() { # <name> <condition exit status>
  if [ "$2" -eq 0 ]; then echo "PASS $1"; else echo "FAIL $1"; failed=$((failed + 1)); fi
}

: > "$CALLS"
sh "$DIR/third-party-images.sh" save single-host-fence "$WORK/out.tar.gz" >/dev/null
grep -qx 'pull -q ghcr.io/excalibase/mirror/alpine:3.20' "$CALLS"; check "save pulls a library image from the mirror" $?
grep -qx 'pull -q ghcr.io/excalibase/mirror/docker:29-dind' "$CALLS"; check "save pulls the dind image from the mirror" $?
! grep -E '^pull .*docker\.io' "$CALLS" >/dev/null; check "save pulls nothing from docker.io" $?
grep -qx 'tag ghcr.io/excalibase/mirror/alpine:3.20 docker.io/library/alpine:3.20' "$CALLS"; check "save keeps the original name" $?
grep -E '^save -o .* ghcr.io/excalibase/mirror/alpine:3.20 docker.io/library/alpine:3.20' "$CALLS" >/dev/null; check "save stores both names" $?

: > "$CALLS"
FOUND="docker.io/library/alpine:3.20" sh "$DIR/third-party-images.sh" load single-host-fence "$WORK/out.tar.gz" >/dev/null 2>&1 || true
grep -qx 'pull -q ghcr.io/excalibase/mirror/python:3.12-alpine' "$CALLS"; check "load pulls what is missing from the mirror" $?
! grep -q 'pull -q ghcr.io/excalibase/mirror/alpine:3.20' "$CALLS"; check "load leaves what it found" $?
grep -qx 'tag ghcr.io/excalibase/mirror/python:3.12-alpine docker.io/library/python:3.12-alpine' "$CALLS"; check "load names the pulled image as the code asks" $?

: > "$CALLS"
unset IMAGE_MIRROR
sh "$DIR/third-party-images.sh" save single-host-fence "$WORK/out2.tar.gz" >/dev/null
grep -qx 'pull -q docker.io/library/alpine:3.20' "$CALLS"; check "without a mirror save pulls Docker Hub as before" $?
! grep -q '^tag ' "$CALLS"; check "without a mirror nothing is tagged" $?

[ "$failed" -eq 0 ] || { echo "$failed check(s) failed"; exit 1; }
echo "all third-party image checks passed"
