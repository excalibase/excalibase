-- A git sign-in started from an app returns the browser to that app (EXC-547 step 5).
ALTER TABLE site_git_states ADD COLUMN app_id TEXT;
ALTER TABLE site_git_states ALTER COLUMN site_id DROP NOT NULL;
ALTER TABLE site_git_states ADD CONSTRAINT site_git_states_one_target CHECK ((site_id IS NULL) <> (app_id IS NULL));
