FROM denoland/deno:alpine-2.5.5

WORKDIR /app

# Pull latest Alpine security patches (openssl/libcrypto3/libssl3 ship CVE
# fixes on a faster cadence than the deno base) AND chown /app to the
# pre-existing non-root `deno` user from the base image — merged into a
# single RUN to keep the layer count down (Sonar docker:S7031).
RUN apk update --no-cache && \
    apk upgrade --no-cache --available openssl libcrypto3 libssl3 musl musl-utils ca-certificates ssl_client && \
    rm -rf /var/cache/apk/* && \
    chown -R deno:deno /app

COPY --chown=deno:deno server.ts .

RUN deno cache server.ts

USER deno

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD deno eval "const r = await fetch('http://localhost:8000/health'); if (!r.ok) Deno.exit(1);" || exit 1

CMD ["deno", "run", "--allow-net", "--allow-env=RUNTIME_SECRET,ALLOWED_HOSTS,PORT,EXCALIBASE_FUNCTIONS_V2,EXCALIBASE_DB_URL,EXCALIBASE_PROVISIONING_URL,EXCALIBASE_RUN_MAX_DEPTH,EXCALIBASE_MUTATION_ISOLATION,EXCALIBASE_MUTATION_RETRY_MAX,EXCALIBASE_MUTATION_RETRY_BACKOFF_MS,EXCALIBASE_QUERY_MAX_RESULTS", "--unstable-worker-options", "server.ts"]
