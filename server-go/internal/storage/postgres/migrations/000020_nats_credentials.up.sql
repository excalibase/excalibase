-- Per-principal NATS credentials (EXC-324).
--
-- One row per bus identity: the platform services (svc-graphql,
-- svc-provisioning, svc-pgdog) and one per-project CDC watcher
-- (tenant-watcher:<projectId>). Only the bcrypt hash is stored — the
-- plaintext lives exactly once, in the k8s Secret the credential was
-- minted into. The auth_callout responder reads this table to decide
-- whether a connection is allowed and which subject permissions it gets.
CREATE TABLE IF NOT EXISTS nats_credentials (
    principal     TEXT PRIMARY KEY,
    project_id    TEXT,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Deprovision removes every credential a project ever held.
CREATE INDEX IF NOT EXISTS idx_nats_credentials_project ON nats_credentials(project_id);
