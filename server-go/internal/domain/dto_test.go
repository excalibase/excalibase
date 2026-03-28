package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestProvisioningRequestRoundTrip(t *testing.T) {
	req := ProvisioningRequest{
		ProjectName: "test-db",
		OrgID:       "org1",
		DBType:      PostgreSQL,
		Tier:        Standard,
		Backup:      &BackupSettings{Enabled: true, Schedule: "0 2 * * *", Retention: 30},
		Parameters:  map[string]string{"shared_preload_libraries": "pg_stat_statements"},
		Tags:        map[string]string{"env": "demo"},
	}

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ProvisioningRequest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ProjectName != "test-db" {
		t.Errorf("projectName: got %s, want test-db", got.ProjectName)
	}
	if got.DBType != PostgreSQL {
		t.Errorf("databaseType: got %s, want POSTGRESQL", got.DBType)
	}
	if got.Tier != Standard {
		t.Errorf("tier: got %s, want STANDARD", got.Tier)
	}
	if got.Backup == nil || !got.Backup.Enabled {
		t.Error("backup should be enabled")
	}
	if got.Parameters["shared_preload_libraries"] != "pg_stat_statements" {
		t.Error("parameters not preserved")
	}
}

func TestDatabaseMetricsNullFields(t *testing.T) {
	m := DatabaseMetrics{
		ProjectID:        "test-db",
		MetricsAvailable: false,
		UnavailableReason: strPtr("Prometheus not reachable"),
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]interface{}
	json.Unmarshal(b, &raw)

	// Null pointer fields should serialize as null
	if raw["cpuUsagePercent"] != nil {
		t.Errorf("cpuUsagePercent should be null, got %v", raw["cpuUsagePercent"])
	}
	if raw["activeConnections"] != nil {
		t.Errorf("activeConnections should be null, got %v", raw["activeConnections"])
	}
	if raw["metricsAvailable"] != false {
		t.Errorf("metricsAvailable should be false")
	}
	if raw["unavailableReason"] != "Prometheus not reachable" {
		t.Errorf("unavailableReason wrong: %v", raw["unavailableReason"])
	}
}

func TestDatabaseMetricsWithRealData(t *testing.T) {
	now := &FlexTime{Time: time.Now()}
	active := 3
	maxConn := 100
	m := DatabaseMetrics{
		ProjectID:         "duke-database",
		Timestamp:         now,
		MetricsAvailable:  true,
		ActiveConnections: &active,
		MaxConnections:    &maxConn,
		HealthStatus:      "HEALTHY",
	}

	b, _ := json.Marshal(m)
	var raw map[string]interface{}
	json.Unmarshal(b, &raw)

	if raw["activeConnections"] != float64(3) {
		t.Errorf("activeConnections: got %v", raw["activeConnections"])
	}
	if raw["maxConnections"] != float64(100) {
		t.Errorf("maxConnections: got %v", raw["maxConnections"])
	}
	if raw["metricsAvailable"] != true {
		t.Error("metricsAvailable should be true")
	}
	// CPU should still be null
	if raw["cpuUsagePercent"] != nil {
		t.Errorf("cpuUsagePercent should be null without metrics-server")
	}
}

func strPtr(s string) *string { return &s }
