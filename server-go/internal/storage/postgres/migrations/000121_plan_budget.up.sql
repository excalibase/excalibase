-- Reserved sizes for the plan budget (EXC-901). Additive only: no running
-- database is restarted, resized or shrunk by this migration or by any flag.
-- The per-copy size is what one copy of the database reserves; copies = 0 means
-- "not recorded yet", and the boot backfill fills it from the plan the database
-- was created under.
ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS reserved_cpu_milli    BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reserved_memory_bytes BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reserved_disk_bytes   BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reserved_copies       INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS has_functions_runtime BOOLEAN NOT NULL DEFAULT FALSE;

-- Apps hold their size in their record (tier, replicas, disk), so they need no
-- column. The per-org sum reads through idx_database_instances_org_status.
