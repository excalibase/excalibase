FROM golang:1.25-alpine AS builder

WORKDIR /build

# Copy the entire monorepo for the replace directive
COPY server-go/ server-go/
COPY excalibase-vault/ excalibase-vault/

WORKDIR /build/excalibase-vault

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /vault ./cmd/server/

FROM alpine:3.21

RUN apk --no-cache add ca-certificates
COPY --from=builder /vault /vault

EXPOSE 24010

ENTRYPOINT ["/vault"]
