package service

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// metricsRetention is how long sampled metrics are kept.
const metricsRetention = 7 * 24 * time.Hour

// sampleTimeout bounds one database's sample, so a stuck pod cannot stall the rest.
const sampleTimeout = 20 * time.Second

// defaultHistoryPoints is a history read that names no limit.
const defaultHistoryPoints = 50

// MetricsHistory keeps sampled metrics: the platform DB's database_metrics table.
type MetricsHistory interface {
	AppendMetrics(ctx context.Context, m *domain.DatabaseMetrics) error
	// GetMetricsHistory answers newest first.
	GetMetricsHistory(ctx context.Context, projectID string, limit int) ([]domain.DatabaseMetrics, error)
	PruneMetricsBefore(ctx context.Context, cutoff time.Time) error
}

// SetHistory keeps metrics in the table, sampled by the loop rather than by
// reads of the live endpoint, and checks each sample against the alert thresholds.
func (s *MetricsService) SetHistory(history MetricsHistory, alerts *AlertingService) {
	s.table = history
	s.alerts = alerts
}

// StartSampler samples every running database each interval on the leading replica.
func (s *MetricsService) StartSampler(ctx context.Context, leader LeaderChecker, interval time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sampleIfLeader(ctx, leader)
			}
		}
	}()
	return cancel
}

func (s *MetricsService) sampleIfLeader(ctx context.Context, leader LeaderChecker) {
	leading, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("metrics sampler: leadership check: %v", err)
		return
	}
	if leading {
		s.SampleAll(ctx)
	}
}

// SampleAll records one row per running database, raises or resolves its
// alerts, and drops rows past the retention.
func (s *MetricsService) SampleAll(ctx context.Context) {
	instances, err := s.store.FindAll()
	if err != nil {
		log.Printf("metrics sampler: list projects: %v", err)
		return
	}
	for _, inst := range instances {
		if !sampled(inst) {
			continue
		}
		sampleCtx, cancel := context.WithTimeout(ctx, sampleTimeout)
		metrics := s.collectMetrics(sampleCtx, inst)
		cancel()
		if err := s.table.AppendMetrics(ctx, metrics); err != nil {
			log.Printf("metrics sampler: record %s: %v", inst.ProjectID, err)
		}
		if s.alerts != nil {
			s.alerts.CheckMetrics(metrics)
		}
	}
	if err := s.table.PruneMetricsBefore(ctx, time.Now().Add(-metricsRetention)); err != nil {
		log.Printf("metrics sampler: prune: %v", err)
	}
}

// sampled is a running CNPG or MOCO database on Kubernetes.
func sampled(inst *domain.DatabaseInstance) bool {
	return inst.Status == "ACTIVE" && !inst.NoDatabase &&
		(inst.DBType == domain.PostgreSQL || inst.DBType == domain.MySQL) && inst.DeploymentMode != domain.ModeDocker
}

// tableHistory is the table's newest rows for a project, oldest first.
func (s *MetricsService) tableHistory(ctx context.Context, projectID string, limit int) (*domain.MetricsHistory, error) {
	if limit <= 0 {
		limit = defaultHistoryPoints
	}
	rows, err := s.table.GetMetricsHistory(ctx, projectID, limit)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return &domain.MetricsHistory{ProjectID: projectID, Metrics: rows, TotalPoints: len(rows)}, nil
}
