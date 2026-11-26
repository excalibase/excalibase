-- Per-app network metering (EXC-844): one row per app each minute, rolled up
-- to one row per app per hour. Neither table references an app, project or
-- user: the evidence must outlive a deleted app, so there is no cascade.
CREATE TABLE IF NOT EXISTS app_net_samples (
    ts TIMESTAMPTZ NOT NULL,
    project_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    instances INTEGER NOT NULL,
    rx_bps DOUBLE PRECISION NOT NULL,
    tx_bps DOUBLE PRECISION NOT NULL,
    max_instance_tx_bps DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (project_id, app_id, ts)
);

-- The prune deletes by age across every app.
CREATE INDEX IF NOT EXISTS idx_app_net_samples_ts ON app_net_samples(ts);

CREATE TABLE IF NOT EXISTS app_net_hourly (
    hour TIMESTAMPTZ NOT NULL,
    project_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    samples INTEGER NOT NULL,
    avg_rx_bps DOUBLE PRECISION NOT NULL,
    avg_tx_bps DOUBLE PRECISION NOT NULL,
    max_tx_bps DOUBLE PRECISION NOT NULL,
    max_instance_tx_bps DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (project_id, app_id, hour)
);

CREATE INDEX IF NOT EXISTS idx_app_net_hourly_hour ON app_net_hourly(hour);
