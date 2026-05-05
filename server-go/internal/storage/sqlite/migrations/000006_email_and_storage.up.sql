-- Email: one-time tokens for verify/reset/invite flows.
-- We store the SHA-256 of the token, never the token itself, so a
-- platform-db dump can't be used to forge clicks. Tokens are 32 random
-- bytes (256 bits); collision risk is negligible at our scale.
CREATE TABLE IF NOT EXISTS email_verifications (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    email TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    consumed_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_email_verifications_user ON email_verifications(user_id);
CREATE INDEX IF NOT EXISTS idx_email_verifications_token ON email_verifications(token_hash);

CREATE TABLE IF NOT EXISTS password_resets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    consumed_at TEXT,
    -- IP address at request time, surfaced in the email body so the
    -- recipient can see "wait, that wasn't me" before clicking.
    requested_from TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_password_resets_user ON password_resets(user_id);
CREATE INDEX IF NOT EXISTS idx_password_resets_token ON password_resets(token_hash);

-- Org invites: distinct from pending_invites in 000003 because the
-- email-driven flow needs a click-token, not just a directory entry.
-- pending_invites stays as-is for the in-app /orgs/:id/invite UI;
-- email_invites is the URL-clickable variant. They can co-exist:
-- accepting either binds the user to the org.
CREATE TABLE IF NOT EXISTS email_invites (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id TEXT NOT NULL,
    inviter_user_id TEXT NOT NULL,
    invitee_email TEXT NOT NULL,
    role TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    consumed_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_email_invites_org ON email_invites(org_id);
CREATE INDEX IF NOT EXISTS idx_email_invites_token ON email_invites(token_hash);

-- Storage: bucket + object metadata. The actual bytes live in R2;
-- platform-db only knows the existence + size + ownership.
--
-- Public=1 means anonymous reads are allowed via /storage/v1/object/public/...
-- Even public buckets require an authenticated user to UPLOAD.
CREATE TABLE IF NOT EXISTS storage_buckets (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    name TEXT NOT NULL,
    public INTEGER NOT NULL DEFAULT 0,
    file_size_limit INTEGER DEFAULT 0,    -- bytes; 0 = unlimited (still subject to project quota)
    allowed_mime_types TEXT,              -- JSON array; empty = any
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(project_id, name)
);
CREATE INDEX IF NOT EXISTS idx_storage_buckets_project ON storage_buckets(project_id);

CREATE TABLE IF NOT EXISTS storage_objects (
    id TEXT PRIMARY KEY,
    bucket_id TEXT NOT NULL REFERENCES storage_buckets(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    size INTEGER NOT NULL DEFAULT 0,      -- bytes
    mime_type TEXT,
    etag TEXT,
    owner_id TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(bucket_id, key)
);
CREATE INDEX IF NOT EXISTS idx_storage_objects_bucket ON storage_objects(bucket_id);
CREATE INDEX IF NOT EXISTS idx_storage_objects_owner ON storage_objects(owner_id);

-- Per-project storage quota tracker. Updated on confirm-upload; the daily
-- janitor reconciles it against R2 in case a confirm got lost.
CREATE TABLE IF NOT EXISTS storage_quota (
    project_id TEXT PRIMARY KEY,
    bytes_used INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
