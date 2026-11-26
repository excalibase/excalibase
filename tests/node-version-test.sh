#!/usr/bin/env bash
# EXC-864: vitest 5 and jsdom 31 need Node 22.12+/24, so every CI job that installs or tests the
# frontend or the edge Workers runs Node 24, and the Studio image builds with the same major.
# The deno-server job is not covered: it builds a library that has no such floor.
set -u
root="$(cd "$(dirname "$0")/.." && pwd)"
want=24
fail=0

# Each setup-node step in ci.yml, with the cache path that says what it installs.
while IFS='|' read -r line version cache; do
  case "$cache" in
    frontend/*|edge/*)
      [ "$version" = "'$want'" ] || { echo "FAIL ci.yml:$line setup-node for $cache uses $version, want '$want'"; fail=1; } ;;
  esac
done < <(awk '
  /uses: actions\/setup-node/ { inblock = 1; line = NR; version = ""; cache = ""; next }
  inblock && /node-version:/ { version = $2 }
  inblock && /cache-dependency-path:/ { cache = $2 }
  inblock && (/^      - / || /^  [a-z]/) { if (cache != "") print line "|" version "|" cache; inblock = 0 }
  END { if (inblock && cache != "") print line "|" version "|" cache }
' "$root/.github/workflows/ci.yml")

grep -q "^FROM node:${want}-alpine AS build" "$root/frontend/Dockerfile" ||
  { echo "FAIL frontend/Dockerfile build stage is not node:${want}-alpine"; fail=1; }

# the awk must actually have found the frontend jobs, or the loop above proves nothing
count=$(grep -c "cache-dependency-path: \(frontend\|edge\)/" "$root/.github/workflows/ci.yml")
[ "$count" -ge 6 ] || { echo "FAIL expected at least 6 frontend/edge setup-node steps, saw $count"; fail=1; }

[ "$fail" -eq 0 ] && echo "node version tests passed"
exit "$fail"
