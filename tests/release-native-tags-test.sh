#!/usr/bin/env bash
# Checks the tag plan the release workflow uses for the native engine image.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
plan="$here/../scripts/release-native-tags.sh"
fail=0
expect() {
  actual="$("$plan" "$1")"
  if [ "$actual" != "$2" ]; then
    echo "FAIL $1: got '$actual', want '$2'"
    fail=1
  fi
}
expect 1.7.0 "1.7.0-native 1.7-native 1-native native"
expect 10.20.30 "10.20.30-native 10.20-native 10-native native"
expect 1.7.0-rc.1 "1.7.0-rc.1-native"
[ "$fail" -eq 0 ] && echo "release-native-tags: ok"
exit "$fail"
