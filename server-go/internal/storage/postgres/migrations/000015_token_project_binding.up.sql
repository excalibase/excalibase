-- Optional project binding for personal access tokens (EXC-323).
--
-- A token created with project_id set may only act on that project: every
-- project-scoped route whose path names a different project answers 404.
-- NULL keeps the pre-existing behaviour (the token reaches every project its
-- user is a member of).
ALTER TABLE access_tokens ADD COLUMN IF NOT EXISTS project_id TEXT;
