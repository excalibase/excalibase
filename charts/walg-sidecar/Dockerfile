FROM alpine:3.20

# WAL-G sidecar image. Runs alongside the project's postgres container,
# shares the WAL archive volume, and runs `wal-g backup-push` on the
# platform-driven schedule. Continuous WAL archiving comes from the
# postgres container's archive_command — the sidecar is just a thin
# scheduler + binary host.
#
# Pinned alpine 3.20 because 3.21 ships glibc 2.40 which broke the
# pre-built wal-g binary at the time of writing.

RUN apk add --no-cache \
        ca-certificates=20240705-r0 \
        bash \
        tini \
        curl \
        gzip \
        tzdata \
    && update-ca-certificates

ARG WALG_VERSION=v3.0.5
RUN curl -fSL "https://github.com/wal-g/wal-g/releases/download/${WALG_VERSION}/wal-g-pg-ubuntu-20.04-amd64.tar.gz" \
      | tar -xz -C /usr/local/bin \
    && mv /usr/local/bin/wal-g-pg-ubuntu-20.04-amd64 /usr/local/bin/wal-g \
    && chmod +x /usr/local/bin/wal-g \
    && /usr/local/bin/wal-g --version

COPY entrypoint.sh /entrypoint.sh
COPY healthcheck.sh /healthcheck.sh
RUN chmod +x /entrypoint.sh /healthcheck.sh

# WAL-G needs no persistent state of its own — all state lives in
# the shared volume (PG WAL files) or in the configured S3 bucket.
WORKDIR /

HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD /healthcheck.sh

ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
