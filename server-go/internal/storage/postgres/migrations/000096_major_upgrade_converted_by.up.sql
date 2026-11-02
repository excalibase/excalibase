-- Which actor's mark recorded a run's conversion (EXC-668): a driver and
-- recovery that both convert the same run each finish only their own mark.
ALTER TABLE database_major_upgrades ADD COLUMN IF NOT EXISTS converted_by TEXT NOT NULL DEFAULT '';
