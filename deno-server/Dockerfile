FROM denoland/deno:alpine-2.1.4

WORKDIR /app

# Run as non-root user
RUN addgroup -S deno && adduser -S deno -G deno
RUN chown -R deno:deno /app

COPY --chown=deno:deno server.ts .

RUN deno cache server.ts

USER deno

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD deno eval "const r = await fetch('http://localhost:8000/health'); if (!r.ok) Deno.exit(1);" || exit 1

CMD ["deno", "run", "--allow-net", "--allow-env=RUNTIME_SECRET,ALLOWED_HOSTS", "--unstable-worker-options", "server.ts"]
