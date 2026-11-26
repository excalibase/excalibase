-- Limited trial (EXC-899): the derived trust decision per account and its
-- audit trail. No GitHub data is stored here, only the result and a reason code.
CREATE TABLE IF NOT EXISTS user_trust (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN ('limited', 'full')),
    method TEXT NOT NULL CHECK (method IN ('github', 'grandfathered', 'admin')),
    reason TEXT NOT NULL,
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    check_version INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS trust_audit (
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    from_state TEXT NOT NULL,
    to_state TEXT NOT NULL,
    method TEXT NOT NULL,
    reason TEXT NOT NULL,
    actor TEXT NOT NULL,
    at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_trust_audit_user ON trust_audit (user_id, id);
