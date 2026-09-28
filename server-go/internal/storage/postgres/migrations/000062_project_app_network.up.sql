-- A project's opt-in private network between its Containers apps (EXC-524).
-- No row reads as off: apps accept only the edge until an admin turns it on.
CREATE TABLE IF NOT EXISTS project_app_network (
    project_id      TEXT PRIMARY KEY,
    private_network BOOLEAN NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
