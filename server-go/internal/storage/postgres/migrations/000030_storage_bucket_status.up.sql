-- Bucket lifecycle state. A bucket is marked 'deleting' before any of its
-- objects are removed from the blob store, so a cascade interrupted halfway
-- is visible (its catalogue rows may outlive their bytes) instead of looking
-- like a healthy bucket, and a repeated DELETE finishes the job.
ALTER TABLE storage_buckets
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
