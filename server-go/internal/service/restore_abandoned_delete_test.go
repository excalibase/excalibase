package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const abandonedRestoreProject = "restored-orphan"

// A restore whose driving process died leaves a RESTORING row and everything
// the restore had created. The only way out is DELETE, so it must be
// accepted and must tear down what the restore left behind.
func TestDeletingAProjectStuckInRestoringRemovesWhatTheRestoreCreated(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	namespace := "org1-" + abandonedRestoreProject
	mock.SetupPostgreSQLMock(abandonedRestoreProject, namespace, 1)
	if err := svc.store.Create(&domain.DatabaseInstance{
		ProjectID:      abandonedRestoreProject,
		OrgID:          "org1",
		DBType:         domain.PostgreSQL,
		DeploymentMode: domain.ModeK8s,
		Namespace:      namespace,
		Status:         string(domain.StatusRestoring),
	}); err != nil {
		t.Fatalf("seed abandoned restore: %v", err)
	}
	// The restore had got as far as the namespace, the object-store secret
	// and the recovery Cluster CR.
	mock.Namespaces[namespace] = true
	mock.Secrets[namespace+"/"+s3CredsKey] = map[string][]byte{"ACCESS_KEY_ID": []byte("k")}

	if err := svc.Deprovision(context.Background(), abandonedRestoreProject); err != nil {
		t.Fatalf("a project stuck in RESTORING must be deletable: %v", err)
	}

	if inst, _ := store.FindByProjectID(abandonedRestoreProject); inst != nil {
		t.Errorf("the row must be gone, got %+v", inst)
	}
	if mock.Namespaces[namespace] {
		t.Error("the namespace the restore created must be removed")
	}
	if _, ok := mock.CRDs[namespace+"/"+abandonedRestoreProject+postgresClusterSuffix]; ok {
		t.Error("the recovery Cluster CR must be removed")
	}
}

// BeginDeletion is the door a DELETE goes through. It must let a RESTORING
// project in — refusing would make an abandoned restore permanent.
func TestBeginDeletionAcceptsARestoringProject(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	if err := svc.store.Create(&domain.DatabaseInstance{
		ProjectID: "restoring-door", OrgID: "org1", Status: string(domain.StatusRestoring),
		DeploymentMode: domain.ModeK8s, DBType: domain.PostgreSQL,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := store.BeginDeletion("restoring-door", nil); err != nil {
		t.Fatalf("BeginDeletion on a RESTORING project: %v", err)
	}
	inst, _ := store.FindByProjectID("restoring-door")
	if inst.Status != string(domain.StatusDeleting) {
		t.Errorf("status: got %s, want DELETING", inst.Status)
	}
}

// Credentials for a project whose restore has not been confirmed open a
// database that may never have recovered.
func TestGetCredentialsRefusesARestoringProject(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	if err := svc.store.Create(&domain.DatabaseInstance{
		ProjectID: "creds-restoring", OrgID: "org1", Status: string(domain.StatusRestoring),
		Host: "h", DatabaseName: "app", Username: "u", Password: "p", SSLMode: "require",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := svc.GetCredentials("creds-restoring")
	if !errors.Is(err, ErrProjectRestoring) {
		t.Fatalf("err: got %v, want ErrProjectRestoring", err)
	}
}

func TestGetCredentialsStillRefusesADeletingProject(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	if err := svc.store.Create(&domain.DatabaseInstance{
		ProjectID: "creds-deleting", OrgID: "org1", Status: string(domain.StatusDeleting),
		Host: "h", DatabaseName: "app", Username: "u", Password: "p",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := svc.GetCredentials("creds-deleting")
	if !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("err: got %v, want ErrProjectDeleting", err)
	}
}
