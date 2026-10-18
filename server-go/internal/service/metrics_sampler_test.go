package service

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakeMetricsHistory stands in for the platform DB's database_metrics table.
type fakeMetricsHistory struct {
	mu       sync.Mutex
	rows     []domain.DatabaseMetrics
	prunedAt time.Time
}

func (f *fakeMetricsHistory) AppendMetrics(_ context.Context, m *domain.DatabaseMetrics) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, *m)
	return nil
}

// GetMetricsHistory answers newest first, as the table query does.
func (f *fakeMetricsHistory) GetMetricsHistory(_ context.Context, projectID string, limit int) ([]domain.DatabaseMetrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.DatabaseMetrics
	for i := len(f.rows) - 1; i >= 0 && len(out) < limit; i-- {
		if f.rows[i].ProjectID == projectID {
			out = append(out, f.rows[i])
		}
	}
	return out, nil
}

func (f *fakeMetricsHistory) PruneMetricsBefore(_ context.Context, cutoff time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunedAt = cutoff
	return nil
}

// fakeAlertStore stands in for the platform DB's alerts table.
type fakeAlertStore struct {
	mu     sync.Mutex
	alerts map[string]domain.Alert
}

func newFakeAlertStore() *fakeAlertStore { return &fakeAlertStore{alerts: map[string]domain.Alert{}} }

func (f *fakeAlertStore) SaveAlert(_ context.Context, a *domain.Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts[a.ID] = *a
	return nil
}

func (f *fakeAlertStore) list(keep func(domain.Alert) bool) []domain.Alert {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Alert{}
	for _, a := range f.alerts {
		if keep(a) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (f *fakeAlertStore) GetActiveAlerts(context.Context) ([]domain.Alert, error) {
	return f.list(func(a domain.Alert) bool { return !a.Resolved }), nil
}

func (f *fakeAlertStore) GetActiveAlertsForProject(_ context.Context, projectID string) ([]domain.Alert, error) {
	return f.list(func(a domain.Alert) bool { return !a.Resolved && a.ProjectID == projectID }), nil
}

func (f *fakeAlertStore) GetAlertHistory(_ context.Context, _ int) ([]domain.Alert, error) {
	return f.list(func(domain.Alert) bool { return true }), nil
}

func samplerFixture(t *testing.T) (*MetricsService, *fakeMetricsHistory, *AlertingService, *fakeAlertStore, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	for _, inst := range []*domain.DatabaseInstance{
		{ProjectID: "live-db", Namespace: "org-live-db", Status: "ACTIVE", DBType: domain.PostgreSQL, Tier: domain.Free, DeploymentMode: domain.ModeK8s, DatabaseName: "app"},
		{ProjectID: "paused-db", Namespace: "org-paused-db", Status: "PAUSED", DBType: domain.PostgreSQL, Tier: domain.Free, DeploymentMode: domain.ModeK8s},
		{ProjectID: "bare", Namespace: "org-bare", Status: "ACTIVE", DBType: domain.PostgreSQL, NoDatabase: true, DeploymentMode: domain.ModeK8s},
		{ProjectID: "my-db", Namespace: "org-my-db", Status: "ACTIVE", DBType: domain.MySQL, DeploymentMode: domain.ModeK8s},
		{ProjectID: "host-db", Namespace: "org-host-db", Status: "ACTIVE", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker},
	} {
		if err := store.Create(inst); err != nil {
			t.Fatalf("seed %s: %v", inst.ProjectID, err)
		}
	}
	mock.SetupPostgreSQLMock("live-db", "org-live-db", 1)
	mock.Metrics["org-live-db"] = []k8s.PodResourceMetrics{{Name: "live-db-postgres-1", CPUMillis: 475, MemoryMB: 100}}

	history := &fakeMetricsHistory{}
	alertStore := newFakeAlertStore()
	alerts := NewAlertingService(dir)
	alerts.SetStore(alertStore)
	metrics := NewMetricsService(store, mock, dir)
	metrics.SetHistory(history, alerts)
	return metrics, history, alerts, alertStore, mock
}

// Every tick keeps one row per running database, checks it against the alert
// thresholds and drops what is older than the retention.
func TestSampleAllRecordsEveryRunningDatabase(t *testing.T) {
	metrics, history, _, alertStore, _ := samplerFixture(t)

	before := time.Now()
	metrics.SampleAll(context.Background())

	if len(history.rows) != 1 || history.rows[0].ProjectID != "live-db" {
		t.Fatalf("rows = %+v, want one for live-db", history.rows)
	}
	row := history.rows[0]
	if row.CPUUsagePercent == nil || *row.CPUUsagePercent < 94 || row.ActiveConnections == nil {
		t.Fatalf("row lacks its figures: %+v", row)
	}
	active, _ := alertStore.GetActiveAlertsForProject(context.Background(), "live-db")
	if len(active) != 1 || active[0].Metric != "cpuUsagePercent" {
		t.Fatalf("alerts = %+v, want one CPU alert", active)
	}
	wantCutoff := before.Add(-metricsRetention)
	if history.prunedAt.Before(wantCutoff.Add(-time.Minute)) || history.prunedAt.After(time.Now().Add(-metricsRetention)) {
		t.Fatalf("pruned before %s, want about %s", history.prunedAt, wantCutoff)
	}
}

func TestHistoryIsServedFromTheTableOldestFirst(t *testing.T) {
	metrics, history, _, _, _ := samplerFixture(t)
	for minute := 0; minute < 3; minute++ {
		history.rows = append(history.rows, domain.DatabaseMetrics{ProjectID: "live-db",
			Timestamp: &domain.FlexTime{Time: time.Date(2026, 10, 19, 1, minute, 0, 0, time.UTC)}})
	}

	got, err := metrics.GetMetricsHistory(context.Background(), "live-db", 2)
	if err != nil {
		t.Fatalf("GetMetricsHistory: %v", err)
	}
	if got.TotalPoints != 2 || got.Metrics[0].Timestamp.Minute() != 1 || got.Metrics[1].Timestamp.Minute() != 2 {
		t.Fatalf("history = %+v, want minutes 1 then 2", got.Metrics)
	}
}

// A reading of the live endpoint is not a sample: only the loop writes rows.
func TestCurrentMetricsDoNotWriteHistoryWhenTheTableKeepsIt(t *testing.T) {
	metrics, history, _, _, _ := samplerFixture(t)
	if _, err := metrics.GetCurrentMetrics(context.Background(), "live-db"); err != nil {
		t.Fatalf("GetCurrentMetrics: %v", err)
	}
	if len(history.rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(history.rows))
	}
}

func TestAnAlertIsRaisedOnceAndResolvedWhenUsageFalls(t *testing.T) {
	_, _, alerts, alertStore, _ := samplerFixture(t)
	high, low := 95.0, 40.0
	reading := func(cpu float64) *domain.DatabaseMetrics {
		return &domain.DatabaseMetrics{ProjectID: "live-db", MetricsAvailable: true, CPUUsagePercent: &cpu}
	}

	alerts.CheckMetrics(reading(high))
	alerts.CheckMetrics(reading(high))
	if active := alerts.GetActiveAlertsForProject("live-db"); len(active) != 1 {
		t.Fatalf("active = %+v, want one alert across two high readings", active)
	}

	alerts.CheckMetrics(reading(low))
	if active := alerts.GetActiveAlertsForProject("live-db"); len(active) != 0 {
		t.Fatalf("active = %+v, want none once usage fell", active)
	}
	if history, _ := alertStore.GetAlertHistory(context.Background(), 10); len(history) != 1 || !history[0].Resolved {
		t.Fatalf("history = %+v, want the one alert, resolved", history)
	}
}

func TestTheSamplerRunsOnlyOnTheLeader(t *testing.T) {
	metrics, history, _, _, _ := samplerFixture(t)
	metrics.sampleIfLeader(context.Background(), staticLeader(false))
	if len(history.rows) != 0 {
		t.Fatalf("a follower wrote %d rows", len(history.rows))
	}
	metrics.sampleIfLeader(context.Background(), staticLeader(true))
	if len(history.rows) != 1 {
		t.Fatalf("the leader wrote %d rows, want 1", len(history.rows))
	}
}

type staticLeader bool

func (l staticLeader) IsLeader(context.Context) (bool, error) { return bool(l), nil }

func TestWithoutTheTableAnAlertIsStillRaisedOnceAndResolved(t *testing.T) {
	alerts := NewAlertingService(t.TempDir())
	high, low := 97.0, 10.0
	reading := func(memory float64) *domain.DatabaseMetrics {
		return &domain.DatabaseMetrics{ProjectID: "p1", MetricsAvailable: true, MemoryUsagePercent: &memory}
	}

	alerts.CheckMetrics(reading(high))
	alerts.CheckMetrics(reading(high))
	if active := alerts.GetActiveAlertsForProject("p1"); len(active) != 1 || active[0].Metric != "memoryUsagePercent" {
		t.Fatalf("active = %+v, want one memory alert", active)
	}
	alerts.CheckMetrics(reading(low))
	if active := alerts.GetActiveAlerts(); len(active) != 0 {
		t.Fatalf("active = %+v, want none", active)
	}
	if history := alerts.GetAlertHistory(10); len(history) != 1 || !history[0].Resolved {
		t.Fatalf("history = %+v, want the one alert, resolved", history)
	}
}

type failingMetricsHistory struct{}

func (failingMetricsHistory) AppendMetrics(context.Context, *domain.DatabaseMetrics) error {
	return context.DeadlineExceeded
}

func (failingMetricsHistory) GetMetricsHistory(context.Context, string, int) ([]domain.DatabaseMetrics, error) {
	return nil, context.DeadlineExceeded
}

func (failingMetricsHistory) PruneMetricsBefore(context.Context, time.Time) error {
	return context.DeadlineExceeded
}

type failingAlertStore struct{}

func (failingAlertStore) SaveAlert(context.Context, *domain.Alert) error {
	return context.DeadlineExceeded
}
func (failingAlertStore) GetActiveAlerts(context.Context) ([]domain.Alert, error) {
	return nil, context.DeadlineExceeded
}
func (failingAlertStore) GetActiveAlertsForProject(context.Context, string) ([]domain.Alert, error) {
	return nil, context.DeadlineExceeded
}
func (failingAlertStore) GetAlertHistory(context.Context, int) ([]domain.Alert, error) {
	return nil, context.DeadlineExceeded
}

// A table that does not answer costs the tick, never the process.
func TestSamplingSurvivesATableThatDoesNotAnswer(t *testing.T) {
	metrics, _, alerts, _, _ := samplerFixture(t)
	metrics.SetHistory(failingMetricsHistory{}, alerts)
	alerts.SetStore(failingAlertStore{})

	metrics.SampleAll(context.Background())

	if _, err := metrics.GetMetricsHistory(context.Background(), "live-db", 10); err == nil {
		t.Fatal("expected the table's error")
	}
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("active = %+v, want none from a failing table", got)
	}
}

func TestAHistoryReadWithoutALimitTakesTheDefault(t *testing.T) {
	metrics, history, _, _, _ := samplerFixture(t)
	for minute := 0; minute < defaultHistoryPoints+5; minute++ {
		history.rows = append(history.rows, domain.DatabaseMetrics{ProjectID: "live-db"})
	}
	got, _ := metrics.GetMetricsHistory(context.Background(), "live-db", 0)
	if got.TotalPoints != defaultHistoryPoints {
		t.Fatalf("points = %d, want %d", got.TotalPoints, defaultHistoryPoints)
	}
}

type erroringLeader struct{}

func (erroringLeader) IsLeader(context.Context) (bool, error) { return false, context.DeadlineExceeded }

func TestTheSamplerTicksUntilStopped(t *testing.T) {
	metrics, history, _, _, _ := samplerFixture(t)
	metrics.sampleIfLeader(context.Background(), erroringLeader{})

	stop := metrics.StartSampler(context.Background(), staticLeader(true), 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		history.mu.Lock()
		sampled := len(history.rows)
		history.mu.Unlock()
		if sampled > 0 {
			stop()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	t.Fatal("the sampler never sampled")
}
