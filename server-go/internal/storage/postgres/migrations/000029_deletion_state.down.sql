ALTER TABLE database_instances
    DROP COLUMN IF EXISTS deletion_step,
    DROP COLUMN IF EXISTS deletion_error;
