# The upstream DocumentDB gateway with a guard in front of it. The base must
# stay on the same release as documentDBRef in
# server-go/internal/config/postgres_catalog.yaml.
FROM ghcr.io/documentdb/documentdb-kubernetes-operator/gateway:0.117.0@sha256:8a5c7e533dc0fbed8ed780b22e31001a6b12ae0eb670f4aac62c1ded782742ed
USER root
# Upstream's base ships Debian packages with fixed CVEs (EXC-587).
RUN apt-get update \
  && DEBIAN_FRONTEND=noninteractive apt-get upgrade -y --no-install-recommends \
  && apt-get clean && rm -rf /var/lib/apt/lists/*
COPY NOTICE /home/documentdb/gateway/NOTICE
COPY gateway_guard.sh /home/documentdb/gateway/scripts/gateway_guard.sh
USER documentdb
ENTRYPOINT ["/bin/bash", "/home/documentdb/gateway/scripts/gateway_guard.sh"]
