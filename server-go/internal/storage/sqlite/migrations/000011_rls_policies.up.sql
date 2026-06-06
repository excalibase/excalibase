-- SQLite mirror of the Postgres rls_policies + column_policies tables.
-- Arrays are stored as JSON text since SQLite has no native array type.

CREATE TABLE IF NOT EXISTS rls_policies (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL,
    name          TEXT NOT NULL,
    resource      TEXT NOT NULL,
    effect        TEXT NOT NULL CHECK (effect IN ('ALLOW', 'DENY')),
    operations    TEXT NOT NULL,                            -- JSON array of operation strings
    rule_logic    TEXT NOT NULL DEFAULT 'AND' CHECK (rule_logic IN ('AND', 'OR')),
    rules         TEXT NOT NULL,                            -- JSON
    assignments   TEXT NOT NULL,                            -- JSON
    priority      INTEGER NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_rls_policies_project_resource ON rls_policies(project_id, resource);

CREATE TABLE IF NOT EXISTS column_policies (
    id                 TEXT PRIMARY KEY,
    project_id         TEXT NOT NULL,
    name               TEXT NOT NULL,
    resource           TEXT NOT NULL,
    columns            TEXT NOT NULL,                       -- JSON array
    operations         TEXT NOT NULL,                       -- JSON array
    mode               TEXT NOT NULL CHECK (mode IN ('HIDE', 'NULL', 'PARTIAL', 'HASH', 'CUSTOM')),
    partial_spec       TEXT,                                -- JSON, nullable
    custom_masker_key  TEXT,
    assignments        TEXT NOT NULL,                       -- JSON
    priority           INTEGER NOT NULL DEFAULT 0,
    enabled            INTEGER NOT NULL DEFAULT 1,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (mode <> 'PARTIAL' OR partial_spec IS NOT NULL),
    CHECK (mode <> 'CUSTOM' OR (custom_masker_key IS NOT NULL AND custom_masker_key <> ''))
);

CREATE INDEX IF NOT EXISTS idx_column_policies_project_resource ON column_policies(project_id, resource);
