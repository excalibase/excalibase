package metrics

import (
	"database/sql"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRegisterPoolStats_ExposesInUseIdleAndWaits(t *testing.T) {
	db := sql.OpenDB(nopConnector{})
	defer db.Close()
	db.SetMaxOpenConns(7)
	if err := RegisterPoolStats(db, "pooltest"); err != nil {
		t.Fatal(err)
	}
	if err := RegisterPoolStats(db, "pooltest"); err != nil {
		t.Fatalf("second registration must be a no-op: %v", err)
	}
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, family := range families {
		seen[family.GetName()] = true
	}
	for _, name := range []string{
		"go_sql_max_open_connections", "go_sql_in_use_connections", "go_sql_idle_connections",
		"go_sql_wait_count_total", "go_sql_wait_duration_seconds_total",
	} {
		if !seen[name] {
			t.Errorf("metric %s is not exposed", name)
		}
	}
}
