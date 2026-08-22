FROM golang:1.25-alpine AS build

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /bin/excalibase-provisioning ./cmd/server/

FROM alpine:3.20

RUN apk add --no-cache ca-certificates sqlite-libs
COPY --from=build /bin/excalibase-provisioning /bin/excalibase-provisioning

# Bundle watcher Helm chart so provisioning can auto-deploy per-tenant CDC watchers.
# Path matches default WATCHER_CHART_PATH env.
COPY charts/excalibase-watcher-go /charts/excalibase-watcher-go

RUN adduser -D -u 1000 appuser
# Writable state dir for selfhosted mode's bbolt vault (STORAGE_PATH). A fresh
# named volume mounted here inherits this ownership, so the non-root user can
# write vault.bolt. Cloud mode uses the Postgres vault and never touches this.
RUN mkdir -p /var/lib/excalibase && chown appuser:appuser /var/lib/excalibase
USER appuser
ENV WATCHER_CHART_PATH=/charts/excalibase-watcher-go
ENV STORAGE_PATH=/var/lib/excalibase

EXPOSE 24005

ENTRYPOINT ["/bin/excalibase-provisioning"]
