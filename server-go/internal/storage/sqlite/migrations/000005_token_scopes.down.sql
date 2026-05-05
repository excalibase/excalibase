-- SQLite doesn't support DROP COLUMN before 3.35; use the rebuild idiom.
CREATE TABLE access_tokens_new (
    token_hash TEXT PRIMARY KEY,
    token_prefix TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT,
    created_at TEXT DEFAULT CURRENT_TIMESTAMP,
    expires_at TEXT,
    last_used TEXT
);
INSERT INTO access_tokens_new SELECT token_hash, token_prefix, user_id, name, created_at, expires_at, last_used FROM access_tokens;
DROP TABLE access_tokens;
ALTER TABLE access_tokens_new RENAME TO access_tokens;
