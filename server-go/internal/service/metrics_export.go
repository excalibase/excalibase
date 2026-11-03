package service

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/promexport"
)

// MetricsExportService reads what one project's Prometheus endpoint answers
// (EXC-657): the newest database sample and the running request totals.
type MetricsExportService struct {
	history MetricsHistory
	// totals is nil without the request_metric_totals table.
	totals RequestTotalsStore
	now    func() time.Time
}

func NewMetricsExportService(history MetricsHistory, totals RequestTotalsStore) *MetricsExportService {
	return &MetricsExportService{history: history, totals: totals, now: time.Now}
}

// Snapshot is projectID's stored metrics, and only projectID's.
func (s *MetricsExportService) Snapshot(ctx context.Context, projectID string) (promexport.Snapshot, error) {
	snapshot := promexport.Snapshot{ProjectID: projectID, Now: s.now()}
	newest, err := s.history.GetMetricsHistory(ctx, projectID, 1)
	if err != nil {
		return snapshot, fmt.Errorf("read database metrics: %w", err)
	}
	if len(newest) > 0 {
		snapshot.Database = &newest[0]
	}
	if s.totals == nil {
		return snapshot, nil
	}
	if snapshot.Requests, err = s.totals.RequestTotals(ctx, projectID); err != nil {
		return snapshot, fmt.Errorf("read request totals: %w", err)
	}
	return snapshot, nil
}
