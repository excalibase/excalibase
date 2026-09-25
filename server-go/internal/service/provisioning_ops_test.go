package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const (
	testOpsDB         = "ops-db"
	testOpsDBNS       = "org1-ops-db"
	testExpectedErr   = "expected error"
	testOpsDBPostgres = "ops-db-postgres"
)

func setupOpsTest(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	pgProv := provisioner.NewPostgreSQLProvisioner(mock, "")
	factory := provisioner.NewFactory(pgProv)
	svc := NewProvisioningService(store, factory, mock)

	port := 5432
	store.Create(&domain.DatabaseInstance{
		ProjectID: testOpsDB, OrgID: "org1", Namespace: testOpsDBNS,
		DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE", PostgresVersion: "17",
		Host: "h.local", Port: &port, DatabaseName: "app",
		Username: "app", Password: testutil.FixturePassword(testOpsDB), SSLMode: "require",
	})
	mock.ExecOutput["org1-ops-db/ops-db-postgres-1"] = "log line 1\nlog line 2"

	return svc, store, mock
}

func TestSetMaintenanceWindow(t *testing.T) {
	svc, store, _ := setupOpsTest(t)

	err := svc.SetMaintenanceWindow(context.Background(), testOpsDB, domain.MaintenanceWindowConfig{
		Window:          "0 3 * * 0",
		DurationMinutes: 60,
		AutoUpgrade:     true,
	})
	if err != nil {
		t.Fatalf("SetMaintenanceWindow: %v", err)
	}

	inst, _ := store.FindByProjectID(testOpsDB)
	if inst.MaintenanceWindow != "0 3 * * 0" {
		t.Errorf("window: got %s", inst.MaintenanceWindow)
	}
	if inst.MaintenanceWindowDurationMinutes == nil || *inst.MaintenanceWindowDurationMinutes != 60 {
		t.Error("duration not set")
	}
	if inst.AutoMinorVersionUpgrade == nil || !*inst.AutoMinorVersionUpgrade {
		t.Error("auto upgrade not set")
	}
}

func TestGetMaintenanceWindow(t *testing.T) {
	svc, _, _ := setupOpsTest(t)

	svc.SetMaintenanceWindow(context.Background(), testOpsDB, domain.MaintenanceWindowConfig{
		Window: "0 4 * * 1", DurationMinutes: 30, AutoUpgrade: false,
	})

	cfg, err := svc.GetMaintenanceWindow(testOpsDB)
	if err != nil {
		t.Fatalf("GetMaintenanceWindow: %v", err)
	}
	if cfg.Window != "0 4 * * 1" {
		t.Errorf("window: got %s", cfg.Window)
	}
	if cfg.DurationMinutes != 30 {
		t.Errorf("duration: got %d", cfg.DurationMinutes)
	}
}

func TestGetMaintenanceWindowNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	_, err := svc.GetMaintenanceWindow("nonexistent")
	if err == nil {
		t.Error(testExpectedErr)
	}
}

func TestGetLogs(t *testing.T) {
	svc, _, _ := setupOpsTest(t)

	logs, err := svc.GetLogs(context.Background(), testOpsDB, 50)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if logs == "" {
		t.Error("logs should not be empty")
	}
}

func TestGetLogsNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	_, err := svc.GetLogs(context.Background(), "nope", 50)
	if err == nil {
		t.Error(testExpectedErr)
	}
}

// The rotation sequence itself is covered in credential_rotation_test.go,
// which drives it with the vault, verifier and change publisher it needs.

func TestRotateCredentialsNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	_, err := svc.RotateCredentials(context.Background(), "nope")
	if err == nil {
		t.Error(testExpectedErr)
	}
}

func TestGeneratePassword(t *testing.T) {
	p1 := generatePassword(32)
	p2 := generatePassword(32)

	if len(p1) != 32 {
		t.Errorf("length: got %d, want 32", len(p1))
	}
	if p1 == p2 {
		t.Error("passwords should be unique")
	}
}

func TestUpgradeVersion(t *testing.T) {
	svc, _, mock := setupOpsTest(t)

	clusterObj := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: testOpsDB, Namespace: testOpsDBNS,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, clusterObj)

	err := svc.UpgradeVersion(context.Background(), testOpsDB, "17")
	if err != nil {
		t.Fatalf("UpgradeVersion: %v", err)
	}

	// The upgrade resolves the catalogue's digest-pinned image for the major,
	// never a tag built from the version string.
	want, err := config.PostgresImage("17")
	if err != nil {
		t.Fatalf("resolve image: %v", err)
	}
	got, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
	spec := got.Object["spec"].(map[string]interface{})
	if spec["imageName"] != want {
		t.Errorf("imageName: got %v, want %v", spec["imageName"], want)
	}
}

func TestUpgradeVersionNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	err := svc.UpgradeVersion(context.Background(), "nope", "17")
	if err == nil {
		t.Error(testExpectedErr)
	}
}

