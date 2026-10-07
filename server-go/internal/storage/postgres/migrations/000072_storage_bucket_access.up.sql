-- What app users may do in a bucket, by role (EXC-560): {"role": {"read":
-- "own"|"all", "write": ..., "delete": ...}}. Empty grants app users nothing.
ALTER TABLE storage_buckets
    ADD COLUMN IF NOT EXISTS access JSONB NOT NULL DEFAULT '{}'::jsonb;
