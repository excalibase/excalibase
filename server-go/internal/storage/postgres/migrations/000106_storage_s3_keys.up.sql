-- S3 access keys (EXC-630, ADR 0044). The secret is in the vault under the
-- project; SigV4 needs it in plain text, so only the id is kept here.
CREATE TABLE IF NOT EXISTS storage_s3_keys (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL,
    name          TEXT NOT NULL,
    access_key_id TEXT NOT NULL UNIQUE,
    -- Empty means every bucket of the project.
    buckets       TEXT[] NOT NULL DEFAULT '{}',
    can_read      BOOLEAN NOT NULL,
    can_write     BOOLEAN NOT NULL,
    can_delete    BOOLEAN NOT NULL,
    created_by    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at    TIMESTAMPTZ,
    last_used_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_storage_s3_keys_project ON storage_s3_keys(project_id);
