package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

// Network metering history (EXC-844): minute rows for 48 hours, hourly rollups for 30 days.
const (
	netMinuteRetention = 48 * time.Hour
	netHourlyRetention = 30 * 24 * time.Hour
)

// netPruneBatch is how many rows one delete removes, so a prune never holds a long lock.
var netPruneBatch = 10_000

// AppendNetSamples stores one round of per-app minutes.
func (s *Store) AppendNetSamples(ctx context.Context, samples []netmeter.Sample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin net samples: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO app_net_samples
		(ts, project_id, app_id, instances, rx_bps, tx_bps, max_instance_tx_bps)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (project_id, app_id, ts) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("prepare net samples: %w", err)
	}
	defer statement.Close()
	for _, sample := range samples {
		if _, err := statement.ExecContext(ctx, sample.At.UTC(), sample.ProjectID, sample.AppID, sample.Instances,
			sample.RxBps, sample.TxBps, sample.MaxInstanceTxBps); err != nil {
			return fmt.Errorf("insert net sample: %w", err)
		}
	}
	return tx.Commit()
}

// ListNetSamples reads an app's minutes since a time, oldest first.
func (s *Store) ListNetSamples(ctx context.Context, projectID, appID string, since time.Time) ([]netmeter.Sample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, project_id, app_id, instances, rx_bps, tx_bps, max_instance_tx_bps
		FROM app_net_samples WHERE project_id = $1 AND app_id = $2 AND ts >= $3 ORDER BY ts`, projectID, appID, since)
	if err != nil {
		return nil, fmt.Errorf("list net samples: %w", err)
	}
	defer rows.Close()
	var out []netmeter.Sample
	for rows.Next() {
		var sample netmeter.Sample
		if err := rows.Scan(&sample.At, &sample.ProjectID, &sample.AppID, &sample.Instances, &sample.RxBps, &sample.TxBps, &sample.MaxInstanceTxBps); err != nil {
			return nil, err
		}
		sample.At = sample.At.UTC()
		out = append(out, sample)
	}
	return out, rows.Err()
}

// ListNetHourly reads an app's hourly rollups since a time, oldest first.
func (s *Store) ListNetHourly(ctx context.Context, projectID, appID string, since time.Time) ([]netmeter.Hourly, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hour, project_id, app_id, samples, avg_rx_bps, avg_tx_bps, max_tx_bps, max_instance_tx_bps
		FROM app_net_hourly WHERE project_id = $1 AND app_id = $2 AND hour >= $3 ORDER BY hour`, projectID, appID, since)
	if err != nil {
		return nil, fmt.Errorf("list net hourly: %w", err)
	}
	defer rows.Close()
	var out []netmeter.Hourly
	for rows.Next() {
		var hour netmeter.Hourly
		if err := rows.Scan(&hour.Hour, &hour.ProjectID, &hour.AppID, &hour.Samples, &hour.AvgRxBps, &hour.AvgTxBps, &hour.MaxTxBps, &hour.MaxInstanceTxBps); err != nil {
			return nil, err
		}
		hour.Hour = hour.Hour.UTC()
		out = append(out, hour)
	}
	return out, rows.Err()
}

// rollupNetHoursSQL folds every finished hour still held as minutes into its
// hourly row; running it again gives the same rows.
const rollupNetHoursSQL = `
	INSERT INTO app_net_hourly (hour, project_id, app_id, samples, avg_rx_bps, avg_tx_bps, max_tx_bps, max_instance_tx_bps)
	SELECT date_trunc('hour', ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', project_id, app_id,
	       COUNT(*), AVG(rx_bps), AVG(tx_bps), MAX(tx_bps), MAX(max_instance_tx_bps)
	FROM app_net_samples
	WHERE ts < date_trunc('hour', $1::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
	GROUP BY 1, 2, 3
	ON CONFLICT (project_id, app_id, hour) DO UPDATE SET
		samples = EXCLUDED.samples, avg_rx_bps = EXCLUDED.avg_rx_bps, avg_tx_bps = EXCLUDED.avg_tx_bps,
		max_tx_bps = EXCLUDED.max_tx_bps, max_instance_tx_bps = EXCLUDED.max_instance_tx_bps`

// PruneNetSamples rolls finished hours up first, then deletes minutes past 48
// hours and hourly rows past 30 days, a batch at a time.
func (s *Store) PruneNetSamples(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, rollupNetHoursSQL, now.UTC()); err != nil {
		return fmt.Errorf("roll up net samples: %w", err)
	}
	if err := s.deleteInBatches(ctx, "app_net_samples", "ts", now.Add(-netMinuteRetention)); err != nil {
		return err
	}
	return s.deleteInBatches(ctx, "app_net_hourly", "hour", now.Add(-netHourlyRetention))
}

// deleteInBatches removes rows older than the cutoff; table and column are
// constants of this file, never input.
func (s *Store) deleteInBatches(ctx context.Context, table, column string, cutoff time.Time) error {
	query := fmt.Sprintf(`DELETE FROM %[1]s WHERE ctid IN (SELECT ctid FROM %[1]s WHERE %[2]s < $1 LIMIT $2)`, table, column)
	for {
		result, err := s.db.ExecContext(ctx, query, cutoff.UTC(), netPruneBatch)
		if err != nil {
			return fmt.Errorf("prune %s: %w", table, err)
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("prune %s: %w", table, err)
		}
		if deleted < int64(netPruneBatch) {
			return nil
		}
	}
}
