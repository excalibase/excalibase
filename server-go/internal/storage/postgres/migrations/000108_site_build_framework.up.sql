-- A server-rendered site is built with a framework preset (EXC-661); empty builds an output folder as static files.
ALTER TABLE site_git_connections ADD COLUMN IF NOT EXISTS build_framework TEXT NOT NULL DEFAULT '';
