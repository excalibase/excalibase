#!/usr/bin/env bash
# Checks the tag plan the release workflow uses for the native engine image.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
plan="$here/../scripts/release-native-tags.sh"
fail=0
expect() {
  actual="$("$plan" "$1")"
  if [ "$actual" != "$2" ]; then
    echo "FAIL $1: got '$actual', want '$2'"
    fail=1
  fi
}
expect 1.7.0 "1.7.0-native 1.7-native 1-native native"
expect 10.20.30 "10.20.30-native 10.20-native 10-native native"
expect 1.7.0-rc.1 "1.7.0-rc.1-native"

# inspect-digest: found, genuine not-found, and every other failure
inspect="$here/../scripts/inspect-digest.sh"
stub="$(mktemp)"
trap 'rm -f "$stub"' EXIT
cat > "$stub" <<'STUB'
#!/usr/bin/env bash
case "$STUB_MODE" in
  found) printf 'Name: x\nDigest: sha256:abc\n' ;;
  notfound) echo 'ERROR: docker.io/x:y: not found' >&2; exit 1 ;;
  manifest) echo 'manifest unknown' >&2; exit 1 ;;
  auth) echo 'unauthorized: incorrect username or password' >&2; exit 1 ;;
  ratelimit) echo 'toomanyrequests: 429' >&2; exit 1 ;;
  outage) echo '503 Service Unavailable' >&2; exit 1 ;;
  nodigest) printf 'Name: x\n' ;;
esac
STUB
chmod +x "$stub"
status_of() {
  status=0
  STUB_MODE="$1" INSPECT_CMD="$stub" bash "$inspect" example/image:tag >/dev/null 2>&1 || status=$?
  if [ "$status" -ne "$2" ]; then
    echo "FAIL inspect $1: exit $status, want $2"
    fail=1
  fi
}
status_of found 0
status_of notfound 3
status_of manifest 3
status_of auth 1
status_of ratelimit 1
status_of outage 1
status_of nodigest 1
digest="$(STUB_MODE=found INSPECT_CMD="$stub" bash "$inspect" example/image:tag)"
[ "$digest" = "sha256:abc" ] || { echo "FAIL digest: got '$digest'"; fail=1; }

[ "$fail" -eq 0 ] && echo "release-native-tags: ok"
exit "$fail"
