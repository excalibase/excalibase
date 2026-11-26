-- Limited trial (EXC-899): the derived trust decision per account and its
-- audit trail. No GitHub data is stored here, only the result and a reason code.
-- The two states, written once.
DO $$
BEGIN
    CREATE DOMAIN trust_state AS TEXT CHECK (VALUE IN ('limited', 'full'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS user_trust (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    state trust_state NOT NULL,
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
    -- NULL for an account's first decision: there was no state before it.
    from_state trust_state,
    to_state trust_state NOT NULL,
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
