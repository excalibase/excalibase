package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/requestmetrics"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

type fakeRequestStore struct {
	mu       sync.Mutex
	rows     []domain.RequestMetricsRow
	prunedAt time.Time
}

func (f *fakeRequestStore) AppendRequestMetrics(_ context.Context, rows []domain.RequestMetricsRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, rows...)
	return nil
}

func (f *fakeRequestStore) RequestMetricsSince(_ context.Context, projectID string, since time.Time) ([]domain.RequestMetricsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.RequestMetricsRow
	for _, row := range f.rows {
		if row.ProjectID == projectID && !row.Timestamp.Before(since) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeRequestStore) PruneRequestMetricsBefore(_ context.Context, cutoff time.Time) error {
	f.prunedAt = cutoff
	return nil
}

type fakeScraper struct{ rounds [][]requestmetrics.Reading }

func (f *fakeScraper) Scrape(context.Context) ([]requestmetrics.Reading, []error) {
	if len(f.rounds) == 0 {
		return nil, []error{errors.New("no pods")}
	}
	round := f.rounds[0]
	f.rounds = f.rounds[1:]
	return round, nil
}

func cumulative(count float64) []float64 {
	buckets := make([]float64, requestmetrics.BucketCount)
	for i := range buckets {
		buckets[i] = count
	}
	return buckets
}

func scrapeOf(instance string, counts map[string]float64) requestmetrics.Reading {
	histograms := map[requestmetrics.SeriesKey][]float64{}
	for project, count := range counts {
		histograms[requestmetrics.SeriesKey{Project: project, Family: "graphql", Outcome: "ok"}] = cumulative(count)
	}
	return requestmetrics.Reading{Instance: instance, Histograms: histograms, Realtime: map[string]requestmetrics.Realtime{}}
}

func requestFixture(t *testing.T, rounds ...[]requestmetrics.Reading) (*RequestMetricsService, *fakeRequestStore, *time.Time) {
	t.Helper()
	instances := fakestore.NewInstances()
	if err := instances.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "org", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	store := &fakeRequestStore{}
	clock := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	svc := NewRequestMetricsService(&fakeScraper{rounds: rounds}, store, instances)
	svc.now = func() time.Time { return clock }
	return svc, store, &clock
}

func TestRequestSample_StoresWhatEachKnownProjectDidSinceTheLastScrape(t *testing.T) {
	svc, store, clock := requestFixture(t,
		[]requestmetrics.Reading{scrapeOf("a", map[string]float64{"p1": 10, "stranger": 5})},
		[]requestmetrics.Reading{scrapeOf("a", map[string]float64{"p1": 13, "stranger": 9})},
	)
	svc.Sample(context.Background())
	if len(store.rows) != 0 {
		t.Fatalf("the first scrape is a baseline: %+v", store.rows)
	}
	*clock = clock.Add(time.Minute)
	svc.Sample(context.Background())

	if len(store.rows) != 1 {
		t.Fatalf("one row, for the project provisioning knows: %+v", store.rows)
	}
	row := store.rows[0]
	if row.ProjectID != "p1" || row.Family != "graphql" || row.Requests != 3 || !row.Timestamp.Equal(*clock) {
		t.Errorf("row: %+v", row)
	}
	if !store.prunedAt.Equal(clock.Add(-metricsRetention)) {
		t.Errorf("pruned at %s, want the metrics retention", store.prunedAt)
	}
}

func TestRequestSample_NothingScrapedStoresNothing(t *testing.T) {
	svc, store, _ := requestFixture(t)
	svc.Sample(context.Background())
	if len(store.rows) != 0 {
		t.Errorf("rows: %+v", store.rows)
	}
}

func bucketsAt(index int, count int64) []int64 {
	buckets := make([]int64, requestmetrics.BucketCount)
	buckets[index] = count
	return buckets
}

func TestRequestCharts_MergesRowsIntoStepsWithPercentilesAcrossThem(t *testing.T) {
	svc, store, clock := requestFixture(t)
	base := clock.Add(-30 * time.Minute)
	store.rows = []domain.RequestMetricsRow{
		{ProjectID: "p1", Timestamp: base, Family: "graphql", Requests: 90, LatencyBuckets: bucketsAt(0, 90)},
		{ProjectID: "p1", Timestamp: base.Add(20 * time.Second), Family: "graphql", Requests: 10, ServerErrors: 10, LatencyBuckets: bucketsAt(6, 10)},
		{ProjectID: "p1", Timestamp: base, Family: "realtime", Connections: 4, Messages: 7},
		{ProjectID: "p1", Timestamp: base.Add(40 * time.Second), Family: "realtime", Connections: 2, Messages: 3},
		{ProjectID: "p1", Timestamp: clock.Add(-2 * time.Hour), Family: "graphql", Requests: 1000},
	}

	charts, err := svc.Charts(context.Background(), "p1", "1h")
	if err != nil {
		t.Fatalf("Charts: %v", err)
	}
	if charts.StepSeconds != 60 || !charts.To.Equal(*clock) || !charts.From.Equal(clock.Add(-time.Hour)) {
		t.Errorf("range: %+v", charts)
	}
	var graphql, realtime *domain.RequestChartPoint
	for i := range charts.Points {
		switch charts.Points[i].Family {
		case "graphql":
			graphql = &charts.Points[i]
		case "realtime":
			realtime = &charts.Points[i]
		}
	}
	if len(charts.Points) != 2 || graphql == nil || realtime == nil {
		t.Fatalf("one step per family: %+v", charts.Points)
	}
	if graphql.Requests != 100 || graphql.ServerErrors != 10 || graphql.P95Ms == nil || *graphql.P95Ms <= 250 {
		t.Errorf("graphql step: %+v", graphql)
	}
	if graphql.P50Ms == nil || *graphql.P50Ms > 5 {
		t.Errorf("p50 stays in the fast bucket: %v", graphql.P50Ms)
	}
	if realtime.Connections != 4 || realtime.Messages != 10 || realtime.P50Ms != nil {
		t.Errorf("realtime keeps the peak of open connections and sums messages: %+v", realtime)
	}
	if len(charts.Totals) != 2 || charts.Totals[0].Family != "graphql" || charts.Totals[0].Requests != 100 {
		t.Errorf("totals over the range, in family order: %+v", charts.Totals)
	}
}

func TestRequestCharts_EachRangeHasItsStep(t *testing.T) {
	svc, _, _ := requestFixture(t)
	for rangeName, step := range map[string]int{"1h": 60, "24h": 900, "7d": 3600} {
		charts, err := svc.Charts(context.Background(), "p1", rangeName)
		if err != nil || charts.StepSeconds != step || charts.Points == nil || charts.Totals == nil {
			t.Errorf("%s: %+v %v", rangeName, charts, err)
		}
	}
	if _, err := svc.Charts(context.Background(), "p1", "30d"); !errors.Is(err, ErrUnknownChartRange) {
		t.Errorf("an unknown range is refused: %v", err)
	}
}

func TestSampleAll_TakesTheRequestSampleToo(t *testing.T) {
	metrics, _, _, _, _ := samplerFixture(t)
	sampled := 0
	metrics.SetRequestSampler(requestSamplerFunc(func(context.Context) { sampled++ }))
	metrics.SampleAll(context.Background())
	if sampled != 1 {
		t.Errorf("request sample taken %d times", sampled)
	}
}

type requestSamplerFunc func(context.Context)

func (f requestSamplerFunc) Sample(ctx context.Context) { f(ctx) }
