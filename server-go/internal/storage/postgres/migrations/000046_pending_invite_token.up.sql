-- An invite is joined only through its one-time link. Rows from before the
-- link existed carry no token and can never be claimed, so they are dropped.
ALTER TABLE pending_invites ADD COLUMN token_hash TEXT;
ALTER TABLE pending_invites ADD COLUMN expires_at TIMESTAMPTZ;
DELETE FROM pending_invites WHERE token_hash IS NULL;
ALTER TABLE pending_invites ALTER COLUMN token_hash SET NOT NULL;
ALTER TABLE pending_invites ALTER COLUMN expires_at SET NOT NULL;
CREATE UNIQUE INDEX idx_pending_invites_token_hash ON pending_invites(token_hash);
