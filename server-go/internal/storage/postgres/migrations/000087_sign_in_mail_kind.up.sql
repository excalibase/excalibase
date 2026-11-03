-- EXC-662: account mails (email change, added sign-in method) keep a daily
-- budget apart from sign-in links and codes. Existing rows are sign-in mail.
ALTER TABLE sign_in_mail_sends
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'sign_in'
    CHECK (kind IN ('sign_in', 'account'));
CREATE INDEX IF NOT EXISTS idx_sign_in_mail_sends_project_kind ON sign_in_mail_sends (project_id, kind, sent_at);
