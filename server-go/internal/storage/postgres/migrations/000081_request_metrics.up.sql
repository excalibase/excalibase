-- Per-project request charts (EXC-656): one row per project, family and
-- sample interval, written by the metrics loop and kept as long as
-- database_metrics. latency_buckets are per-bucket counts so any range
-- merges into exact percentiles.
CREATE TABLE IF NOT EXISTS request_metrics (
    id BIGSERIAL PRIMARY KEY,
    project_id TEXT NOT NULL,
    timestamp TIMESTAMPTZ NOT NULL,
    family TEXT NOT NULL,
    requests BIGINT NOT NULL DEFAULT 0,
    client_errors BIGINT NOT NULL DEFAULT 0,
    server_errors BIGINT NOT NULL DEFAULT 0,
    latency_buckets BIGINT[] NOT NULL DEFAULT '{}',
    connections BIGINT NOT NULL DEFAULT 0,
    connections_opened BIGINT NOT NULL DEFAULT 0,
    messages BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_request_metrics_project_ts ON request_metrics(project_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_request_metrics_ts ON request_metrics(timestamp);
