#!/usr/bin/env bash
# Say "no coverage file" before the Sonar scan instead of letting the scan
# report a misleading 0.0% coverage when an upstream job produced nothing.
#   usage: check-coverage-files.sh <go-required: true|false>
# A missing Go file fails when the Go integration job was expected to run
# (go-required=true); other missing files only warn, since their jobs can be
# skipped on a PR that does not touch them.
set -uo pipefail
go_required="${1:-false}"
status=0
check() {
  local label="$1" path="$2" required="$3"
  if [ -s "$path" ]; then
    echo "$label coverage: $path ($(wc -l <"$path") lines)"
    return
  fi
  if [ "$required" = "true" ]; then
    echo "::error title=no coverage file::$label coverage file $path is missing or empty; the scan would report 0.0% coverage. Look at the job that should have produced it."
    status=1
  else
    echo "::warning title=no coverage file::$label coverage file $path is missing; its coverage is not in this scan."
  fi
}
check "Go" server-go/coverage.out "$go_required"
check "Deno" deno-server/coverage/lcov.info false
check "Frontend" frontend/coverage/lcov.info false
exit "$status"
