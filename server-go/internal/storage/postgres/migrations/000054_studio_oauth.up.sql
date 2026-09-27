-- Studio sign-in with Google and GitHub: the state a sign-in holds between
-- leaving for the provider and coming back, and which provider accounts are
-- linked to which Studio account.
CREATE TABLE IF NOT EXISTS studio_oauth_states (
    state_hash    TEXT PRIMARY KEY,
    provider      TEXT NOT NULL,
    code_verifier TEXT NOT NULL,
    invite_hash   TEXT NOT NULL DEFAULT '',
    expires_at    TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_studio_oauth_states_expires ON studio_oauth_states(expires_at);

CREATE TABLE IF NOT EXISTS studio_identities (
    provider   TEXT NOT NULL,
    subject    TEXT NOT NULL,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (provider, subject)
);
CREATE INDEX IF NOT EXISTS idx_studio_identities_user ON studio_identities(user_id);
