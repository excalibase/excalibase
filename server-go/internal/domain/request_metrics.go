package domain

import "time"

// RequestMetricsRow is what one project's family did over one sample interval
// (EXC-656). LatencyBuckets are per-bucket counts over requestmetrics.Bounds,
// kept so any range can merge them into exact percentiles.
type RequestMetricsRow struct {
	ProjectID         string
	Timestamp         time.Time
	Family            string
	Requests          int64
	ClientErrors      int64
	ServerErrors      int64
	LatencyBuckets    []int64
	Connections       int64
	ConnectionsOpened int64
	Messages          int64
}

// RequestChartPoint is one family over one step of a chart, or over the whole
// range in the totals. Percentiles are absent when there were no requests.
type RequestChartPoint struct {
	Timestamp         time.Time `json:"timestamp"`
	Family            string    `json:"family"`
	Requests          int64     `json:"requests"`
	ClientErrors      int64     `json:"clientErrors"`
	ServerErrors      int64     `json:"serverErrors"`
	P50Ms             *float64  `json:"p50Ms"`
	P95Ms             *float64  `json:"p95Ms"`
	Connections       int64     `json:"connections"`
	ConnectionsOpened int64     `json:"connectionsOpened"`
	Messages          int64     `json:"messages"`
}

// RequestCharts is a project's request history for Studio's charts.
type RequestCharts struct {
	ProjectID   string              `json:"projectId"`
	Range       string              `json:"range"`
	From        time.Time           `json:"from"`
	To          time.Time           `json:"to"`
	StepSeconds int                 `json:"stepSeconds"`
	Points      []RequestChartPoint `json:"points"`
	Totals      []RequestChartPoint `json:"totals"`
}

// RequestTotalsRow is a project's family summed over every sample since the
// metrics endpoint was turned on (EXC-657): monotonic, so it exports as
// Prometheus counters. Connections is the latest reading, at UpdatedAt.
type RequestTotalsRow struct {
	ProjectID         string
	Family            string
	Requests          int64
	ClientErrors      int64
	ServerErrors      int64
	LatencyBuckets    []int64
	Connections       int64
	ConnectionsOpened int64
	Messages          int64
	UpdatedAt         time.Time
}
