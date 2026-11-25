#!/usr/bin/env bash
# Prints the tags a one-database engine image carries for a release version (EXC-799).
# The suffix names the variant: postgres, mysql, postgres-native or mysql-native. A pre-release gets
# its own tag only; a release also moves the floating tags (X-<suffix> and <suffix> too unless
# move_major_latest is false), as release-native-tags.sh does for the native image.
# Usage: release-slim-tags.sh <version> <suffix> [move_major_latest]
set -eu
version="${1:?version required}"
suffix="${2:?suffix required}"
move="${3:-true}"
if ! printf '%s' "$suffix" | grep -Eq '^(postgres|mysql)(-native)?$'; then
  echo "unknown slim image suffix: $suffix" >&2
  exit 1
fi
major="${version%%.*}"
rest="${version#*.}"
minor="${rest%%.*}"
if printf '%s' "$version" | grep -q -- '-'; then
  echo "$version-$suffix"
elif [ "$move" = false ]; then
  echo "$version-$suffix $major.$minor-$suffix"
else
  echo "$version-$suffix $major.$minor-$suffix $major-$suffix $suffix"
fi
