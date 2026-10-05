DROP INDEX IF EXISTS idx_pending_invites_org_email_lower;
ALTER TABLE pending_invites DROP CONSTRAINT IF EXISTS pending_invites_org_id_email_key;
ALTER TABLE pending_invites ADD CONSTRAINT pending_invites_org_id_email_key UNIQUE (org_id, email);
DROP INDEX IF EXISTS idx_users_human_email_lower;
