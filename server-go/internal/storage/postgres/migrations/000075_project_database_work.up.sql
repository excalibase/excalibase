-- The last reading of a project's client work, taken from the database itself
-- for engines whose apps connect directly (MySQL, EXC-588). The idle sweep
-- compares each reading with the previous one.
CREATE TABLE IF NOT EXISTS project_database_work (
    project_id TEXT PRIMARY KEY REFERENCES database_instances(project_id) ON DELETE CASCADE,
    work       BIGINT NOT NULL,
    read_at    TIMESTAMPTZ NOT NULL
);
