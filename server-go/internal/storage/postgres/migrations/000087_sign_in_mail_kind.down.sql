DROP INDEX IF EXISTS idx_sign_in_mail_sends_project_kind;
ALTER TABLE sign_in_mail_sends DROP COLUMN IF EXISTS kind;
