#!/bin/bash
# build: install then build, in the site's root directory. Exit codes name the
# step for the controller: 10 install, 11 build, 12 root directory missing, 13 the
# framework preset refused the app (nextjs_preset below).
set -uo pipefail

WRANGLER_VERSION=4.150.0
# The Worker is bundled and published with this one date and these flags, never the repository's.
COMPAT_DATE=2025-09-01
COMPAT_FLAGS='"nodejs_compat"'

refuse() {
  echo "this app cannot run as a server-rendered site: $1"
  echo "deploy it as an app container instead, or build it in your own CI and upload the output"
  exit 13
}

package_manager() {
  if [[ -f pnpm-lock.yaml ]]; then echo pnpm
  elif [[ -f yarn.lock ]]; then echo yarn
  else echo npm
  fi
}

# opennext_for prints the OpenNext version to add for the project's Next.js, or nothing when none fits. The pairs are
# the ones each release declares as its peer range; next 16.4.1 with OpenNext 1.20.11 builds green and then fails
# every dynamic route (spike 2026-10-29), so 16.4.1 and later take 1.20.12.
opennext_for() {
  node -e '
    const [major, minor, patch] = require("next/package.json").version.split("-")[0].split(".").map(Number);
    const at = (a, b, c) => major > a || (major === a && (minor > b || (minor === b && patch >= c)));
    if (at(16, 4, 1) || (major === 15 && at(15, 5, 28))) console.log("1.20.12");
    else if (at(16, 3, 8) || (major === 15 && at(15, 5, 27))) console.log("1.20.11");
  ' 2>/dev/null
}

# The tools must resolve from the project: the generated config imports @opennextjs/cloudflare and
# esbuild looks for it there. A repository that does not declare them gets the pinned versions,
# added to the throwaway checkout (its package.json is not kept).
ensure_tools() {
  [[ -x node_modules/.bin/opennextjs-cloudflare && -x node_modules/.bin/wrangler ]] && return 0
  local opennext
  opennext=$(opennext_for)
  if [[ -z "$opennext" ]]; then
    refuse "its Next.js version is not one OpenNext supports (15.5.27 or later in 15.x, or 16.3.8 or later); upgrade Next.js"
  fi
  local packages=("@opennextjs/cloudflare@$opennext" "wrangler@$WRANGLER_VERSION")
  echo "\$ add ${packages[*]} (the repository does not declare them)"
  case "$(package_manager)" in
    pnpm) corepack pnpm add -D --ignore-scripts "${packages[@]}" ;;
    yarn) corepack yarn add -D "${packages[@]}" ;;
    *) npm install -D --no-audit --no-fund "${packages[@]}" ;;
  esac
}

# check_with runs the named check of the resolved configuration; it answers 13 with the reason when
# the app does not fit, and prints what it ignores.
check_with() {
  local check=.excalibase-check.mjs
  cat > "$check" <<'CHECK'
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const refuse = (reason) => {
  console.log(`this app cannot run as a server-rendered site: ${reason}`);
  console.log("deploy it as an app container instead, or build it in your own CI and upload the output");
  process.exit(13);
};
const filled = (value) => (Array.isArray(value) ? value.length > 0 : value && typeof value === "object" ? Object.keys(value).length > 0 : Boolean(value));

async function wranglerFile() {
  const file = ["wrangler.jsonc", "wrangler.json", "wrangler.toml"].find((name) => existsSync(name));
  if (!file) return;
  const { unstable_readConfig } = await import("wrangler");
  const config = unstable_readConfig({ config: file }, { hideWarnings: true });
  const bindings = {
    r2_buckets: "an R2 bucket", kv_namespaces: "a KV namespace", d1_databases: "a D1 database", services: "a service binding",
    durable_objects: "a Durable Object", queues: "a queue", ai: "the AI binding", vectorize: "a Vectorize index", hyperdrive: "Hyperdrive",
    analytics_engine_datasets: "Analytics Engine", send_email: "email sending", browser: "the browser binding",
    mtls_certificates: "an mTLS certificate", dispatch_namespaces: "a dispatch namespace", unsafe: "an unsafe binding",
  };
  for (const [key, label] of Object.entries(bindings)) {
    const value = key === "durable_objects" ? config.durable_objects?.bindings : key === "queues" ? [...(config.queues?.producers ?? []), ...(config.queues?.consumers ?? [])] : key === "unsafe" ? config.unsafe?.bindings : config[key];
    if (filled(value)) refuse(`${file} binds ${label}; a site gets only its assets, and bindings are never sent`);
  }
  const ignored = ["vars", "routes", "route", "account_id", "triggers"].filter((key) => filled(config[key]));
  if (ignored.length > 0) console.log(`note: ${file} sets ${ignored.join(", ")}; they are ignored. The site is routed by Excalibase and runtime variables are not supported yet`);
}

function middleware() {
  const manifest = ".next/server/functions-config-manifest.json";
  if (!existsSync(manifest)) refuse("the Next.js build wrote no functions manifest");
  const functions = JSON.parse(readFileSync(manifest, "utf8")).functions ?? {};
  if (functions["/_middleware"]?.runtime === "nodejs") {
    refuse("it uses Node.js middleware (proxy.ts, or middleware.ts with the nodejs runtime); use the edge runtime middleware");
  }
}

// What is in force after the repository's own config is bundled, so a re-export or a helper cannot hide a cache override.
const allowed = {
  incrementalCache: ["dummy", "cf-static-assets-incremental-cache"],
  tagCache: ["dummy"],
  queue: ["dummy", "memory-queue"],
  cdnInvalidation: ["dummy"],
};
async function overrides() {
  const module = await import(pathToFileURL(resolve(".open-next/server-functions/default/open-next.config.mjs")).href);
  const config = module.default;
  for (const section of [config.default, config.middleware]) {
    for (const [key, names] of Object.entries(allowed)) {
      const value = section?.override?.[key];
      if (value === undefined) continue;
      const resolved = typeof value === "function" ? await value() : value;
      const name = typeof resolved === "string" ? resolved : resolved?.name;
      if (!names.includes(name)) refuse(`its OpenNext config uses the ${key} override "${name}", which needs storage a site does not have; sites use the static-assets cache`);
    }
  }
}

const checks = { wrangler: wranglerFile, built: async () => { middleware(); await overrides(); } };
await checks[process.argv[2]]();
CHECK
  node "$check" "$1"
  local status=$?
  rm -f "$check"
  return $status
}

# nextjs_preset builds a Next.js app for a server-rendered site (EXC-661, ADR 0045). OpenNext turns the
# Next build into a Worker; wrangler bundles it without uploading anything (dry run) into $work/bundle,
# which collect.sh packs with the assets and $work/site.json.
nextjs_preset() {
  export SKIP_WRANGLER_CONFIG_CHECK=yes WRANGLER_SEND_METRICS=false
  ensure_tools || exit 11
  check_with wrangler || exit $?
  if [[ ! -f open-next.config.ts ]]; then
    cat > open-next.config.ts <<'CONFIG'
import { defineCloudflareConfig } from "@opennextjs/cloudflare";
import staticAssetsIncrementalCache from "@opennextjs/cloudflare/overrides/incremental-cache/static-assets-incremental-cache";

export default defineCloudflareConfig({
  incrementalCache: staticAssetsIncrementalCache,
  enableCacheInterception: true,
});
CONFIG
  fi
  local own=wrangler.excalibase.jsonc
  cat > "$own" <<CONFIG
{
  "name": "site",
  "main": ".open-next/worker.js",
  "compatibility_date": "$COMPAT_DATE",
  "compatibility_flags": [$COMPAT_FLAGS],
  "assets": { "directory": ".open-next/assets", "binding": "ASSETS" }
}
CONFIG
  # OpenNext's own steps want a wrangler file; the repository's is used there, ours when none.
  if [[ ! -f wrangler.jsonc && ! -f wrangler.json && ! -f wrangler.toml ]]; then
    cp "$own" wrangler.jsonc
  fi
  local opennext_args=(build)
  if [[ -n "${BUILD_COMMAND:-}" ]]; then
    echo "\$ $BUILD_COMMAND"
    bash -c "$BUILD_COMMAND" || exit 11
    opennext_args+=(--skipNextBuild)
  fi
  echo "\$ opennextjs-cloudflare ${opennext_args[*]}"
  node_modules/.bin/opennextjs-cloudflare "${opennext_args[@]}" || exit 11
  check_with built || exit $?
  echo "\$ opennextjs-cloudflare populateCache local"
  node_modules/.bin/opennextjs-cloudflare populateCache local || exit 11
  # The OpenNext worker imports its siblings; only wrangler's bundle is one uploadable module.
  rm -rf "$work/bundle"
  echo "\$ wrangler deploy --dry-run --minify --outdir $work/bundle"
  node_modules/.bin/wrangler deploy --dry-run --minify --outdir "$work/bundle" --config "$own" || exit 11
  printf '{"kind":"server","main":"worker.js","compatibilityDate":"%s","compatibilityFlags":[%s],"framework":"nextjs"}' \
    "$COMPAT_DATE" "$COMPAT_FLAGS" > "$work/site.json"
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
