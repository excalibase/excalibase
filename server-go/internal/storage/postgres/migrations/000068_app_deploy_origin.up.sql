-- Where a deploy came from (EXC-543): Studio, the API or the image watcher,
-- the commit the caller built the image from, and the reference it named
-- with the digest that reference resolved to.
ALTER TABLE app_deploys ADD COLUMN source TEXT
    CONSTRAINT app_deploys_source_known CHECK (source IN ('studio', 'api', 'image-watcher'));
ALTER TABLE app_deploys ADD COLUMN commit_sha TEXT;
ALTER TABLE app_deploys ADD COLUMN image_ref TEXT;
ALTER TABLE app_deploys ADD COLUMN digest TEXT;
