-- Single-host stored logs (EXC-678): each project's log text stored per UTC
-- day, so a restart resumes the day's budget instead of granting a new one.
CREATE TABLE IF NOT EXISTS host_log_budget (
    project_id TEXT NOT NULL,
    day DATE NOT NULL,
    bytes BIGINT NOT NULL DEFAULT 0,
    noticed BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (project_id, day)
);
