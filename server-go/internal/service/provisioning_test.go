package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func setupProvisioningTest(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	pgProv := provisioner.NewPostgreSQLProvisioner(mock)
	factory := provisioner.NewFactory(pgProv)
	svc := NewProvisioningService(store, factory)
	return svc, store, mock
}

func TestProvisionSuccess(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock("test-db", "org1-test-db", 1)

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "test-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if resp.Status != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", resp.Status)
	}
	if resp.CurrentStage != domain.StageCompleted {
		t.Errorf("stage: got %s, want COMPLETED", resp.CurrentStage)
	}
	if resp.Namespace != "org1-test-db" {
		t.Errorf("namespace: got %s", resp.Namespace)
	}
}

func TestProvisionDuplicate(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	store.Save(&domain.DatabaseInstance{ProjectID: "dup-db", Status: "ACTIVE"})

	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "dup-db",
		OrgID:       "org1",
		DBType:      domain.PostgreSQL,
		Tier:        domain.Free,
	})
	if err == nil {
		t.Error("expected error for duplicate project")
	}
}

func TestProvisionUnsupportedType(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)

	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "test",
		OrgID:       "org1",
		DBType:      "REDIS", // unsupported
		Tier:        domain.Free,
	})
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestDeprovision(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock("del-db", "org1-del-db", 1)

	store.Save(&domain.DatabaseInstance{
		ProjectID: "del-db",
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Namespace: "org1-del-db",
		Status:    "ACTIVE",
	})

	if err := svc.Deprovision(context.Background(), "del-db"); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}

	inst, _ := store.FindByProjectID("del-db")
	if inst != nil {
		t.Error("instance should be deleted")
	}
}

func TestDeprovisionNotFound(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	err := svc.Deprovision(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestDeprovisionDeletionProtection(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	protected := true
	store.Save(&domain.DatabaseInstance{
		ProjectID:          "protected-db",
		DBType:             domain.PostgreSQL,
		Status:             "ACTIVE",
		DeletionProtection: &protected,
	})

	err := svc.Deprovision(context.Background(), "protected-db")
	if err == nil {
		t.Error("expected error for deletion-protected project")
	}
}

func TestGetCredentials(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	port := 5432
	store.Save(&domain.DatabaseInstance{
		ProjectID:    "cred-db",
		Host:         "host.local",
		Port:         &port,
		DatabaseName: "mydb",
		Username:     "user",
		Password:     "pass",
		SSLMode:      "require",
		Status:       "ACTIVE",
	})

	creds, err := svc.GetCredentials("cred-db")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.Host != "host.local" {
		t.Errorf("host: got %s", creds.Host)
	}
	if creds.Password != "pass" {
		t.Errorf("password not returned")
	}
	if creds.ConnectionURL == "" {
		t.Error("connectionUrl should be set")
	}
}

func TestSetDeletionProtection(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	store.Save(&domain.DatabaseInstance{ProjectID: "dp-db", Status: "ACTIVE"})

	if err := svc.SetDeletionProtection("dp-db", true); err != nil {
		t.Fatalf("SetDeletionProtection: %v", err)
	}

	inst, _ := store.FindByProjectID("dp-db")
	if inst.DeletionProtection == nil || !*inst.DeletionProtection {
		t.Error("deletion protection should be enabled")
	}
}

func TestGetAllInstances(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	store.Save(&domain.DatabaseInstance{ProjectID: "a", Status: "ACTIVE"})
	store.Save(&domain.DatabaseInstance{ProjectID: "b", Status: "ACTIVE"})

	all, _ := svc.GetAllInstances()
	if len(all) != 2 {
		t.Errorf("expected 2, got %d", len(all))
	}
}
