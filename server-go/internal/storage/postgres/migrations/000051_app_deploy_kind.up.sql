-- A resize entry records a resume that brought the app back at its organisation's current plan size.
ALTER TABLE app_deploys ADD COLUMN kind TEXT NOT NULL DEFAULT 'deploy'
    CONSTRAINT app_deploys_kind_known CHECK (kind IN ('deploy', 'resize'));
