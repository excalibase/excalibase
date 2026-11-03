-- End-user MFA (EXC-662): which vault key sealed the project's factors, so a
-- lost key is refused instead of replaced. Additive and unread while authmfa is off.
ALTER TABLE project_auth_settings ADD COLUMN IF NOT EXISTS mfa_key_fingerprint TEXT;
