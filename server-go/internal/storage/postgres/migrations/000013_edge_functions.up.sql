-- Edge-function storage moved off the filesystem (EXC-333).
--
-- Function source previously lived at {STORAGE_PATH}/functions/{project}/{id}.json,
-- and STORAGE_PATH is an emptyDir in the AIO chart — so every provisioning pod
-- restart, reschedule, or scale-to-zero silently destroyed tenant-authored code.
--
-- `doc` holds the whole Function record as JSON, exactly as the filesystem store
-- wrote it. Document-shaped on purpose: the struct carries a dozen optional
-- fields (files, exportMetadata, schemaJson, kind, httpRoutes, cronJobs, …) and a
-- column-per-field schema would silently drop any field added later. The columns
-- beside it are extracted only for keys/ordering.

CREATE TABLE IF NOT EXISTS edge_functions (
    project_id TEXT NOT NULL,
    id         TEXT NOT NULL,               -- slug, unique per project
    version    INTEGER NOT NULL DEFAULT 1,
    active     BOOLEAN NOT NULL DEFAULT TRUE,
    doc        JSONB NOT NULL,              -- full Function record
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, id)
);

CREATE INDEX IF NOT EXISTS idx_edge_functions_project ON edge_functions(project_id);

-- Project-level shared modules (EXC-334), Supabase's `_shared/` convention.
-- Seeded into the bundler's virtual-file map before a function's own files so
-- `../_shared/cors.ts` resolves, without duplicating the source into every
-- function. Path is stored as authored (e.g. "_shared/cors.ts").
CREATE TABLE IF NOT EXISTS edge_shared_files (
    project_id TEXT NOT NULL,
    path       TEXT NOT NULL,
    content    TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, path)
);
