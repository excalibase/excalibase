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

plan() { : > "$out"; BRANCH=$1 SHA=$2 GITHUB_OUTPUT=$out bash "$step" >/dev/null 2>&1; }
SHA=0123456789abcdef0123456789abcdef01234567

expect() {
  local want=$1 branch=$2
  if plan "$branch" "$SHA"; then got=$(tr '\n' ' ' < "$out"); else got=refused; fi
  if [ "$got" != "$want" ]; then echo "FAIL $branch: want '$want' got '$got'"; fail=1; fi
}

expect "sha=$SHA prefix=main short=0123456 " main
expect "sha=$SHA prefix=release-1.7 short=0123456 " release/1.7
expect "sha=$SHA prefix=release-12.30 short=0123456 " release/12.30
for branch in develop latest release/1 release/1.7.1 release/1.7/x release/../main 'release/1.7;id' feature/x v1.7.0 ""; do
  expect refused "$branch"
done
if plan main notasha; then echo "FAIL a non-commit sha was accepted"; fail=1; fi
grep -q 'latest' <(grep -v '^ *#' "$cd" | grep -E 'tags:|prefix') && { echo "FAIL cd.yml publishes latest"; fail=1; }
[ "$fail" -eq 0 ] && echo "cd plan tests passed"
exit "$fail"
