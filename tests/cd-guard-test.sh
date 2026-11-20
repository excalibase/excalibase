#!/usr/bin/env bash
# scripts/guard-release-tag.sh: a release-<major>.<minor>-<sha7> tag is immutable,
# so CD refuses to push over one that exists. main-<sha7> is left as it was.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
guard="$here/../scripts/guard-release-tag.sh"
fail=0
stub=$(mktemp); trap 'rm -f "$stub"' EXIT
cat > "$stub" <<'STUB'
#!/usr/bin/env bash
case "$STUB_MODE" in
  found) printf 'Name: x\nDigest: sha256:abc\n' ;;
  notfound) echo 'ERROR: not found' >&2; exit 1 ;;
  *) echo '503 Service Unavailable' >&2; exit 1 ;;
esac
STUB
chmod +x "$stub"

run() { STUB_MODE=$1 INSPECT_CMD="$stub" bash "$guard" "$2" "$3" >/dev/null 2>&1 && echo ok || echo refused; }
expect() {
  got=$(run "$2" "$3" "$4")
  [ "$got" = "$1" ] || { echo "FAIL $2 $3 $4: want $1 got $got"; fail=1; }
}
expect refused found excalibase/studio release-1.7-0123456
expect ok notfound excalibase/studio release-1.7-0123456
expect refused outage excalibase/studio release-1.7-0123456
# main is deliberately mutable, as before: no registry call at all
expect ok found excalibase/studio main-0123456
expect ok outage excalibase/studio main-0123456
# a malformed release tag or repo is refused, never passed on to the registry
expect refused notfound excalibase/studio release-1.7
expect refused notfound 'evil/studio' release-1.7-0123456
expect refused notfound excalibase/studio 'release-1.7-0123456;id'
[ "$fail" -eq 0 ] && echo "cd guard tests passed"
exit "$fail"
