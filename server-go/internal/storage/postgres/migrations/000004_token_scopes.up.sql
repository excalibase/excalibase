-- Add scopes column to access_tokens for finer-grained PAT permissions.
ALTER TABLE access_tokens ADD COLUMN IF NOT EXISTS scopes TEXT;
