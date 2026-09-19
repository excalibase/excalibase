package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const testPurgeProject = "purge-db"

func saveActiveProject(t *testing.T, svc *ProvisioningService, projectID string) {
	t.Helper()
	err := svc.store.Create(&domain.DatabaseInstance{
		ProjectID:      projectID,
		OrgID:          "org1",
		DBType:         domain.PostgreSQL,
		DeploymentMode: domain.ModeK8s,
		Namespace:      "org1-" + projectID,
		Status:         "ACTIVE",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
}

func TestDeprovisionKeepsBackupsByDefault(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testPurgeProject, "org1-"+testPurgeProject, 1)
	deleter := newFakeObjectDeleter(testPurgeProject + "/cloud/base/b1/data.tar.gz")
	svc.SetBackupPurger(newTestPurger(deleter))
	saveActiveProject(t, svc, testPurgeProject)

	if err := svc.Deprovision(context.Background(), testPurgeProject); err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}
	if inst, _ := store.FindByProjectID(testPurgeProject); inst != nil {
		t.Fatal("row should be deleted")
	}
	if len(deleter.remaining()) != 1 || deleter.listCalls != 0 {
		t.Fatal("backups must be untouched unless confirmDeleteBackups is set")
	}
}

func TestDeprovisionWithDeleteBackupsPurgesAfterResourcesGone(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testPurgeProject, "org1-"+testPurgeProject, 1)
	deleter := newFakeObjectDeleter(testPurgeProject+"/cloud/base/b1/data.tar.gz", "other/cloud/keep")
	svc.SetBackupPurger(newTestPurger(deleter))
	saveActiveProject(t, svc, testPurgeProject)

	err := svc.DeprovisionWithOptions(context.Background(), testPurgeProject, DeprovisionOptions{DeleteBackups: true})
	if err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}
	if inst, _ := store.FindByProjectID(testPurgeProject); inst != nil {
		t.Fatal("row should be deleted after a successful purge")
	}
	if rem := deleter.remaining(); len(rem) != 1 || rem[0] != "other/cloud/keep" {
		t.Fatalf("remaining = %v, want only the other project's objects", rem)
	}
	// The namespace must be gone before the objects are: the purge is the
	// last step, never a substitute for a failed deprovision.
	if _, stillThere := mock.Namespaces["org1-"+testPurgeProject]; stillThere {
		t.Fatal("namespace should have been deleted")
	}
}

func TestDeprovisionPurgeFailureMarksRowPendingDelete(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testPurgeProject, "org1-"+testPurgeProject, 1)
	deleter := newFakeObjectDeleter(testPurgeProject + "/cloud/base/b1/data.tar.gz")
	deleter.deleteErr = errors.New("r2 unavailable")
	svc.SetBackupPurger(newTestPurger(deleter))
	saveActiveProject(t, svc, testPurgeProject)

	err := svc.DeprovisionWithOptions(context.Background(), testPurgeProject, DeprovisionOptions{DeleteBackups: true})
	if err == nil {
		t.Fatal("a purge the caller asked for and did not get must be reported")
	}
	inst, _ := store.FindByProjectID(testPurgeProject)
	if inst == nil {
		t.Fatal("row must be kept as a retry marker")
	}
	if inst.Status != string(domain.StatusBackupsPendingDelete) {
		t.Fatalf("status = %q, want %s", inst.Status, domain.StatusBackupsPendingDelete)
	}
	if inst.FailureReason == "" {
		t.Fatal("failure reason should record why the purge failed")
	}
	if inst.DeletionStep != domain.DeletionStepDeleteBackups {
		t.Fatalf("deletion step = %q, want %s", inst.DeletionStep, domain.DeletionStepDeleteBackups)
	}
}

func TestDeprovisionWithDeleteBackupsRefusedWhenPurgerNotWired(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testPurgeProject, "org1-"+testPurgeProject, 1)
	saveActiveProject(t, svc, testPurgeProject)

	err := svc.DeprovisionWithOptions(context.Background(), testPurgeProject, DeprovisionOptions{DeleteBackups: true})
	if !errors.Is(err, ErrBackupPurgeNotConfigured) {
		t.Fatalf("err = %v, want ErrBackupPurgeNotConfigured", err)
	}
	if inst, _ := store.FindByProjectID(testPurgeProject); inst == nil || inst.Status != "ACTIVE" {
		t.Fatal("nothing must be deprovisioned when the request cannot be honoured")
	}
}

func TestDeprovisionWithDeleteBackupsSkipsModeWithoutBackups(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	deleter := newFakeObjectDeleter()
	svc.SetBackupPurger(newTestPurger(deleter))
	unwired := domain.DeploymentMode("unwired")
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "unwired-1", OrgID: "org1", DBType: domain.PostgreSQL, DeploymentMode: unwired, Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	err := svc.DeprovisionWithOptions(context.Background(), "unwired-1", DeprovisionOptions{DeleteBackups: true})
	if err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}
	if inst, _ := store.FindByProjectID("unwired-1"); inst != nil {
		t.Fatal("the row should be deleted; a mode with no platform backups has nothing to purge")
	}
	if deleter.listCalls != 0 {
		t.Fatal("object store must not be listed for a mode with no platform backups")
	}
}

func TestPurgeBackupsRetriesPendingRow(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	deleter := newFakeObjectDeleter(testPurgeProject + "/cloud/wals/x.gz")
	svc.SetBackupPurger(newTestPurger(deleter))
	if err := store.Create(&domain.DatabaseInstance{ProjectID: testPurgeProject, OrgID: "org1", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeK8s, Status: string(domain.StatusBackupsPendingDelete)}); err != nil {
		t.Fatal(err)
	}
	deleted, err := svc.PurgeBackups(context.Background(), testPurgeProject)
	if err != nil {
		t.Fatalf("PurgeBackups: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if inst, _ := store.FindByProjectID(testPurgeProject); inst != nil {
		t.Fatal("row should be removed once the retry succeeds")
	}
}

func TestPurgeBackupsRefusesLiveProject(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	deleter := newFakeObjectDeleter(testPurgeProject + "/cloud/wals/x.gz")
	svc.SetBackupPurger(newTestPurger(deleter))
	saveActiveProject(t, svc, testPurgeProject)

	_, err := svc.PurgeBackups(context.Background(), testPurgeProject)
	if !errors.Is(err, ErrBackupsNotPendingDelete) {
		t.Fatalf("err = %v, want ErrBackupsNotPendingDelete", err)
	}
	if len(deleter.remaining()) != 1 {
		t.Fatal("a live project's backups must never be purged")
	}
}

func TestPurgeBackupsKeepsMarkerOnFailure(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	deleter := newFakeObjectDeleter(testPurgeProject + "/cloud/wals/x.gz")
	deleter.deleteErr = errors.New("still down")
	svc.SetBackupPurger(newTestPurger(deleter))
	if err := store.Create(&domain.DatabaseInstance{ProjectID: testPurgeProject, OrgID: "org1", DBType: domain.PostgreSQL, Status: string(domain.StatusBackupsPendingDelete)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PurgeBackups(context.Background(), testPurgeProject); err == nil {
		t.Fatal("expected purge error")
	}
	if inst, _ := store.FindByProjectID(testPurgeProject); inst == nil || inst.Status != string(domain.StatusBackupsPendingDelete) {
		t.Fatal("marker row must survive a failed retry")
	}
}

func TestPurgeBackupsNotFound(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	svc.SetBackupPurger(newTestPurger(newFakeObjectDeleter()))
	if _, err := svc.PurgeBackups(context.Background(), "missing"); err == nil {
		t.Fatal("expected not-found error")
	}
}
