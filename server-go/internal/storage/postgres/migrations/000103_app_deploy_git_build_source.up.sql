-- A deploy of an image the platform built from a pushed commit (EXC-547).
ALTER TABLE app_deploys DROP CONSTRAINT app_deploys_source_known;
ALTER TABLE app_deploys ADD CONSTRAINT app_deploys_source_known
    CHECK (source IN ('studio', 'api', 'image-watcher', 'git-build'));
