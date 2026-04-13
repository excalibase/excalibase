-- SQLite doesn't support DROP COLUMN in older versions; recreate or leave columns.
-- Since dropping in SQLite is messy, the down migration is a no-op.
-- If rollback is required, restore from backup.
SELECT 1;
