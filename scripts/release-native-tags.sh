#!/usr/bin/env bash
# Prints the tags the native engine image carries for a release version.
# A pre-release gets its own tag only; a release also moves the floating tags.
# Usage: release-native-tags.sh <version>
set -eu
version="${1:?version required}"
major="${version%%.*}"
rest="${version#*.}"
minor="${rest%%.*}"
if printf '%s' "$version" | grep -q -- '-'; then
  echo "$version-native"
else
  echo "$version-native $major.$minor-native $major-native native"
fi
