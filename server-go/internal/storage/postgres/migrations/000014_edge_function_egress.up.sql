-- Per-project outbound allowlist for edge functions (EXC-348).
--
-- The Deno runtime grants a function worker `net` access only to the hosts in
-- ALLOWED_HOSTS; the control plane used to render that as "" for every project,
-- so no function could ever call out. This row is the project's setting:
-- rendered into the runtime (pod env + NetworkPolicy on k8s, per-deploy on the
-- shared docker runtime) merged with the operator default
-- EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS. No row / empty array = no egress.
--
-- Entries are stored already canonical (host, host:port, *.suffix[:port] or a
-- public IP literal, lower-cased, sorted) — the API validates before writing.
CREATE TABLE IF NOT EXISTS edge_function_settings (
    project_id           TEXT PRIMARY KEY,
    egress_allowed_hosts TEXT[] NOT NULL DEFAULT '{}',
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
