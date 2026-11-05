# syntax=docker/dockerfile:1
FROM node:22-alpine AS deps
ARG GREETING=unset
ARG PROBE=0
ARG HOG=0
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund
# A build-visible secret reaches RUN as a file only; its hash is checked so the value never reaches the log.
RUN --mount=type=secret,id=NPM_TOKEN test "$(sha256sum < /run/secrets/NPM_TOKEN | cut -d' ' -f1)" = "b3e4d08d0be1b5ab8c2c504221ac482d602626718ab43bcf2c866333af962a0b" && echo "secret mount ok"
RUN echo "$GREETING" > /app/greeting
# The fence: private and metadata addresses stay shut while the build runs.
RUN if [ "$PROBE" = 1 ]; then node -e "for (const [h,p] of [['169.254.169.254',80],['10.43.0.1',443]]) { const s=require('net').connect(p,h); s.on('connect',()=>{console.log('reached '+h); process.exit(1)}); s.on('error',()=>{}); } setTimeout(()=>{console.log('fence held'); process.exit(0)},4000)"; fi
RUN if [ "$HOG" = 1 ]; then node -e "const held=[]; for(;;) held.push(Buffer.alloc(16<<20, 1))"; fi
FROM node:22-alpine
WORKDIR /app
COPY --from=deps /app ./
COPY server.js ./
USER node
EXPOSE 8080
CMD ["node", "server.js"]
