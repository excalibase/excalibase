package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// passRetryWindow moves the sweep's clock past any backoff it has set.
func passRetryWindow(svc *ProvisioningService) {
	svc.rollbackRetries.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
}

func restoreLab(t *testing.T, registrar *fakeRegistrar) (*K8sBackupAdapter, *ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	svc, store, mock := setupProvisioningTest(t)
	adapter := newRestoreReadyAdapter(t, mock, registrar)
	adapter.SetInstanceStore(store)
	registrar.store = store
	return adapter, svc, store, mock
}

func restoreSource() *domain.DatabaseInstance {
	source := sourceInstance()
	source.OrgID = "org1"
	source.DBType = domain.PostgreSQL
	return source
}

func requireFailedCleanupRecorded(t *testing.T, row *domain.DatabaseInstance, name string) {
	t.Helper()
	for _, result := range parseRollbackLog(row.RollbackLog) {
		if result.Name == name && !result.OK {
			return
		}
	}
	t.Fatalf("rollback log %q does not record %q failing", row.RollbackLog, name)
}

// A registered copy whose namespace delete fails: the row must outlive the
// cleanup, so the cluster stays on record, and the sweep then finishes both.
func TestRestore_AFailedCleanupKeepsTheRowUntilTheSweepFinishesIt(t *testing.T) {
	registrar := &fakeRegistrar{carryErr: errors.New("settings refused")}
	adapter, svc, store, mock := restoreLab(t, registrar)
	mock.DeleteNamespaceError = errors.New("etcdserver: request timed out")

	_, err := adapter.Restore(context.Background(), restoreSource(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("Restore: %v", err)
	}

	row, findErr := store.FindByProjectID("dst")
	if findErr != nil || row == nil {
		t.Fatalf("the row went while the cluster stayed: %v", findErr)
	}
	if row.Status != string(domain.StageFailed) || row.FailureReason == "" {
		t.Fatalf("row status=%s reason=%q, want FAILED with a reason", row.Status, row.FailureReason)
	}
	requireFailedCleanupRecorded(t, row, "delete restore namespace")
	if !mock.Namespaces[row.Namespace] {
		t.Fatal("the test needs the namespace left behind")
	}

	mock.DeleteNamespaceError = nil
	svc.RetryFailedRollbacks(context.Background())
	if mock.Namespaces[row.Namespace] {
		t.Fatal("the sweep did not remove the namespace")
	}
	passRetryWindow(svc)
	svc.RetryFailedRollbacks(context.Background())
	if got, _ := store.FindByProjectID("dst"); got != nil {
		t.Fatalf("the row outlived its cleanup: %s", got.Status)
	}
}

// Failing before the row exists leaves a cluster nothing else knows of, so the
// failure itself writes the record.
func TestRestore_AFailedCleanupBeforeTheRowExistsWritesOne(t *testing.T) {
	registrar := &fakeRegistrar{err: errors.New("vault sealed")}
	adapter, svc, store, mock := restoreLab(t, registrar)
	mock.DeleteNamespaceError = errors.New("etcdserver: request timed out")

	if _, err := adapter.Restore(context.Background(), restoreSource(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("Restore: %v", err)
	}

	row, _ := store.FindByProjectID("dst")
	if row == nil || row.Status != string(domain.StageFailed) || row.DeploymentMode != domain.ModeK8s || row.Namespace == "" {
		t.Fatalf("row %+v, want a FAILED record naming the namespace", row)
	}
	mock.DeleteNamespaceError = nil
	svc.RetryFailedRollbacks(context.Background())
	passRetryWindow(svc)
	svc.RetryFailedRollbacks(context.Background())
	if got, _ := store.FindByProjectID("dst"); got != nil || mock.Namespaces[row.Namespace] {
		t.Fatalf("row %v, namespace there=%v", got, mock.Namespaces[row.Namespace])
	}
}

// Nothing failed: nothing is left, as before.
func TestRestore_ACleanRollbackLeavesNoRow(t *testing.T) {
	registrar := &fakeRegistrar{carryErr: errors.New("settings refused")}
	adapter, _, store, mock := restoreLab(t, registrar)

	if _, err := adapter.Restore(context.Background(), restoreSource(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("Restore: %v", err)
	}
	if got, _ := store.FindByProjectID("dst"); got != nil {
		t.Fatalf("row %s left by a clean rollback", got.Status)
	}
	if mock.Namespaces["org1-dst"] {
		t.Fatal("namespace left by a clean rollback")
	}
}

// An id another project holds is not this restore's row to touch.
func TestRestore_ARowThisRestoreDidNotCreateIsLeftAlone(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := &domain.DatabaseInstance{ProjectID: "dst", OrgID: "org1", Namespace: "org1-dst", DeploymentMode: domain.ModeK8s, Status: string(domain.StatusActive)}
	if err := store.Create(other); err != nil {
		t.Fatal(err)
	}
	pc := provisioner.NewProvisionContext(nil, nil)
	pc.RegisterCleanup("delete restore namespace", func(context.Context) error { return errors.New("blip") })

	_ = failRestore(context.Background(), pc, store, &domain.DatabaseInstance{ProjectID: "dst", OrgID: "org1", Namespace: "org1-dst", DeploymentMode: domain.ModeK8s}, errors.New("conflict"))

	got, _ := store.FindByProjectID("dst")
	if got == nil || got.Status != string(domain.StatusActive) || got.RollbackLog != "" {
		t.Fatalf("another project's row was rewritten: %+v", got)
	}
}
