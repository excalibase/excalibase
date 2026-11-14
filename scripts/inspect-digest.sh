#!/usr/bin/env bash
# Prints the digest of an image reference.
# Exit 0: found. Exit 3: the registry says it does not exist. Exit 1: any other failure
# (auth, rate limit, outage), so a real error never looks like a missing tag.
# INSPECT_CMD overrides the registry call for tests.
set -u
ref="${1:?image reference required}"
inspect="${INSPECT_CMD:-docker buildx imagetools inspect}"
errors="$(mktemp)"
trap 'rm -f "$errors"' EXIT
if output="$($inspect "$ref" 2>"$errors")"; then
  digest="$(printf '%s\n' "$output" | awk '/^Digest:/ { print $2; exit }')"
  if [ -z "$digest" ] || [ "$digest" = "null" ]; then
    echo "no digest in the inspect output for $ref" >&2
    exit 1
  fi
  echo "$digest"
  exit 0
fi
if grep -qiE 'not found|manifest unknown|no such manifest|404' "$errors"; then
  exit 3
fi
cat "$errors" >&2
exit 1
