-- The actor a RUNNING major upgrade belongs to (EXC-668): its driver, or
-- the recovery that claimed it once the driver's heartbeat went stale. Every
-- write to the run names it, so two actors never act on one run.
ALTER TABLE database_major_upgrades ADD COLUMN IF NOT EXISTS owner TEXT NOT NULL DEFAULT '';
