# Excalibase PostgreSQL image, one build per supported major.
#
# CREATE EXTENSION only succeeds when the extension's files are already in the
# image, so every extension on the tenant allowlist
# (server-go/internal/schema/extensions.go) has to be installed here.
#
# Measured against the stock CNPG image on 14/15/16/17: every allowlisted
# extension already installs there — pgvector included — except postgis. So
# postgis is the only allowlist addition this image makes. pg_cron is added
# too, but not for tenants: it is a DocumentDB prerequisite and stays off the
# allowlist.
#
# BASE_IMAGE is always passed digest-pinned from the catalogue
# (server-go/internal/config/postgres_catalog.yaml) so a rebuild of a given
# major resolves to the same upstream bits. The catalogue pins the -bookworm
# (Debian 12) variants rather than the default bullseye ones: pgdg bullseye
# publishes no postgis for every major we carry, while pgdg bookworm has
# postgis 3.6 for 14 through 17.

ARG BASE_IMAGE

# ---------------------------------------------------------------------------
# DocumentDB, compiled from source.
#
# Upstream's published Debian packages are not the constraint here: they stop
# at the majors Microsoft chose to package (no deb12 build for 15, and none
# for 16 after 0.114). The extension is open source, so we build it ourselves
# from a release tag and decide which majors we support by whether the build
# and the smoke test pass.
#
# The builder runs on the same digest-pinned CNPG base as the runtime stage, so
# the extension is compiled against exactly the PostgreSQL build it will load
# into.
# ---------------------------------------------------------------------------
FROM ${BASE_IMAGE} AS documentdb-build

ARG PG_MAJOR
# The DocumentDB release tag to compile, e.g. "v0.117-0". Empty means this
# major gets no DocumentDB and the stage does nothing.
ARG DOCUMENTDB_REF=""

USER root
WORKDIR /build

RUN set -eux; \
    if [ -z "${DOCUMENTDB_REF}" ]; then mkdir -p /stage; exit 0; fi; \
    test -n "${PG_MAJOR}"; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        build-essential cmake git curl ca-certificates pkg-config python3 \
        "postgresql-server-dev-${PG_MAJOR}" \
        libicu-dev libkrb5-dev libssl-dev zlib1g-dev; \
    rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    if [ -z "${DOCUMENTDB_REF}" ]; then exit 0; fi; \
    git clone --depth 1 --branch "${DOCUMENTDB_REF}" https://github.com/microsoft/documentdb.git /build/src

# The three out-of-distro dependencies DocumentDB links against. Upstream's own
# packaging builds them exactly this way. All three end up linked statically
# into the extension modules, so nothing from this stage but the installed
# extension has to reach the runtime image — checked with ldd: the
# pg_documentdb*.so files name no libbson, pcre2 or intelmathlib at runtime.
RUN set -eux; \
    if [ -z "${DOCUMENTDB_REF}" ]; then exit 0; fi; \
    export INSTALL_DEPENDENCIES_ROOT=/build/deps CLEANUP_SETUP=1; \
    mkdir -p "${INSTALL_DEPENDENCIES_ROOT}"; \
    MAKE_PROGRAM=cmake /build/src/scripts/install_setup_libbson.sh; \
    /build/src/scripts/install_setup_pcre2.sh; \
    /build/src/scripts/install_setup_intel_decimal_math_lib.sh

# install-documentdb is upstream's non-distributed target: documentdb_core,
# documentdb and the extended RUM index access method. Citus and the
# distributed extension are deliberately left out — single-node projects.
RUN set -eux; \
    mkdir -p /stage; \
    if [ -z "${DOCUMENTDB_REF}" ]; then exit 0; fi; \
    cd /build/src; \
    make -j"$(nproc)"; \
    make install-documentdb DESTDIR=/stage; \
    test -f "/stage/usr/share/postgresql/${PG_MAJOR}/extension/documentdb.control"

# ---------------------------------------------------------------------------
# Runtime image.
# ---------------------------------------------------------------------------
FROM ${BASE_IMAGE}

# Re-declared: an ARG above the first FROM is out of scope inside the stage.
ARG PG_MAJOR
ARG DOCUMENTDB_REF=""

USER root

RUN set -eux; \
    test -n "${PG_MAJOR}"; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        "postgresql-${PG_MAJOR}-postgis-3" \
        "postgresql-${PG_MAJOR}-postgis-3-scripts" \
        "postgresql-${PG_MAJOR}-cron"; \
    rm -rf /var/lib/apt/lists/*

COPY --from=documentdb-build /stage/ /

RUN set -eux; \
    if [ -n "${DOCUMENTDB_REF}" ]; then \
        test -f "/usr/share/postgresql/${PG_MAJOR}/extension/documentdb.control"; \
    fi

# Back to the CNPG runtime user: CNPG refuses to start a cluster whose image
# runs as root.
USER 26
