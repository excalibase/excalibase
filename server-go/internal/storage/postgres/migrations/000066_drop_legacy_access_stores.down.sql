-- Recreates the legacy stores empty, in their last shape (000011, 000023,
-- 000034, 000064 and the 000065 marker). Nothing reads them any more.
CREATE TABLE IF NOT EXISTS rls_policies (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL,
    name          TEXT NOT NULL,
    resource      TEXT NOT NULL,
    effect        TEXT NOT NULL CHECK (effect IN ('ALLOW', 'DENY')),
    operations    TEXT[] NOT NULL,
    rule_logic    TEXT NOT NULL DEFAULT 'AND' CHECK (rule_logic IN ('AND', 'OR')),
    rules         JSONB NOT NULL,
    assignments   JSONB NOT NULL,
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
    resource           TEXT NOT NULL,
    columns            TEXT[] NOT NULL,
    operations         TEXT[] NOT NULL,
    mode               TEXT NOT NULL CHECK (mode IN ('HIDE', 'NULL', 'PARTIAL', 'HASH', 'CUSTOM')),
    partial_spec       JSONB,
    custom_masker_key  TEXT,
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

CREATE TABLE IF NOT EXISTS table_grants (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL,
    resource    TEXT NOT NULL,
    operations  TEXT[] NOT NULL,
    role_name   TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT table_grants_operations_not_empty CHECK (cardinality(operations) > 0),
    CONSTRAINT table_grants_role_is_grantable
        CHECK (role_name ~ '^[a-z][a-z0-9_]{0,62}$' AND role_name <> 'service')
);

CREATE INDEX IF NOT EXISTS idx_table_grants_project_resource ON table_grants(project_id, resource);

CREATE UNIQUE INDEX IF NOT EXISTS ux_table_grants_project_resource_role
    ON table_grants(project_id, resource, role_name);

CREATE TABLE IF NOT EXISTS legacy_permissions_migrated (
    project_id   TEXT PRIMARY KEY REFERENCES database_instances(project_id) ON DELETE CASCADE,
    migrated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
