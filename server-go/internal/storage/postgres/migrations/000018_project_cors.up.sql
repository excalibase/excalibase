-- Per-project browser-origin allowlist (EXC-23).
--
-- excalibase-graphql answers CORS per project: it reads this list through
-- GET /api/projects/{id}/info and only echoes an Origin that is on it. No row /
-- empty array = the project sends no CORS headers, so browser apps are blocked
-- until the project opts its origins in; non-browser clients are unaffected.
--
-- Entries are stored already canonical (scheme://host[:port], lower-cased,
-- default ports dropped, sorted) — the API validates before writing. The
-- single entry '*' means every origin and is only written when the caller
-- confirmed it explicitly.
CREATE TABLE IF NOT EXISTS project_cors_settings (
    project_id      TEXT PRIMARY KEY,
    allowed_origins TEXT[] NOT NULL DEFAULT '{}',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
