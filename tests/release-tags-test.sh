#!/usr/bin/env bash
# scripts/release-tags.sh and release-native-tags.sh: which tags a version carries.
# move_major_latest=false (an older minor patched after a newer one shipped)
# stops 1 and latest, and 1-native and native, from moving backwards.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
fail=0
expect() {
  got=$("$here/../scripts/$1" "${@:2}" 2>/dev/null) || got=refused
  [ "$got" = "$WANT" ] || { echo "FAIL $*: want '$WANT' got '$got'"; fail=1; }
}
WANT="1.7.1 1.7 1 latest" expect release-tags.sh 1.7.1 true
WANT="1.7.1 1.7 1 latest" expect release-tags.sh 1.7.1
WANT="1.7.1 1.7" expect release-tags.sh 1.7.1 false
WANT="1.8.0-rc.1" expect release-tags.sh 1.8.0-rc.1 true
WANT="1.8.0-rc.1" expect release-tags.sh 1.8.0-rc.1 false
WANT=refused expect release-tags.sh 1.7.1 maybe
WANT=refused expect release-tags.sh v1.7.1 true
WANT="1.7.1-native 1.7-native 1-native native" expect release-native-tags.sh 1.7.1 true
WANT="1.7.1-native 1.7-native" expect release-native-tags.sh 1.7.1 false
WANT="1.7.1-native 1.7-native 1-native native" expect release-native-tags.sh 1.7.1
[ "$fail" -eq 0 ] && echo "release tag tests passed"
exit "$fail"
