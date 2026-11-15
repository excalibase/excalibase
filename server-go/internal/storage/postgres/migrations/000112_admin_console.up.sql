-- Admin console (EXC-757).

-- abuse_suspensions was created by hand on prod on 2026-11-16; this makes the
-- schema it needs part of the migration history. The row says why an account
-- was suspended; users.active=false is what blocks it.
CREATE TABLE IF NOT EXISTS abuse_suspensions (
    user_id      TEXT PRIMARY KEY,
    email        TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    suspended_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_abuse_suspensions_at ON abuse_suspensions(suspended_at);

-- The apps a suspension stopped and the replicas each ran, so unsuspending
-- restores those and leaves an app its owner had paused alone.
CREATE TABLE IF NOT EXISTS abuse_suspension_apps (
    user_id           TEXT NOT NULL,
    project_id        TEXT NOT NULL,
    app_id            TEXT NOT NULL,
    app_name          TEXT NOT NULL DEFAULT '',
    previous_replicas INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, project_id, app_id)
);

-- Last sign-in, for the admin's users table and its IP search.
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_ip TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_users_created_at ON users(created_at);
