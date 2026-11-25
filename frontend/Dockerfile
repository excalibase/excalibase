# Studio frontend — Vite build served by nginx, with /api proxied to the
# provisioning backend (see nginx.conf). Part of the all-in-one stack, published
# as excalibase/studio and referenced by the platform-aio Helm chart.
# EXC-881: CI builds from the mirror of Docker Hub (--build-arg HUB_MIRROR=ghcr.io/excalibase/mirror/); empty pulls Docker Hub.
ARG HUB_MIRROR=
FROM ${HUB_MIRROR}node:20-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
# Relative /api so the built SPA calls the same origin; nginx proxies it on.
ARG VITE_API_URL=/api
ENV VITE_API_URL=$VITE_API_URL
RUN npm run build

FROM ${HUB_MIRROR}nginx:1.29-alpine
# Security fixes from Alpine published since the base image was cut (EXC-642).
RUN apk upgrade --no-cache
COPY nginx.conf /etc/nginx/conf.d/default.conf
# The /api resolver is injected at container start from /etc/resolv.conf so the
# proxy works under both Docker's embedded DNS and Kubernetes cluster DNS.
COPY docker-entrypoint.d/10-resolver.sh /docker-entrypoint.d/10-resolver.sh
# connect-src names the storage origin Studio uploads files to (STORAGE_PUBLIC_ORIGIN, EXC-809).
COPY docker-entrypoint.d/20-storage-origin.sh /docker-entrypoint.d/20-storage-origin.sh
RUN chmod +x /docker-entrypoint.d/10-resolver.sh /docker-entrypoint.d/20-storage-origin.sh
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
# busybox wget (present in nginx:alpine); curl is not installed in this base.
# 127.0.0.1, not localhost: localhost resolves to ::1 and nginx listens on IPv4.
HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD wget -qO- http://127.0.0.1/ >/dev/null 2>&1 || exit 1
