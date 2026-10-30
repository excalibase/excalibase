#!/bin/bash
# clone: the only step holding the git credential. The token goes to git through
# GIT_CONFIG_* for this process only, so it is never written to /work/src/.git.
set -euo pipefail
token=""
[[ -s /run/git/token ]] && token=$(cat /run/git/token)
if [[ -n "$token" ]]; then
  export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=http.extraHeader
  GIT_CONFIG_VALUE_0="Authorization: Basic $(printf 'x-access-token:%s' "$token" | base64 -w0)"
  export GIT_CONFIG_VALUE_0
fi
unset token
mkdir -p -m 700 "$HOME"
git init -q /work/src
cd /work/src
if ! git fetch -q --depth 1 -- "$CLONE_URL" "$COMMIT"; then
  echo "could not fetch commit $COMMIT"
  exit 1
fi
git checkout -q FETCH_HEAD
echo "checked out $COMMIT"
