-- A deploy freezes the config it rolled out, so a redeploy can roll the same
-- config out again without reading the app record.
ALTER TABLE app_deploys ADD COLUMN config JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE app_deploys ALTER COLUMN config DROP DEFAULT;
ALTER TABLE app_deploys ADD COLUMN redeploy_of TEXT REFERENCES app_deploys (id);
