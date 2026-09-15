-- Idle auto-pause (EXC-280). Per-tier threshold in days; 0 = never. FREE
-- projects pause after 7 idle days (warning at day 6). Admin-editable via
-- PUT /api/admin/tiers/{tier}.
ALTER TABLE tier_configs
    ADD COLUMN IF NOT EXISTS auto_pause_after_days INTEGER NOT NULL DEFAULT 0;

UPDATE tier_configs SET auto_pause_after_days = 7 WHERE tier = 'FREE';

-- When the idle warning was issued. Cleared by fresh activity so the next
-- idle cycle warns again; a warning older than the last-seen marker is stale.
ALTER TABLE project_activity
    ADD COLUMN IF NOT EXISTS idle_warned_at TIMESTAMPTZ;
