package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/lib/pq"
)

// AppendRequestMetrics stores one sample's rows together.
func (s *Store) AppendRequestMetrics(ctx context.Context, rows []domain.RequestMetricsRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin request metrics: %w", err)
	}
	defer tx.Rollback()
	for _, row := range rows {
		buckets := row.LatencyBuckets
		if buckets == nil {
			buckets = []int64{}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO request_metrics (
			project_id, timestamp, family, requests, client_errors, server_errors,
			latency_buckets, connections, connections_opened, messages
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			row.ProjectID, row.Timestamp, row.Family, row.Requests, row.ClientErrors, row.ServerErrors,
			pq.Array(buckets), row.Connections, row.ConnectionsOpened, row.Messages); err != nil {
			return fmt.Errorf("insert request metrics: %w", err)
		}
	}
	return tx.Commit()
}

// RequestMetricsSince is a project's rows from since on, oldest first.
func (s *Store) RequestMetricsSince(ctx context.Context, projectID string, since time.Time) ([]domain.RequestMetricsRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, timestamp, family, requests, client_errors, server_errors,
		latency_buckets, connections, connections_opened, messages
		FROM request_metrics WHERE project_id = $1 AND timestamp >= $2 ORDER BY timestamp, family`, projectID, since)
	if err != nil {
		return nil, fmt.Errorf("query request metrics: %w", err)
	}
	defer rows.Close()
	result := make([]domain.RequestMetricsRow, 0)
	for rows.Next() {
		var row domain.RequestMetricsRow
		var buckets pq.Int64Array
		if err := rows.Scan(&row.ProjectID, &row.Timestamp, &row.Family, &row.Requests, &row.ClientErrors,
			&row.ServerErrors, &buckets, &row.Connections, &row.ConnectionsOpened, &row.Messages); err != nil {
			return nil, fmt.Errorf("scan request metrics: %w", err)
		}
		if len(buckets) > 0 {
			row.LatencyBuckets = buckets
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(errIterateRows, err)
	}
	return result, nil
}

// PruneRequestMetricsBefore drops rows sampled before cutoff.
func (s *Store) PruneRequestMetricsBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM request_metrics WHERE timestamp < $1`, cutoff)
	return err
}
