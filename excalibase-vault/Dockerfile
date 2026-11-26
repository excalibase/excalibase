# EXC-881: CI builds from the mirror of Docker Hub (--build-arg HUB_MIRROR=ghcr.io/excalibase/mirror/); empty pulls Docker Hub.
ARG HUB_MIRROR=
FROM ${HUB_MIRROR}golang:1.27.4-alpine AS builder

WORKDIR /build

# Copy the entire monorepo for the replace directive
COPY server-go/ server-go/
COPY excalibase-vault/ excalibase-vault/

WORKDIR /build/excalibase-vault

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /vault ./cmd/server/

FROM ${HUB_MIRROR}alpine:3.24

RUN apk --no-cache add ca-certificates
COPY --from=builder /vault /vault

EXPOSE 24010

ENTRYPOINT ["/vault"]
