# The upstream DocumentDB gateway with a guard in front of it. The pair in
# server-go/internal/config/postgres_catalog.yaml must name this build's digest,
# and its base must stay on the same release as documentDBRef.
FROM ghcr.io/documentdb/documentdb-kubernetes-operator/gateway:0.117.0@sha256:8a5c7e533dc0fbed8ed780b22e31001a6b12ae0eb670f4aac62c1ded782742ed
COPY gateway_guard.sh /home/documentdb/gateway/scripts/gateway_guard.sh
ENTRYPOINT ["/bin/bash", "/home/documentdb/gateway/scripts/gateway_guard.sh"]
