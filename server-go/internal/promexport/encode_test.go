package promexport

import (
	"bytes"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

var now = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func floatPtr(v float64) *float64 { return &v }
func int64Ptr(v int64) *int64     { return &v }
func intPtr(v int) *int           { return &v }

func freshDatabase() *domain.DatabaseMetrics {
	return &domain.DatabaseMetrics{
		ProjectID: "p1", Timestamp: &domain.FlexTime{Time: now.Add(-time.Minute)}, MetricsAvailable: true,
		CPUUsagePercent: floatPtr(25), MemoryUsagePercent: floatPtr(50), DiskUsagePercent: floatPtr(10),
		CPUUsageCores: floatPtr(0.25), CPULimitCores: floatPtr(1),
		MemoryUsageMB: int64Ptr(512), MemoryLimitMB: int64Ptr(1024),
		ActiveConnections: intPtr(3), IdleConnections: intPtr(7), MaxConnections: intPtr(100),
	}
}

func graphqlTotals() domain.RequestTotalsRow {
	return domain.RequestTotalsRow{
		ProjectID: "p1", Family: "graphql", Requests: 10, ClientErrors: 2, ServerErrors: 1,
		LatencyBuckets: []int64{5, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}, UpdatedAt: now.Add(-time.Minute),
	}
}

func encode(t *testing.T, snapshot Snapshot) (string, map[string]*dto.MetricFamily) {
	t.Helper()
	var out bytes.Buffer
	if err := Encode(&out, snapshot); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("not Prometheus text format: %v\n%s", err, out.String())
	}
	return out.String(), families
}

func labelsOf(metric *dto.Metric) map[string]string {
	labels := map[string]string{}
	for _, pair := range metric.GetLabel() {
		labels[pair.GetName()] = pair.GetValue()
	}
	return labels
}

func find(t *testing.T, families map[string]*dto.MetricFamily, name string, want map[string]string) *dto.Metric {
	t.Helper()
	family, ok := families[name]
	if !ok {
		t.Fatalf("no %s", name)
	}
	for _, metric := range family.GetMetric() {
		labels := labelsOf(metric)
		match := true
		for key, value := range want {
			if labels[key] != value {
				match = false
			}
		}
		if match {
			return metric
		}
	}
	t.Fatalf("no %s with %v", name, want)
	return nil
}

func TestEncode_DatabaseGaugesInBaseUnits(t *testing.T) {
	_, families := encode(t, Snapshot{ProjectID: "p1", Now: now, Database: freshDatabase()})
	cases := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"excalibase_database_metrics_available", nil, 1},
		{"excalibase_database_cpu_usage_cores", nil, 0.25},
		{"excalibase_database_cpu_limit_cores", nil, 1},
		{"excalibase_database_cpu_usage_ratio", nil, 0.25},
		{"excalibase_database_memory_usage_bytes", nil, 512 * 1024 * 1024},
		{"excalibase_database_memory_limit_bytes", nil, 1024 * 1024 * 1024},
		{"excalibase_database_memory_usage_ratio", nil, 0.5},
		{"excalibase_database_disk_usage_ratio", nil, 0.1},
		{"excalibase_database_connections", map[string]string{"state": "active"}, 3},
		{"excalibase_database_connections", map[string]string{"state": "idle"}, 7},
		{"excalibase_database_max_connections", nil, 100},
		{"excalibase_database_sample_timestamp_seconds", nil, float64(now.Add(-time.Minute).Unix())},
	}
	for _, tc := range cases {
		if families[tc.name].GetType() != dto.MetricType_GAUGE {
			t.Errorf("%s is %v, want a gauge", tc.name, families[tc.name].GetType())
		}
		metric := find(t, families, tc.name, tc.labels)
		if got := metric.GetGauge().GetValue(); got != tc.want {
			t.Errorf("%s%v = %v, want %v", tc.name, tc.labels, got, tc.want)
		}
		if labelsOf(metric)["project"] != "p1" {
			t.Errorf("%s carries project=%q", tc.name, labelsOf(metric)["project"])
		}
	}
}

func TestEncode_AStaleOrMissingDatabaseSampleIsLeftOut(t *testing.T) {
	stale := freshDatabase()
	stale.Timestamp = &domain.FlexTime{Time: now.Add(-6 * time.Minute)}
	for name, database := range map[string]*domain.DatabaseMetrics{"stale": stale, "missing": nil} {
		text, _ := encode(t, Snapshot{ProjectID: "p1", Now: now, Database: database})
		if strings.Contains(text, "excalibase_database_") {
			t.Errorf("%s sample exported:\n%s", name, text)
		}
	}
}

func TestEncode_AnUnmeasuredDatabaseSaysSoAndNothingElse(t *testing.T) {
	database := &domain.DatabaseMetrics{ProjectID: "p1", Timestamp: &domain.FlexTime{Time: now}, MetricsAvailable: false}
	_, families := encode(t, Snapshot{ProjectID: "p1", Now: now, Database: database})
	if got := find(t, families, "excalibase_database_metrics_available", nil).GetGauge().GetValue(); got != 0 {
		t.Errorf("available = %v", got)
	}
	if _, ok := families["excalibase_database_cpu_usage_cores"]; ok {
		t.Error("no usage is exported for an unmeasured sample")
	}
}

func TestEncode_RequestCountersByStatus(t *testing.T) {
	_, families := encode(t, Snapshot{ProjectID: "p1", Now: now, Requests: []domain.RequestTotalsRow{graphqlTotals()}})
	if families["excalibase_requests_total"].GetType() != dto.MetricType_COUNTER {
		t.Fatalf("requests are %v, want a counter", families["excalibase_requests_total"].GetType())
	}
	for status, want := range map[string]float64{"ok": 7, "client_error": 2, "server_error": 1} {
		metric := find(t, families, "excalibase_requests_total", map[string]string{"project": "p1", "surface": "graphql", "status": status})
		if got := metric.GetCounter().GetValue(); got != want {
			t.Errorf("%s = %v, want %v", status, got, want)
		}
	}
}

func TestEncode_LatencyIsACumulativeHistogram(t *testing.T) {
	text, families := encode(t, Snapshot{ProjectID: "p1", Now: now, Requests: []domain.RequestTotalsRow{graphqlTotals()}})
	family := families["excalibase_request_duration_seconds"]
	if family.GetType() != dto.MetricType_HISTOGRAM {
		t.Fatalf("latency is %v, want a histogram", family.GetType())
	}
	histogram := find(t, families, "excalibase_request_duration_seconds", map[string]string{"surface": "graphql"}).GetHistogram()
	if histogram.GetSampleCount() != 10 {
		t.Errorf("count = %d", histogram.GetSampleCount())
	}
	want := map[float64]uint64{0.005: 5, 0.01: 8, 0.025: 8, 10: 8}
	for _, bucket := range histogram.GetBucket() {
		if expected, ok := want[bucket.GetUpperBound()]; ok && bucket.GetCumulativeCount() != expected {
			t.Errorf("le=%v: %d, want %d", bucket.GetUpperBound(), bucket.GetCumulativeCount(), expected)
		}
	}
	if !strings.Contains(text, `excalibase_request_duration_seconds_bucket{project="p1",surface="graphql",le="+Inf"} 10`) {
		t.Errorf("+Inf bucket equals the count:\n%s", text)
	}
	if strings.Contains(text, "excalibase_request_duration_seconds_sum") {
		t.Error("no _sum: the platform does not record total latency")
	}
}

func TestEncode_HistogramBucketsOfAnotherLayoutAreLeftOut(t *testing.T) {
	row := graphqlTotals()
	row.LatencyBuckets = []int64{1, 2, 3}
	text, _ := encode(t, Snapshot{ProjectID: "p1", Now: now, Requests: []domain.RequestTotalsRow{row}})
	if strings.Contains(text, "excalibase_request_duration_seconds") {
		t.Errorf("buckets that do not match the bounds are not guessed at:\n%s", text)
	}
	if !strings.Contains(text, "excalibase_requests_total") {
		t.Error("the counts are still exported")
	}
}

func TestEncode_Realtime(t *testing.T) {
	realtime := domain.RequestTotalsRow{ProjectID: "p1", Family: "realtime", Connections: 4, ConnectionsOpened: 9, Messages: 120, UpdatedAt: now.Add(-time.Minute)}
	_, families := encode(t, Snapshot{ProjectID: "p1", Now: now, Requests: []domain.RequestTotalsRow{realtime}})
	if got := find(t, families, "excalibase_realtime_connections", nil).GetGauge().GetValue(); got != 4 {
		t.Errorf("connections = %v", got)
	}
	if got := find(t, families, "excalibase_realtime_connections_opened_total", nil).GetCounter().GetValue(); got != 9 {
		t.Errorf("opened = %v", got)
	}
	if got := find(t, families, "excalibase_realtime_messages_total", nil).GetCounter().GetValue(); got != 120 {
		t.Errorf("messages = %v", got)
	}
	if _, ok := families["excalibase_requests_total"]; ok {
		t.Error("realtime is not a request surface")
	}
}

// A realtime row is only written while something happens, so an old reading
// is no longer what is open.
func TestEncode_OldRealtimeConnectionsReadZero(t *testing.T) {
	realtime := domain.RequestTotalsRow{ProjectID: "p1", Family: "realtime", Connections: 4, ConnectionsOpened: 9, UpdatedAt: now.Add(-2 * time.Minute)}
	_, families := encode(t, Snapshot{ProjectID: "p1", Now: now, Requests: []domain.RequestTotalsRow{realtime}})
	if got := find(t, families, "excalibase_realtime_connections", nil).GetGauge().GetValue(); got != 0 {
		t.Errorf("connections = %v", got)
	}
	if got := find(t, families, "excalibase_realtime_connections_opened_total", nil).GetCounter().GetValue(); got != 9 {
		t.Errorf("a counter keeps its total: %v", got)
	}
}

func TestEncode_OnlyTheSnapshotsProjectIsWritten(t *testing.T) {
	foreign := graphqlTotals()
	foreign.ProjectID = "p2"
	text, _ := encode(t, Snapshot{ProjectID: "p1", Now: now, Requests: []domain.RequestTotalsRow{foreign}})
	if strings.Contains(text, "p2") {
		t.Errorf("another project's rows are never written:\n%s", text)
	}
}

func TestEncode_LabelValuesAreEscaped(t *testing.T) {
	_, families := encode(t, Snapshot{ProjectID: "a\"b\\c\nd", Now: now, Database: &domain.DatabaseMetrics{
		ProjectID: "a\"b\\c\nd", Timestamp: &domain.FlexTime{Time: now}, MetricsAvailable: false}})
	metric := find(t, families, "excalibase_database_metrics_available", nil)
	if got := labelsOf(metric)["project"]; got != "a\"b\\c\nd" {
		t.Errorf("project label round-trips: %q", got)
	}
}

func TestEncode_EveryFamilyHasHelpAndType(t *testing.T) {
	text, families := encode(t, Snapshot{ProjectID: "p1", Now: now, Database: freshDatabase(),
		Requests: []domain.RequestTotalsRow{graphqlTotals(), {ProjectID: "p1", Family: "realtime", Connections: 1, UpdatedAt: now}}})
	for name, family := range families {
		if family.GetHelp() == "" {
			t.Errorf("%s has no HELP", name)
		}
		if strings.Count(text, "# TYPE "+name+" ") != 1 {
			t.Errorf("%s needs exactly one TYPE line", name)
		}
	}
}

func TestEncode_NothingToSayIsAnEmptyBody(t *testing.T) {
	text, _ := encode(t, Snapshot{ProjectID: "p1", Now: now})
	if text != "" {
		t.Errorf("body = %q", text)
	}
}
