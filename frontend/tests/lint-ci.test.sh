#!/bin/sh
# CI's frontend-lint job runs oxlint as well as tsc, and the tree passes it (EXC-861).
#   sh frontend/tests/lint-ci.test.sh
set -u
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
WORKFLOW="$DIR/../../.github/workflows/ci.yml"
FAILURES=0

fail() { echo "   FAIL $1"; FAILURES=$((FAILURES + 1)); }
pass() { echo "   PASS $1"; }

job=$(awk '/^  frontend-lint:/{on=1; next} on && /^  [a-z]/{exit} on' "$WORKFLOW")
if printf '%s\n' "$job" | grep -q 'npx tsc -b --noEmit'; then pass "the job still type-checks"; else fail "the job lost its type check"; fi
if printf '%s\n' "$job" | grep -Eq 'run: (npm run lint|npx oxlint)'; then pass "the job runs oxlint"; else fail "the job does not run oxlint"; fi

if (cd "$DIR/.." && npx --no-install oxlint >/dev/null 2>&1); then pass "oxlint reports no errors"; else fail "oxlint reports errors"; fi

[ "$FAILURES" -eq 0 ] || { echo "$FAILURES failure(s)"; exit 1; }
echo "lint-ci ok"
