package requestmetrics

import (
	"strings"
	"testing"
)

// engineScrape is shaped like Micrometer's output; authScrape like client_golang's.
const engineScrape = `# HELP excalibase_requests_seconds
# TYPE excalibase_requests_seconds histogram
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="0.005"} 1
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="0.01"} 2
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="0.025"} 3
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="0.05"} 3
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="0.1"} 3
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="0.25"} 3
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="0.5"} 3
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="1.0"} 3
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="2.5"} 3
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="5.0"} 3
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="10.0"} 3
excalibase_requests_seconds_bucket{family="graphql",outcome="ok",project="p1",le="+Inf"} 4
excalibase_requests_seconds_count{family="graphql",outcome="ok",project="p1"} 4
excalibase_requests_seconds_sum{family="graphql",outcome="ok",project="p1"} 12.5
excalibase_requests_seconds_max{family="graphql",outcome="ok",project="p1"} 11.0
excalibase_requests_seconds_bucket{family="rest",outcome="server_error",project="p1",le="+Inf"} 2
excalibase_realtime_connections{project="p1"} 3.0
excalibase_realtime_connections_opened_total{project="p1"} 5.0
excalibase_realtime_messages_total{project="p1"} 40.0
excalibase_request_metrics_info{instance="11111111-2222-3333-4444-555555555555"} 1.7614e+09
excalibase_request_metrics_dropped_total 0.0
`

const authScrape = `excalibase_requests_seconds_bucket{family="auth",outcome="client_error",project="p2",le="0.005"} 0
excalibase_requests_seconds_bucket{family="auth",outcome="client_error",project="p2",le="+Inf"} 7
excalibase_request_metrics_info{instance="aaaa"} 1.761e+09
`

func TestParse_ReadsHistogramsByProjectFamilyAndOutcome(t *testing.T) {
	reading, err := Parse(engineScrape)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if reading.Instance != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("instance: %q", reading.Instance)
	}
	ok := reading.Histograms[SeriesKey{Project: "p1", Family: "graphql", Outcome: "ok"}]
	want := []float64{1, 2, 3, 3, 3, 3, 3, 3, 3, 3, 3, 4}
	if len(ok) != len(want) {
		t.Fatalf("buckets: %v", ok)
	}
	for i := range want {
		if ok[i] != want[i] {
			t.Errorf("bucket %d: got %v, want %v", i, ok[i], want[i])
		}
	}
	if got := reading.Histograms[SeriesKey{Project: "p1", Family: "rest", Outcome: "server_error"}]; got[BucketCount-1] != 2 {
		t.Errorf("a series with only +Inf counts its requests: %v", got)
	}
	if got := reading.Realtime["p1"]; got != (Realtime{Connections: 3, Opened: 5, Messages: 40}) {
		t.Errorf("realtime: %+v", got)
	}
}

func TestParse_ReadsClientGolangLabelsAndBounds(t *testing.T) {
	reading, err := Parse(authScrape)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := reading.Histograms[SeriesKey{Project: "p2", Family: "auth", Outcome: "client_error"}]
	if got[0] != 0 || got[BucketCount-1] != 7 {
		t.Errorf("auth buckets: %v", got)
	}
}

func TestParse_IgnoresUnknownFamiliesOutcomesAndBounds(t *testing.T) {
	text := `excalibase_requests_seconds_bucket{family="admin",outcome="ok",project="p1",le="+Inf"} 9
excalibase_requests_seconds_bucket{family="rest",outcome="weird",project="p1",le="+Inf"} 9
excalibase_requests_seconds_bucket{family="rest",outcome="ok",project="p1",le="0.3"} 9
excalibase_requests_seconds_bucket{family="rest",outcome="ok",project="",le="+Inf"} 9
excalibase_request_metrics_info{instance="x"} 1
`
	reading, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for key, buckets := range reading.Histograms {
		for _, count := range buckets {
			if count != 0 {
				t.Errorf("%+v recorded %v", key, buckets)
			}
		}
	}
}

func TestParse_RefusesAScrapeWithoutAnInstance(t *testing.T) {
	if _, err := Parse(strings.Replace(engineScrape, "excalibase_request_metrics_info", "other_info", 1)); err == nil {
		t.Error("without an instance id a counter reset cannot be told apart")
	}
}

func TestParse_HandlesEscapedLabelValuesAndTimestamps(t *testing.T) {
	text := `excalibase_requests_seconds_bucket{project="p\"1",family="rest",outcome="ok",le="+Inf"} 3 1700000000000
excalibase_request_metrics_info{instance="x"} 1
`
	reading, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := reading.Histograms[SeriesKey{Project: `p"1`, Family: "rest", Outcome: "ok"}]; got[BucketCount-1] != 3 {
		t.Errorf("escaped label: %v", reading.Histograms)
	}
}
