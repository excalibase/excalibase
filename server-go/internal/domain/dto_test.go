package domain

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const (
	testDTODBName        = "test-db"
	testDTOMarshalFmt    = "marshal: %v"
	testDTOUnmarshalFmt  = "unmarshal: %v"
	testDTORestoredName  = "my-restored-project"
	testDTOProjID        = "proj-id-123"
	testDTOGetNewFmt     = "GetNewProject: got %q, want %q"
	testDTONameWins      = "name-wins"
)


func TestProvisioningRequestRoundTrip(t *testing.T) {
	req := ProvisioningRequest{
		ProjectName: testDTODBName,
		OrgID:       "org1",
		DBType:      PostgreSQL,
		Tier:        Standard,
		Backup:      &BackupSettings{Enabled: true, Schedule: "0 2 * * *", Retention: 30},
		Parameters:  map[string]string{"shared_preload_libraries": "pg_stat_statements"},
		Tags:        map[string]string{"env": "demo"},
	}

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf(testDTOMarshalFmt, err)
	}

	var got ProvisioningRequest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf(testDTOUnmarshalFmt, err)
	}

	if got.ProjectName != testDTODBName {
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
		ProjectID:        testDTODBName,
		MetricsAvailable: false,
		UnavailableReason: strPtr("Prometheus not reachable"),
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf(testDTOMarshalFmt, err)
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

// --- RestoreRequest.GetNewProject ---

func TestRestoreRequestGetNewProjectReturnsName(t *testing.T) {
	r := RestoreRequest{
		NewProjectName: testDTORestoredName,
		NewProjectID:   testDTOProjID,
	}
	got := r.GetNewProject()
	if got != testDTORestoredName {
		t.Errorf(testDTOGetNewFmt, got, testDTORestoredName)
	}
}

func TestRestoreRequestGetNewProjectFallsBackToID(t *testing.T) {
	r := RestoreRequest{
		NewProjectName: "",
		NewProjectID:   testDTOProjID,
	}
	got := r.GetNewProject()
	if got != testDTOProjID {
		t.Errorf(testDTOGetNewFmt, got, testDTOProjID)
	}
}

func TestRestoreRequestGetNewProjectBothEmpty(t *testing.T) {
	r := RestoreRequest{}
	got := r.GetNewProject()
	if got != "" {
		t.Errorf("GetNewProject: expected empty string, got %q", got)
	}
}

func TestRestoreRequestGetNewProjectNameTakesPriority(t *testing.T) {
	r := RestoreRequest{
		NewProjectName: testDTONameWins,
		NewProjectID:   "",
	}
	got := r.GetNewProject()
	if got != testDTONameWins {
		t.Errorf(testDTOGetNewFmt, got, testDTONameWins)
	}
}

// --- ProvisioningResponse JSON ---

func TestProvisioningResponseJSON(t *testing.T) {
	port := 5432
	resp := ProvisioningResponse{
		ProjectID:    "db-1",
		Status:       "ACTIVE",
		CurrentStage: StageCompleted,
		Namespace:    "org1-db-1",
		Host:         "db-1.svc.cluster.local",
		Port:         &port,
		DatabaseName: "myapp",
	}

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf(testDTOMarshalFmt, err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf(testDTOUnmarshalFmt, err)
	}

	if raw["projectId"] != "db-1" {
		t.Errorf("projectId: got %v", raw["projectId"])
	}
	if raw["status"] != "ACTIVE" {
		t.Errorf("status: got %v", raw["status"])
	}
	if raw["port"] != float64(5432) {
		t.Errorf("port: got %v", raw["port"])
	}
}

// --- CredentialsResponse ---

func TestCredentialsResponseFields(t *testing.T) {
	user := testutil.FixtureToken("user")
	pass := testutil.FixturePassword("creds")
	resp := CredentialsResponse{
		ProjectID:    "p1",
		Host:         "db.local",
		Port:         5432,
		DatabaseName: "app",
		Username:     user,
		Password:     pass,
		SSLMode:      "require",
		ConnectionURL: fmt.Sprintf("postgresql://%s:%s@db.local:5432/app?sslmode=require", user, pass),
	}

	b, _ := json.Marshal(resp)
	var raw map[string]interface{}
	json.Unmarshal(b, &raw)

	if raw["connectionUrl"] != resp.ConnectionURL {
		t.Errorf("connectionUrl: got %v", raw["connectionUrl"])
	}
	if raw["sslMode"] != "require" {
		t.Errorf("sslMode: got %v", raw["sslMode"])
	}
}

// --- BackupRecord ---

func TestBackupRecordJSON(t *testing.T) {
	rec := BackupRecord{
		ID:        "bk-1",
		ProjectID: "proj-1",
		Timestamp: "2025-01-01T00:00:00Z",
		Type:      "MANUAL",
		Status:    "COMPLETED",
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf(testDTOMarshalFmt, err)
	}
	var got BackupRecord
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf(testDTOUnmarshalFmt, err)
	}
	if got.Type != "MANUAL" {
		t.Errorf("type: got %s", got.Type)
	}
	if got.Status != "COMPLETED" {
		t.Errorf("status: got %s", got.Status)
	}
}
