-- A Studio account signs in only after proving it owns its email address.
ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ;
