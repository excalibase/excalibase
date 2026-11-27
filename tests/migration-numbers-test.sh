#!/usr/bin/env bash
# A migration added by a change must be numbered above the highest on the base
# branch, and no number may be used twice. golang-migrate only moves forward, so
# a lower number merged after a higher one never runs.
# Usage: BASE_REF=origin/main bash tests/migration-numbers-test.sh
set -euo pipefail

dir=server-go/internal/storage/postgres/migrations
base=${BASE_REF:-origin/main}

numbers() { sed -E 's#.*/([0-9]+)_.*#\1#' | sort -u; }

dupes=$(ls "$dir"/*.up.sql | sed -E 's#.*/([0-9]+)_.*#\1#' | sort | uniq -d)
if [ -n "$dupes" ]; then
  echo "migration number used by more than one file: $dupes" >&2
  exit 1
fi

highest=$(git ls-tree --name-only "$base" "$dir/" | grep '\.up\.sql$' | numbers | tail -1)
added=$(git diff --name-only --diff-filter=A "$base"...HEAD -- "$dir" | grep '\.up\.sql$' | numbers || true)
for number in $added; do
  if [ "$((10#$number))" -le "$((10#$highest))" ]; then
    echo "migration $number must be numbered above $highest, the highest on $base: renumber it" >&2
    exit 1
  fi
done
echo "migration numbers ok (highest on $base: $highest)"
