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

RUN adduser -D -u 1000 appuser
USER appuser

EXPOSE 24005

ENTRYPOINT ["/bin/excalibase-provisioning"]
