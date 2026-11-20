#!/usr/bin/env bash
# Prints the tags an image carries for a release version.
# Usage: release-tags.sh <version> [move_major_latest: true|false]
# A pre-release gets its own tag only. A release gets X.Y.Z and X.Y, and, unless
# move_major_latest is false, X and latest too. Patching an older minor after a
# newer one shipped passes false so it cannot move X or latest backwards.
set -eu
version="${1:?version required}"
move="${2:-true}"
printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || { echo "bad version $version" >&2; exit 1; }
case "$move" in true | false) ;; *) echo "move_major_latest must be true or false" >&2; exit 1 ;; esac
major="${version%%.*}"
rest="${version#*.}"
minor="${rest%%.*}"
if printf '%s' "$version" | grep -q -- '-'; then
  echo "$version"
elif [ "$move" = true ]; then
  echo "$version $major.$minor $major latest"
else
  echo "$version $major.$minor"
fi
