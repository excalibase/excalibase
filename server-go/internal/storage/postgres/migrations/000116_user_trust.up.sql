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
    -- No foreign key: the trail outlives the account, as evidence of who was
    -- limited or verified and who changed it.
    user_id TEXT NOT NULL,
    from_state TEXT NOT NULL CHECK (from_state IN ('', 'limited', 'full')),
    to_state TEXT NOT NULL CHECK (to_state IN ('limited', 'full')),
    method TEXT NOT NULL CHECK (method IN ('github', 'grandfathered', 'admin')),
    reason TEXT NOT NULL,
    actor TEXT NOT NULL CHECK (actor <> ''),
    at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_trust_audit_user ON trust_audit (user_id, id);

-- One row per finished grandfather backfill and the cutoff it used. With the
-- trial flag on, the server refuses to start while this table is empty.
CREATE TABLE IF NOT EXISTS trust_backfill_runs (
    id BIGSERIAL PRIMARY KEY,
    finished_at TIMESTAMPTZ NOT NULL,
    cutoff TIMESTAMPTZ NOT NULL
);
