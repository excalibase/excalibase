package provisioner

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func TestPostgreSQLProvisionerFree(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock("test-db", "org1-test-db", 1)
	prov := NewPostgreSQLProvisioner(mock)

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

	// Verify all 8 stages were called
	expectedStages := []domain.ProvisioningStage{
		domain.StageValidating,
		domain.StageNamespaceCreation,
		domain.StageCRDDeployment,
		domain.StageWaitingForReady,
		domain.StageCredentialGeneration,
		domain.StageMetricsSetup,
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
		if call == "CreateNamespace:org1-test-db" {
			found = true
		}
	}
	if !found {
		t.Error("CreateNamespace not called")
	}
}

func TestPostgreSQLProvisionerWithBackup(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock("bk-test", "org1-bk-test", 1)
	prov := NewPostgreSQLProvisioner(mock)

	tier, _ := config.GetTierConfig(domain.Free)
	_, err := prov.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "bk-test",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
		Backup:      &domain.BackupSettings{Enabled: true, Schedule: "0 2 * * *", Retention: 30},
	}, tier, func(s domain.ProvisioningStage) {})

	if err != nil {
		t.Fatalf("Provision with backup: %v", err)
	}

	// Verify backup secret and scheduled backup were created
	if _, ok := mock.Secrets["org1-bk-test/backup-s3-creds"]; !ok {
		t.Error("backup S3 credentials secret not created")
	}
	if _, ok := mock.CRDs["org1-bk-test/bk-test-postgres-backup"]; !ok {
		t.Error("ScheduledBackup CRD not created")
	}
}

func TestPostgreSQLProvisionerStandard(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock("std-db", "org1-std-db", 3)
	prov := NewPostgreSQLProvisioner(mock)

	tier, _ := config.GetTierConfig(domain.Standard)
	result, err := prov.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "std-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Standard,
	}, tier, func(s domain.ProvisioningStage) {})

	if err != nil {
		t.Fatalf("Provision STANDARD: %v", err)
	}
	if result.Namespace != "org1-std-db" {
		t.Errorf("namespace: got %s", result.Namespace)
	}

	// Verify all 3 pods were checked
	readyChecks := 0
	for _, call := range mock.Calls {
		if len(call) > 10 && call[:10] == "IsPodReady" {
			readyChecks++
		}
	}
	if readyChecks < 3 {
		t.Errorf("expected 3 IsPodReady checks, got %d", readyChecks)
	}
}

func TestPostgreSQLDeprovision(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.Namespaces["org1-del-db"] = true
	prov := NewPostgreSQLProvisioner(mock)

	err := prov.Deprovision(context.Background(), "org1-del-db", "del-db")
	if err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if mock.Namespaces["org1-del-db"] {
		t.Error("namespace should be deleted")
	}
}

func TestPostgreSQLGetStatus(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.PodReady["ns/test-postgres-1"] = true
	prov := NewPostgreSQLProvisioner(mock)

	status, _ := prov.GetStatus(context.Background(), "ns", "test")
	if !status.Ready {
		t.Error("should be ready")
	}
}

func TestPostgreSQLConfigureBackup(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock)

	err := prov.ConfigureBackup(context.Background(), "ns", "bk-test", "0 3 * * *", 14)
	if err != nil {
		t.Fatalf("ConfigureBackup: %v", err)
	}

	// Verify ScheduledBackup CRD was created
	if _, ok := mock.CRDs["ns/bk-test-postgres-backup"]; !ok {
		t.Error("ScheduledBackup CRD not created")
	}
}

func TestFactoryGet(t *testing.T) {
	mock := k8s.NewMockClient()
	pg := NewPostgreSQLProvisioner(mock)
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
