-- Reverses EXC-400. The recreated table is empty, so every project reads as
-- unenforced again — which is exactly the hole this migration closed.
ALTER TABLE table_grants
    DROP CONSTRAINT IF EXISTS table_grants_role_is_end_user;

CREATE TABLE IF NOT EXISTS project_exposure_settings (
    project_id TEXT PRIMARY KEY,
    enforced   BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
