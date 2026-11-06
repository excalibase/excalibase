#!/usr/bin/env bash
# Say which coverage the Sonar scan is missing instead of letting a number
# mislead: a missing Go report drops Go lines from the analysis, a missing
# frontend or Deno lcov scores those files 0.0%.
#   usage: check-coverage-files.sh <go-expected: true|false>
# Frontend and Deno coverage are expected whenever the scan runs (their jobs run
# on backend changes too). A missing or empty file is an error annotation and a
# step-summary line, but never fails the step: the scan runs on what exists.
set -uo pipefail
go_expected="${1:-false}"
missing=()

# usable <path> <kind>: a Go profile needs a line after its "mode:" header, an
# lcov file needs a source record.
usable() {
  case "$2" in
    go) [ -f "$1" ] && [ "$(wc -l <"$1")" -gt 1 ] ;;
    lcov) grep -q '^SF:' "$1" 2>/dev/null ;;
  esac
}

check() {
  local label="$1" path="$2" kind="$3" expected="$4" effect="$5"
  if usable "$path" "$kind"; then
    echo "$label coverage: $path"
    return
  fi
  if [ "$expected" = "true" ]; then
    echo "::error title=no coverage file::$label coverage file $path is missing or has no records; $effect. Look at the job that should have produced it."
    missing+=("$label")
  else
    echo "::notice title=no coverage file::$label coverage file $path is missing; its job did not run for this change, so $effect."
  fi
}

check "Go" server-go/coverage.out go "$go_expected" "Go lines are left out of this analysis"
check "Deno" deno-server/coverage/lcov.info lcov true "Deno files read 0.0% coverage"
check "Frontend" frontend/coverage/lcov.info lcov true "frontend files read 0.0% coverage"

if [ "${#missing[@]}" -gt 0 ] && [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  echo "Sonar coverage is partial: no coverage file for ${missing[*]}." >>"$GITHUB_STEP_SUMMARY"
fi
exit 0
