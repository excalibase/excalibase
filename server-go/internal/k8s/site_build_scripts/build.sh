#!/bin/bash
# build: install then build, in the site's root directory. Exit codes name the
# step for the controller: 10 install, 11 build, 12 root directory missing, 13 the
# framework preset refused the app (nextjs_preset below).
set -uo pipefail

# Tested together (spike 2026-10-29: next 16.4.1 with OpenNext 1.20.11 builds green and then fails every dynamic route).
OPENNEXT_VERSION=1.20.12
WRANGLER_VERSION=4.150.0

refuse() {
  echo "this app cannot run as a server-rendered site: $1"
  echo "deploy it as an app container instead, or build it in your own CI and upload the output"
  exit 13
}

# The project's own copy when it has one, else the pinned version.
tool() {
  local binary=$1 package=$2 version=$3
  shift 3
  if [[ -x "node_modules/.bin/$binary" ]]; then
    "node_modules/.bin/$binary" "$@"
  else
    npm exec --yes --package="$package@$version" -- "$binary" "$@"
  fi
}

# nextjs_preset builds a Next.js app for a server-rendered site (EXC-661, ADR 0045). OpenNext turns the
# Next build into a Worker; wrangler bundles it without uploading anything (dry run) into $work/bundle,
# which collect.sh packs with the assets.
nextjs_preset() {
  export SKIP_WRANGLER_CONFIG_CHECK=yes WRANGLER_SEND_METRICS=false
  if [[ -f open-next.config.ts ]]; then
    if grep -Eiq '(r2|kv|d1|do)-[a-z-]*(cache|queue)|durable' open-next.config.ts; then
      refuse "open-next.config.ts names an R2, KV, D1 or Durable Object cache; sites use the static-assets cache"
    fi
  else
    cat > open-next.config.ts <<'CONFIG'
import { defineCloudflareConfig } from "@opennextjs/cloudflare";
import staticAssetsIncrementalCache from "@opennextjs/cloudflare/overrides/incremental-cache/static-assets-incremental-cache";

export default defineCloudflareConfig({
  incrementalCache: staticAssetsIncrementalCache,
  enableCacheInterception: true,
});
CONFIG
  fi
  if [[ ! -f wrangler.jsonc && ! -f wrangler.json && ! -f wrangler.toml ]]; then
    cat > wrangler.jsonc <<'CONFIG'
{
  "name": "site",
  "main": ".open-next/worker.js",
  "compatibility_date": "2025-09-01",
  "compatibility_flags": ["nodejs_compat"],
  "assets": { "directory": ".open-next/assets", "binding": "ASSETS" }
}
CONFIG
  fi
  local opennext_args=(build)
  if [[ -n "${BUILD_COMMAND:-}" ]]; then
    echo "\$ $BUILD_COMMAND"
    bash -c "$BUILD_COMMAND" || exit 11
    opennext_args+=(--skipNextBuild)
  fi
  local log
  log=$(mktemp "$work/opennext-log.XXXXXX")
  echo "\$ opennextjs-cloudflare ${opennext_args[*]}"
  tool opennextjs-cloudflare @opennextjs/cloudflare "$OPENNEXT_VERSION" "${opennext_args[@]}" 2>&1 | tee "$log"
  [[ ${PIPESTATUS[0]} -eq 0 ]] || exit 11
  # Experimental upstream and not run by us in v1 (ADR 0045 decision 10).
  if grep -q 'Node.js middleware' "$log"; then
    refuse "it uses Node.js middleware (proxy.ts, or middleware.ts with the nodejs runtime); use the edge runtime middleware"
  fi
  echo "\$ opennextjs-cloudflare populateCache local"
  tool opennextjs-cloudflare @opennextjs/cloudflare "$OPENNEXT_VERSION" populateCache local || exit 11
  # The OpenNext worker imports its siblings; only wrangler's bundle is one uploadable module.
  rm -rf "$work/bundle"
  echo "\$ wrangler deploy --dry-run --minify --outdir $work/bundle"
  tool wrangler wrangler "$WRANGLER_VERSION" deploy --dry-run --minify --outdir "$work/bundle" || exit 11
}

work=${WORK_DIR:-/work}
mkdir -p -m 700 "$HOME"
cd "$work/src/${ROOT_DIR}" 2>/dev/null || { echo "the root directory '${ROOT_DIR}' is not in the repository"; exit 12; }
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
if [[ "${FRAMEWORK:-}" == "nextjs" ]]; then
  nextjs_preset
  exit $?
fi
build=${BUILD_COMMAND:-npm run build}
echo "\$ $build"
bash -c "$build" || exit 11
