package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return result, nil
}
