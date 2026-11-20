#!/usr/bin/env bash
# cd.yml's "Plan image tags" step decides what CD may publish. Run the real
# step (extracted from the workflow, not a copy) against branch names and check
# the tags: main and release branches get their own, nothing else gets any,
# and none of them is ever latest or a version.
set -eu
root="$(cd "$(dirname "$0")/.." && pwd)"
cd="$root/.github/workflows/cd.yml"
fail=0
step=$(mktemp); out=$(mktemp)
trap 'rm -f "$step" "$out"' EXIT
python3 -I - "$cd" > "$step" <<'PY'
import re, sys
text = open(sys.argv[1]).read()
block = text.split("id: plan\n", 1)[1].split("run: |\n", 1)[1]
lines = []
for line in block.splitlines():
    if line.strip() and not line.startswith("          "):
        break
    lines.append(line[10:])
print("\n".join(lines))
PY

# gh is stubbed: STUB_TIP is the branch tip the API would report; "fail" is an API error.
shim=$(mktemp -d); trap 'rm -f "$step" "$out"; rm -rf "$shim"' EXIT
cat > "$shim/gh" <<'GH'
#!/bin/sh
[ "$STUB_TIP" = fail ] && exit 1
echo "$STUB_TIP"
GH
chmod +x "$shim/gh"
SHA=0123456789abcdef0123456789abcdef01234567
OLD=89abcdef0123456789abcdef0123456789abcdef
plan() { : > "$out"; PATH="$shim:$PATH" REPO=o/r GH_TOKEN=x STUB_TIP=${3:-$SHA} BRANCH=$1 SHA=$2 GITHUB_OUTPUT=$out bash "$step" >/dev/null 2>&1; }

expect() {
  local want=$1 branch=$2 tip=${3:-$SHA}
  if plan "$branch" "$SHA" "$tip"; then got=$(tr '\n' ' ' < "$out"); else got=refused; fi
  if [ "$got" != "$want" ]; then echo "FAIL $branch: want '$want' got '$got'"; fail=1; fi
}

expect "sha=$SHA prefix=main short=0123456 floating=true " main
expect "sha=$SHA prefix=release-1.7 short=0123456 floating=true " release/1.7
expect "sha=$SHA prefix=release-12.30 short=0123456 floating=true " release/12.30
for branch in develop latest release/1 release/01.07 release/1.07 release/1.7.1 release/1.7/x release/../main 'release/1.7;id' feature/x v1.7.0 ""; do
  expect refused "$branch"
done
# an older run finishing late does not move the floating tag, but keeps its own sha tag
expect "sha=$SHA prefix=release-1.7 short=0123456 floating=false " release/1.7 "$OLD"
expect "sha=$SHA prefix=main short=0123456 floating=false " main "$OLD"
expect refused release/1.7 fail
if plan main notasha; then echo "FAIL a non-commit sha was accepted"; fail=1; fi
tag_blocks=$(awk '/^ *tags: \|/ {on=1; next} on && /^ *[a-z-]+:/ {on=0} on {print}' "$cd")
[ -n "$tag_blocks" ] || { echo "FAIL found no tags: blocks in cd.yml"; fail=1; }
printf '%s' "$tag_blocks" | grep -Eq 'latest|[0-9]+\.[0-9]+\.[0-9]+' && { echo "FAIL cd.yml publishes latest or a version"; fail=1; }
[ "$(grep -c 'run: bash scripts/guard-release-tag.sh' "$cd")" -eq 3 ] || { echo "FAIL every image job must run the release tag guard"; fail=1; }
[ "$fail" -eq 0 ] && echo "cd plan tests passed"
exit "$fail"
