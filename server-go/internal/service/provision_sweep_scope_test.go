package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// scopedFailure is a rollback log as a real failure writes it.
func scopedFailure(name, reason string) string {
	encoded, _ := json.Marshal([]provisioner.CleanupResult{{Name: name, Error: "x", ForFailure: reason}})
	return string(encoded)
}

func unscopedLog(name string, ok bool) string {
	encoded, _ := json.Marshal([]provisioner.CleanupResult{{Name: name, OK: ok}})
	return string(encoded)
}

// failedRestoreRow is the shape a failed restore leaves.
func failedRestoreRow(id string) *domain.DatabaseInstance {
	namespace := "org1-" + id
	return &domain.DatabaseInstance{ProjectID: id, ProjectName: id, OrgID: "org1", Namespace: namespace, DeploymentMode: domain.ModeK8s, DBType: domain.PostgreSQL,
		Status: string(domain.StageFailed), RestoredFromProjectID: "src", FailureReason: failedRestoreReason,
		RollbackLog: unscopedLog(restoreNamespaceCleanup(namespace), false)}
}

// Every way a row can look like a failed restore without being one.
func TestRetryFailedRollbacks_ARestoreRowMustBeOneInEveryRespect(t *testing.T) {
	yes := true
	cases := map[string]func(*domain.DatabaseInstance){
		"no provenance":  func(row *domain.DatabaseInstance) { row.RestoredFromProjectID = "" },
		"another reason": func(row *domain.DatabaseInstance) { row.FailureReason = "upgrade failed" },
		"protected":      func(row *domain.DatabaseInstance) { row.DeletionProtection = &yes },
		"the cleanup worked": func(row *domain.DatabaseInstance) {
			row.RollbackLog = unscopedLog(restoreNamespaceCleanup(row.Namespace), true)
		},
		"an unnamed cleanup": func(row *domain.DatabaseInstance) { row.RollbackLog = unscopedLog("delete restore namespace", false) },
		"another project's namespace": func(row *domain.DatabaseInstance) {
			row.Namespace = "org1-victim"
			row.RollbackLog = unscopedLog(restoreNamespaceCleanup("org1-victim"), false)
		},
		"the log names another namespace": func(row *domain.DatabaseInstance) {
			row.RollbackLog = unscopedLog(restoreNamespaceCleanup("org1-victim"), false)
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			svc, store, mock := setupProvisioningTest(t)
			row := failedRestoreRow("copy")
			spoil(row)
			mock.Namespaces[row.Namespace] = true
			if err := store.Create(row); err != nil {
				t.Fatal(err)
			}
			svc.RetryFailedRollbacks(context.Background())
			passRetryWindow(svc)
			svc.RetryFailedRollbacks(context.Background())
			if !mock.Namespaces[row.Namespace] {
				t.Fatal("the namespace was deleted")
			}
			if got, _ := store.FindByProjectID("copy"); got == nil || got.Status != string(domain.StageFailed) {
				t.Fatalf("row %+v", got)
			}
		})
	}
	t.Run("and the real shape does get swept", func(t *testing.T) {
		svc, store, mock := setupProvisioningTest(t)
		row := failedRestoreRow("copy")
		mock.Namespaces[row.Namespace] = true
		if err := store.Create(row); err != nil {
			t.Fatal(err)
		}
		svc.RetryFailedRollbacks(context.Background())
		if mock.Namespaces[row.Namespace] {
			t.Fatal("the namespace is still there")
		}
		if got, _ := store.FindByProjectID("copy"); got != nil {
			t.Fatalf("row left: %s", got.Status)
		}
	})
}

// The MySQL adapter's real failure must be recognised too.
func TestRestore_TheMySQLAdaptersFailedRowIsSwept(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	registrar, _ := lab.restoreReady(t)
	registrar.carryErr = errors.New("settings refused")
	lab.mock.DeleteNamespaceError = errors.New("etcdserver: request timed out")
	if _, err := lab.adapter.Restore(context.Background(), lab.source, restoreRequest(domain.RestoreRequest{})); !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("Restore: %v", err)
	}
	row, _ := lab.adapter.instances.FindByProjectID(restoredProject)
	if row == nil {
		t.Fatal("no row kept")
	}
	svc := NewProvisioningService(lab.adapter.instances, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(lab.mock, "")), lab.mock)
	if got := svc.leftoverOf(row); got != leftoverRestore {
		t.Fatalf("leftover %v for row status=%s reason=%q from=%q ns=%s log=%s", got, row.Status, row.FailureReason, row.RestoredFromProjectID, row.Namespace, row.RollbackLog)
	}
}

// A real failed create keeps its license only while it is that failure.
func TestRetryFailedRollbacks_DeletionProtectionStopsANamespaceDelete(t *testing.T) {
	svc, store, mock, row := failedCreateWithLeftoverNamespace(t)
	mock.DeleteNamespaceError = nil
	if err := svc.SetDeletionProtection(row.ProjectID, true); err != nil {
		t.Fatal(err)
	}
	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 || !mock.Namespaces[row.Namespace] {
		t.Fatalf("finished %v, namespace there=%v", finished, mock.Namespaces[row.Namespace])
	}
	if err := svc.SetDeletionProtection(row.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 1 {
		t.Fatalf("finished %v once unprotected", finished)
	}
	_ = store
}

// An operator brings a failed project back with its old log still on the row,
// then a major upgrade fails: the old log must license nothing.
func TestRetryFailedRollbacks_AStaleLogLicensesNothing(t *testing.T) {
	t.Run("a failed create", func(t *testing.T) {
		svc, store, mock, row := failedCreateWithLeftoverNamespace(t)
		mock.DeleteNamespaceError = nil
		broughtBackThenFailedAnUpgrade(t, store, row.ProjectID, "")
		if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 || !mock.Namespaces[row.Namespace] {
			t.Fatalf("finished %v, namespace there=%v", finished, mock.Namespaces[row.Namespace])
		}
	})
	t.Run("a failed restore", func(t *testing.T) {
		svc, store, mock := setupProvisioningTest(t)
		row := failedRestoreRow("copy")
		mock.Namespaces[row.Namespace] = true
		if err := store.Create(row); err != nil {
			t.Fatal(err)
		}
		broughtBackThenFailedAnUpgrade(t, store, "copy", "")
		if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 || !mock.Namespaces[row.Namespace] {
			t.Fatalf("finished %v, namespace there=%v", finished, mock.Namespaces[row.Namespace])
		}
	})
	t.Run("a log with no scope", func(t *testing.T) {
		svc, store, mock := setupProvisioningTest(t)
		mock.Namespaces["org1-p"] = true
		inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "org1", Namespace: "org1-p", DeploymentMode: domain.ModeK8s, Status: "FAILED",
			FailureReason: "exec: boom", RollbackLog: unscopedLog(deleteNamespaceCleanup("org1-p"), false)}
		if err := store.Create(inst); err != nil {
			t.Fatal(err)
		}
		if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 || !mock.Namespaces["org1-p"] {
			t.Fatalf("finished %v", finished)
		}
	})
	t.Run("a row pointing at another project's namespace", func(t *testing.T) {
		svc, store, mock := setupProvisioningTest(t)
		mock.Namespaces["org1-victim"] = true
		inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "org1", Namespace: "org1-victim", DeploymentMode: domain.ModeK8s, Status: "FAILED",
			FailureReason: "exec: boom", RollbackLog: scopedFailure(deleteNamespaceCleanup("org1-victim"), "exec: boom")}
		if err := store.Create(inst); err != nil {
			t.Fatal(err)
		}
		if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 || !mock.Namespaces["org1-victim"] {
			t.Fatalf("finished %v", finished)
		}
	})
}

func broughtBackThenFailedAnUpgrade(t *testing.T, store *storage.FileSystemStore, projectID, _ string) {
	t.Helper()
	row, _ := store.FindByProjectID(projectID)
	row.Status = string(domain.StatusActive)
	if err := store.Update(row); err != nil {
		t.Fatal(err)
	}
	row.Status = string(domain.StageFailed)
	row.FailureReason = "the upgrade stopped before it finished. Its data from just before the upgrade is in backup b1"
	if err := store.Update(row); err != nil {
		t.Fatal(err)
	}
}

type seizableVault struct {
	*fakeVault
	sealed  bool
	listErr error
}

func (v *seizableVault) Sealed() bool { return v.sealed }

func (v *seizableVault) List(prefix string) ([]string, error) {
	if v.listErr != nil {
		return nil, v.listErr
	}
	return v.fakeVault.List(prefix)
}

func failedRestoreLab(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient, *seizableVault) {
	t.Helper()
	svc, store, mock := setupProvisioningTest(t)
	vault := &seizableVault{fakeVault: newFakeVault()}
	svc.SetVault(vault)
	row := failedRestoreRow("copy")
	mock.Namespaces[row.Namespace] = true
	if err := store.Create(row); err != nil {
		t.Fatal(err)
	}
	return svc, store, mock, vault
}

// At boot the vault is sealed: starting the delete then only strands the row.
func TestRetryFailedRollbacks_DoesNotStartTheFinalDeleteWhileTheVaultIsSealed(t *testing.T) {
	svc, store, _, vault := failedRestoreLab(t)
	vault.sealed = true

	svc.RetryFailedRollbacks(context.Background())

	row, _ := store.FindByProjectID("copy")
	if row == nil || row.Status != string(domain.StageFailed) {
		t.Fatalf("row %+v, want it FAILED until the vault opens", row)
	}
	vault.sealed = false
	svc.RetryFailedRollbacks(context.Background())
	if got, _ := store.FindByProjectID("copy"); got != nil {
		t.Fatalf("row left after the vault opened: %s", got.Status)
	}
}

// A delete the sweep started and that stopped part way is the sweep's to finish:
// nobody asked for it, so nobody else will retry it.
func TestRetryFailedRollbacks_ResumesAFinalDeleteThatStopped(t *testing.T) {
	svc, store, _, vault := failedRestoreLab(t)
	vault.listErr = errors.New("vault unavailable")
	svc.RetryFailedRollbacks(context.Background())
	row, _ := store.FindByProjectID("copy")
	if row == nil || row.Status != string(domain.StatusDeleting) {
		t.Fatalf("row %+v, the test needs the delete stopped", row)
	}

	vault.listErr = nil
	passRetryWindow(svc)
	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 1 {
		t.Fatalf("finished %v", finished)
	}
	if got, _ := store.FindByProjectID("copy"); got != nil {
		t.Fatalf("row left: %s step=%s", got.Status, got.DeletionStep)
	}
}

// A delete someone asked for is not the sweep's.
func TestRetryFailedRollbacks_LeavesADeleteItDidNotStart(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.Namespaces["org1-p"] = true
	inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "org1", Namespace: "org1-p", DeploymentMode: domain.ModeK8s, DBType: domain.PostgreSQL, Status: "ACTIVE"}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDeletion("p", nil); err != nil {
		t.Fatal(err)
	}
	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 {
		t.Fatalf("finished %v", finished)
	}
	if !mock.Namespaces["org1-p"] {
		t.Fatal("namespace removed")
	}
}

func namespaceDeletes(mock *k8s.MockClient) int {
	count := 0
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, "DeleteNamespace:") {
			count++
		}
	}
	return count
}

// Terminating is progress: no delete of the row, and no growing backoff either.
func TestRetryFailedRollbacks_ATerminatingNamespaceIsWaitedForNotPenalised(t *testing.T) {
	svc, store, mock, _ := failedRestoreLab(t)
	mock.StuckNamespaces = map[string]bool{"org1-copy": true}

	svc.RetryFailedRollbacks(context.Background())

	row, _ := store.FindByProjectID("copy")
	if row == nil || row.Status != string(domain.StageFailed) {
		t.Fatalf("row %+v, want it kept while the namespace stands", row)
	}
	first := namespaceDeletes(mock)
	svc.RetryFailedRollbacks(context.Background())
	if namespaceDeletes(mock) == first {
		t.Fatal("backed off a namespace that is only terminating")
	}
}

// A panic in one project's retry is that project's failure.
type panicOnDelete struct {
	*k8s.MockClient
	project string
	mu      sync.Mutex
}

func (c *panicOnDelete) DeleteNamespace(ctx context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name == "org1-"+c.project {
		panic("sweep panic")
	}
	return c.MockClient.DeleteNamespace(ctx, name)
}

func TestRetryFailedRollbacks_APanicIsThatProjectsFailure(t *testing.T) {
	_, store, mock := setupProvisioningTest(t)
	cluster := &panicOnDelete{MockClient: mock, project: "bad"}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(cluster, "")), cluster)
	for _, id := range []string{"bad", "good"} {
		namespace := "org1-" + id
		mock.Namespaces[namespace] = true
		inst := &domain.DatabaseInstance{ProjectID: id, OrgID: "org1", Namespace: namespace, DeploymentMode: domain.ModeK8s, Status: "FAILED",
			FailureReason: "exec: boom", RollbackLog: scopedFailure(deleteNamespaceCleanup(namespace), "exec: boom")}
		if err := store.Create(inst); err != nil {
			t.Fatal(err)
		}
	}

	finished := svc.RetryFailedRollbacks(context.Background())

	if len(finished) != 1 || finished[0] != "good" {
		t.Fatalf("finished %v", finished)
	}
	if svc.rollbackRetries.due("bad") {
		t.Fatal("a panicking project is retried at once")
	}
}

// Someone else holding the project is no failure of the retry's.
func TestRetryFailedRollbacks_ABusyProjectDoesNotBackOff(t *testing.T) {
	svc, _, mock, row := failedCreateWithLeftoverNamespace(t)
	mock.DeleteNamespaceError = nil
	release, claimed, err := svc.claimer().Claim(context.Background(), row.ProjectID, OperationDeletion)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	svc.RetryFailedRollbacks(context.Background())
	release()
	if !svc.rollbackRetries.due(row.ProjectID) {
		t.Fatal("backed off because someone else held the lease")
	}
}

// The first retry lands on the next pass, not the one after.
func TestRollbackRetries_TheFirstRetryIsDueOnTheNextPass(t *testing.T) {
	retries := &rollbackRetrySchedule{}
	started := time.Now()
	retries.now = func() time.Time { return started.Add(rollbackRetryAttemptLimit) }
	retries.failed("p")
	retries.now = func() time.Time { return started.Add(RollbackRetryInterval) }
	if !retries.due("p") {
		t.Fatal("an attempt that took its whole limit misses the next pass")
	}
}

// A create without a database has a limit as a build does.
type deadlineSeeingNamespace struct {
	*k8s.MockClient
	bounded bool
}

func (c *deadlineSeeingNamespace) CreateProjectNamespace(ctx context.Context, name, org string) error {
	_, c.bounded = ctx.Deadline()
	return c.MockClient.CreateProjectNamespace(ctx, name, org)
}

func TestProvisionWithoutDatabase_IsBounded(t *testing.T) {
	_, store, mock := setupProvisioningTest(t)
	cluster := &deadlineSeeingNamespace{MockClient: mock}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(cluster, "")), cluster)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())
	if _, err := svc.Provision(context.Background(), domain.ProvisioningRequest{ProjectName: "apps", OrgID: "org1", OwnerID: testUser1, NoDatabase: true}); err != nil {
		t.Fatal(err)
	}
	if !cluster.bounded {
		t.Fatal("the namespace create ran with no deadline")
	}
}

// Without a namespace a record could not be swept and must not take a slot.
func TestRestore_NoNamespaceWritesNoRow(t *testing.T) {
	store := emptyInstanceStore(t)
	pc := provisioner.NewProvisionContext(nil, nil)
	pc.RegisterCleanup("remove restored container", func(context.Context) error { return errors.New("engine busy") })

	_ = failRestore(context.Background(), pc, store, &domain.DatabaseInstance{ProjectID: "dst"}, errors.New("never healthy"))

	if got, _ := store.FindByProjectID("dst"); got != nil {
		t.Fatalf("row written: %+v", got)
	}
}

// The source's protection must not stop the copy's own removal.
func TestRestore_AFailedCopyDoesNotKeepProtection(t *testing.T) {
	store := emptyInstanceStore(t)
	yes := true
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "dst", OrgID: "org1", Status: string(domain.StatusRestoring), DeletionProtection: &yes}); err != nil {
		t.Fatal(err)
	}
	markRestoredRowFailed(store, "dst", "[]")
	got, _ := store.FindByProjectID("dst")
	if got.Status != "FAILED" || got.DeletionProtection != nil {
		t.Fatalf("row %+v", got)
	}
}

// The waiting note alone also holds a new add: the cluster's removal is recorded
// done only by the attempt that then purges.
func TestAddDatabase_RefusedWhileThePurgeStillWaits(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	row, _ := store.FindByProjectID(created.ProjectID)
	row.FailureReason = "exec: boom" + purgeWaitingNote
	if err := store.Update(row); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("add: %v", err)
	}
}
