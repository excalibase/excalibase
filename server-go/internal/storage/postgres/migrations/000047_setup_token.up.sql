-- Holds the hash of the one-time first-admin setup token. At most one row
-- ever exists: the startup bootstrap replaces it, and creating the first
-- admin deletes it in the same transaction (EXC-451).
CREATE TABLE setup_tokens (
    token_hash TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
