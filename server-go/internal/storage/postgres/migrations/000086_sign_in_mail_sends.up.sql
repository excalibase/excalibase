-- End-user sign-in mails (EXC-662): one row per link or code the relay sent,
-- kept a day so a project's daily budget on the shared sender can be counted.
-- Additive; written only while authotp is on.
CREATE TABLE IF NOT EXISTS sign_in_mail_sends (
    id         BIGSERIAL PRIMARY KEY,
    project_id TEXT NOT NULL,
    sent_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_sign_in_mail_sends_project ON sign_in_mail_sends (project_id, sent_at);
CREATE INDEX IF NOT EXISTS idx_sign_in_mail_sends_sent_at ON sign_in_mail_sends (sent_at);
