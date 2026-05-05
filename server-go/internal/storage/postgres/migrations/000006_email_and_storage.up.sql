-- Postgres mirror of 000006 — same shape, Postgres-native types.
CREATE TABLE IF NOT EXISTS email_verifications (
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL,
    email TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_email_verifications_user ON email_verifications(user_id);
CREATE INDEX IF NOT EXISTS idx_email_verifications_token ON email_verifications(token_hash);

CREATE TABLE IF NOT EXISTS password_resets (
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    requested_from TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_password_resets_user ON password_resets(user_id);
CREATE INDEX IF NOT EXISTS idx_password_resets_token ON password_resets(token_hash);

CREATE TABLE IF NOT EXISTS email_invites (
    id BIGSERIAL PRIMARY KEY,
    org_id TEXT NOT NULL,
    inviter_user_id TEXT NOT NULL,
    invitee_email TEXT NOT NULL,
    role TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_email_invites_org ON email_invites(org_id);
CREATE INDEX IF NOT EXISTS idx_email_invites_token ON email_invites(token_hash);

CREATE TABLE IF NOT EXISTS storage_buckets (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    name TEXT NOT NULL,
    public BOOLEAN NOT NULL DEFAULT false,
    file_size_limit BIGINT DEFAULT 0,
    allowed_mime_types JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, name)
);
CREATE INDEX IF NOT EXISTS idx_storage_buckets_project ON storage_buckets(project_id);

CREATE TABLE IF NOT EXISTS storage_objects (
    id TEXT PRIMARY KEY,
    bucket_id TEXT NOT NULL REFERENCES storage_buckets(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    size BIGINT NOT NULL DEFAULT 0,
    mime_type TEXT,
    etag TEXT,
    owner_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(bucket_id, key)
);
CREATE INDEX IF NOT EXISTS idx_storage_objects_bucket ON storage_objects(bucket_id);
CREATE INDEX IF NOT EXISTS idx_storage_objects_owner ON storage_objects(owner_id);

CREATE TABLE IF NOT EXISTS storage_quota (
    project_id TEXT PRIMARY KEY,
    bytes_used BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
