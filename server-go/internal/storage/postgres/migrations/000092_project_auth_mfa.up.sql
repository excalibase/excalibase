-- End-user TOTP MFA (EXC-662): the project's switch. The key that seals the
-- factors lives in the vault. Additive and unread while authmfa is off.
ALTER TABLE project_auth_settings ADD COLUMN IF NOT EXISTS mfa_enabled BOOLEAN NOT NULL DEFAULT false;
