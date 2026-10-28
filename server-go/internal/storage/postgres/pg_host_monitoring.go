package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
	"github.com/lib/pq"
)

// Single-host monitoring (EXC-670). Host rows are kept thirty days; container
// rows a minute apart for seven days, then one in ten up to thirty days.
const (
	hostMonitoringRetention = 30 * 24 * time.Hour
	containerFullRetention  = 7 * 24 * time.Hour
)

const insertHostSampleSQL = `INSERT INTO host_metrics (
	timestamp, cpu_percent, cpu_count, load1, load5, load15,
	memory_total_bytes, memory_used_bytes, swap_total_bytes, swap_used_bytes,
	disk_total_bytes, disk_used_bytes, disk_read_bytes_per_sec, disk_write_bytes_per_sec,
	network_rx_bytes_per_sec, network_tx_bytes_per_sec, network_source, temperature_max_celsius, temperatures
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`

// AppendHostSample stores one host minute.
func (s *Store) AppendHostSample(ctx context.Context, sample hostmetrics.Sample) error {
	sensors := sample.Temperatures
	if sensors == nil {
		sensors = []hostmetrics.Sensor{}
	}
	temperatures, err := json.Marshal(sensors)
	if err != nil {
		return fmt.Errorf("host sample temperatures: %w", err)
	}
	_, err = s.db.ExecContext(ctx, insertHostSampleSQL,
		sample.Timestamp, sample.CPUPercent, sample.CPUCount, sample.Load1, sample.Load5, sample.Load15,
		sample.MemoryTotalBytes, sample.MemoryUsedBytes, sample.SwapTotalBytes, sample.SwapUsedBytes,
		sample.DiskTotalBytes, sample.DiskUsedBytes, sample.DiskReadBytesPerSec, sample.DiskWriteBytesPerSec,
		sample.NetworkRxBytesPerSec, sample.NetworkTxBytesPerSec, sample.NetworkSource, sample.TemperatureMaxCelsius, temperatures)
	return err
}

const latestHostSampleSQL = `SELECT timestamp, cpu_percent, cpu_count, load1, load5, load15,
	memory_total_bytes, memory_used_bytes, swap_total_bytes, swap_used_bytes,
	disk_total_bytes, disk_used_bytes, disk_read_bytes_per_sec, disk_write_bytes_per_sec,
	network_rx_bytes_per_sec, network_tx_bytes_per_sec, network_source, temperature_max_celsius, temperatures
	FROM host_metrics ORDER BY timestamp DESC LIMIT 1`

// LatestHostSample is the newest host minute, or nil before the first.
func (s *Store) LatestHostSample(ctx context.Context) (*hostmetrics.Sample, error) {
	sample, err := scanHostSample(s.db.QueryRowContext(ctx, latestHostSampleSQL), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sample, nil
}

// Each bucket averages its minutes; totals take the bucket's largest.
const hostHistorySQL = `SELECT date_bin($2::interval, timestamp, TIMESTAMPTZ '2000-01-01') AS bucket,
	avg(cpu_percent), max(cpu_count), avg(load1), avg(load5), avg(load15),
	max(memory_total_bytes), avg(memory_used_bytes)::bigint, max(swap_total_bytes), avg(swap_used_bytes)::bigint,
	max(disk_total_bytes), avg(disk_used_bytes)::bigint, avg(disk_read_bytes_per_sec), avg(disk_write_bytes_per_sec),
	avg(network_rx_bytes_per_sec), avg(network_tx_bytes_per_sec), max(network_source), max(temperature_max_celsius), '[]'::jsonb
	FROM host_metrics WHERE timestamp >= $1 GROUP BY bucket ORDER BY bucket`

// HostHistory is the host from since on in buckets, oldest first.
func (s *Store) HostHistory(ctx context.Context, since time.Time, bucket time.Duration) ([]hostmetrics.Sample, error) {
	rows, err := s.db.QueryContext(ctx, hostHistorySQL, since, intervalOf(bucket))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	samples := []hostmetrics.Sample{}
	for rows.Next() {
		sample, err := scanHostSample(rows, false)
		if err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

func scanHostSample(row rowScanner, withSensors bool) (hostmetrics.Sample, error) {
	var sample hostmetrics.Sample
	var cpuCount sql.NullInt64
	var source sql.NullString
	var temperatures []byte
	floats := [12]sql.NullFloat64{}
	ints := [6]sql.NullInt64{}
	err := row.Scan(&sample.Timestamp, &floats[0], &cpuCount, &floats[1], &floats[2], &floats[3],
		&ints[0], &ints[1], &ints[2], &ints[3], &ints[4], &ints[5], &floats[4], &floats[5],
		&floats[6], &floats[7], &source, &floats[8], &temperatures)
	if err != nil {
		return sample, err
	}
	sample.CPUPercent, sample.Load1, sample.Load5, sample.Load15 = nullFloat(floats[0]), nullFloat(floats[1]), nullFloat(floats[2]), nullFloat(floats[3])
	sample.MemoryTotalBytes, sample.MemoryUsedBytes, sample.SwapTotalBytes, sample.SwapUsedBytes = nullInt(ints[0]), nullInt(ints[1]), nullInt(ints[2]), nullInt(ints[3])
	sample.DiskTotalBytes, sample.DiskUsedBytes = nullInt(ints[4]), nullInt(ints[5])
	sample.DiskReadBytesPerSec, sample.DiskWriteBytesPerSec = nullFloat(floats[4]), nullFloat(floats[5])
	sample.NetworkRxBytesPerSec, sample.NetworkTxBytesPerSec = nullFloat(floats[6]), nullFloat(floats[7])
	sample.TemperatureMaxCelsius = nullFloat(floats[8])
	if cpuCount.Valid {
		count := int(cpuCount.Int64)
		sample.CPUCount = &count
	}
	if source.Valid {
		sample.NetworkSource = &source.String
	}
	sample.Temperatures = []hostmetrics.Sensor{}
	if withSensors && len(temperatures) > 0 {
		if err := json.Unmarshal(temperatures, &sample.Temperatures); err != nil {
			return sample, fmt.Errorf("host sample temperatures: %w", err)
		}
	}
	return sample, nil
}

const hostAveragesSQL = `SELECT count(*), avg(cpu_percent),
	avg(memory_used_bytes::double precision / NULLIF(memory_total_bytes, 0) * 100),
	avg(disk_used_bytes::double precision / NULLIF(disk_total_bytes, 0) * 100),
	avg(temperature_max_celsius), avg(load5), avg(network_rx_bytes_per_sec + network_tx_bytes_per_sec)
	FROM host_metrics WHERE timestamp >= $1`

// HostAverages averages the host from since on, for the alert window.
func (s *Store) HostAverages(ctx context.Context, since time.Time) (hostmetrics.Averages, error) {
	var averages hostmetrics.Averages
	values := [6]sql.NullFloat64{}
	err := s.db.QueryRowContext(ctx, hostAveragesSQL, since).Scan(&averages.Samples,
		&values[0], &values[1], &values[2], &values[3], &values[4], &values[5])
	if err != nil {
		return averages, err
	}
	averages.CPUPercent, averages.MemoryPercent, averages.DiskPercent = nullFloat(values[0]), nullFloat(values[1]), nullFloat(values[2])
	averages.TemperatureCelsius, averages.Load5, averages.NetworkBytesPerSec = nullFloat(values[3]), nullFloat(values[4]), nullFloat(values[5])
	return averages, nil
}

const insertContainerSampleSQL = `INSERT INTO container_metrics (
	timestamp, container_name, project_id, component, state, health,
	cpu_cores, cpu_percent, memory_bytes, memory_limit_bytes, network_rx_bytes_per_sec, network_tx_bytes_per_sec
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`

// AppendContainerSamples stores one minute's containers together.
func (s *Store) AppendContainerSamples(ctx context.Context, samples []domain.ContainerSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin container metrics: %w", err)
	}
	defer tx.Rollback()
	for _, sample := range samples {
		if _, err := tx.ExecContext(ctx, insertContainerSampleSQL,
			sample.Timestamp, sample.Name, sample.ProjectID, sample.Component, sample.State, sample.Health,
			sample.CPUCores, sample.CPUPercent, sample.MemoryBytes, sample.MemoryLimitBytes,
			sample.NetworkRxBytesPerSec, sample.NetworkTxBytesPerSec); err != nil {
			return fmt.Errorf("insert container metrics: %w", err)
		}
	}
	return tx.Commit()
}

const latestContainerSamplesSQL = `SELECT container_name, project_id, component, state, health, timestamp,
	cpu_cores, cpu_percent, memory_bytes, memory_limit_bytes, network_rx_bytes_per_sec, network_tx_bytes_per_sec
	FROM container_metrics WHERE timestamp = (SELECT max(timestamp) FROM container_metrics) ORDER BY container_name`

// LatestContainerSamples is the newest minute's containers and its time, nil before the first.
func (s *Store) LatestContainerSamples(ctx context.Context) ([]domain.ContainerSample, *time.Time, error) {
	samples, err := s.queryContainerSamples(ctx, latestContainerSamplesSQL)
	if err != nil || len(samples) == 0 {
		return samples, nil, err
	}
	at := samples[0].Timestamp
	return samples, &at, nil
}

const containerHistorySQL = `SELECT container_name, max(project_id), max(component),
	(array_agg(state ORDER BY timestamp DESC))[1], (array_agg(health ORDER BY timestamp DESC))[1],
	date_bin($3::interval, timestamp, TIMESTAMPTZ '2000-01-01') AS bucket,
	avg(cpu_cores), avg(cpu_percent), avg(memory_bytes)::bigint, max(memory_limit_bytes),
	avg(network_rx_bytes_per_sec), avg(network_tx_bytes_per_sec)
	FROM container_metrics WHERE container_name = $1 AND timestamp >= $2
	GROUP BY container_name, bucket ORDER BY bucket`

// ContainerHistory is one container from since on in buckets, oldest first.
func (s *Store) ContainerHistory(ctx context.Context, name string, since time.Time, bucket time.Duration) ([]domain.ContainerSample, error) {
	return s.queryContainerSamples(ctx, containerHistorySQL, name, since, intervalOf(bucket))
}

const projectContainerHistorySQL = `SELECT container_name, max(project_id), max(component),
	(array_agg(state ORDER BY timestamp DESC))[1], (array_agg(health ORDER BY timestamp DESC))[1],
	date_bin($3::interval, timestamp, TIMESTAMPTZ '2000-01-01') AS bucket,
	avg(cpu_cores), avg(cpu_percent), avg(memory_bytes)::bigint, max(memory_limit_bytes),
	avg(network_rx_bytes_per_sec), avg(network_tx_bytes_per_sec)
	FROM container_metrics WHERE project_id = $1 AND timestamp >= $2
	GROUP BY container_name, bucket ORDER BY container_name, bucket`

// ProjectContainerHistory is every container of one project from since on, by name then time.
func (s *Store) ProjectContainerHistory(ctx context.Context, projectID string, since time.Time, bucket time.Duration) ([]domain.ContainerSample, error) {
	if projectID == "" {
		return []domain.ContainerSample{}, nil
	}
	return s.queryContainerSamples(ctx, projectContainerHistorySQL, projectID, since, intervalOf(bucket))
}

func (s *Store) queryContainerSamples(ctx context.Context, query string, args ...any) ([]domain.ContainerSample, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	samples := []domain.ContainerSample{}
	for rows.Next() {
		var sample domain.ContainerSample
		floats := [4]sql.NullFloat64{}
		ints := [2]sql.NullInt64{}
		if err := rows.Scan(&sample.Name, &sample.ProjectID, &sample.Component, &sample.State, &sample.Health, &sample.Timestamp,
			&floats[0], &floats[1], &ints[0], &ints[1], &floats[2], &floats[3]); err != nil {
			return nil, err
		}
		sample.CPUCores, sample.CPUPercent = nullFloat(floats[0]), nullFloat(floats[1])
		sample.MemoryBytes, sample.MemoryLimitBytes = nullInt(ints[0]), nullInt(ints[1])
		sample.NetworkRxBytesPerSec, sample.NetworkTxBytesPerSec = nullFloat(floats[2]), nullFloat(floats[3])
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

// PruneHostMonitoring drops what is past retention and thins old container rows.
func (s *Store) PruneHostMonitoring(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-hostMonitoringRetention)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM host_metrics WHERE timestamp < $1`, cutoff); err != nil {
		return fmt.Errorf("prune host metrics: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM container_metrics WHERE timestamp < $1`, cutoff); err != nil {
		return fmt.Errorf("prune container metrics: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM container_metrics
		WHERE timestamp < $1 AND extract(minute FROM timestamp AT TIME ZONE 'UTC')::int % 10 <> 0`, now.Add(-containerFullRetention))
	if err != nil {
		return fmt.Errorf("thin container metrics: %w", err)
	}
	return nil
}

// GetMonitoringAlertSettings reads the saved rules; found is false before the first save.
func (s *Store) GetMonitoringAlertSettings(ctx context.Context) (domain.MonitoringAlertSettings, bool, error) {
	var settings domain.MonitoringAlertSettings
	values := [6]sql.NullFloat64{}
	err := s.db.QueryRowContext(ctx, `SELECT enabled, cpu_percent, memory_percent, disk_percent, temperature_celsius,
		load5, network_mbps, window_minutes, down_minutes, emails, webhook_display FROM monitoring_alert_settings WHERE id = 1`).
		Scan(&settings.Enabled, &values[0], &values[1], &values[2], &values[3], &values[4], &values[5],
			&settings.WindowMinutes, &settings.DownMinutes, pq.Array(&settings.Emails), &settings.WebhookDisplay)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, false, nil
	}
	if err != nil {
		return settings, false, err
	}
	settings.CPUPercent, settings.MemoryPercent, settings.DiskPercent = nullFloat(values[0]), nullFloat(values[1]), nullFloat(values[2])
	settings.TemperatureCelsius, settings.Load5, settings.NetworkMbps = nullFloat(values[3]), nullFloat(values[4]), nullFloat(values[5])
	if settings.Emails == nil {
		settings.Emails = []string{}
	}
	return settings, true, nil
}

// SaveMonitoringAlertSettings writes the one row.
func (s *Store) SaveMonitoringAlertSettings(ctx context.Context, settings domain.MonitoringAlertSettings) error {
	emails := settings.Emails
	if emails == nil {
		emails = []string{}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO monitoring_alert_settings (id, enabled, cpu_percent, memory_percent, disk_percent,
		temperature_celsius, load5, network_mbps, window_minutes, down_minutes, emails, webhook_display, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now())
		ON CONFLICT (id) DO UPDATE SET enabled = EXCLUDED.enabled, cpu_percent = EXCLUDED.cpu_percent,
		memory_percent = EXCLUDED.memory_percent, disk_percent = EXCLUDED.disk_percent,
		temperature_celsius = EXCLUDED.temperature_celsius, load5 = EXCLUDED.load5, network_mbps = EXCLUDED.network_mbps,
		window_minutes = EXCLUDED.window_minutes, down_minutes = EXCLUDED.down_minutes, emails = EXCLUDED.emails,
		webhook_display = EXCLUDED.webhook_display, updated_at = now()`,
		settings.Enabled, settings.CPUPercent, settings.MemoryPercent, settings.DiskPercent, settings.TemperatureCelsius,
		settings.Load5, settings.NetworkMbps, settings.WindowMinutes, settings.DownMinutes, pq.Array(emails), settings.WebhookDisplay)
	return err
}

func intervalOf(bucket time.Duration) string {
	return fmt.Sprintf("%d seconds", max(int64(bucket.Seconds()), 1))
}

func nullFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

func nullInt(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}
