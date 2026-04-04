package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func setupOpsTest(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	pgProv := provisioner.NewPostgreSQLProvisioner(mock)
	factory := provisioner.NewFactory(pgProv)
	svc := NewProvisioningService(store, factory, mock)

	port := 5432
	store.Save(&domain.DatabaseInstance{
		ProjectID: "ops-db", OrgID: "org1", Namespace: "org1-ops-db",
		DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE",
		Host: "h.local", Port: &port, DatabaseName: "app",
		Username: "app", Password: "oldpass", SSLMode: "require",
	})
	mock.ExecOutput["org1-ops-db/ops-db-postgres-1"] = "log line 1\nlog line 2"

	return svc, store, mock
}

func TestSetMaintenanceWindow(t *testing.T) {
	svc, store, _ := setupOpsTest(t)

	err := svc.SetMaintenanceWindow("ops-db", domain.MaintenanceWindowConfig{
		Window:          "0 3 * * 0",
		DurationMinutes: 60,
		AutoUpgrade:     true,
	})
	if err != nil {
		t.Fatalf("SetMaintenanceWindow: %v", err)
	}

	inst, _ := store.FindByProjectID("ops-db")
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

	svc.SetMaintenanceWindow("ops-db", domain.MaintenanceWindowConfig{
		Window: "0 4 * * 1", DurationMinutes: 30, AutoUpgrade: false,
	})

	cfg, err := svc.GetMaintenanceWindow("ops-db")
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
		t.Error("expected error")
	}
}

func TestGetLogs(t *testing.T) {
	svc, _, _ := setupOpsTest(t)

	logs, err := svc.GetLogs(context.Background(), "ops-db", 50)
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
		t.Error("expected error")
	}
}

func TestRotateCredentials(t *testing.T) {
	svc, store, mock := setupOpsTest(t)
	mock.ExecOutput["org1-ops-db/ops-db-postgres-1"] = "ALTER ROLE"

	creds, err := svc.RotateCredentials(context.Background(), "ops-db")
	if err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	if creds.Password == "oldpass" {
		t.Error("password should have changed")
	}
	if creds.Password == "" {
		t.Error("new password should not be empty")
	}

	// Verify stored
	inst, _ := store.FindByProjectID("ops-db")
	if inst.Password == "oldpass" {
		t.Error("stored password should be updated")
	}
}

func TestRotateCredentialsNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	_, err := svc.RotateCredentials(context.Background(), "nope")
	if err == nil {
		t.Error("expected error")
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
		ProjectID: "ops-db", Namespace: "org1-ops-db",
		Tier: struct{ Instances int; StorageSize, Memory, CPU string }{1, "5Gi", "512Mi", "0.5"},
	})
	mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, "org1-ops-db", clusterObj)

	err := svc.UpdateParameters(context.Background(), "ops-db", map[string]string{
		"max_connections":  "200",
		"work_mem":         "64MB",
	})
	if err != nil {
		t.Fatalf("UpdateParameters: %v", err)
	}

	// Verify CRD was patched
	got, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, "org1-ops-db", "ops-db-postgres")
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
		t.Error("expected error")
	}
}

func TestEnablePooler(t *testing.T) {
	svc, store, mock := setupOpsTest(t)

	err := svc.EnablePooler(context.Background(), "ops-db", domain.PoolerSettings{
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
	inst, _ := store.FindByProjectID("ops-db")
	if inst.PoolerEnabled == nil || !*inst.PoolerEnabled {
		t.Error("poolerEnabled should be true")
	}
}

func TestEnablePoolerNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	err := svc.EnablePooler(context.Background(), "nope", domain.PoolerSettings{Enabled: true})
	if err == nil {
		t.Error("expected error")
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
		ProjectID: "ops-db", Namespace: "org1-ops-db",
		Tier: struct{ Instances int; StorageSize, Memory, CPU string }{1, "5Gi", "512Mi", "0.5"},
	})
	mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, "org1-ops-db", clusterObj)

	err := svc.ResizeStorage(context.Background(), "ops-db", "20Gi")
	if err != nil {
		t.Fatalf("ResizeStorage: %v", err)
	}

	// Verify CRD was patched with new storage size
	got, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, "org1-ops-db", "ops-db-postgres")
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
		ProjectID: "ops-db", Namespace: "org1-ops-db",
		Tier: struct{ Instances int; StorageSize, Memory, CPU string }{1, "5Gi", "512Mi", "0.5"},
	})
	mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, "org1-ops-db", clusterObj)

	err := svc.UpgradeVersion(context.Background(), "ops-db", "17")
	if err != nil {
		t.Fatalf("UpgradeVersion: %v", err)
	}

	got, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, "org1-ops-db", "ops-db-postgres")
	spec := got.Object["spec"].(map[string]interface{})
	if spec["imageName"] != "ghcr.io/cloudnative-pg/postgresql:17" {
		t.Errorf("imageName: got %v", spec["imageName"])
	}
}

func TestUpgradeVersionNotFound(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	err := svc.UpgradeVersion(context.Background(), "nope", "17")
	if err == nil {
		t.Error("expected error")
	}
}

func TestCloneDatabase(t *testing.T) {
	svc, _, mock := setupOpsTest(t)

	resp, err := svc.CloneDatabase(context.Background(), "ops-db", domain.CloneRequest{
		NewProjectName: "ops-db-clone",
	})
	if err != nil {
		t.Fatalf("CloneDatabase: %v", err)
	}
	if resp.ProjectID != "ops-db-clone" {
		t.Errorf("projectId: got %s", resp.ProjectID)
	}

	// Verify namespace created
	if !mock.Namespaces["org1-ops-db-clone"] {
		t.Error("clone namespace not created")
	}

	// Verify CRD applied with pg_basebackup bootstrap
	crd, ok := mock.CRDs["org1-ops-db-clone/ops-db-clone-postgres"]
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
		t.Error("expected error")
	}
}

func TestScaleTier(t *testing.T) {
	svc, store, mock := setupOpsTest(t)

	// Seed a CRD so GetCRD succeeds
	mock.SetupPostgreSQLMock("ops-db", "org1-ops-db", 1)
	clusterObj := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: "ops-db", Namespace: "org1-ops-db",
		Tier: struct{ Instances int; StorageSize, Memory, CPU string }{1, "5Gi", "512Mi", "0.5"},
	})
	mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, "org1-ops-db", clusterObj)

	err := svc.ScaleTier(context.Background(), "ops-db", domain.Standard)
	if err != nil {
		t.Fatalf("ScaleTier: %v", err)
	}

	// Verify store updated
	inst, _ := store.FindByProjectID("ops-db")
	if inst.Tier != domain.Standard {
		t.Errorf("tier: got %s, want STANDARD", inst.Tier)
	}

	// Verify CRD was patched with new instances/resources
	got, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, "org1-ops-db", "ops-db-postgres")
	spec := got.Object["spec"].(map[string]interface{})
	if spec["instances"] != int64(3) {
		t.Errorf("CRD instances: got %v, want 3", spec["instances"])
	}
	resources := spec["resources"].(map[string]interface{})
	requests := resources["requests"].(map[string]interface{})
	if requests["memory"] != "4Gi" {
		t.Errorf("CRD memory: got %v, want 4Gi", requests["memory"])
	}
}
