-- PgDog route table, read by the excalibase/pgdog fork (pgdog-config
-- postgres loader) when PGDOG_CONFIG_DATABASE_URL points at the platform DB.
-- Column set mirrors the fork's sql/pgdog_config_tables.sql so its
-- CREATE TABLE IF NOT EXISTS is a no-op against this schema. Provisioning
-- writes one logical database per project (name = project id) and one row
-- per engine role in pgdog_users; PgDog authenticates clients on the
-- (name, database) pair and reuses the same credential toward the backend.
CREATE TABLE IF NOT EXISTS pgdog_databases (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT NOT NULL,
    host          TEXT NOT NULL,
    port          INTEGER DEFAULT 5432,
    database_name TEXT DEFAULT 'app',
    role          TEXT DEFAULT 'primary',
    shard         INTEGER DEFAULT 0,
    pool_size     INTEGER,
    read_only     BOOLEAN DEFAULT FALSE,
    active        BOOLEAN DEFAULT TRUE,
    created_at    TIMESTAMPTZ DEFAULT NOW(),
    updated_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS pgdog_users (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    database   TEXT NOT NULL,
    password   TEXT NOT NULL,
    pool_size  INTEGER,
    active     BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (name, database)
);

-- Tables may pre-exist from the fork's SQL, which has no uniqueness on the
-- route key; collapse duplicates before enforcing it.
DELETE FROM pgdog_databases a
    USING pgdog_databases b
    WHERE a.ctid < b.ctid
      AND a.name = b.name AND a.role = b.role AND a.shard = b.shard;

CREATE UNIQUE INDEX IF NOT EXISTS uq_pgdog_databases_route ON pgdog_databases (name, role, shard);
CREATE INDEX IF NOT EXISTS idx_pgdog_databases_name ON pgdog_databases (name);
CREATE INDEX IF NOT EXISTS idx_pgdog_users_database ON pgdog_users (database);
