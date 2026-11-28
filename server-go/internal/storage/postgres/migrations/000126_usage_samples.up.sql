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
    -- The plan's free allowance when the sample was taken, so a later plan change never reprices a sampled hour.
    allowance_cpu_milli    BIGINT NOT NULL DEFAULT 0 CHECK (allowance_cpu_milli >= 0),
    allowance_memory_bytes BIGINT NOT NULL DEFAULT 0 CHECK (allowance_memory_bytes >= 0),
    allowance_disk_bytes   BIGINT NOT NULL DEFAULT 0 CHECK (allowance_disk_bytes >= 0),
    -- Services whose size was never recorded and were billed at their plan's default size.
    unrecorded_services    INTEGER NOT NULL DEFAULT 0,
    breakdown    JSONB       NOT NULL DEFAULT '[]'::jsonb,
    sampled_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, slot_start)
);
CREATE INDEX IF NOT EXISTS idx_usage_samples_slot ON usage_samples (slot_start);

-- When metering first ran. An org's first watermark never starts before it.
CREATE TABLE IF NOT EXISTS credit_meter_state (
    key TEXT PRIMARY KEY,
    at  TIMESTAMPTZ NOT NULL
);
