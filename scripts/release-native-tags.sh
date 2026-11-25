#!/usr/bin/env bash
# Prints the tags the native engine image carries for a release version.
# A pre-release gets its own tag only; a release also moves the floating tags
# (X-native and native too unless move_major_latest is false).
# Usage: release-native-tags.sh <version> [move_major_latest]
set -eu
version="${1:?version required}"
move="${2:-true}"
major="${version%%.*}"
rest="${version#*.}"
minor="${rest%%.*}"
if printf '%s' "$version" | grep -q -- '-'; then
  echo "$version-native"
elif [ "$move" = false ]; then
  echo "$version-native $major.$minor-native"
else
  echo "$version-native $major.$minor-native $major-native native"
fi
