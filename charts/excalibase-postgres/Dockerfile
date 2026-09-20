# Excalibase PostgreSQL image, one build per supported major.
#
# CREATE EXTENSION only succeeds when the extension's files are already in the
# image, so every extension on the tenant allowlist
# (server-go/internal/schema/extensions.go) has to be installed here.
#
# Measured against the stock CNPG image on 14/15/16/17/18: every allowlisted
# extension already installs there — pgvector included — except postgis. So
# postgis is the only allowlist addition this image makes. pg_cron is added
# too, but not for tenants: it is a DocumentDB prerequisite and stays off the
# allowlist.
#
# BASE_IMAGE is always passed digest-pinned from the catalogue
# (server-go/internal/config/postgres_catalog.yaml) so a rebuild of a given
# major resolves to the same upstream bits. The catalogue pins the -bookworm
# (Debian 12) variants rather than the default bullseye ones: pgdg bullseye
# publishes no postgresql-18-postgis-3, so a bullseye base cannot carry postgis
# on Postgres 18, while pgdg bookworm has postgis 3.6 for 14 through 18.
ARG BASE_IMAGE
FROM ${BASE_IMAGE}

# Re-declared: an ARG above the first FROM is out of scope inside the stage.
ARG PG_MAJOR
# Empty means "this major gets no DocumentDB". Upstream publishes the Debian 12
# packages for 16, 17 and 18 only, so 14 and 15 build without it and the
# catalogue records them as DocumentDB-incapable.
ARG DOCUMENTDB_VERSION=""

USER root

RUN set -eux; \
    test -n "${PG_MAJOR}"; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        "postgresql-${PG_MAJOR}-postgis-3" \
        "postgresql-${PG_MAJOR}-postgis-3-scripts" \
        "postgresql-${PG_MAJOR}-cron"; \
    rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    if [ -n "${DOCUMENTDB_VERSION}" ]; then \
        apt-get update; \
        apt-get install -y --no-install-recommends curl ca-certificates; \
        curl -fSL -o /tmp/documentdb.deb \
            "https://github.com/microsoft/documentdb/releases/download/v${DOCUMENTDB_VERSION}/deb12-postgresql-${PG_MAJOR}-documentdb_${DOCUMENTDB_VERSION}_amd64.deb"; \
        apt-get install -y --no-install-recommends /tmp/documentdb.deb; \
        rm -f /tmp/documentdb.deb; \
        apt-get purge -y --auto-remove curl; \
        rm -rf /var/lib/apt/lists/*; \
        test -f "/usr/share/postgresql/${PG_MAJOR}/extension/documentdb.control"; \
    fi

# Back to the CNPG runtime user: CNPG refuses to start a cluster whose image
# runs as root.
USER 26
