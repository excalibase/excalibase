package service

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestParseLabeledMetrics(t *testing.T) {
	raw := `# HELP cnpg_backends_total Number of backends
# TYPE cnpg_backends_total gauge
cnpg_backends_total{application_name="cnpg_metrics_exporter",datname="app",state="active",usename="postgres"} 1
cnpg_backends_total{application_name="",datname="",state="idle",usename="postgres"} 2
cnpg_pg_database_size_bytes{datname="app"} 7926243
cnpg_pg_database_size_bytes{datname="postgres"} 7729635
cnpg_pg_settings_setting{name="max_connections"} 100
cnpg_collector_last_available_backup_timestamp 1.772911385e+09
`
	labeled := parseLabeledMetrics(raw)

	// Total active connections
	total := sumMetric(labeled, "cnpg_backends_total")
	if total != 3 { // 1 active + 2 idle
		t.Errorf("total backends: got %d, want 3", total)
	}

	// Idle connections
	idle := sumMetricFiltered(labeled, "cnpg_backends_total", "\"idle\"")
	if idle != 2 {
		t.Errorf("idle backends: got %d, want 2", idle)
	}

	// Max connections
	maxConn, ok := labeled[`cnpg_pg_settings_setting{name="max_connections"}`]
	if !ok || int(maxConn) != 100 {
		t.Errorf("max_connections: got %v", maxConn)
	}

	// DB size
	dbSize, ok := labeled[`cnpg_pg_database_size_bytes{datname="app"}`]
	if !ok || int(dbSize) != 7926243 {
		t.Errorf("db size: got %v", dbSize)
	}

	// Backup timestamp
	backupTs, ok := labeled["cnpg_collector_last_available_backup_timestamp"]
	if !ok || backupTs < 1e9 {
		t.Errorf("backup timestamp: got %v", backupTs)
	}
}

func TestSumMetricEmpty(t *testing.T) {
	labeled := map[string]float64{}
	if sumMetric(labeled, "nonexistent") != 0 {
		t.Error("expected 0 for empty map")
	}
}

func TestTierInstanceCount(t *testing.T) {
	tests := []struct {
		tier domain.TierType
		want int
	}{
		{domain.Free, 1},
		{domain.Standard, 3},
		{domain.Enterprise, 5},
	}
	for _, tt := range tests {
		got := tierInstanceCount(tt.tier)
		if got != tt.want {
			t.Errorf("%s: got %d, want %d", tt.tier, got, tt.want)
		}
	}
}
