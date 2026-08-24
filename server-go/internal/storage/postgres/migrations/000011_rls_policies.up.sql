-- RLS engine policy storage (EXC-318).
-- Powers excalibase-graphql's row-level + column-level enforcement.
-- Field shapes match the engine's record types one-for-one so the Go
-- handlers can serialize directly into Policy / ColumnPolicy.

CREATE TABLE IF NOT EXISTS rls_policies (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL,
    name          TEXT NOT NULL,
    resource      TEXT NOT NULL,                              -- table name
    effect        TEXT NOT NULL CHECK (effect IN ('ALLOW', 'DENY')),
    operations    TEXT[] NOT NULL,                            -- {'SELECT','INSERT','UPDATE','DELETE'}
    rule_logic    TEXT NOT NULL DEFAULT 'AND' CHECK (rule_logic IN ('AND', 'OR')),
    rules         JSONB NOT NULL,                             -- [{field, fieldType, operator, value}, ...]
    assignments   JSONB NOT NULL,                             -- [{targetType, targetId}, ...]
    priority      INTEGER NOT NULL DEFAULT 0,
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_rls_policies_project_resource ON rls_policies(project_id, resource);

CREATE TABLE IF NOT EXISTS column_policies (
    id                 TEXT PRIMARY KEY,
    project_id         TEXT NOT NULL,
    name               TEXT NOT NULL,
    resource           TEXT NOT NULL,                         -- table name
    columns            TEXT[] NOT NULL,                       -- columns this rule covers
    operations         TEXT[] NOT NULL,
    mode               TEXT NOT NULL CHECK (mode IN ('HIDE', 'NULL', 'PARTIAL', 'HASH', 'CUSTOM')),
    partial_spec       JSONB,                                 -- nullable; only when mode = 'PARTIAL'
    custom_masker_key  TEXT,                                  -- nullable; only when mode = 'CUSTOM'
    assignments        JSONB NOT NULL,
    priority           INTEGER NOT NULL DEFAULT 0,
    enabled            BOOLEAN NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT column_policies_partial_spec_required
        CHECK (mode <> 'PARTIAL' OR partial_spec IS NOT NULL),
    CONSTRAINT column_policies_custom_key_required
        CHECK (mode <> 'CUSTOM' OR (custom_masker_key IS NOT NULL AND custom_masker_key <> ''))
);

CREATE INDEX IF NOT EXISTS idx_column_policies_project_resource ON column_policies(project_id, resource);
