CREATE TABLE IF NOT EXISTS vault_barrier (
    id INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    encrypted_barrier BYTEA NOT NULL,
    meta JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS vault_secrets (
    path TEXT PRIMARY KEY,
    entry BYTEA NOT NULL
);
