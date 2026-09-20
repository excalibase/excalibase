package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// DocumentDB is a create-time choice (EXC-409), so a provision is the only
// place it is ever decided. These tests drive the real Provision path and read
// the stored project back.

// documentDBProvisionService wires a provision that reaches a fake cluster.
func documentDBProvisionService(t *testing.T) (*ProvisioningService, storage.InstanceStore) {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	return svc, store
}

// documentDBRequest asks for a project on a major that can carry DocumentDB.
func documentDBRequest(name string, documentDB bool) domain.ProvisioningRequest {
	majors := config.DocumentDBMajors()
	return domain.ProvisioningRequest{
		ProjectName:     name,
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Enterprise,
		PostgresVersion: majors[len(majors)-1],
		DocumentDB:      documentDB,
	}
}

// The request's choice reaches the stored project. Without this nothing
// downstream — the API, the extension step, whatever fronts the Mongo wire
// protocol — can tell a DocumentDB project from an ordinary one.
func TestProvisioningRecordsTheDocumentDBChoice(t *testing.T) {
	svc, store := documentDBProvisionService(t)

	resp, err := svc.Provision(context.Background(), documentDBRequest("keeps-documentdb", true))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	inst, err := store.FindByProjectID(resp.ProjectID)
	if err != nil || inst == nil {
		t.Fatalf("read the provisioned project: %v", err)
	}
	if !inst.DocumentDB {
		t.Error("a project provisioned with DocumentDB was not recorded as one")
	}
}

// And a project that did not ask for it does not get it, on the very same
// major. The image could carry the extension; the project still has not
// asked, so it is not a DocumentDB project.
func TestProvisioningWithoutDocumentDBRecordsNone(t *testing.T) {
	svc, store := documentDBProvisionService(t)

	resp, err := svc.Provision(context.Background(), documentDBRequest("no-documentdb", false))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	inst, err := store.FindByProjectID(resp.ProjectID)
	if err != nil || inst == nil {
		t.Fatalf("read the provisioned project: %v", err)
	}
	if inst.DocumentDB {
		t.Error("a project provisioned without DocumentDB was recorded as having it")
	}
}
