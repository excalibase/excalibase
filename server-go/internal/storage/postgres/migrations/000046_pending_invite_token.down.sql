DROP INDEX IF EXISTS idx_pending_invites_token_hash;
ALTER TABLE pending_invites DROP COLUMN expires_at;
ALTER TABLE pending_invites DROP COLUMN token_hash;
