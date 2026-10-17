#!/bin/bash
# build: install then build, in the site's root directory. Exit codes name the
# step for the controller: 10 install, 11 build, 12 root directory missing.
set -uo pipefail
mkdir -p -m 700 "$HOME"
cd "/work/src/${ROOT_DIR}" 2>/dev/null || { echo "the root directory '${ROOT_DIR}' is not in the repository"; exit 12; }
install=${INSTALL_COMMAND:-}
if [[ -z "$install" ]]; then
  if [[ -f pnpm-lock.yaml ]]; then install="corepack pnpm install --frozen-lockfile"
  elif [[ -f yarn.lock ]] && [[ -f .yarnrc.yml ]]; then install="corepack yarn install --immutable"
  elif [[ -f yarn.lock ]]; then install="corepack yarn install --frozen-lockfile"
  elif [[ -f package-lock.json ]] || [[ -f npm-shrinkwrap.json ]]; then install="npm ci --no-audit --no-fund"
  else install="npm install --no-audit --no-fund"
  fi
fi
echo "\$ $install"
bash -c "$install" || exit 10
build=${BUILD_COMMAND:-npm run build}
echo "\$ $build"
bash -c "$build" || exit 11
