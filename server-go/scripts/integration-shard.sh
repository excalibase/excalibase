#!/usr/bin/env bash
# Prints the go test -run pattern for shard SHARD (1-based) of TOTAL of the
# integration suite. Each package's tests are dealt round-robin by name, so
# the slow container-backed packages are split evenly across the CI jobs.
set -euo pipefail
usage() { echo "usage: $0 SHARD TOTAL [SERVER_GO_DIR]" >&2; exit 2; }
[[ $# -ge 2 ]] || usage
shard=$1 total=$2 root=${3:-$(cd "$(dirname "$0")/.." && pwd)}
[[ $shard =~ ^[0-9]+$ && $total =~ ^[0-9]+$ ]] || usage
((total >= 1 && shard >= 1 && shard <= total)) || usage

selected=$(
  find "$root/internal" "$root/pkg" -name '*_test.go' -printf '%h\n' | sort -u | while read -r dir; do
    grep -hoE '^func (Test|Example|Fuzz)[A-Za-z0-9_]*\(' "$dir"/*_test.go \
      | sed -E 's/^func //; s/\($//' | grep -vx TestMain | sort -u \
      | awk -v s="$shard" -v n="$total" 'NR % n == s % n'
  done | sort -u | paste -sd '|'
)
echo "^(${selected})\$"
