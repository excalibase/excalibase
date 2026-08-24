package provisioner

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	testExpectedErr  = "expected error"
	testStageErrFmt  = "expected StageError, got %T: %v"
	testBKTest       = "bk-test"
	testDelDBNS      = "org1-del-db"
)


func TestPostgreSQLProvisionerFree(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock("test-db", "org1-test-db", 1)
	prov := NewPostgreSQLProvisioner(mock, "")

	var stages []domain.ProvisioningStage
	cb := func(stage domain.ProvisioningStage) { stages = append(stages, stage) }

	tier, _ := config.GetTierConfig(domain.Free)
	result, err := prov.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "test-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	}, tier, cb)

	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if result.Port != 5432 {
		t.Errorf("port: got %d", result.Port)
	}
	if result.Username != "app" {
		t.Errorf("username: got %s", result.Username)
	}
	if result.Password != "testpassword123" {
		t.Errorf("password: got %s", result.Password)
	}

	// Verify all stages were called (including watcher deployment)
	expectedStages := []domain.ProvisioningStage{
		domain.StageValidating,
		domain.StageNamespaceCreation,
		domain.StageCRDDeployment,
		domain.StageWaitingForReady,
		domain.StageCredentialGeneration,
		domain.StageMetricsSetup,
		domain.StageWatcherDeployment,
		domain.StageCompleted,
	}
	if len(stages) < len(expectedStages) {
		t.Errorf("expected %d stages, got %d: %v", len(expectedStages), len(stages), stages)
	}
	for i, expected := range expectedStages {
		if i < len(stages) && stages[i] != expected {
			t.Errorf("stage %d: got %s, want %s", i, stages[i], expected)
		}
	}

	// Verify K8s calls
	found := false
	for _, call := range mock.Calls {
		if call == "CreateNamespaceWithLabels:org1-test-db" {
			found = true
		}
	}
	if !found {
		t.Error("CreateNamespaceWithLabels not called")
	}
	// Verify namespace labels
	labels := mock.NamespaceLabels["org1-test-db"]
	if labels["excalibase.io/type"] != "project" {
		t.Errorf("label excalibase.io/type: got %q, want %q", labels["excalibase.io/type"], "project")
	}
	if labels["excalibase.io/org"] != "org1" {
		t.Errorf("label excalibase.io/org: got %q, want %q", labels["excalibase.io/org"], "org1")
	}
}

func TestPostgreSQLProvisionerWithBackup(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock(testBKTest, "org1-bk-test", 1)
	prov := NewPostgreSQLProvisioner(mock, "")

	tier, _ := config.GetTierConfig(domain.Free)
	_, err := prov.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: testBKTest,
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
		Backup: &domain.BackupSettings{
			Enabled:   true,
			Schedule:  "0 2 * * *",
			Retention: 30,
			S3: &domain.S3Credentials{
				AccessKeyID:     "AKIA123",
				SecretAccessKey: "secret456",
				Bucket:          "excalibase-backups",
				Region:          "ap-southeast-1",
			},
		},
	}, tier, func(s domain.ProvisioningStage) { /* noop: stage progress not checked in this test */ })

	if err != nil {
		t.Fatalf("Provision with backup: %v", err)
	}

	// Verify backup secret uses real S3 credentials from request
	secret, ok := mock.Secrets["org1-bk-test/backup-s3-creds"]
	if !ok {
		t.Fatal("backup S3 credentials secret not created")
	}
	if string(secret["ACCESS_KEY_ID"]) != "AKIA123" {
		t.Errorf("ACCESS_KEY_ID: got %q, want %q", secret["ACCESS_KEY_ID"], "AKIA123")
	}
	if string(secret["ACCESS_SECRET_KEY"]) != "secret456" {
		t.Errorf("ACCESS_SECRET_KEY: got %q, want %q", secret["ACCESS_SECRET_KEY"], "secret456")
	}
	if _, ok := mock.CRDs["org1-bk-test/bk-test-postgres-backup"]; !ok {
		t.Error("ScheduledBackup CRD not created")
	}
}

func TestPostgreSQLProvisionerStandard(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock("std-db", "org1-std-db", 1)
	prov := NewPostgreSQLProvisioner(mock, "")

	tier, _ := config.GetTierConfig(domain.Standard)
	result, err := prov.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "std-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Standard,
	}, tier, func(s domain.ProvisioningStage) { /* noop: stage progress not checked in this test */ })

	if err != nil {
		t.Fatalf("Provision STANDARD: %v", err)
	}
	if result.Namespace != "org1-std-db" {
		t.Errorf("namespace: got %s", result.Namespace)
	}

	// Single-instance tier (no HA) — exactly one pod is waited on.
	readyChecks := 0
	for _, call := range mock.Calls {
		if len(call) > 10 && call[:10] == "IsPodReady" {
			readyChecks++
		}
	}
	if readyChecks < 1 {
		t.Errorf("expected 1 IsPodReady check, got %d", readyChecks)
	}
}

func TestPostgreSQLDeprovision(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.Namespaces[testDelDBNS] = true
	prov := NewPostgreSQLProvisioner(mock, "")

	err := prov.Deprovision(context.Background(), testDelDBNS, "del-db")
	if err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if mock.Namespaces[testDelDBNS] {
		t.Error("namespace should be deleted")
	}
}

func TestPostgreSQLGetStatus(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.PodReady["ns/test-postgres-1"] = true
	prov := NewPostgreSQLProvisioner(mock, "")

	status, _ := prov.GetStatus(context.Background(), "ns", "test")
	if !status.Ready {
		t.Error("should be ready")
	}
}

func TestPostgreSQLConfigureBackup(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, "")

	err := prov.ConfigureBackup(context.Background(), "ns", testBKTest, "0 3 * * *", 14)
	if err != nil {
		t.Fatalf("ConfigureBackup: %v", err)
	}

	// Verify ScheduledBackup CRD was created
	if _, ok := mock.CRDs["ns/bk-test-postgres-backup"]; !ok {
		t.Error("ScheduledBackup CRD not created")
	}
}

// --- ProvisionWithRollback (rollback-aware path) ---

func TestPostgreSQL_ProvisionWithRollback_Success(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock("ok-db", "org1-ok-db", 1)
	prov := NewPostgreSQLProvisioner(mock, "")

	pc := NewProvisionContext(nil, nil)
	tier, _ := config.GetTierConfig(domain.Free)
	result, err := prov.ProvisionWithRollback(context.Background(), domain.ProvisioningRequest{
		ProjectName: "ok-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	}, tier, pc)

	if err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	if result == nil || result.Username != "app" {
		t.Fatalf("result: %+v", result)
	}
	if pc.Stage() != domain.StageCompleted {
		t.Errorf("stage: got %s, want COMPLETED", pc.Stage())
	}
	// Namespace cleanup registered (covers all K8s resources via cascade)
	if pc.CleanupCount() < 1 {
		t.Error("expected at least 1 cleanup (namespace) to be registered")
	}
}

func TestPostgreSQL_ProvisionWithRollback_NamespaceFailsNoCleanups(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.NamespaceError = errors.New("quota exceeded")
	prov := NewPostgreSQLProvisioner(mock, "")

	pc := NewProvisionContext(nil, nil)
	tier, _ := config.GetTierConfig(domain.Free)
	_, err := prov.ProvisionWithRollback(context.Background(), domain.ProvisioningRequest{
		ProjectName: "ns-fail",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	}, tier, pc)

	if err == nil {
		t.Fatal(testExpectedErr)
	}
	var se *StageError
	if !errors.As(err, &se) {
		t.Fatalf(testStageErrFmt, err, err)
	}
	if se.Stage != domain.StageNamespaceCreation {
		t.Errorf("stage: got %s, want NAMESPACE_CREATION", se.Stage)
	}
	// Nothing created before the failure → no cleanups to run
	if pc.CleanupCount() != 0 {
		t.Errorf("expected 0 cleanups, got %d", pc.CleanupCount())
	}
}

func TestPostgreSQL_ProvisionWithRollback_CRDFailRollsBackNamespace(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.CRDError = errors.New("forbidden: CNPG CRD missing")
	prov := NewPostgreSQLProvisioner(mock, "")

	pc := NewProvisionContext(nil, nil)
	tier, _ := config.GetTierConfig(domain.Free)
	_, err := prov.ProvisionWithRollback(context.Background(), domain.ProvisioningRequest{
		ProjectName: "crd-fail",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	}, tier, pc)

	if err == nil {
		t.Fatal(testExpectedErr)
	}
	var se *StageError
	if !errors.As(err, &se) {
		t.Fatalf(testStageErrFmt, err, err)
	}
	if se.Stage != domain.StageCRDDeployment {
		t.Errorf("stage: got %s, want CRD_DEPLOYMENT", se.Stage)
	}
	// Namespace was created → exactly one cleanup registered
	if pc.CleanupCount() != 1 {
		t.Errorf("expected 1 cleanup, got %d", pc.CleanupCount())
	}
	if !mock.Namespaces["org1-crd-fail"] {
		t.Fatal("namespace should exist before rollback runs")
	}

	results := pc.Rollback(context.Background())
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("rollback results: %+v", results)
	}
	if mock.Namespaces["org1-crd-fail"] {
		t.Error("namespace should be deleted by rollback")
	}
}

func TestPostgreSQL_ProvisionWithRollback_WaitFailRollsBackNamespace(t *testing.T) {
	// No SetupPostgreSQLMock → IsPodReady errors → loop hits ctx.Done()
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel up-front; early stages don't check ctx but waitForPodReady does
	pc := NewProvisionContext(nil, nil)
	tier, _ := config.GetTierConfig(domain.Free)
	_, err := prov.ProvisionWithRollback(ctx, domain.ProvisioningRequest{
		ProjectName: "wait-fail",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	}, tier, pc)

	if err == nil {
		t.Fatal(testExpectedErr)
	}
	var se *StageError
	if !errors.As(err, &se) {
		t.Fatalf(testStageErrFmt, err, err)
	}
	if se.Stage != domain.StageWaitingForReady {
		t.Errorf("stage: got %s, want WAITING_FOR_READY", se.Stage)
	}
	if pc.CleanupCount() != 1 {
		t.Errorf("expected 1 cleanup, got %d", pc.CleanupCount())
	}
	results := pc.Rollback(context.Background())
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("rollback results: %+v", results)
	}
	if mock.Namespaces["org1-wait-fail"] {
		t.Error("namespace should be deleted")
	}
}

func TestPostgreSQL_ProvisionWithRollback_ReplicaStepCaptured(t *testing.T) {
	mock := k8s.NewMockClient()
	// Only primary is ready; replicas 2 and 3 are missing
	mock.SetupPostgreSQLMock("rep-fail", "org1-rep-fail", 1)
	prov := NewPostgreSQLProvisioner(mock, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // primary returns ready=true immediately; replica 2 enters select → ctx.Done()
	pc := NewProvisionContext(nil, nil)
	// Explicit multi-instance tier: the default tiers are single-instance for
	// the alpha, but the provisioner still supports N replicas — this exercises
	// the replica-wait/rollback path regardless of the default tier spec.
	tier := config.TierConfig{Instances: 3, StorageSize: "50Gi", Memory: "4Gi", CPU: "2", BackupEnabled: true}

	_, err := prov.ProvisionWithRollback(ctx, domain.ProvisioningRequest{
		ProjectName: "rep-fail",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Standard,
	}, tier, pc)

	if err == nil {
		t.Fatal(testExpectedErr)
	}
	var se *StageError
	if !errors.As(err, &se) {
		t.Fatalf(testStageErrFmt, err, err)
	}
	if se.Stage != domain.StageWaitingForReady {
		t.Errorf("stage: got %s", se.Stage)
	}
	if se.Step != "replica 2" {
		t.Errorf("step: got %q, want %q", se.Step, "replica 2")
	}
}

// --- RollbackAware interface assertion ---

func TestPostgreSQLProvisioner_ImplementsRollbackAware(t *testing.T) {
	var _ RollbackAware = (*PostgreSQLProvisioner)(nil)
}

func TestFactoryGet(t *testing.T) {
	mock := k8s.NewMockClient()
	pg := NewPostgreSQLProvisioner(mock, "")
	factory := NewFactory(pg)

	p, ok := factory.Get(domain.PostgreSQL)
	if !ok {
		t.Error("PostgreSQL provisioner not found")
	}
	if p.SupportedType() != domain.PostgreSQL {
		t.Error("wrong type")
	}

	_, ok = factory.Get(domain.MySQL)
	if ok {
		t.Error("MySQL provisioner should not exist")
	}
}
