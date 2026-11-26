#!/usr/bin/env bash
# scripts/k3d-import-image.sh (EXC-767): k3d image import can print "Successfully imported" after
# every node failed ("ctr: open /k3d/images/...tar: no such file"), so the smoke only found the
# missing image minutes later. The script loads the image itself and checks each node has it.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
script="$here/../scripts/k3d-import-image.sh"
fail=0
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT

# Fake docker. STUB_FAIL_IMPORTS=n makes the first n node imports silently do nothing, which is the
# k3d failure: the command exits 0 but the image never reaches the node.
cat > "$work/docker" <<'STUB'
#!/usr/bin/env bash
state="$STUB_DIR"
case "$1" in
  ps) printf '%s\n' k3d-c-server-0 k3d-c-agent-0 ;;
  save) echo tar-bytes ;;
  exec)
    node=""; for a in "$@"; do case "$a" in k3d-c-*) node=$a ;; esac; done
    if [[ "$*" == *"images import"* ]]; then
      cat >/dev/null
      n=$(cat "$state/imports" 2>/dev/null || echo 0); echo $((n+1)) > "$state/imports"
      if [ "$((n+1))" -le "${STUB_FAIL_IMPORTS:-0}" ]; then exit 0; fi
      echo "docker.io/excalibase/provisioning:ci" >> "$state/$node.images"
    elif [[ "$*" == *"images ls"* ]]; then
      cat "$state/$node.images" 2>/dev/null
    fi ;;
esac
STUB
chmod +x "$work/docker"

run() { # <fail-imports> -> exit code; state in $work/s
  rm -rf "$work/s"; mkdir "$work/s"
  STUB_DIR="$work/s" STUB_FAIL_IMPORTS=$1 DOCKER="$work/docker" RETRY_SLEEP=0 \
    bash "$script" c excalibase/provisioning:ci >"$work/out" 2>&1
}
expect() { # <want-exit> <fail-imports> <label>
  run "$2"; got=$?
  [ "$got" -eq "$1" ] || { echo "FAIL $3: want exit $1 got $got"; cat "$work/out"; fail=1; }
}
expect 0 0 "imports into every node"
expect 0 1 "a silent failed import is retried and then verified"
expect 1 99 "refuses when the image never lands on a node"
[ "$(cat "$work/s/imports")" -ge 3 ] || { echo "FAIL gave up without retrying"; fail=1; }
[ "$fail" -eq 0 ] && echo "k3d import image tests passed"
exit "$fail"
