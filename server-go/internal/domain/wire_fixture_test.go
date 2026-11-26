package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The Studio types its reads against these same files, so a field the server
// stops sending, or renames, fails a test on both sides (EXC-893).
func assertWireFixture(t *testing.T, name string, dto any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "shared-fixtures", "wire", name))
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	sent, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal(sent, &got); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("%s is not what the server sends\nserver:  %s\nfixture: %s", name, gotJSON, wantJSON)
	}
}

func TestAlertWireShape(t *testing.T) {
	value, threshold := 95.0, 90.0
	at := FlexTime{Time: time.Date(2026, 11, 20, 10, 15, 30, 0, time.UTC)}
	assertWireFixture(t, "alert.json", Alert{
		ID: "alert-1", ProjectID: "proj-1", Severity: "CRITICAL",
		Message: "Connections are at 95% of the limit", Metric: "connections",
		Value: &value, Threshold: &threshold, Timestamp: &at,
	})
}

func TestPerformanceSummaryWireShape(t *testing.T) {
	active, total, slow := 3, 12, 2
	hit, avg := 99.4, 12.5
	assertWireFixture(t, "performance-summary.json", PerformanceSummary{
		ActiveConnections: &active, TotalConnections: &total, CacheHitRatio: &hit,
		DatabaseSize: "48 MB", SlowQueryCount: &slow, AvgQueryTimeMs: &avg,
		Available: false, UnavailableReason: "pg_stat_statements extension is not enabled",
	})
}

func TestQueryStatWireShape(t *testing.T) {
	assertWireFixture(t, "query-stat.json", QueryStat{
		Query: "SELECT * FROM orders WHERE id = $1", Calls: 120, TotalExecTimeMs: 840.5,
		AvgExecTimeMs: 7, MinExecTimeMs: 1.5, MaxExecTimeMs: 40, Rows: 120,
	})
}

func TestWaitEventWireShape(t *testing.T) {
	assertWireFixture(t, "wait-event.json", WaitEvent{WaitEventType: "IO", WaitEvent: "DataFileRead", Count: 4})
}
