-- Behaviour review (EXC-844): what the network meter's findings leave for a
-- person to look at. Nothing here references an app, a project, an org or a
-- user: each row snapshots the ids and names it was made with, so the evidence
-- outlives a deleted app, project or account. Evidence is numbers only.
CREATE TABLE IF NOT EXISTS abuse_review (
    id BIGSERIAL PRIMARY KEY,
    owner_id TEXT NOT NULL,
    org_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    project_name TEXT NOT NULL DEFAULT '',
    -- Empty for a finding about the owner as a whole.
    app_id TEXT NOT NULL DEFAULT '',
    app_name TEXT NOT NULL DEFAULT '',
    tier TEXT NOT NULL DEFAULT '',
    signal TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'dismissed', 'actioned')),
    first_seen TIMESTAMPTZ NOT NULL,
    last_seen TIMESTAMPTZ NOT NULL,
    peak DOUBLE PRECISION NOT NULL,
    evidence JSONB NOT NULL,
    dismissed_at TIMESTAMPTZ,
    dismissed_by TEXT NOT NULL DEFAULT '',
    dismissed_peak DOUBLE PRECISION NOT NULL DEFAULT 0,
    mute_until TIMESTAMPTZ
);

-- One open review per owner, app and signal; repeats update it.
CREATE UNIQUE INDEX IF NOT EXISTS uq_abuse_review_open ON abuse_review(owner_id, app_id, signal) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS idx_abuse_review_status_seen ON abuse_review(status, last_seen DESC);
-- The latest dismissal of a key, for the mute rule.
CREATE INDEX IF NOT EXISTS idx_abuse_review_key_dismissed ON abuse_review(owner_id, app_id, signal, dismissed_at DESC) WHERE status = 'dismissed';
