ALTER TABLE database_instances
    DROP COLUMN IF EXISTS parameters,
    DROP COLUMN IF EXISTS storage_class;
