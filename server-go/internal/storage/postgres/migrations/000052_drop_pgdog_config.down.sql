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

CREATE UNIQUE INDEX IF NOT EXISTS uq_pgdog_databases_route ON pgdog_databases (name, role, shard);
CREATE INDEX IF NOT EXISTS idx_pgdog_databases_name ON pgdog_databases (name);
CREATE INDEX IF NOT EXISTS idx_pgdog_users_database ON pgdog_users (database);
