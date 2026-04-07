-- Users
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);

-- Access tokens
CREATE TABLE IF NOT EXISTS access_tokens (
    token_hash TEXT PRIMARY KEY,
    token_prefix TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    last_used TIMESTAMPTZ
);

-- Database instances
CREATE TABLE IF NOT EXISTS database_instances (
    project_id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL DEFAULT '',
    owner_id TEXT NOT NULL DEFAULT '',
    database_type TEXT NOT NULL DEFAULT 'POSTGRESQL',
    tier TEXT NOT NULL DEFAULT 'FREE',
    namespace TEXT NOT NULL DEFAULT '',
    host TEXT DEFAULT '',
    read_only_host TEXT DEFAULT '',
    port INTEGER,
    database_name TEXT DEFAULT '',
    username TEXT DEFAULT '',
    password TEXT DEFAULT '',
    deletion_protection BOOLEAN DEFAULT FALSE,
    pooler_enabled BOOLEAN DEFAULT FALSE,
    pooler_host TEXT DEFAULT '',
    ssl_mode TEXT DEFAULT '',
    webhook_url TEXT DEFAULT '',
    postgres_version TEXT DEFAULT '',
    tags TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    current_stage TEXT DEFAULT '',
    failure_reason TEXT DEFAULT '',
    network_policy_enabled BOOLEAN DEFAULT FALSE,
    maintenance_window TEXT DEFAULT '',
    maintenance_window_duration_min INTEGER,
    auto_minor_version_upgrade BOOLEAN DEFAULT FALSE,
    backup_enabled BOOLEAN DEFAULT FALSE,
    backup_schedule TEXT DEFAULT '',
    backup_retention_days INTEGER,
    metrics_endpoint TEXT DEFAULT '',
    grafana_dashboard_url TEXT DEFAULT '',
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    last_health_check TIMESTAMPTZ
);

-- Database metrics
CREATE TABLE IF NOT EXISTS database_metrics (
    id BIGSERIAL PRIMARY KEY,
    project_id TEXT NOT NULL,
    timestamp TIMESTAMPTZ,
    status TEXT DEFAULT '',
    health_status TEXT DEFAULT '',
    metrics_available BOOLEAN DEFAULT FALSE,
    unavailable_reason TEXT,
    cpu_usage_percent DOUBLE PRECISION DEFAULT 0,
    memory_usage_percent DOUBLE PRECISION DEFAULT 0,
    disk_usage_percent DOUBLE PRECISION DEFAULT 0,
    cpu_usage_cores DOUBLE PRECISION DEFAULT 0,
    memory_usage_mb DOUBLE PRECISION DEFAULT 0,
    disk_usage_gb DOUBLE PRECISION DEFAULT 0,
    active_connections INTEGER,
    idle_connections INTEGER,
    max_connections INTEGER,
    queries_per_second DOUBLE PRECISION DEFAULT 0,
    avg_query_latency_ms DOUBLE PRECISION DEFAULT 0,
    slow_query_count INTEGER DEFAULT 0,
    database_size_gb DOUBLE PRECISION DEFAULT 0,
    last_backup_time TIMESTAMPTZ,
    next_backup_time TIMESTAMPTZ,
    cpu_limit_cores DOUBLE PRECISION DEFAULT 0,
    memory_limit_mb DOUBLE PRECISION DEFAULT 0,
    storage_limit TEXT DEFAULT '',
    instance_count INTEGER DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_metrics_project_ts ON database_metrics(project_id, timestamp);

-- Alerts
CREATE TABLE IF NOT EXISTS alerts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL DEFAULT '',
    severity TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL DEFAULT '',
    metric TEXT DEFAULT '',
    value DOUBLE PRECISION DEFAULT 0,
    threshold DOUBLE PRECISION DEFAULT 0,
    timestamp TIMESTAMPTZ,
    resolved BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS idx_alerts_project_resolved ON alerts(project_id, resolved);

-- Migration records
CREATE TABLE IF NOT EXISTS migration_records (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    description TEXT DEFAULT '',
    sql_text TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    applied_at TIMESTAMPTZ,
    execution_time_ms INTEGER DEFAULT 0,
    error_message TEXT DEFAULT '',
    checksum TEXT DEFAULT '',
    output TEXT DEFAULT ''
);

-- Backup records
CREATE TABLE IF NOT EXISTS backup_records (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL DEFAULT '',
    timestamp TIMESTAMPTZ,
    type TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT ''
);

-- Parameter groups
CREATE TABLE IF NOT EXISTS parameter_groups (
    name TEXT PRIMARY KEY,
    description TEXT DEFAULT '',
    parameters_json JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Audit log
CREATE TABLE IF NOT EXISTS audit_log (
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT DEFAULT '',
    action TEXT NOT NULL DEFAULT '',
    resource TEXT DEFAULT '',
    resource_id TEXT DEFAULT '',
    details TEXT DEFAULT '',
    ip_address TEXT DEFAULT '',
    timestamp TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp);

-- Organizations
CREATE TABLE IF NOT EXISTS orgs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT UNIQUE NOT NULL,
    tier TEXT NOT NULL DEFAULT 'free',
    owner_id TEXT NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Org members
CREATE TABLE IF NOT EXISTS org_members (
    org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'developer',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (org_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_org_members_user ON org_members(user_id);

-- Project members
CREATE TABLE IF NOT EXISTS project_members (
    project_id TEXT NOT NULL,
    org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'viewer',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_project_members_user ON project_members(user_id);
CREATE INDEX IF NOT EXISTS idx_project_members_org ON project_members(org_id);

-- Pending invites
CREATE TABLE IF NOT EXISTS pending_invites (
    id BIGSERIAL PRIMARY KEY,
    org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'developer',
    invited_by TEXT NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(org_id, email)
);

CREATE INDEX IF NOT EXISTS idx_pending_invites_email ON pending_invites(email);
