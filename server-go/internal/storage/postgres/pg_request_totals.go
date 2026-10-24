package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/lib/pq"
)

// addRequestTotalsSQL adds one sample to a family's running totals; open
// connections are a reading, so the newest replaces the last.
const addRequestTotalsSQL = `INSERT INTO request_metric_totals AS t (
	project_id, family, requests, client_errors, server_errors, latency_buckets,
	connections, connections_opened, messages, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (project_id, family) DO UPDATE SET
	requests = t.requests + EXCLUDED.requests,
	client_errors = t.client_errors + EXCLUDED.client_errors,
	server_errors = t.server_errors + EXCLUDED.server_errors,
	latency_buckets = CASE
		WHEN cardinality(EXCLUDED.latency_buckets) = 0 THEN t.latency_buckets
		-- another bucket layout restarts the histogram rather than mixing two
		WHEN cardinality(t.latency_buckets) NOT IN (0, cardinality(EXCLUDED.latency_buckets)) THEN EXCLUDED.latency_buckets
		ELSE COALESCE((
			SELECT array_agg(COALESCE(old, 0) + COALESCE(added, 0) ORDER BY position)
			FROM unnest(t.latency_buckets, EXCLUDED.latency_buckets) WITH ORDINALITY AS u(old, added, position)
		), '{}')
	END,
	connections = EXCLUDED.connections,
	connections_opened = t.connections_opened + EXCLUDED.connections_opened,
	messages = t.messages + EXCLUDED.messages,
	updated_at = GREATEST(t.updated_at, EXCLUDED.updated_at)`

// AddRequestTotals adds one sample's rows to the running totals together (EXC-657).
func (s *Store) AddRequestTotals(ctx context.Context, rows []domain.RequestMetricsRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin request totals: %w", err)
	}
	defer tx.Rollback()
	for _, row := range rows {
		buckets := row.LatencyBuckets
		if buckets == nil {
			buckets = []int64{}
		}
		if _, err := tx.ExecContext(ctx, addRequestTotalsSQL,
			row.ProjectID, row.Family, row.Requests, row.ClientErrors, row.ServerErrors, pq.Array(buckets),
			row.Connections, row.ConnectionsOpened, row.Messages, row.Timestamp); err != nil {
			return fmt.Errorf("add request totals: %w", err)
		}
	}
	return tx.Commit()
}

// RequestTotals is one project's running totals, a row per family.
func (s *Store) RequestTotals(ctx context.Context, projectID string) ([]domain.RequestTotalsRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, family, requests, client_errors, server_errors,
		latency_buckets, connections, connections_opened, messages, updated_at
		FROM request_metric_totals WHERE project_id = $1 ORDER BY family`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query request totals: %w", err)
	}
	defer rows.Close()
	result := make([]domain.RequestTotalsRow, 0)
	for rows.Next() {
		var row domain.RequestTotalsRow
		var buckets pq.Int64Array
		if err := rows.Scan(&row.ProjectID, &row.Family, &row.Requests, &row.ClientErrors, &row.ServerErrors,
			&buckets, &row.Connections, &row.ConnectionsOpened, &row.Messages, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan request totals: %w", err)
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

// PruneRequestTotalsBefore drops the totals of families idle since cutoff;
// their counters restart from zero, which Prometheus reads as a reset.
func (s *Store) PruneRequestTotalsBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM request_metric_totals WHERE updated_at < $1`, cutoff)
	return err
}
