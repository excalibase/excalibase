#!/usr/bin/env bash
# Checks the tag plan the release workflow uses for the one-database engine images (EXC-799).
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
plan="$here/../scripts/release-slim-tags.sh"
fail=0
expect() {
  want="${!#}"
  set -- "${@:1:$(($# - 1))}"
  actual="$("$plan" "$@" 2>&1 || true)"
  if [ "$actual" != "$want" ]; then
    echo "FAIL $*: got '$actual', want '$want'"
    fail=1
  fi
}
expect 1.7.0 postgres "1.7.0-postgres 1.7-postgres 1-postgres postgres"
expect 1.7.0 mysql "1.7.0-mysql 1.7-mysql 1-mysql mysql"
expect 10.20.30 postgres-native "10.20.30-postgres-native 10.20-postgres-native 10-postgres-native postgres-native"
expect 1.7.0 mysql-native false "1.7.0-mysql-native 1.7-mysql-native"
expect 1.7.0-rc.1 postgres "1.7.0-rc.1-postgres"
expect 1.7.0 sqlite "unknown slim image suffix: sqlite"

# No slim tag may equal a tag the all-in-one or native plan already uses.
for suffix in postgres mysql postgres-native mysql-native; do
  for tag in $("$plan" 1.7.0 "$suffix"); do
    for taken in $("$here/../scripts/release-tags.sh" 1.7.0 true) $("$here/../scripts/release-native-tags.sh" 1.7.0 true); do
      [ "$tag" != "$taken" ] || { echo "FAIL $tag collides with $taken"; fail=1; }
    done
  done
done

[ "$fail" -eq 0 ] && echo "release-slim-tags: ok"
exit "$fail"
