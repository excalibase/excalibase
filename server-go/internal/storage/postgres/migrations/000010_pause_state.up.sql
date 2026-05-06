-- Idle-pause for FREE-tier projects (Supabase-style 7-day inactivity).
--   last_active_at  — most recent detected activity from the activity tracker
--   last_xact_count — running total of xact_commit + xact_rollback at the
--                     last poll, so we can detect deltas between ticks
--   pause_reason    — why the project was paused (idle_7d | manual | tier_limit)
--
-- Status moves: ACTIVE → PAUSING → backup → workload-stop → PAUSED;
-- PAUSED → RESUMING → workload-start → ACTIVE.
ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS last_active_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_xact_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS pause_reason TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_database_instances_status_tier
    ON database_instances(status, tier);
