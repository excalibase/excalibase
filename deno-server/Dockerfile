# Vendor @excalibase/server into the Deno runtime image.
#
# The library (`@excalibase/server`) is never published to npm. Its source is
# vendored in this repo at `deno-server/lib/excalibase-server/` (it used to
# live in a sibling clone that CI could not fetch), and is built INSIDE this
# image so the resulting ESM dist lands where `deno.json`'s import map points
# (`./vendor/excalibase-server/index.mjs`). No host mount, no external
# publish step, reproducible from this directory alone.
#
# Build context: `deno-server/` (this directory). CD builds it as
#   docker build -f deno-server/Dockerfile deno-server
# and publishes `excalibase/deno-runtime` — the image the platform-aio chart
# and provisioning's DENO_RUNTIME_IMAGE reference.
#
# In-container layout:
#   /app/deno.json                              (Deno config + import map)
#   /app/vendor/excalibase-server/index.mjs     (vendored lib dist)
#   /app/server.ts                              (runtime entrypoint)
#   /app/runtime/*.ts                           (runtime modules)

# ─── Stage 1 — Build the vendored lib (Node) ────────────────────────────
#
# The lib uses tsup; the build emits dist/index.mjs which is what the
# import map references. We tag the stage `lib-build` so a downstream
# `COPY --from=lib-build` can grab just the dist without dragging
# node_modules into the final image.
FROM node:20-alpine AS lib-build

WORKDIR /lib

COPY lib/excalibase-server/ ./
RUN --mount=type=cache,target=/root/.npm \
    npm install --no-audit --no-fund && \
    npm run build && \
    rm -rf node_modules src test

# ─── Stage 2 — Final Deno runtime image ─────────────────────────────────
FROM denoland/deno:alpine-2.5.5

WORKDIR /app

# Pull latest Alpine security patches (openssl/libcrypto3/libssl3 ship CVE
# fixes on a faster cadence than the deno base) AND chown /app to the
# pre-existing non-root `deno` user from the base image — merged into a
# single RUN to keep the layer count down (Sonar docker:S7031).
RUN apk update --no-cache && \
    apk upgrade --no-cache --available openssl libcrypto3 libssl3 musl musl-utils ca-certificates ssl_client && \
    rm -rf /var/cache/apk/* && \
    mkdir -p /app/vendor/excalibase-server && \
    chown -R deno:deno /app

# Bring in the vendored library dist. The import map in deno.json points
# `npm:@excalibase/server@X.Y.Z` at this exact path. If a downstream consumer
# overrides via volume mount (compose), this COPY is still safe — the mount
# supersedes the layer at runtime.
COPY --from=lib-build --chown=deno:deno /lib/dist/index.mjs ./vendor/excalibase-server/index.mjs

# Deno config + import map. MUST live next to server.ts so `deno run`
# (cwd=/app) auto-discovers it without `--config`. The worker subprocess
# inherits the parent process's resolved import map.
COPY --chown=deno:deno deno.json .

COPY --chown=deno:deno server.ts .
COPY --chown=deno:deno runtime ./runtime

# Pre-cache the vendored lib's transitive npm deps (zod-to-json-schema, zod).
# Without this, the first deploy on a fresh image triggers a registry fetch
# from inside the worker, which can be slow on cold starts and (more
# importantly) fails when ALLOWED_HOSTS locks the worker out of the public
# internet.
#
# The cache runs as root (we're pre-USER) so /deno-dir/* ends up owned
# by root by default. Chown after so the `deno` user can still write
# new cache entries at runtime when a function imports an npm: spec we
# didn't pre-cache (e.g. a freshly-published @excalibase/server version).
RUN deno cache server.ts /app/vendor/excalibase-server/index.mjs && \
    chown -R deno:deno /deno-dir

USER deno

# Tell the runtime where the vendored lib lives so the worker permissions
# grant scoped `read` access to that directory only.
ENV EXCALIBASE_VENDORED_LIB_DIR=/app/vendor/excalibase-server

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD deno eval "const r = await fetch('http://localhost:8000/health'); if (!r.ok) Deno.exit(1);" || exit 1

# `--allow-read=/app/vendor` so the parent process can load the import-map
# target. Without this the worker's scoped `read` would fail at
# module-resolve time even though the worker permissions allow it (Deno
# enforces the parent's permissions as the upper bound).
CMD ["deno", "run", "--allow-net", "--allow-read=/app/vendor", "--allow-env=RUNTIME_SECRET,ALLOWED_HOSTS,PORT,EXCALIBASE_FUNCTIONS_V2,EXCALIBASE_DB_URL,EXCALIBASE_PROVISIONING_URL,EXCALIBASE_RUN_MAX_DEPTH,EXCALIBASE_MUTATION_ISOLATION,EXCALIBASE_MUTATION_RETRY_MAX,EXCALIBASE_MUTATION_RETRY_BACKOFF_MS,EXCALIBASE_QUERY_MAX_RESULTS,EXCALIBASE_VENDORED_LIB_DIR", "--unstable-worker-options", "server.ts"]
