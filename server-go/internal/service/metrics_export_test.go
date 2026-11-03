package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/requestmetrics"
)

type fakeTotals struct {
	mu       sync.Mutex
	added    []domain.RequestMetricsRow
	totals   []domain.RequestTotalsRow
	prunedAt time.Time
	err      error
}

func (f *fakeTotals) AddRequestTotals(_ context.Context, rows []domain.RequestMetricsRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, rows...)
	return nil
}

func (f *fakeTotals) RequestTotals(_ context.Context, projectID string) ([]domain.RequestTotalsRow, error) {
	var out []domain.RequestTotalsRow
	for _, row := range f.totals {
		if row.ProjectID == projectID {
			out = append(out, row)
		}
	}
	return out, f.err
}

func (f *fakeTotals) PruneRequestTotalsBefore(_ context.Context, cutoff time.Time) error {
	f.prunedAt = cutoff
	return nil
}

// EXC-657: the running totals grow only while the metrics endpoint is wired in.
func TestRequestSample_AddsToTheTotalsWhenWired(t *testing.T) {
	svc, _, clock := requestFixture(t,
		[]requestmetrics.Reading{scrapeOf("a", map[string]float64{"p1": 10})},
		[]requestmetrics.Reading{scrapeOf("a", map[string]float64{"p1": 13})},
	)
	totals := &fakeTotals{}
	svc.SetTotals(totals)
	svc.Sample(context.Background())
	*clock = clock.Add(time.Minute)
	svc.Sample(context.Background())
	if len(totals.added) != 1 || totals.added[0].Requests != 3 {
		t.Errorf("the sample's rows are added: %+v", totals.added)
	}
	if !totals.prunedAt.Equal(clock.Add(-metricsRetention)) {
		t.Errorf("idle totals pruned at %s", totals.prunedAt)
	}
}

func TestRequestSample_WithoutTotalsWritesNoTotals(t *testing.T) {
	svc, store, clock := requestFixture(t,
		[]requestmetrics.Reading{scrapeOf("a", map[string]float64{"p1": 10})},
		[]requestmetrics.Reading{scrapeOf("a", map[string]float64{"p1": 13})},
	)
	svc.Sample(context.Background())
	*clock = clock.Add(time.Minute)
	svc.Sample(context.Background())
	if len(store.rows) != 1 {
		t.Errorf("the charts are sampled as before: %+v", store.rows)
	}
}

func TestMetricsExport_SnapshotIsTheNewestSampleAndTheTotals(t *testing.T) {
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	history := &fakeMetricsHistory{}
	history.AppendMetrics(context.Background(), &domain.DatabaseMetrics{ProjectID: "p1", Timestamp: &domain.FlexTime{Time: at.Add(-2 * time.Minute)}})
	history.AppendMetrics(context.Background(), &domain.DatabaseMetrics{ProjectID: "p1", Timestamp: &domain.FlexTime{Time: at.Add(-time.Minute)}, MetricsAvailable: true})
	history.AppendMetrics(context.Background(), &domain.DatabaseMetrics{ProjectID: "p2", Timestamp: &domain.FlexTime{Time: at}})
	totals := &fakeTotals{totals: []domain.RequestTotalsRow{{ProjectID: "p1", Family: "graphql", Requests: 4}, {ProjectID: "p2", Family: "graphql"}}}
	export := NewMetricsExportService(history, totals)
	export.now = func() time.Time { return at }

	snapshot, err := export.Snapshot(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.ProjectID != "p1" || !snapshot.Now.Equal(at) {
		t.Errorf("snapshot: %+v", snapshot)
	}
	if snapshot.Database == nil || !snapshot.Database.Timestamp.Equal(at.Add(-time.Minute)) {
		t.Errorf("the newest sample: %+v", snapshot.Database)
	}
	if len(snapshot.Requests) != 1 || snapshot.Requests[0].ProjectID != "p1" {
		t.Errorf("only p1's totals: %+v", snapshot.Requests)
	}
}

func TestMetricsExport_NoHistoryOrTotalsIsAnEmptySnapshot(t *testing.T) {
	snapshot, err := NewMetricsExportService(&fakeMetricsHistory{}, nil).Snapshot(context.Background(), "p1")
	if err != nil || snapshot.Database != nil || len(snapshot.Requests) != 0 {
		t.Errorf("snapshot %+v, %v", snapshot, err)
	}
}

func TestMetricsExport_AFailedReadIsAnError(t *testing.T) {
	if _, err := NewMetricsExportService(failingMetricsHistory{}, nil).Snapshot(context.Background(), "p1"); err == nil {
		t.Error("history read failure")
	}
	totals := &fakeTotals{err: errors.New("down")}
	if _, err := NewMetricsExportService(&fakeMetricsHistory{}, totals).Snapshot(context.Background(), "p1"); err == nil {
		t.Error("totals read failure")
	}
}
