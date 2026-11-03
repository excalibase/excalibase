package service

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/requestmetrics"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// ErrUnknownChartRange is a range the request charts do not offer.
var ErrUnknownChartRange = errors.New("range must be 1h, 24h or 7d")

type chartRange struct {
	span time.Duration
	step time.Duration
}

var chartRanges = map[string]chartRange{
	"1h":  {span: time.Hour, step: time.Minute},
	"24h": {span: 24 * time.Hour, step: 15 * time.Minute},
	"7d":  {span: 7 * 24 * time.Hour, step: time.Hour},
}

// RequestMetricsStore is the platform DB's request_metrics table.
type RequestMetricsStore interface {
	AppendRequestMetrics(ctx context.Context, rows []domain.RequestMetricsRow) error
	RequestMetricsSince(ctx context.Context, projectID string, since time.Time) ([]domain.RequestMetricsRow, error)
	PruneRequestMetricsBefore(ctx context.Context, cutoff time.Time) error
}

// RequestTotalsStore is the platform DB's request_metric_totals table (EXC-657).
type RequestTotalsStore interface {
	AddRequestTotals(ctx context.Context, rows []domain.RequestMetricsRow) error
	RequestTotals(ctx context.Context, projectID string) ([]domain.RequestTotalsRow, error)
	PruneRequestTotalsBefore(ctx context.Context, cutoff time.Time) error
}

// RequestScraper reads every graphql and auth pod's request counters.
type RequestScraper interface {
	Scrape(ctx context.Context) ([]requestmetrics.Reading, []error)
}

// RequestMetricsService samples per-project request counts into the table and
// serves them as Studio's request charts (EXC-656).
type RequestMetricsService struct {
	scraper   RequestScraper
	store     RequestMetricsStore
	instances storage.InstanceStore
	tracker   *requestmetrics.Tracker
	now       func() time.Time
	// totals, when set, also sums every sample for the metrics endpoint.
	totals RequestTotalsStore
}

// SetTotals adds every sample to the running totals the metrics endpoint
// exports; wired only while that feature is on (EXC-657).
func (s *RequestMetricsService) SetTotals(totals RequestTotalsStore) {
	s.totals = totals
}

func NewRequestMetricsService(scraper RequestScraper, store RequestMetricsStore, instances storage.InstanceStore) *RequestMetricsService {
	return &RequestMetricsService{
		scraper:   scraper,
		store:     store,
		instances: instances,
		tracker:   requestmetrics.NewTracker(),
		now:       time.Now,
	}
}

// Sample stores what each project did since the previous sample. Only
// projects provisioning knows get rows, whatever the pods report.
func (s *RequestMetricsService) Sample(ctx context.Context) {
	readings, failures := s.scraper.Scrape(ctx)
	if len(failures) > 0 {
		log.Printf("request metrics: %d pod(s) unread, first: %v", len(failures), failures[0])
	}
	now := s.now()
	usage := s.tracker.Advance(now, readings)
	known, err := s.knownProjects()
	if err != nil {
		log.Printf("request metrics: list projects: %v", err)
		return
	}
	rows := make([]domain.RequestMetricsRow, 0, len(usage))
	for key, span := range usage {
		if known[key.Project] && !span.Empty() {
			rows = append(rows, rowOf(key, span, now))
		}
	}
	if err := s.store.AppendRequestMetrics(ctx, rows); err != nil {
		log.Printf("request metrics: record: %v", err)
	}
	if err := s.store.PruneRequestMetricsBefore(ctx, now.Add(-metricsRetention)); err != nil {
		log.Printf("request metrics: prune: %v", err)
	}
	s.addTotals(ctx, rows, now)
}

func (s *RequestMetricsService) addTotals(ctx context.Context, rows []domain.RequestMetricsRow, now time.Time) {
	if s.totals == nil {
		return
	}
	if err := s.totals.AddRequestTotals(ctx, rows); err != nil {
		log.Printf("request metrics: add totals: %v", err)
	}
	if err := s.totals.PruneRequestTotalsBefore(ctx, now.Add(-metricsRetention)); err != nil {
		log.Printf("request metrics: prune totals: %v", err)
	}
}

func (s *RequestMetricsService) knownProjects() (map[string]bool, error) {
	instances, err := s.instances.FindAll()
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(instances))
	for _, inst := range instances {
		known[inst.ProjectID] = true
	}
	return known, nil
}

func rowOf(key requestmetrics.Key, span requestmetrics.Usage, at time.Time) domain.RequestMetricsRow {
	return domain.RequestMetricsRow{
		ProjectID: key.Project, Timestamp: at, Family: key.Family,
		Requests: span.Requests, ClientErrors: span.ClientErrors, ServerErrors: span.ServerErrors,
		LatencyBuckets: span.Buckets, Connections: span.Connections,
		ConnectionsOpened: span.Opened, Messages: span.Messages,
	}
}

// Charts is a project's request history over rangeName, merged per step and
// per family; steps without traffic are left out.
func (s *RequestMetricsService) Chart(ctx context.Context, projectID, rangeName string) (*domain.RequestCharts, error) {
	window, ok := chartRanges[rangeName]
	if !ok {
		return nil, ErrUnknownChartRange
	}
	to := s.now()
	from := to.Add(-window.span)
	rows, err := s.store.RequestMetricsSince(ctx, projectID, from)
	if err != nil {
		return nil, err
	}
	steps := map[time.Time]map[string]requestmetrics.Usage{}
	var order []time.Time
	totals := map[string]requestmetrics.Usage{}
	for _, row := range rows {
		step := row.Timestamp.Truncate(window.step)
		if _, seen := steps[step]; !seen {
			steps[step] = map[string]requestmetrics.Usage{}
			order = append(order, step)
		}
		span := usageOf(row)
		steps[step][row.Family] = steps[step][row.Family].Merge(span)
		totals[row.Family] = totals[row.Family].Merge(span)
	}
	charts := &domain.RequestCharts{
		ProjectID: projectID, Range: rangeName, From: from, To: to,
		StepSeconds: int(window.step.Seconds()),
		Points:      make([]domain.RequestChartPoint, 0, len(rows)),
		Totals:      make([]domain.RequestChartPoint, 0, len(totals)),
	}
	for _, step := range order {
		for _, family := range requestmetrics.Families {
			if span, ok := steps[step][family]; ok {
				charts.Points = append(charts.Points, pointOf(step, family, span))
			}
		}
	}
	for _, family := range requestmetrics.Families {
		if span, ok := totals[family]; ok {
			charts.Totals = append(charts.Totals, pointOf(from, family, span))
		}
	}
	return charts, nil
}

func usageOf(row domain.RequestMetricsRow) requestmetrics.Usage {
	return requestmetrics.Usage{
		Requests: row.Requests, ClientErrors: row.ClientErrors, ServerErrors: row.ServerErrors,
		Buckets: row.LatencyBuckets, Connections: row.Connections,
		Opened: row.ConnectionsOpened, Messages: row.Messages,
	}
}

func pointOf(at time.Time, family string, span requestmetrics.Usage) domain.RequestChartPoint {
	point := domain.RequestChartPoint{
		Timestamp: at, Family: family,
		Requests: span.Requests, ClientErrors: span.ClientErrors, ServerErrors: span.ServerErrors,
		Connections: span.Connections, ConnectionsOpened: span.Opened, Messages: span.Messages,
	}
	if p50, ok := requestmetrics.QuantileMs(span.Buckets, 0.5); ok {
		point.P50Ms = &p50
	}
	if p95, ok := requestmetrics.QuantileMs(span.Buckets, 0.95); ok {
		point.P95Ms = &p95
	}
	return point
}
