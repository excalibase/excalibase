#!/usr/bin/env bash
# guard-release-tag.sh <repository> <tag>
# CD calls this before pushing. A release-<major>.<minor>-<sha7> tag is what the
# AIO gate tested and release.yml later promotes, so it is written once: exit 1
# if it already exists, or if the registry cannot say. main-<sha7> is not guarded.
set -u
repository="${1:?repository required}"
tag="${2:?tag required}"
here="$(cd "$(dirname "$0")" && pwd)"
case "$tag" in
  main-[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) exit 0 ;;
esac
printf '%s' "$repository" | grep -Eq '^excalibase/[a-z0-9-]+$' || { echo "::error::refusing repository $repository" >&2; exit 1; }
printf '%s' "$tag" | grep -Eq '^release-(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-[0-9a-f]{7}$' || { echo "::error::refusing tag $tag" >&2; exit 1; }
status=0
bash "$here/inspect-digest.sh" "$repository:$tag" >/dev/null || status=$?
case "$status" in
  3) exit 0 ;;
  0) echo "::error::$repository:$tag already exists and is immutable; a re-run must not replace a tested image" >&2; exit 1 ;;
  *) echo "::error::could not check $repository:$tag" >&2; exit 1 ;;
esac
