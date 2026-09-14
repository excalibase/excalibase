#!/usr/bin/env bash
# Runs each test file in its own `deno test` invocation. This avoids
# cross-file subprocess + testcontainer contention that causes ECONNRESET
# flakes when the whole suite runs in one `deno test test/` call.
#
# A failed file is retried up to RETRIES times (default 2). The exit code
# is non-zero only when a file fails on every attempt.
#
# Usage:
#   ./scripts/run-tests.sh                 # default
#   RETRIES=3 ./scripts/run-tests.sh       # more tries per file
#   PATTERN=db_unit ./scripts/run-tests.sh # only files matching the pattern

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DENO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$DENO_DIR"

RETRIES="${RETRIES:-2}"
PATTERN="${PATTERN:-}"

PASS=0
FAIL=0
FAILED_FILES=()
START=$(date +%s)

run_file() {
  local f="$1"
  for attempt in $(seq 1 "$RETRIES"); do
    # DENO_TEST_FLAGS lets CI thread extra flags (e.g. --coverage=cov) into
    # every per-file run without changing the default local invocation.
    # shellcheck disable=SC2086
    if deno test --allow-all --no-check --quiet ${DENO_TEST_FLAGS:-} "$f" > /tmp/dt-out.log 2>&1; then
      if [ "$attempt" -gt 1 ]; then
        printf "  ✓ %s (passed on attempt %d)\n" "${f#test/}" "$attempt"
      else
        printf "  ✓ %s\n" "${f#test/}"
      fi
      return 0
    fi
    printf "  ⟳ %s (attempt %d/%d failed, retrying)\n" "${f#test/}" "$attempt" "$RETRIES" >&2
  done
  printf "  ✗ %s (failed %d/%d attempts)\n" "${f#test/}" "$RETRIES" "$RETRIES"
  tail -20 /tmp/dt-out.log | sed 's/^/      /' >&2
  return 1
}

for f in test/*.test.ts; do
  if [ -n "$PATTERN" ] && ! echo "$f" | grep -q "$PATTERN"; then
    continue
  fi
  if run_file "$f"; then
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
    FAILED_FILES+=("$f")
  fi
done

END=$(date +%s)
echo
echo "=== summary ==="
echo "files: $PASS passed, $FAIL failed (elapsed $((END - START))s, RETRIES=$RETRIES)"
if [ "$FAIL" -gt 0 ]; then
  echo "still failing after $RETRIES attempts:"
  for f in "${FAILED_FILES[@]}"; do echo "  - ${f#test/}"; done
fi
exit "$FAIL"
