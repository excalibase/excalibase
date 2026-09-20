package service

import (
	"context"
	"strings"
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
		DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE",
		Host: "h.local", Port: &port, DatabaseName: "app",
		Username: "app", Password: testutil.FixturePassword(testOpsDB), SSLMode: "require",
	})
	mock.ExecOutput["org1-ops-db/ops-db-postgres-1"] = "log line 1\nlog line 2"

	return svc, store, mock
}

func TestSetMaintenanceWindow(t *testing.T) {
	svc, store, _ := setupOpsTest(t)

	err := svc.SetMaintenanceWindow(testOpsDB, domain.MaintenanceWindowConfig{
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

	svc.SetMaintenanceWindow(testOpsDB, domain.MaintenanceWindowConfig{
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

func TestUpdateParametersLegacy(t *testing.T) {
	// Covered by TestUpdateParametersPatchesCRD
	t.Skip("replaced by TestUpdateParametersPatchesCRD")
}

func TestUpdateParametersPatchesCRD(t *testing.T) {
	svc, _, mock := setupOpsTest(t)

	// Seed CRD
	clusterObj := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: testOpsDB, Namespace: testOpsDBNS,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, clusterObj)

	err := svc.UpdateParameters(context.Background(), testOpsDB, map[string]string{
		"max_connections": "200",
		"work_mem":        "64MB",
	})
	if err != nil {
		t.Fatalf("UpdateParameters: %v", err)
	}

	// Verify CRD was patched
	got, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
	spec := got.Object["spec"].(map[string]interface{})
	pg := spec["postgresql"].(map[string]interface{})
	params := pg["parameters"].(map[string]interface{})
	if params["max_connections"] != "200" {
		t.Errorf("max_connections: got %v", params["max_connections"])
	}
	if params["work_mem"] != "64MB" {
		t.Errorf("work_mem: got %v", params["work_mem"])
	}
}

func TestUpdateParametersNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	err := svc.UpdateParameters(context.Background(), "nope", map[string]string{"k": "v"})
	if err == nil {
		t.Error(testExpectedErr)
	}
}

func TestEnablePooler(t *testing.T) {
	svc, store, mock := setupOpsTest(t)

	err := svc.EnablePooler(context.Background(), testOpsDB, domain.PoolerSettings{
		Enabled:  true,
		PoolMode: "transaction",
		PoolSize: 20,
	})
	if err != nil {
		t.Fatalf("EnablePooler: %v", err)
	}

	// Verify Pooler CRD was created
	if _, ok := mock.CRDs["org1-ops-db/ops-db-postgres-pooler"]; !ok {
		t.Error("Pooler CRD not created")
	}

	// Verify instance updated
	inst, _ := store.FindByProjectID(testOpsDB)
	if inst.PoolerEnabled == nil || !*inst.PoolerEnabled {
		t.Error("poolerEnabled should be true")
	}
}

func TestEnablePoolerNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	err := svc.EnablePooler(context.Background(), "nope", domain.PoolerSettings{Enabled: true})
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

func TestResizeStorage(t *testing.T) {
	svc, _, mock := setupOpsTest(t)

	// Seed CRD
	clusterObj := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: testOpsDB, Namespace: testOpsDBNS,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, clusterObj)

	err := svc.ResizeStorage(context.Background(), testOpsDB, "20Gi")
	if err != nil {
		t.Fatalf("ResizeStorage: %v", err)
	}

	// Verify CRD was patched with new storage size
	got, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
	spec := got.Object["spec"].(map[string]interface{})
	storage := spec["storage"].(map[string]interface{})
	if storage["size"] != "20Gi" {
		t.Errorf("storage size: got %v, want 20Gi", storage["size"])
	}
}

func TestResizeStorageNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	err := svc.ResizeStorage(context.Background(), "nope", "20Gi")
	if err == nil {
		t.Error("expected error for nonexistent project")
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

func TestCloneDatabase(t *testing.T) {
	svc, _, mock := setupOpsTest(t)

	resp, err := svc.CloneDatabase(context.Background(), testOpsDB, domain.CloneRequest{
		NewProjectName: "ops-db-clone",
	})
	if err != nil {
		t.Fatalf("CloneDatabase: %v", err)
	}
	// The clone id is generated; "ops-db-clone" is only its display name.
	if !strings.HasPrefix(resp.ProjectID, "proj-") {
		t.Errorf("projectId must be server-generated, got %s", resp.ProjectID)
	}
	if resp.ProjectName != "ops-db-clone" {
		t.Errorf("projectName: got %s", resp.ProjectName)
	}

	// Verify namespace created
	if !mock.Namespaces["org1-"+resp.ProjectID] {
		t.Error("clone namespace not created")
	}

	// Verify CRD applied with pg_basebackup bootstrap
	crd, ok := mock.CRDs["org1-"+resp.ProjectID+"/"+resp.ProjectID+"-postgres"]
	if !ok {
		t.Fatal("clone Cluster CRD not created")
	}
	spec := crd.Object["spec"].(map[string]interface{})
	bootstrap := spec["bootstrap"].(map[string]interface{})
	if _, ok := bootstrap["pg_basebackup"]; !ok {
		t.Error("bootstrap should use pg_basebackup")
	}
}

func TestCloneDatabaseNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	_, err := svc.CloneDatabase(context.Background(), "nope", domain.CloneRequest{
		NewProjectName: "clone",
	})
	if err == nil {
		t.Error(testExpectedErr)
	}
}

func TestScaleTier(t *testing.T) {
	svc, store, mock := setupOpsTest(t)

	// Seed a CRD so GetCRD succeeds
	mock.SetupPostgreSQLMock(testOpsDB, testOpsDBNS, 1)
	clusterObj := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: testOpsDB, Namespace: testOpsDBNS,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, clusterObj)

	err := svc.ScaleTier(context.Background(), testOpsDB, domain.Standard)
	if err != nil {
		t.Fatalf("ScaleTier: %v", err)
	}

	// Verify store updated
	inst, _ := store.FindByProjectID(testOpsDB)
	if inst.Tier != domain.Standard {
		t.Errorf("tier: got %s, want STANDARD", inst.Tier)
	}

	// Verify CRD was patched with new instances/resources. Tiers are
	// single-instance (no HA) for the alpha, so scaling tunes CPU/RAM only.
	got, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
	spec := got.Object["spec"].(map[string]interface{})
	if spec["instances"] != int64(1) {
		t.Errorf("CRD instances: got %v, want 1", spec["instances"])
	}
	resources := spec["resources"].(map[string]interface{})
	requests := resources["requests"].(map[string]interface{})
	if requests["memory"] != "4Gi" {
		t.Errorf("CRD memory: got %v, want 4Gi", requests["memory"])
	}
}
