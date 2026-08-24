package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const errIterateRows = "iterate rows: %w"


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
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`,
		m.ProjectID, flexTimePtr(m.Timestamp), m.Status, m.HealthStatus, m.MetricsAvailable, ptrStr(m.UnavailableReason),
		m.CPUUsagePercent, m.MemoryUsagePercent, m.DiskUsagePercent,
		m.CPUUsageCores, m.MemoryUsageMB, m.DiskUsageGB,
		m.ActiveConnections, m.IdleConnections, m.MaxConnections,
		m.QueriesPerSecond, m.AvgQueryLatencyMs, m.SlowQueryCount, m.DatabaseSizeGB,
		flexTimePtr(m.LastBackupTime), flexTimePtr(m.NextBackupTime),
		m.CPULimitCores, m.MemoryLimitMB, m.StorageLimit, m.InstanceCount,
	)

	// Prune old entries (keep latest 100 per project)
	if err == nil {
		s.db.ExecContext(ctx, `DELETE FROM database_metrics WHERE project_id = $1 AND id NOT IN (
			SELECT id FROM database_metrics WHERE project_id = $2 ORDER BY timestamp DESC LIMIT 100
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
	FROM database_metrics WHERE project_id = $1 ORDER BY timestamp DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.DatabaseMetrics, 0)
	for rows.Next() {
		var m domain.DatabaseMetrics
		var ts, lastBackup, nextBackup sql.NullTime
		var unavailableReason sql.NullString
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
			m.Timestamp = &domain.FlexTime{Time: ts.Time}
		}
		if unavailableReason.Valid {
			m.UnavailableReason = &unavailableReason.String
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(errIterateRows, err)
	}
	return result, nil
}

// --- AlertStore ---

func (s *Store) SaveAlert(ctx context.Context, a *domain.Alert) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alerts (id, project_id, severity, message, metric, value, threshold, timestamp, resolved)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
			project_id = EXCLUDED.project_id,
			severity = EXCLUDED.severity,
			message = EXCLUDED.message,
			metric = EXCLUDED.metric,
			value = EXCLUDED.value,
			threshold = EXCLUDED.threshold,
			timestamp = EXCLUDED.timestamp,
			resolved = EXCLUDED.resolved`,
		a.ID, a.ProjectID, a.Severity, a.Message, a.Metric, a.Value, a.Threshold,
		flexTimePtr(a.Timestamp), a.Resolved)
	return err
}

func (s *Store) GetActiveAlerts(ctx context.Context) ([]domain.Alert, error) {
	return s.queryAlerts(ctx,
		`SELECT id, project_id, severity, message, metric, value, threshold, timestamp, resolved
		 FROM alerts WHERE resolved = FALSE ORDER BY timestamp DESC`)
}

func (s *Store) GetActiveAlertsForProject(ctx context.Context, projectID string) ([]domain.Alert, error) {
	return s.queryAlerts(ctx,
		`SELECT id, project_id, severity, message, metric, value, threshold, timestamp, resolved
		 FROM alerts WHERE project_id = $1 AND resolved = FALSE ORDER BY timestamp DESC`, projectID)
}

func (s *Store) GetAlertHistory(ctx context.Context, limit int) ([]domain.Alert, error) {
	return s.queryAlerts(ctx,
		`SELECT id, project_id, severity, message, metric, value, threshold, timestamp, resolved
		 FROM alerts ORDER BY timestamp DESC LIMIT $1`, limit)
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
		var ts sql.NullTime
		err := rows.Scan(&a.ID, &a.ProjectID, &a.Severity, &a.Message, &a.Metric, &a.Value, &a.Threshold, &ts, &a.Resolved)
		if err != nil {
			return nil, err
		}
		if ts.Valid {
			a.Timestamp = &domain.FlexTime{Time: ts.Time}
		}
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(errIterateRows, err)
	}
	return result, nil
}

// --- AuditLogStore ---

func (s *Store) LogAudit(ctx context.Context, e *domain.AuditEntry) error {
	ts := time.Now().UTC()
	if e.Timestamp != nil {
		ts = *e.Timestamp
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (user_id, action, resource, resource_id, details, ip_address, timestamp)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.UserID, e.Action, e.Resource, e.ResourceID, e.Details, e.IPAddress, ts)
	return err
}

func (s *Store) QueryAudit(ctx context.Context, limit int) ([]domain.AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, action, resource, resource_id, details, ip_address, timestamp
		 FROM audit_log ORDER BY timestamp DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.AuditEntry, 0)
	for rows.Next() {
		var e domain.AuditEntry
		var ts sql.NullTime
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
			t := ts.Time
			e.Timestamp = &t
		}
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(errIterateRows, err)
	}
	return result, nil
}
