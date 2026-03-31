CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'viewer',
    active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS access_tokens (
    token_hash TEXT PRIMARY KEY,
    token_prefix TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT,
    created_at TEXT,
    expires_at TEXT,
    last_used TEXT
);

CREATE TABLE IF NOT EXISTS database_instances (
    project_id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL DEFAULT '',
    owner_id TEXT NOT NULL DEFAULT '',
    database_type TEXT NOT NULL DEFAULT 'POSTGRESQL',
    tier TEXT NOT NULL DEFAULT 'FREE',
    namespace TEXT NOT NULL DEFAULT '',
    host TEXT,
    read_only_host TEXT,
    port INTEGER,
    database_name TEXT,
    username TEXT,
    password TEXT,
    deletion_protection INTEGER DEFAULT 0,
    pooler_enabled INTEGER DEFAULT 0,
    pooler_host TEXT,
    ssl_mode TEXT,
    webhook_url TEXT,
    postgres_version TEXT,
    tags TEXT,
    status TEXT NOT NULL DEFAULT 'PROVISIONING',
    current_stage TEXT,
    failure_reason TEXT,
    network_policy_enabled INTEGER DEFAULT 0,
    maintenance_window TEXT,
    maintenance_window_duration_min INTEGER,
    auto_minor_version_upgrade INTEGER DEFAULT 0,
    backup_enabled INTEGER DEFAULT 0,
    backup_schedule TEXT,
    backup_retention_days INTEGER,
    metrics_endpoint TEXT,
    grafana_dashboard_url TEXT,
    created_at TEXT,
    updated_at TEXT,
    last_health_check TEXT
);

CREATE TABLE IF NOT EXISTS database_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    status TEXT,
    health_status TEXT,
    metrics_available INTEGER NOT NULL DEFAULT 1,
    unavailable_reason TEXT,
    cpu_usage_percent REAL,
    memory_usage_percent REAL,
    disk_usage_percent REAL,
    cpu_usage_cores REAL,
    memory_usage_mb INTEGER,
    disk_usage_gb INTEGER,
    active_connections INTEGER,
    idle_connections INTEGER,
    max_connections INTEGER,
    queries_per_second REAL,
    avg_query_latency_ms REAL,
    slow_query_count INTEGER,
    database_size_gb INTEGER,
    last_backup_time TEXT,
    next_backup_time TEXT,
    cpu_limit_cores REAL,
    memory_limit_mb INTEGER,
    storage_limit TEXT,
    instance_count INTEGER,
    pods_json TEXT
);

CREATE INDEX IF NOT EXISTS idx_metrics_project_ts ON database_metrics(project_id, timestamp DESC);

CREATE TABLE IF NOT EXISTS alerts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    severity TEXT NOT NULL,
    message TEXT NOT NULL,
    metric TEXT,
    value REAL,
    threshold REAL,
    timestamp TEXT NOT NULL,
    resolved INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_alerts_project ON alerts(project_id);
CREATE INDEX IF NOT EXISTS idx_alerts_resolved ON alerts(resolved);

CREATE TABLE IF NOT EXISTS migration_records (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    version TEXT,
    name TEXT,
    description TEXT,
    sql_text TEXT NOT NULL,
    status TEXT NOT NULL,
    applied_at TEXT,
    execution_time_ms INTEGER,
    error_message TEXT,
    checksum TEXT,
    output TEXT
);

CREATE TABLE IF NOT EXISTS backup_records (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    type TEXT NOT NULL DEFAULT 'MANUAL',
    status TEXT NOT NULL DEFAULT 'IN_PROGRESS'
);

CREATE TABLE IF NOT EXISTS parameter_groups (
    name TEXT PRIMARY KEY,
    description TEXT,
    parameters_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT,
    action TEXT NOT NULL,
    resource TEXT NOT NULL,
    resource_id TEXT,
    details TEXT,
    ip_address TEXT,
    timestamp TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_log(timestamp DESC);
