package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite single writer
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

const schema = `
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

CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	description TEXT NOT NULL,
	applied_at TEXT NOT NULL
);

INSERT OR IGNORE INTO schema_migrations (version, description, applied_at)
VALUES (1, 'Initial schema', datetime('now'));
`

// --- InstanceStore ---

func (s *Store) Save(inst *domain.DatabaseInstance) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO database_instances (
			project_id, org_id, database_type, tier, namespace,
			host, read_only_host, port, database_name, username, password,
			deletion_protection, pooler_enabled, pooler_host, ssl_mode,
			webhook_url, postgres_version, tags,
			status, current_stage, failure_reason,
			network_policy_enabled,
			maintenance_window, maintenance_window_duration_min, auto_minor_version_upgrade,
			backup_enabled, backup_schedule, backup_retention_days,
			metrics_endpoint, grafana_dashboard_url,
			created_at, updated_at, last_health_check
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		inst.ProjectID, inst.OrgID, inst.DBType, inst.Tier, inst.Namespace,
		inst.Host, inst.ReadOnlyHost, inst.Port, inst.DatabaseName, inst.Username, inst.Password,
		boolToInt(inst.DeletionProtection), boolToInt(inst.PoolerEnabled), inst.PoolerHost, inst.SSLMode,
		inst.WebhookURL, inst.PostgresVersion, inst.Tags,
		inst.Status, inst.CurrentStage, inst.FailureReason,
		boolToInt(inst.NetworkPolicyEnabled),
		inst.MaintenanceWindow, inst.MaintenanceWindowDurationMinutes, boolToInt(inst.AutoMinorVersionUpgrade),
		boolToInt(inst.BackupEnabled), inst.BackupSchedule, inst.BackupRetentionDays,
		inst.MetricsEndpoint, inst.GrafanaDashboardURL,
		flexTimeStr(inst.CreatedAt), flexTimeStr(inst.UpdatedAt), flexTimeStr(inst.LastHealthCheck),
	)
	return err
}

func (s *Store) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	row := s.db.QueryRow(`SELECT
		project_id, org_id, database_type, tier, namespace,
		host, read_only_host, port, database_name, username, password,
		deletion_protection, pooler_enabled, pooler_host, ssl_mode,
		webhook_url, postgres_version, tags,
		status, current_stage, failure_reason,
		network_policy_enabled,
		maintenance_window, maintenance_window_duration_min, auto_minor_version_upgrade,
		backup_enabled, backup_schedule, backup_retention_days,
		metrics_endpoint, grafana_dashboard_url,
		created_at, updated_at, last_health_check
	FROM database_instances WHERE project_id = ?`, projectID)

	inst, err := scanInstance(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inst, err
}

func (s *Store) FindAll() ([]*domain.DatabaseInstance, error) {
	rows, err := s.db.Query(`SELECT
		project_id, org_id, database_type, tier, namespace,
		host, read_only_host, port, database_name, username, password,
		deletion_protection, pooler_enabled, pooler_host, ssl_mode,
		webhook_url, postgres_version, tags,
		status, current_stage, failure_reason,
		network_policy_enabled,
		maintenance_window, maintenance_window_duration_min, auto_minor_version_upgrade,
		backup_enabled, backup_schedule, backup_retention_days,
		metrics_endpoint, grafana_dashboard_url,
		created_at, updated_at, last_health_check
	FROM database_instances`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*domain.DatabaseInstance
	for rows.Next() {
		inst, err := scanInstanceRows(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, inst)
	}
	if result == nil {
		result = make([]*domain.DatabaseInstance, 0)
	}
	return result, nil
}

func (s *Store) Delete(projectID string) error {
	_, err := s.db.Exec(`DELETE FROM database_instances WHERE project_id = ?`, projectID)
	return err
}

// --- UserStore ---

func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (id, username, email, password_hash, role, active, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, u.ID, u.Username, u.Email, u.PasswordHash, u.Role, u.Active, now, now)
	return err
}

func (s *Store) FindUserByID(ctx context.Context, id string) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) FindUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) FindAllUsers(ctx context.Context) ([]*domain.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*domain.User
	for rows.Next() {
		var u domain.User
		var createdAt, updatedAt sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		result = append(result, &u)
	}
	if result == nil {
		result = make([]*domain.User, 0)
	}
	return result, nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// --- MetricsStore ---

func (s *Store) AppendMetrics(ctx context.Context, m *domain.DatabaseMetrics) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO database_metrics (
		project_id, timestamp, status, health_status, metrics_available, unavailable_reason,
		cpu_usage_percent, memory_usage_percent, disk_usage_percent,
		cpu_usage_cores, memory_usage_mb, disk_usage_gb,
		active_connections, idle_connections, max_connections,
		queries_per_second, avg_query_latency_ms, slow_query_count, database_size_gb,
		last_backup_time, next_backup_time,
		cpu_limit_cores, memory_limit_mb, storage_limit, instance_count
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ProjectID, flexTimeStr(m.Timestamp), m.Status, m.HealthStatus, m.MetricsAvailable, ptrStr(m.UnavailableReason),
		m.CPUUsagePercent, m.MemoryUsagePercent, m.DiskUsagePercent,
		m.CPUUsageCores, m.MemoryUsageMB, m.DiskUsageGB,
		m.ActiveConnections, m.IdleConnections, m.MaxConnections,
		m.QueriesPerSecond, m.AvgQueryLatencyMs, m.SlowQueryCount, m.DatabaseSizeGB,
		flexTimeStr(m.LastBackupTime), flexTimeStr(m.NextBackupTime),
		m.CPULimitCores, m.MemoryLimitMB, m.StorageLimit, m.InstanceCount,
	)

	// Prune old entries (keep latest 100 per project)
	if err == nil {
		s.db.ExecContext(ctx, `DELETE FROM database_metrics WHERE project_id = ? AND id NOT IN (
			SELECT id FROM database_metrics WHERE project_id = ? ORDER BY timestamp DESC LIMIT 100
		)`, m.ProjectID, m.ProjectID)
	}

	return err
}

func (s *Store) GetMetricsHistory(ctx context.Context, projectID string, limit int) ([]domain.DatabaseMetrics, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		project_id, timestamp, status, health_status, metrics_available, unavailable_reason,
		cpu_usage_percent, memory_usage_percent, disk_usage_percent,
		cpu_usage_cores, memory_usage_mb, disk_usage_gb,
		active_connections, idle_connections, max_connections,
		queries_per_second, avg_query_latency_ms, slow_query_count, database_size_gb,
		last_backup_time, next_backup_time,
		cpu_limit_cores, memory_limit_mb, storage_limit, instance_count
	FROM database_metrics WHERE project_id = ? ORDER BY timestamp DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.DatabaseMetrics, 0)
	for rows.Next() {
		var m domain.DatabaseMetrics
		var ts, lastBackup, nextBackup, unavailableReason sql.NullString
		err := rows.Scan(
			&m.ProjectID, &ts, &m.Status, &m.HealthStatus, &m.MetricsAvailable, &unavailableReason,
			&m.CPUUsagePercent, &m.MemoryUsagePercent, &m.DiskUsagePercent,
			&m.CPUUsageCores, &m.MemoryUsageMB, &m.DiskUsageGB,
			&m.ActiveConnections, &m.IdleConnections, &m.MaxConnections,
			&m.QueriesPerSecond, &m.AvgQueryLatencyMs, &m.SlowQueryCount, &m.DatabaseSizeGB,
			&lastBackup, &nextBackup,
			&m.CPULimitCores, &m.MemoryLimitMB, &m.StorageLimit, &m.InstanceCount,
		)
		if err != nil {
			return nil, err
		}
		if ts.Valid {
			m.Timestamp = parseFlexTime(ts.String)
		}
		if unavailableReason.Valid {
			m.UnavailableReason = &unavailableReason.String
		}
		result = append(result, m)
	}
	return result, nil
}

// --- AlertStore ---

func (s *Store) SaveAlert(ctx context.Context, a *domain.Alert) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO alerts (id, project_id, severity, message, metric, value, threshold, timestamp, resolved)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ProjectID, a.Severity, a.Message, a.Metric, a.Value, a.Threshold,
		flexTimeStr(a.Timestamp), a.Resolved)
	return err
}

func (s *Store) GetActiveAlerts(ctx context.Context) ([]domain.Alert, error) {
	return s.queryAlerts(ctx, `SELECT id, project_id, severity, message, metric, value, threshold, timestamp, resolved FROM alerts WHERE resolved = 0 ORDER BY timestamp DESC`)
}

func (s *Store) GetActiveAlertsForProject(ctx context.Context, projectID string) ([]domain.Alert, error) {
	return s.queryAlerts(ctx, `SELECT id, project_id, severity, message, metric, value, threshold, timestamp, resolved FROM alerts WHERE project_id = ? AND resolved = 0 ORDER BY timestamp DESC`, projectID)
}

func (s *Store) GetAlertHistory(ctx context.Context, limit int) ([]domain.Alert, error) {
	return s.queryAlerts(ctx, `SELECT id, project_id, severity, message, metric, value, threshold, timestamp, resolved FROM alerts ORDER BY timestamp DESC LIMIT ?`, limit)
}

func (s *Store) queryAlerts(ctx context.Context, query string, args ...interface{}) ([]domain.Alert, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.Alert, 0)
	for rows.Next() {
		var a domain.Alert
		var ts sql.NullString
		err := rows.Scan(&a.ID, &a.ProjectID, &a.Severity, &a.Message, &a.Metric, &a.Value, &a.Threshold, &ts, &a.Resolved)
		if err != nil {
			return nil, err
		}
		if ts.Valid {
			a.Timestamp = parseFlexTime(ts.String)
		}
		result = append(result, a)
	}
	return result, nil
}

// --- AuditLogStore ---

func (s *Store) LogAudit(ctx context.Context, e *domain.AuditEntry) error {
	ts := time.Now().Format(time.RFC3339)
	if e.Timestamp != nil {
		ts = e.Timestamp.Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log (user_id, action, resource, resource_id, details, ip_address, timestamp)
		VALUES (?,?,?,?,?,?,?)`, e.UserID, e.Action, e.Resource, e.ResourceID, e.Details, e.IPAddress, ts)
	return err
}

func (s *Store) QueryAudit(ctx context.Context, limit int) ([]domain.AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, action, resource, resource_id, details, ip_address, timestamp FROM audit_log ORDER BY timestamp DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.AuditEntry, 0)
	for rows.Next() {
		var e domain.AuditEntry
		var ts sql.NullString
		var userID, resourceID, details, ip sql.NullString
		err := rows.Scan(&e.ID, &userID, &e.Action, &e.Resource, &resourceID, &details, &ip, &ts)
		if err != nil {
			return nil, err
		}
		if userID.Valid {
			e.UserID = userID.String
		}
		if resourceID.Valid {
			e.ResourceID = resourceID.String
		}
		if details.Valid {
			e.Details = details.String
		}
		if ip.Valid {
			e.IPAddress = ip.String
		}
		if ts.Valid {
			t, _ := time.Parse(time.RFC3339, ts.String)
			e.Timestamp = &t
		}
		result = append(result, e)
	}
	return result, nil
}

// --- TokenStore (PAT pattern) ---

func (s *Store) CreateToken(ctx context.Context, t *domain.AccessToken) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_tokens (token_hash, token_prefix, user_id, name, created_at, expires_at)
		VALUES (?,?,?,?,?,?)`, t.TokenHash, t.TokenPrefix, t.UserID, t.Name, now, nil)
	return err
}

func (s *Store) FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error) {
	row := s.db.QueryRowContext(ctx, `SELECT token_hash, token_prefix, user_id, name, created_at, expires_at, last_used FROM access_tokens WHERE token_hash = ?`, hash)
	var t domain.AccessToken
	var createdAt, expiresAt, lastUsed sql.NullString
	err := row.Scan(&t.TokenHash, &t.TokenPrefix, &t.UserID, &t.Name, &createdAt, &expiresAt, &lastUsed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) ListTokensByUser(ctx context.Context, userID string) ([]*domain.AccessToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash, token_prefix, user_id, name, created_at, expires_at, last_used FROM access_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*domain.AccessToken, 0)
	for rows.Next() {
		var t domain.AccessToken
		var createdAt, expiresAt, lastUsed sql.NullString
		if err := rows.Scan(&t.TokenHash, &t.TokenPrefix, &t.UserID, &t.Name, &createdAt, &expiresAt, &lastUsed); err != nil {
			return nil, err
		}
		result = append(result, &t)
	}
	return result, nil
}

func (s *Store) DeleteToken(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM access_tokens WHERE token_hash = ?`, tokenHash)
	return err
}

// --- Helpers ---

func scanUser(row *sql.Row) (*domain.User, error) {
	var u domain.User
	var createdAt, updatedAt sql.NullString
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanInstance(row *sql.Row) (*domain.DatabaseInstance, error) {
	var inst domain.DatabaseInstance
	var port sql.NullInt64
	var delProt, poolerEn, netPol, autoUpgrade, backupEn sql.NullInt64
	var maintDur, backupRet sql.NullInt64
	var createdAt, updatedAt, lastHealth sql.NullString

	err := row.Scan(
		&inst.ProjectID, &inst.OrgID, &inst.DBType, &inst.Tier, &inst.Namespace,
		&inst.Host, &inst.ReadOnlyHost, &port, &inst.DatabaseName, &inst.Username, &inst.Password,
		&delProt, &poolerEn, &inst.PoolerHost, &inst.SSLMode,
		&inst.WebhookURL, &inst.PostgresVersion, &inst.Tags,
		&inst.Status, &inst.CurrentStage, &inst.FailureReason,
		&netPol,
		&inst.MaintenanceWindow, &maintDur, &autoUpgrade,
		&backupEn, &inst.BackupSchedule, &backupRet,
		&inst.MetricsEndpoint, &inst.GrafanaDashboardURL,
		&createdAt, &updatedAt, &lastHealth,
	)
	if err != nil {
		return nil, err
	}

	if port.Valid {
		p := int(port.Int64)
		inst.Port = &p
	}
	inst.DeletionProtection = intToBoolPtr(delProt)
	inst.PoolerEnabled = intToBoolPtr(poolerEn)
	inst.NetworkPolicyEnabled = intToBoolPtr(netPol)
	inst.AutoMinorVersionUpgrade = intToBoolPtr(autoUpgrade)
	inst.BackupEnabled = intToBoolPtr(backupEn)
	if maintDur.Valid {
		d := int(maintDur.Int64)
		inst.MaintenanceWindowDurationMinutes = &d
	}
	if backupRet.Valid {
		d := int(backupRet.Int64)
		inst.BackupRetentionDays = &d
	}
	if createdAt.Valid {
		inst.CreatedAt = parseFlexTime(createdAt.String)
	}
	if updatedAt.Valid {
		inst.UpdatedAt = parseFlexTime(updatedAt.String)
	}
	if lastHealth.Valid {
		inst.LastHealthCheck = parseFlexTime(lastHealth.String)
	}

	return &inst, nil
}

func scanInstanceRows(rows *sql.Rows) (*domain.DatabaseInstance, error) {
	var inst domain.DatabaseInstance
	var port sql.NullInt64
	var delProt, poolerEn, netPol, autoUpgrade, backupEn sql.NullInt64
	var maintDur, backupRet sql.NullInt64
	var createdAt, updatedAt, lastHealth sql.NullString

	err := rows.Scan(
		&inst.ProjectID, &inst.OrgID, &inst.DBType, &inst.Tier, &inst.Namespace,
		&inst.Host, &inst.ReadOnlyHost, &port, &inst.DatabaseName, &inst.Username, &inst.Password,
		&delProt, &poolerEn, &inst.PoolerHost, &inst.SSLMode,
		&inst.WebhookURL, &inst.PostgresVersion, &inst.Tags,
		&inst.Status, &inst.CurrentStage, &inst.FailureReason,
		&netPol,
		&inst.MaintenanceWindow, &maintDur, &autoUpgrade,
		&backupEn, &inst.BackupSchedule, &backupRet,
		&inst.MetricsEndpoint, &inst.GrafanaDashboardURL,
		&createdAt, &updatedAt, &lastHealth,
	)
	if err != nil {
		return nil, err
	}

	if port.Valid {
		p := int(port.Int64)
		inst.Port = &p
	}
	inst.DeletionProtection = intToBoolPtr(delProt)
	inst.PoolerEnabled = intToBoolPtr(poolerEn)
	inst.NetworkPolicyEnabled = intToBoolPtr(netPol)
	inst.AutoMinorVersionUpgrade = intToBoolPtr(autoUpgrade)
	inst.BackupEnabled = intToBoolPtr(backupEn)
	if maintDur.Valid {
		d := int(maintDur.Int64)
		inst.MaintenanceWindowDurationMinutes = &d
	}
	if backupRet.Valid {
		d := int(backupRet.Int64)
		inst.BackupRetentionDays = &d
	}
	if createdAt.Valid {
		inst.CreatedAt = parseFlexTime(createdAt.String)
	}
	if updatedAt.Valid {
		inst.UpdatedAt = parseFlexTime(updatedAt.String)
	}
	if lastHealth.Valid {
		inst.LastHealthCheck = parseFlexTime(lastHealth.String)
	}

	return &inst, nil
}

func boolToInt(b *bool) int {
	if b != nil && *b {
		return 1
	}
	return 0
}

func intToBoolPtr(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	b := n.Int64 == 1
	return &b
}

func flexTimeStr(ft *domain.FlexTime) *string {
	if ft == nil {
		return nil
	}
	s := ft.Time.Format(time.RFC3339Nano)
	return &s
}

func parseFlexTime(s string) *domain.FlexTime {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &domain.FlexTime{Time: t}
		}
	}
	return nil
}

func ptrStr(s *string) *string {
	return s
}
