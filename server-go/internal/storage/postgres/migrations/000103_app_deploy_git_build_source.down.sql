-- Keeps the deploys; only their origin is forgotten.
UPDATE app_deploys SET source = NULL WHERE source = 'git-build';
ALTER TABLE app_deploys DROP CONSTRAINT app_deploys_source_known;
ALTER TABLE app_deploys ADD CONSTRAINT app_deploys_source_known
    CHECK (source IN ('studio', 'api', 'image-watcher'));
