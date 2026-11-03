-- Running request totals per project and family for the Prometheus metrics
-- endpoint (EXC-657): request_metrics holds per-interval rows that are
-- pruned, so only this monotonic sum exports as counters. Written only while
-- the metricsendpoint feature is on.
CREATE TABLE IF NOT EXISTS request_metric_totals (
    project_id TEXT NOT NULL,
    family TEXT NOT NULL,
    requests BIGINT NOT NULL DEFAULT 0,
    client_errors BIGINT NOT NULL DEFAULT 0,
    server_errors BIGINT NOT NULL DEFAULT 0,
    latency_buckets BIGINT[] NOT NULL DEFAULT '{}',
    connections BIGINT NOT NULL DEFAULT 0,
    connections_opened BIGINT NOT NULL DEFAULT 0,
    messages BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, family)
);

CREATE INDEX IF NOT EXISTS idx_request_metric_totals_updated ON request_metric_totals(updated_at);
