-- Usage samples for credit metering (EXC-866). Every five minutes the sampler
-- writes what an org reserves, stamped with the database clock. slot_start is
-- date_bin('5 minutes', now()), so a repeat in the same slot is merged, never
-- doubled. No foreign keys: like the ledger, samples outlive the org.
CREATE TABLE IF NOT EXISTS usage_samples (
    org_id       TEXT        NOT NULL,
    slot_start   TIMESTAMPTZ NOT NULL,
    cpu_milli    BIGINT      NOT NULL CHECK (cpu_milli >= 0),
    memory_bytes BIGINT      NOT NULL CHECK (memory_bytes >= 0),
    disk_bytes   BIGINT      NOT NULL CHECK (disk_bytes >= 0),
    breakdown    JSONB       NOT NULL DEFAULT '[]'::jsonb,
    sampled_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, slot_start)
);
CREATE INDEX IF NOT EXISTS idx_usage_samples_slot ON usage_samples (slot_start);
