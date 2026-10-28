-- Single-host monitoring (EXC-670): one host row and one row per container
-- each minute, written by the metrics loop on a Docker or Podman host. A NULL
-- figure was not measured.
CREATE TABLE IF NOT EXISTS host_metrics (
    id BIGSERIAL PRIMARY KEY,
    timestamp TIMESTAMPTZ NOT NULL,
    cpu_percent DOUBLE PRECISION,
    cpu_count INTEGER,
    load1 DOUBLE PRECISION,
    load5 DOUBLE PRECISION,
    load15 DOUBLE PRECISION,
    memory_total_bytes BIGINT,
    memory_used_bytes BIGINT,
    swap_total_bytes BIGINT,
    swap_used_bytes BIGINT,
    disk_total_bytes BIGINT,
    disk_used_bytes BIGINT,
    disk_read_bytes_per_sec DOUBLE PRECISION,
    disk_write_bytes_per_sec DOUBLE PRECISION,
    network_rx_bytes_per_sec DOUBLE PRECISION,
    network_tx_bytes_per_sec DOUBLE PRECISION,
    network_source TEXT,
    temperature_max_celsius DOUBLE PRECISION,
    temperatures JSONB NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_host_metrics_ts ON host_metrics(timestamp);

CREATE TABLE IF NOT EXISTS container_metrics (
    id BIGSERIAL PRIMARY KEY,
    timestamp TIMESTAMPTZ NOT NULL,
    container_name TEXT NOT NULL,
    project_id TEXT NOT NULL DEFAULT '',
    component TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    health TEXT NOT NULL DEFAULT '',
    cpu_cores DOUBLE PRECISION,
    cpu_percent DOUBLE PRECISION,
    memory_bytes BIGINT,
    memory_limit_bytes BIGINT,
    network_rx_bytes_per_sec DOUBLE PRECISION,
    network_tx_bytes_per_sec DOUBLE PRECISION
);

CREATE INDEX IF NOT EXISTS idx_container_metrics_ts ON container_metrics(timestamp);
CREATE INDEX IF NOT EXISTS idx_container_metrics_name_ts ON container_metrics(container_name, timestamp);
CREATE INDEX IF NOT EXISTS idx_container_metrics_project_ts ON container_metrics(project_id, timestamp);

-- One row: the host's alert rules. A NULL threshold is a rule that is off;
-- the webhook URL itself is in the vault, only its scheme and host here.
CREATE TABLE IF NOT EXISTS monitoring_alert_settings (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    cpu_percent DOUBLE PRECISION,
    memory_percent DOUBLE PRECISION,
    disk_percent DOUBLE PRECISION,
    temperature_celsius DOUBLE PRECISION,
    load5 DOUBLE PRECISION,
    network_mbps DOUBLE PRECISION,
    window_minutes INTEGER NOT NULL DEFAULT 5,
    down_minutes INTEGER NOT NULL DEFAULT 2,
    emails TEXT[] NOT NULL DEFAULT '{}',
    webhook_display TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
