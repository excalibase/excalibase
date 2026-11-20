package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// sealingVault seals once a path with the given suffix is filed, as a vault
// sealed by an operator mid-registration does.
type sealingVault struct {
	*fakeVault
	sealAfter string
	sealed    bool
}

func (v *sealingVault) Sealed() bool { return v.sealed }

func (v *sealingVault) Put(path string, data map[string]string) error {
	if v.sealed {
		return vault.ErrSealed
	}
	if err := v.fakeVault.Put(path, data); err != nil {
		return err
	}
	if v.sealAfter != "" && len(path) >= len(v.sealAfter) && path[len(path)-len(v.sealAfter):] == v.sealAfter {
		v.sealed = true
	}
	return nil
}

func (v *sealingVault) Delete(path string) error {
	if v.sealed {
		return vault.ErrSealed
	}
	return v.fakeVault.Delete(path)
}

func TestAddDatabase_CanBeRetriedAfterTheVaultSealedDuringRegistration(t *testing.T) {
	svc, store, _ := liveClusterService(t)
	created := createWithoutDatabase(t, svc)
	sealing := &sealingVault{fakeVault: newFakeVault(), sealAfter: "/" + roleAdmin}
	svc.SetVault(sealing)

	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add: %v", err)
	}
	row, _ := store.FindByProjectID(created.ProjectID)
	if !row.NoDatabase || row.FailureReason == "" {
		t.Fatalf("the test needs a failed add: nodb=%v reason=%q", row.NoDatabase, row.FailureReason)
	}
	if _, stored := sealing.data[vaultCredentialPath(created.ProjectID, roleAdmin)]; !stored {
		t.Fatal("the test needs the admin secret left behind")
	}

	// Still sealed: the logins cannot be cleared, so no cluster is built for it.
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("add while sealed: %v, want ErrProjectOperationRunning", err)
	}

	sealing.sealed, sealing.sealAfter = false, ""
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	row, _ = store.FindByProjectID(created.ProjectID)
	if row.NoDatabase || row.Status != string(domain.StatusActive) {
		t.Fatalf("retry after unseal: status=%s nodb=%v reason=%q", row.Status, row.NoDatabase, row.FailureReason)
	}
}

// The secrets a failed add never filed are not its to remove, and an add with no
// failure behind it keeps the guard against a project sharing the id.
func TestAddDatabase_AFirstAddStillRefusesCredentialsAlreadyFiled(t *testing.T) {
	svc, store, _ := liveClusterService(t)
	created := createWithoutDatabase(t, svc)
	secrets := newFakeVault()
	svc.SetVault(secrets)
	path := vaultCredentialPath(created.ProjectID, roleApp)
	secrets.data[path] = map[string]string{"username": "someone else"}

	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add: %v", err)
	}
	row, _ := store.FindByProjectID(created.ProjectID)
	if !row.NoDatabase {
		t.Fatal("the add went through over credentials it did not file")
	}
	if _, kept := secrets.data[path]; !kept {
		t.Fatal("a first add removed credentials it did not file")
	}
}

// A process that dies after the role records are filed: the watcher deploy runs after them.
func TestRecoverInterruptedAdd_ClearsEveryDatabaseLogin(t *testing.T) {
	svc, store, _ := failingExecService(t)
	created := createWithoutDatabase(t, svc)
	secrets := newFakeVault()
	svc.SetVault(secrets)
	for _, role := range []string{roleAdmin, roleAuthAdmin, roleApp, roleWatcher, roleScheduler, roleReader, docBrowserCredential} {
		if err := secrets.Put(vaultCredentialPath(created.ProjectID, role), map[string]string{"username": role}); err != nil {
			t.Fatal(err)
		}
	}
	row, _ := store.FindByProjectID(created.ProjectID)
	row.Status = string(domain.StatusProvisioning)
	if err := store.Update(row); err != nil {
		t.Fatal(err)
	}
	if got := svc.RecoverInterruptedDatabaseAdds(context.Background()); len(got) != 1 {
		t.Fatalf("recovered %v", got)
	}
	if err := svc.assertNoStoredCredentials(created.ProjectID); err != nil {
		left, _ := secrets.List(vaultCredentialPrefix(created.ProjectID))
		t.Errorf("the add a recovery promises can be retried is refused: %v, left %v", err, left)
	}
}

// failedAddOpenToDelete is a failed add whose protection a caller has turned off,
// as they must before a DELETE: the add recorded the plan's protection.
func failedAddOpenToDelete(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient, *fakeObjectDeleter, string) {
	t.Helper()
	svc, store, mock, deleter, projectID := failedAddWithLeftovers(t)
	if err := svc.SetDeletionProtection(projectID, false); err != nil {
		t.Fatal(err)
	}
	return svc, store, mock, deleter, projectID
}

func TestScheduleDeletion_RemovesAFailedAddsClusterAndKeepsItsBackups(t *testing.T) {
	svc, store, mock, deleter, projectID := failedAddOpenToDelete(t)
	mock.DeleteCRDError = nil
	deleter.keys = append(deleter.keys, projectID+"/cloud/base/1")

	scheduled, err := svc.ScheduleDeletion(context.Background(), projectID, DeprovisionOptions{})
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if scheduled.Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("status %s", scheduled.Status)
	}
	if len(mock.CRDs) != 0 {
		t.Errorf("%d resources of the failed add stand through the grace period", len(mock.CRDs))
	}
	if len(deleter.deleteCalls) != 0 {
		t.Error("backups were purged ahead of the grace period")
	}
	row, _ := store.FindByProjectID(projectID)
	if failedAddRemovalFailed(row) {
		t.Errorf("the removal is still on record as failed: %s", row.RollbackLog)
	}
	if svc.leftoverOf(row) != noLeftover {
		t.Errorf("leftover %v, the sweep would touch the grace period's backups", svc.leftoverOf(row))
	}
}

func TestScheduleDeletion_AProtectedFailedAddKeepsItsCluster(t *testing.T) {
	svc, _, mock, _, projectID := failedAddOpenToDelete(t)
	mock.DeleteCRDError = nil
	standing := len(mock.CRDs)
	if err := svc.SetDeletionProtection(projectID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ScheduleDeletion(context.Background(), projectID, DeprovisionOptions{}); !errors.Is(err, ErrDeletionProtected) {
		t.Fatalf("DELETE: %v, want ErrDeletionProtected", err)
	}
	if len(mock.CRDs) != standing {
		t.Fatal("a refused DELETE removed the cluster")
	}
}

// A removal that fails at schedule time doesn't fail the DELETE; the sweep goes on trying.
func TestRetryFailedRollbacks_FinishesARemovalThatFailedWhenTheDeleteWasScheduled(t *testing.T) {
	svc, store, mock, deleter, projectID := failedAddOpenToDelete(t)
	if _, err := svc.ScheduleDeletion(context.Background(), projectID, DeprovisionOptions{}); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if len(mock.CRDs) == 0 {
		t.Fatal("the test needs the cluster left behind")
	}
	mock.DeleteCRDError = nil
	deleter.keys = append(deleter.keys, projectID+"/cloud/base/1")

	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 1 {
		t.Fatalf("finished %v", finished)
	}

	if len(mock.CRDs) != 0 {
		t.Errorf("%d resources left", len(mock.CRDs))
	}
	if len(deleter.deleteCalls) != 0 {
		t.Error("the sweep purged backups the grace period keeps")
	}
	row, _ := store.FindByProjectID(projectID)
	if row.Status != string(domain.StatusPendingDeletion) || row.DeletionDueAt == nil {
		t.Fatalf("the schedule did not survive: %s", row.Status)
	}
}

func TestCancelDeletion_APurgeThatWaitedRunsOnceCancelled(t *testing.T) {
	svc, _, mock, deleter, projectID := failedAddOpenToDelete(t)
	mock.DeleteCRDError = nil
	if _, err := svc.ScheduleDeletion(context.Background(), projectID, DeprovisionOptions{}); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if err := svc.CancelDeletion(context.Background(), projectID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	deleter.keys = append(deleter.keys, projectID+"/cloud/base/1")

	svc.RetryFailedRollbacks(context.Background())

	if len(deleter.deleteCalls) == 0 {
		t.Fatal("the failed add's backups were never purged")
	}
}

// A log written before the scope existed: the cluster may stand all the same.
func TestAddDatabase_AnOldLogCannotLetAnAddThroughOverAStandingCluster(t *testing.T) {
	svc, store, mock, _, projectID := failedAddWithLeftovers(t)
	row, _ := store.FindByProjectID(projectID)
	row.FailureReason = "exec: boom"
	row.RollbackLog = unscopedLog(removeDatabaseCleanup(row.Namespace), false)
	if err := store.Update(row); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.AddDatabase(context.Background(), projectID, addPostgres()); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("add: %v, want ErrProjectOperationRunning", err)
	}

	mock.DeleteCRDError = nil
	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 1 {
		t.Fatalf("the sweep left the old row: finished %v", finished)
	}
	if len(mock.CRDs) != 0 {
		t.Errorf("%d resources left", len(mock.CRDs))
	}
}

func TestRetryFailedRollbacks_LeavesAProtectedRowAwaitingDeletion(t *testing.T) {
	svc, store, mock, _, projectID := failedAddOpenToDelete(t)
	if _, err := svc.ScheduleDeletion(context.Background(), projectID, DeprovisionOptions{}); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	mock.DeleteCRDError = nil
	standing := len(mock.CRDs)
	row, _ := store.FindByProjectID(projectID)
	protected := true
	row.DeletionProtection = &protected
	if err := store.Update(row); err != nil {
		t.Fatal(err)
	}

	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 || len(mock.CRDs) != standing {
		t.Fatalf("finished %v, %d of %d resources left", finished, len(mock.CRDs), standing)
	}
}

func TestScheduleDeletion_TouchesNoClusterWithoutAFailedAdd(t *testing.T) {
	svc, _, mock := failingExecService(t)
	created := createWithoutDatabase(t, svc)
	if err := svc.SetDeletionProtection(created.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ScheduleDeletion(context.Background(), created.ProjectID, DeprovisionOptions{}); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, "DeleteCRD:") {
			t.Fatalf("a project with no failed add saw %s", call)
		}
	}
}
