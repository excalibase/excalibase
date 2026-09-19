package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// pendingPurgeProject drives a project to BACKUPS_PENDING_DELETE: the purge
// was confirmed and failed, so its backup objects are still out there.
func pendingPurgeProject(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *fakeObjectDeleter) {
	t.Helper()
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testPurgeProject, "org1-"+testPurgeProject, 1)
	deleter := newFakeObjectDeleter(testPurgeProject + "/cloud/base/b1/data.tar.gz")
	deleter.deleteErr = errors.New("r2 unavailable")
	svc.SetBackupPurger(newTestPurger(deleter))
	saveActiveProject(t, svc, testPurgeProject)

	if err := svc.DeprovisionWithOptions(context.Background(), testPurgeProject,
		DeprovisionOptions{DeleteBackups: DeleteBackupsOption(true)}); err == nil {
		t.Fatal("the failed purge must be reported")
	}
	inst, _ := store.FindByProjectID(testPurgeProject)
	if inst == nil || inst.Status != string(domain.StatusBackupsPendingDelete) {
		t.Fatalf("precondition: row = %v, want BACKUPS_PENDING_DELETE", inst)
	}
	return svc, store, deleter
}

// The finding: a retry carrying no body used to drop the confirmed purge,
// remove the record and strand the objects with nothing left pointing at them.
func TestBareRetryKeepsTheConfirmedBackupPurge(t *testing.T) {
	svc, store, deleter := pendingPurgeProject(t)
	deleter.deleteErr = nil

	if err := svc.DeprovisionWithOptions(context.Background(), testPurgeProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("bare retry: %v", err)
	}
	if inst, _ := store.FindByProjectID(testPurgeProject); inst != nil {
		t.Error("row should be gone once the purge succeeded")
	}
	if rem := deleter.remaining(); len(rem) != 0 {
		t.Errorf("backups orphaned by the retry: %v", rem)
	}
}

// Asking to keep backups a previous attempt was told to purge is refused:
// the objects may already be partly gone, so "keep" would name a set that no
// longer exists.
func TestRetryCannotDowngradeAConfirmedPurge(t *testing.T) {
	svc, store, deleter := pendingPurgeProject(t)
	deleter.deleteErr = nil

	err := svc.DeprovisionWithOptions(context.Background(), testPurgeProject,
		DeprovisionOptions{DeleteBackups: DeleteBackupsOption(false)})
	if !errors.Is(err, storage.ErrBackupPurgeAlreadyConfirmed) {
		t.Fatalf("err = %v, want ErrBackupPurgeAlreadyConfirmed", err)
	}
	if inst, _ := store.FindByProjectID(testPurgeProject); inst == nil {
		t.Error("row must survive a refused retry")
	}
	if len(deleter.remaining()) != 1 {
		t.Error("a refused retry must not touch the objects")
	}
}

// A deletion that keeps the backups reports where they stay, because once the
// record is gone nothing in the API names them any more.
func TestDeletionReportsRetainedBackupPrefix(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(testPurgeProject, "org1-"+testPurgeProject, 1)
	svc.SetBackupPurger(newTestPurger(newFakeObjectDeleter(testPurgeProject + "/cloud/base/b1/data.tar.gz")))
	saveActiveProject(t, svc, testPurgeProject)

	inst, _ := svc.GetInstance(testPurgeProject)
	prefix, ok := svc.RetainedBackupPrefix(inst)
	if !ok || prefix == "" {
		t.Fatalf("RetainedBackupPrefix = %q, %v; want the project's prefix", prefix, ok)
	}
	if err := svc.Deprovision(context.Background(), testPurgeProject); err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}
}
