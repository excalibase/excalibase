-- Add scopes column to access_tokens for finer-grained PAT permissions.
-- Existing rows get NULL = legacy all-purpose; new tokens always set a scope.
ALTER TABLE access_tokens ADD COLUMN scopes TEXT;
