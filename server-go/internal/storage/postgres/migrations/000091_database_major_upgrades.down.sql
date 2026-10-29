-- Refused while any project's data was moved by an upgrade: its backups now
-- live in an archive only these rows name.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM database_major_upgrades WHERE converted) THEN
        RAISE EXCEPTION 'a project was moved to a newer major; its backup archive is named by database_major_upgrades';
    END IF;
END $$;

ALTER TABLE backup_records DROP COLUMN IF EXISTS postgres_version;
DROP TABLE IF EXISTS database_major_upgrades;
