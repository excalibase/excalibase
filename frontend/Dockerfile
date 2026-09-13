# Studio frontend — Vite build served by nginx, with /api proxied to the
# provisioning backend (see nginx.conf). Part of the all-in-one stack, published
# as excalibase/studio and referenced by the platform-aio Helm chart.
FROM node:20-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
# Relative /api so the built SPA calls the same origin; nginx proxies it on.
ARG VITE_API_URL=/api
ENV VITE_API_URL=$VITE_API_URL
RUN npm run build

FROM nginx:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
# The /api resolver is injected at container start from /etc/resolv.conf so the
# proxy works under both Docker's embedded DNS and Kubernetes cluster DNS.
COPY docker-entrypoint.d/10-resolver.sh /docker-entrypoint.d/10-resolver.sh
RUN chmod +x /docker-entrypoint.d/10-resolver.sh
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
# busybox wget (present in nginx:alpine); curl is not installed in this base.
HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD wget -qO- http://localhost/ >/dev/null 2>&1 || exit 1
