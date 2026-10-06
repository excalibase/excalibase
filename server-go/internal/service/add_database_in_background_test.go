package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Adding a database builds it for about as long as creating a project does,
// longer than a browser waits for one request (EXC-426, as EXC-534 did for
// creation). Accepting the add answers once the project is recorded as
// building it; the build runs on, and its outcome is on the project.

func backgroundAdds(svc *ProvisioningService) *[]func() {
	builds := &[]func(){}
	svc.SetBackgroundRunner(func(f func()) { *builds = append(*builds, f) })
	return builds
}

func TestAddDatabaseInBackground_AnswersWhileTheDatabaseIsBuiltThenBuildsIt(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	builds := backgroundAdds(svc)

	resp, err := svc.AddDatabaseInBackground(context.Background(), created.ProjectID, addPostgres())
	if err != nil {
		t.Fatalf("add in background: %v", err)
	}
	if resp.ProjectID != created.ProjectID || resp.Status != string(domain.StatusProvisioning) || !resp.NoDatabase {
		t.Fatalf("answer %+v, want the project PROVISIONING, its database not there yet", resp)
	}
	if row, _ := store.FindByProjectID(created.ProjectID); row.Status != string(domain.StatusProvisioning) {
		t.Fatalf("row is %s before the build ran, want PROVISIONING", row.Status)
	}
	if len(*builds) != 1 {
		t.Fatalf("builds started: %d, want 1", len(*builds))
	}

	(*builds)[0]()
	row, _ := store.FindByProjectID(created.ProjectID)
	if row.Status != string(domain.StatusActive) || row.NoDatabase || row.Host == "" {
		t.Fatalf("after the build: %+v, want ACTIVE with its database", row)
	}
}

// The add keeps the project's lease until the build ends, as the synchronous
// add does, so nothing else acts on a project while its database is built.
func TestAddDatabaseInBackground_HoldsTheProjectUntilTheBuildEnds(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	builds := backgroundAdds(svc)
	if _, err := svc.AddDatabaseInBackground(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add in background: %v", err)
	}

	maintenance := domain.MaintenanceWindowConfig{Window: "sunday 03:00", DurationMinutes: 60}
	if err := svc.SetMaintenanceWindow(context.Background(), created.ProjectID, maintenance); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("a change during the build: got %v, want ErrProjectOperationRunning", err)
	}
	(*builds)[0]()
	if err := svc.SetMaintenanceWindow(context.Background(), created.ProjectID, maintenance); err != nil {
		t.Fatalf("a change after the build: %v", err)
	}
}

// What can be refused before anything is built is refused in the answer, and
// a refusal leaves the project free.
func TestAddDatabaseInBackground_RefusesWithoutStartingABuild(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	builds := backgroundAdds(svc)

	noMajor := domain.ProvisioningRequest{DBType: domain.PostgreSQL}
	if _, err := svc.AddDatabaseInBackground(context.Background(), created.ProjectID, noMajor); !errors.Is(err, ErrDatabaseRequestInvalid) {
		t.Fatalf("no major: got %v, want ErrDatabaseRequestInvalid", err)
	}
	if _, err := svc.AddDatabaseInBackground(context.Background(), "no-such-project", addPostgres()); err == nil {
		t.Fatal("an unknown project was accepted")
	}
	if len(*builds) != 0 {
		t.Fatalf("refusals started %d builds", len(*builds))
	}
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("an add after the refusals: %v", err)
	}
	if _, err := svc.AddDatabaseInBackground(context.Background(), created.ProjectID, addPostgres()); !errors.Is(err, storage.ErrProjectHasDatabase) {
		t.Fatalf("a second database: got %v, want ErrProjectHasDatabase", err)
	}
	if len(*builds) != 0 {
		t.Fatalf("a refused second database started a build")
	}
}

// The caller going away does not stop the build.
func TestAddDatabaseInBackground_TheBuildOutlivesTheRequest(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	builds := backgroundAdds(svc)
	ctx, cancel := context.WithCancel(context.Background())

	if _, err := svc.AddDatabaseInBackground(ctx, created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add in background: %v", err)
	}
	cancel()
	(*builds)[0]()
	if row, _ := store.FindByProjectID(created.ProjectID); row.Status != string(domain.StatusActive) || row.NoDatabase {
		t.Fatalf("after the caller left: %+v, want ACTIVE with its database", row)
	}
}

// A build that fails is on the project the way a synchronous one leaves it:
// ACTIVE without a database, the failure named, and the add can be retried.
func TestAddDatabaseInBackground_AFailedBuildIsRecordedOnTheProject(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	builds := backgroundAdds(svc)
	mock.CRDError = errors.New("cluster rejected")

	if _, err := svc.AddDatabaseInBackground(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add in background: %v", err)
	}
	(*builds)[0]()
	row, _ := store.FindByProjectID(created.ProjectID)
	if row.Status != string(domain.StatusActive) || !row.NoDatabase || row.FailureReason == "" {
		t.Fatalf("after a failed build: %+v, want ACTIVE without a database and the failure named", row)
	}

	mock.CRDError = nil
	if _, err := svc.AddDatabaseInBackground(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	(*builds)[1]()
	if row, _ := store.FindByProjectID(created.ProjectID); row.NoDatabase {
		t.Fatal("the retried add did not give the project its database")
	}
}
