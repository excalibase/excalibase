-- Sign-ins live ten minutes; an app's in flight are dropped.
DELETE FROM site_git_states WHERE app_id IS NOT NULL;
ALTER TABLE site_git_states DROP CONSTRAINT IF EXISTS site_git_states_one_target;
ALTER TABLE site_git_states ALTER COLUMN site_id SET NOT NULL;
ALTER TABLE site_git_states DROP COLUMN IF EXISTS app_id;
