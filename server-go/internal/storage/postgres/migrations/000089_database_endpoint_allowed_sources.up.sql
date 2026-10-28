-- Who may reach a project's public database port (EXC-665). No row: the
-- project never chose, and an already open port stays open to any address.
-- access 'any' admits everyone by choice; 'listed' admits only sources.
-- Additive and unread while the dballowlist flag is off.
CREATE TABLE IF NOT EXISTS database_endpoint_allowed_sources (
    project_id TEXT PRIMARY KEY,
    access     TEXT        NOT NULL,
    sources    TEXT[]      NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT database_endpoint_allowed_sources_access CHECK (access IN ('any', 'listed')),
    CONSTRAINT database_endpoint_allowed_sources_shape CHECK (
        (access = 'any' AND cardinality(sources) = 0) OR
        (access = 'listed' AND cardinality(sources) BETWEEN 1 AND 20))
);
