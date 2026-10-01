#!/usr/bin/env bash
# Merges go coverprofiles of the integration shards into OUT: one mode line,
# and each block's counts summed across the shards that ran it.
set -euo pipefail
[[ $# -ge 2 ]] || { echo "usage: $0 OUT PROFILE..." >&2; exit 2; }
out=$1
shift
awk '
  /^mode:/ { if (mode == "") mode = $0; next }
  NF == 3 {
    key = $1 " " $2
    if (!(key in count)) order[++n] = key
    count[key] += $3
  }
  END {
    print mode
    for (i = 1; i <= n; i++) print order[i], count[order[i]]
  }' "$@" > "$out"
