FROM denoland/deno:alpine-2.1.4

WORKDIR /app

COPY server.ts .

RUN deno cache server.ts

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD deno eval "const r = await fetch('http://localhost:8000/health'); if (!r.ok) Deno.exit(1);" || exit 1

CMD ["deno", "run", "--allow-net", "--unstable-worker-options", "server.ts"]
