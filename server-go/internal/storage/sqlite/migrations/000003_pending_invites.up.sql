CREATE TABLE IF NOT EXISTS pending_invites (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'developer',
    invited_by TEXT NOT NULL REFERENCES users(id),
    created_at TEXT DEFAULT (datetime('now')),
    UNIQUE(org_id, email)
);

CREATE INDEX IF NOT EXISTS idx_pending_invites_email ON pending_invites(email);
