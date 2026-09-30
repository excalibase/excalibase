-- Hasura-style API permissions (EXC-370 step C): one object per table, role
-- and operation, the functions a project tracks and the roles that may call
-- them. The engine reads all three as one document from
-- GET /api/provision/{projectId}/permissions/. Role names follow table_grants
-- (000064): lower-case identifiers, never service.

CREATE TABLE IF NOT EXISTS api_permissions (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES database_instances(project_id) ON DELETE CASCADE,
    table_key   TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    operation   TEXT NOT NULL,
    definition  JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT api_permissions_operation CHECK (operation IN ('select', 'insert', 'update', 'delete')),
    CONSTRAINT api_permissions_role CHECK (role_name ~ '^[a-z][a-z0-9_]{0,62}$' AND role_name <> 'service'),
    CONSTRAINT api_permissions_definition_object CHECK (jsonb_typeof(definition) = 'object'),
    CONSTRAINT api_permissions_one_per_operation UNIQUE (project_id, table_key, role_name, operation)
);

CREATE TABLE IF NOT EXISTS tracked_functions (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id         TEXT NOT NULL REFERENCES database_instances(project_id) ON DELETE CASCADE,
    function_key       TEXT NOT NULL,
    exposed_as         TEXT NOT NULL CHECK (exposed_as IN ('QUERY', 'MUTATION')),
    infer_permissions  BOOLEAN NOT NULL DEFAULT TRUE,
    session_argument   TEXT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT tracked_functions_one_per_function UNIQUE (project_id, function_key)
);

-- Untracking a function removes who may call it: the permissions cascade.
CREATE TABLE IF NOT EXISTS function_permissions (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id    TEXT NOT NULL REFERENCES database_instances(project_id) ON DELETE CASCADE,
    function_key  TEXT NOT NULL,
    role_name     TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT function_permissions_role CHECK (role_name ~ '^[a-z][a-z0-9_]{0,62}$' AND role_name <> 'service'),
    CONSTRAINT function_permissions_one_per_role UNIQUE (project_id, function_key, role_name),
    CONSTRAINT function_permissions_tracked FOREIGN KEY (project_id, function_key)
        REFERENCES tracked_functions (project_id, function_key) ON DELETE CASCADE
);

-- Bumped on every write to the three tables above, so two documents of one
-- project can be told apart.
CREATE TABLE IF NOT EXISTS permission_versions (
    project_id  TEXT PRIMARY KEY REFERENCES database_instances(project_id) ON DELETE CASCADE,
    version     BIGINT NOT NULL CHECK (version > 0)
);

-- Projects whose table grants, row policies and column policies have been
-- folded into permissions (done at startup, once per project).
CREATE TABLE IF NOT EXISTS legacy_permissions_migrated (
    project_id   TEXT PRIMARY KEY REFERENCES database_instances(project_id) ON DELETE CASCADE,
    migrated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
