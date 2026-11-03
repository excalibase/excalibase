-- End-user sign-in by emailed link or code (EXC-662): the project's switch.
-- Additive and unread while authotp is off.
ALTER TABLE project_auth_settings
    ADD COLUMN IF NOT EXISTS email_otp_enabled BOOLEAN NOT NULL DEFAULT false;
