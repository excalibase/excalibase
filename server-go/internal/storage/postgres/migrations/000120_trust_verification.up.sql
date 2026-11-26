-- Verify with GitHub (EXC-899): what a verification round trip remembers,
-- which GitHub account holds which Excalibase account, and the latest attempt.
ALTER TABLE studio_oauth_states ADD COLUMN IF NOT EXISTS purpose TEXT NOT NULL DEFAULT '';
ALTER TABLE studio_oauth_states ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';

-- One row per claim key ("github:<numeric id>"); the credits grant uses the
-- same keys. A claim is released when its account is deleted and then stays
-- unusable by another account for the configured cooldown.
CREATE TABLE IF NOT EXISTS trust_claims (
    claim_key   TEXT PRIMARY KEY,
    user_id     TEXT REFERENCES users(id) ON DELETE SET NULL,
    claimed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_trust_claims_live_user
    ON trust_claims (user_id) WHERE released_at IS NULL AND user_id IS NOT NULL;

CREATE OR REPLACE FUNCTION release_trust_claims() RETURNS trigger AS $$
BEGIN
    UPDATE trust_claims SET released_at = now() WHERE user_id = OLD.id AND released_at IS NULL;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_release_trust_claims ON users;
CREATE TRIGGER trg_release_trust_claims BEFORE DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION release_trust_claims();

-- The latest attempt per account: counts and ages only, never GitHub content.
CREATE TABLE IF NOT EXISTS trust_verifications (
    user_id              TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    checked_at           TIMESTAMPTZ NOT NULL,
    github_id            TEXT NOT NULL DEFAULT '',
    account_age_days     INTEGER NOT NULL DEFAULT 0,
    public_events        INTEGER NOT NULL DEFAULT 0,
    public_repos         INTEGER NOT NULL DEFAULT 0,
    oldest_repo_age_days INTEGER NOT NULL DEFAULT 0,
    passed               BOOLEAN NOT NULL,
    reason               TEXT NOT NULL
);
