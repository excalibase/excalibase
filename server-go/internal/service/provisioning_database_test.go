package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-426: a project is a container of services; a database is one of them.

func createWithoutDatabase(t *testing.T, svc *ProvisioningService) *domain.ProvisioningResponse {
	t.Helper()
	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "apps only", OrgID: "org1", OwnerID: testUser1, NoDatabase: true,
	})
	if err != nil {
		t.Fatalf("create without a database: %v", err)
	}
	return resp
}

func namespaceCreations(mock *k8s.MockClient) int {
	n := 0
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, "CreateProjectNamespace:") {
			n++
		}
	}
	return n
}

func TestAProjectCanBeCreatedWithoutADatabase(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	vault := svc.vault.(*fakeVault)

	resp := createWithoutDatabase(t, svc)

	if resp.Status != "ACTIVE" || !resp.NoDatabase {
		t.Fatalf("response %+v, want ACTIVE without a database", resp)
	}
	row, _ := store.FindByProjectID(resp.ProjectID)
	if row == nil || !row.NoDatabase || row.Status != "ACTIVE" || row.Host != "" {
		t.Fatalf("row %+v, want an ACTIVE project with no database", row)
	}
	if !mock.Namespaces[row.Namespace] {
		t.Fatalf("namespace %s was not created", row.Namespace)
	}
	if len(mock.CRDs) != 0 {
		t.Fatalf("a project without a database got cluster objects: %v", mock.CRDs)
	}
	for path := range vault.data {
		if strings.Contains(path, resp.ProjectID) {
			t.Fatalf("a project without a database got a credential at %s", path)
		}
	}
}

func TestAProjectWithoutADatabaseRefusesDatabaseSettings(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	for name, req := range map[string]domain.ProvisioningRequest{
		"version":    {PostgresVersion: "17"},
		"documentdb": {DocumentDB: true},
		"engine":     {DBType: domain.PostgreSQL},
		"parameters": {Parameters: map[string]string{"work_mem": "8MB"}},
	} {
		t.Run(name, func(t *testing.T) {
			req.ProjectName, req.OrgID, req.NoDatabase = "x", "org1", true
			if _, err := svc.Provision(context.Background(), req); !errors.Is(err, ErrDatabaseSettingsWithoutDatabase) {
				t.Fatalf("got %v, want ErrDatabaseSettingsWithoutDatabase", err)
			}
		})
	}
	if all, _ := store.FindAll(); len(all) != 0 {
		t.Fatalf("a refused request left rows: %d", len(all))
	}
}

// A project holds its org slot whether or not it has a database.
func TestAProjectWithoutADatabaseTakesAnOrgSlot(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	createWithoutDatabase(t, svc) // FREE allows one project

	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "second", OrgID: "org1", NoDatabase: true,
	})
	var limit *OrgProjectLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("a second project without a database: got %v, want the org limit", err)
	}
	_, err = svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "third", OrgID: "org1", DBType: domain.PostgreSQL, PostgresVersion: "17",
	})
	if !errors.As(err, &limit) {
		t.Fatalf("a project with a database: got %v, want the org limit", err)
	}
}

func TestAProjectWithoutADatabaseNeedsKubernetes(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	svc.SetDefaultDeploymentMode(domain.ModeDocker)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{ProjectName: "x", OrgID: "org1", NoDatabase: true})
	if !errors.Is(err, ErrNoDatabaseNeedsKubernetes) {
		t.Fatalf("got %v, want ErrNoDatabaseNeedsKubernetes", err)
	}
}

func addPostgres() domain.ProvisioningRequest {
	return domain.ProvisioningRequest{DBType: domain.PostgreSQL, PostgresVersion: "17"}
}

func TestAddingADatabaseGivesTheProjectItsDatabase(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	vault := svc.vault.(*fakeVault)
	created := createWithoutDatabase(t, svc)

	resp, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres())
	if err != nil {
		t.Fatalf("add database: %v", err)
	}
	if resp.Status != "ACTIVE" || resp.NoDatabase || resp.Host == "" {
		t.Fatalf("response %+v, want ACTIVE with a database", resp)
	}
	row, _ := store.FindByProjectID(created.ProjectID)
	if row.NoDatabase || row.Status != "ACTIVE" || row.PostgresVersion != "17" || row.StorageSize == "" {
		t.Fatalf("row %+v, want the added database recorded", row)
	}
	if _, ok := mock.CRDs[row.Namespace+"/"+created.ProjectID+"-postgres"]; !ok {
		t.Fatal("no cluster in the project's namespace")
	}
	if namespaceCreations(mock) != 1 {
		t.Fatalf("namespace created %d times, want once", namespaceCreations(mock))
	}
	if _, ok := vault.data["projects/"+created.ProjectID+"/credentials/excalibase_app"]; !ok {
		t.Fatal("the added database's credentials were not filed")
	}
	if _, err := svc.GetCredentials(created.ProjectID); err != nil {
		t.Fatalf("credentials after the add: %v", err)
	}
}

func TestAddingASecondDatabaseIsRefused(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add database: %v", err)
	}
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); !errors.Is(err, storage.ErrProjectHasDatabase) {
		t.Fatalf("got %v, want ErrProjectHasDatabase", err)
	}
}

func TestAddDatabaseTakesOnlyDatabaseSettings(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	for name, req := range map[string]domain.ProvisioningRequest{
		"project name": {ProjectName: "rename", DBType: domain.PostgreSQL, PostgresVersion: "17"},
		"org":          {OrgID: "org2", DBType: domain.PostgreSQL, PostgresVersion: "17"},
		"no database":  {NoDatabase: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.AddDatabase(context.Background(), created.ProjectID, req); !errors.Is(err, ErrAddDatabaseRequest) {
				t.Fatalf("got %v, want ErrAddDatabaseRequest", err)
			}
		})
	}
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, domain.ProvisioningRequest{DBType: domain.PostgreSQL}); err == nil {
		t.Fatal("an add without a version was accepted; there is no default major")
	}
}

func TestAFailedDatabaseAddLeavesTheProjectRunningWithoutOne(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	mock.CRDError = errors.New("cluster rejected")

	resp, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres())
	if err != nil {
		t.Fatalf("add database: %v", err)
	}
	if resp.Status != "ACTIVE" || !resp.NoDatabase || resp.FailureReason == "" {
		t.Fatalf("response %+v, want ACTIVE without a database and the failure named", resp)
	}
	row, _ := store.FindByProjectID(created.ProjectID)
	if !row.NoDatabase || row.Status != "ACTIVE" || row.Host != "" {
		t.Fatalf("row %+v, want the project back to ACTIVE without a database", row)
	}
	if !mock.Namespaces[row.Namespace] {
		t.Fatal("the failed add deleted the project's namespace")
	}
	if row.BackupEnabled != nil && *row.BackupEnabled {
		t.Fatal("a project whose database add failed is still recorded as backed up")
	}

	// The add can be retried once the cause is gone.
	mock.CRDError = nil
	if resp, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil || resp.NoDatabase {
		t.Fatalf("retry: resp %+v err %v", resp, err)
	}
}

func TestADatabaseIsAddedOnlyToAnActiveProject(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	row, _ := store.FindByProjectID(created.ProjectID)
	row.Status = string(domain.StatusPendingDeletion)
	_ = store.Update(row)
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("got %v, want ErrProjectNotActive", err)
	}
}

func TestCredentialsOfAProjectWithoutADatabaseAreRefused(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	if _, err := svc.GetCredentials(created.ProjectID); !errors.Is(err, domain.ErrNoDatabase) {
		t.Fatalf("got %v, want domain.ErrNoDatabase", err)
	}
}

// Deleting a project without a database keeps the grace period: its files,
// apps and settings are the customer's. Nothing is paused, because there is
// no database to stop, and a cancelled deletion leaves it ACTIVE.
func TestAProjectWithoutADatabaseIsDeletedWithTheGracePeriod(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	pauser := &stoppingPauser{store: store}
	svc.SetDeletionPauser(pauser)
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	svc.SetDeletionClock(func() time.Time { return now })
	created := createWithoutDatabase(t, svc)
	unprotect(t, store, created.ProjectID)

	scheduled, err := svc.ScheduleDeletion(context.Background(), created.ProjectID, DeprovisionOptions{})
	if err != nil || scheduled == nil || scheduled.Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("schedule: %+v %v", scheduled, err)
	}
	if len(pauser.calls) != 0 {
		t.Fatal("a project without a database was paused")
	}
	if err := svc.CancelDeletion(context.Background(), created.ProjectID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if row, _ := store.FindByProjectID(created.ProjectID); row.Status != "ACTIVE" {
		t.Fatalf("after cancel: %s, want ACTIVE", row.Status)
	}

	unprotect(t, store, created.ProjectID)
	if _, err := svc.ScheduleDeletion(context.Background(), created.ProjectID, DeprovisionOptions{}); err != nil {
		t.Fatalf("schedule again: %v", err)
	}
	now = now.Add(DeletionGracePeriod + time.Minute)
	report := svc.RunDueDeletions(context.Background())
	if !slices.Contains(report.Deleted, created.ProjectID) {
		t.Fatalf("sweep %+v did not delete the project", report)
	}
	if row, _ := store.FindByProjectID(created.ProjectID); row != nil {
		t.Fatal("the row survived the hard delete")
	}
	if mock.Namespaces["org1-"+created.ProjectID] {
		t.Fatal("the namespace survived the hard delete")
	}
}

func unprotect(t *testing.T, store storage.InstanceStore, projectID string) {
	t.Helper()
	row, _ := store.FindByProjectID(projectID)
	off := false
	row.DeletionProtection = &off
	if err := store.Update(row); err != nil {
		t.Fatal(err)
	}
}
