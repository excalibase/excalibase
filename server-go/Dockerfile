FROM golang:1.25-alpine AS build

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /bin/excalibase-provisioning ./cmd/server/
# The single-host engine proxy ships in this image so it needs no pipeline of its own.
RUN CGO_ENABLED=0 go build -o /bin/excalibase-engine-proxy ./cmd/engine-proxy/
# Copied into every app container on a single host as its readiness check: static, so any image runs it.
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/excalibase-app-probe ./cmd/app-probe/

FROM alpine:3.20

RUN apk add --no-cache ca-certificates sqlite-libs
COPY --from=build /bin/excalibase-provisioning /bin/excalibase-provisioning
COPY --from=build /bin/excalibase-engine-proxy /bin/excalibase-engine-proxy
COPY --from=build /bin/excalibase-app-probe /usr/local/bin/excalibase-app-probe

# Bundle watcher Helm chart so provisioning can auto-deploy per-tenant CDC watchers.
# Path matches default WATCHER_CHART_PATH env.
COPY charts/excalibase-watcher-go /charts/excalibase-watcher-go

RUN adduser -D -u 1000 appuser
# Writable state dir for the docker provisioner's persisted vault unseal key
# (STORAGE_PATH/unseal.key). A fresh named volume mounted here inherits this
# ownership, so the non-root user can write it. Vault data itself lives in the
# platform Postgres; cloud mode never touches this dir.
RUN mkdir -p /var/lib/excalibase && chown appuser:appuser /var/lib/excalibase
# The single host's app routes (EXC-575): written here, read by the edge from the same volume.
RUN mkdir -p /srv/excalibase-app-routes && chown appuser:appuser /srv/excalibase-app-routes
USER appuser
ENV WATCHER_CHART_PATH=/charts/excalibase-watcher-go
ENV STORAGE_PATH=/var/lib/excalibase

EXPOSE 24005

ENTRYPOINT ["/bin/excalibase-provisioning"]
