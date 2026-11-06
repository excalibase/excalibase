package service

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// failedAddWithLeftovers is an add whose role creation failed and whose removal
// failed once. Its cluster stands, and so goes on archiving.
func failedAddWithLeftovers(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient, *fakeObjectDeleter, string) {
	t.Helper()
	svc, store, mock := failingExecService(t)
	created := createWithoutDatabase(t, svc)
	deleter := newFakeObjectDeleter()
	svc.SetBackupPurger(newTestPurger(deleter))
	mock.DeleteCRDError = errors.New("etcdserver: request timed out")
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add: %v", err)
	}
	return svc, store, mock, deleter, created.ProjectID
}

func TestFailedAdd_APurgeWaitsForTheClusterAndTheSweepRunsIt(t *testing.T) {
	svc, store, mock, deleter, projectID := failedAddWithLeftovers(t)
	if len(deleter.deleteCalls) != 0 {
		t.Fatal("purged while the cluster still stands and archives")
	}
	wal := projectID + "/cloud/wals/0001/000000010000000000000002.gz"
	deleter.keys = append(deleter.keys, wal)
	mock.DeleteCRDError = nil

	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 1 {
		t.Fatalf("finished %v", finished)
	}

	if len(mock.CRDs) != 0 {
		t.Errorf("%d resources left", len(mock.CRDs))
	}
	if len(deleter.keys) != 0 {
		t.Fatalf("objects left under the prefix: %v", deleter.keys)
	}
	row, _ := store.FindByProjectID(projectID)
	requireCleanRollback(t, row)
	if strings.Contains(row.FailureReason, "backup files") {
		t.Errorf("the reason still talks about the backup files: %q", row.FailureReason)
	}
}

func TestFailedAdd_APurgeThatFailsInTheSweepIsTriedAgain(t *testing.T) {
	svc, store, mock, deleter, projectID := failedAddWithLeftovers(t)
	deleter.keys = append(deleter.keys, projectID+"/cloud/wals/x.gz")
	deleter.deleteErr = errors.New("object store down")
	mock.DeleteCRDError = nil

	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 {
		t.Fatalf("reported finished with the objects still there: %v", finished)
	}
	row, _ := store.FindByProjectID(projectID)
	requireCleanRollback(t, row)
	if !strings.Contains(row.FailureReason, "could not be removed") {
		t.Fatalf("reason %q does not say the purge failed", row.FailureReason)
	}

	deleter.deleteErr = nil
	passRetryWindow(svc)
	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 1 {
		t.Fatalf("finished %v once the store works", finished)
	}
	if len(deleter.keys) != 0 {
		t.Fatalf("objects left: %v", deleter.keys)
	}
}

// A row already stuck on a purge that failed, from before the sweep ran purges.
func TestFailedAdd_ARowAlreadyStuckOnAFailedPurgeHeals(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	deleter := newFakeObjectDeleter()
	svc.SetBackupPurger(newTestPurger(deleter))
	inst := &domain.DatabaseInstance{ProjectID: "stuck", OrgID: "org1", Namespace: "org1-stuck", DeploymentMode: domain.ModeK8s, DBType: domain.PostgreSQL,
		NoDatabase: true, Status: string(domain.StatusActive), FailureReason: "exec: boom" + purgeFailedNote}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	deleter.keys = append(deleter.keys, "stuck/cloud/wals/x.gz")

	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 1 {
		t.Fatalf("finished %v", finished)
	}
	if len(deleter.keys) != 0 {
		t.Fatalf("objects left: %v", deleter.keys)
	}
}

// Writing back the row read before a long removal would undo whatever changed
// meanwhile; deletion protection takes no lease.
type holdingRemoval struct {
	*failingExecCluster
	entered, release chan struct{}
	armed            atomic.Bool
}

func (c *holdingRemoval) DeleteCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	if c.armed.CompareAndSwap(true, false) {
		close(c.entered)
		<-c.release
	}
	return c.MockClient.DeleteCRD(ctx, gvr, namespace, name)
}

func TestRetryFailedRollbacks_DoesNotUndoAWriteMadeWhileItRan(t *testing.T) {
	_, store, mock := setupProvisioningTest(t)
	cluster := &holdingRemoval{failingExecCluster: &failingExecCluster{MockClient: mock}, entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(cluster, "")), cluster)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())
	withBackupTarget(t, svc)
	created := createWithoutDatabase(t, svc)
	mock.DeleteCRDError = errors.New("blip")
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatal(err)
	}
	mock.DeleteCRDError = nil
	cluster.armed.Store(true)
	done := make(chan []string, 1)
	go func() { done <- svc.RetryFailedRollbacks(context.Background()) }()
	<-cluster.entered
	if err := svc.SetDeletionProtection(created.ProjectID, true); err != nil {
		t.Fatal(err)
	}
	close(cluster.release)
	if finished := <-done; len(finished) != 1 {
		t.Fatalf("finished %v", finished)
	}

	row, _ := store.FindByProjectID(created.ProjectID)
	if row.DeletionProtection == nil || !*row.DeletionProtection {
		t.Fatal("the sweep undid deletion protection set while it ran")
	}
	requireCleanRollback(t, row)
}

// stuckRemoval never sees a cluster go, as a wedged finalizer looks.
type stuckRemoval struct {
	*failingExecCluster
	stuckNamespace string
	attempts       atomic.Int32
}

func (c *stuckRemoval) CRDExists(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (bool, error) {
	if namespace == c.stuckNamespace {
		c.attempts.Add(1)
		return true, nil
	}
	return c.MockClient.CRDExists(ctx, gvr, namespace, name)
}

func stuckRemovalService(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient, *stuckRemoval) {
	t.Helper()
	_, store, mock := setupProvisioningTest(t)
	cluster := &stuckRemoval{failingExecCluster: &failingExecCluster{MockClient: mock}}
	pg := provisioner.NewPostgreSQLProvisioner(cluster, "")
	pg.SetDeletionPoller(provisioner.Poller{Interval: 5 * time.Millisecond, Timeout: time.Hour})
	svc := NewProvisioningService(store, provisioner.NewFactory(pg), cluster)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())
	withBackupTarget(t, svc)
	svc.rollbackRetries.attemptLimit = 50 * time.Millisecond
	return svc, store, mock, cluster
}

func addFailedRemovalRow(t *testing.T, store *storage.FileSystemStore, projectID string) {
	t.Helper()
	namespace := "org1-" + projectID
	inst := &domain.DatabaseInstance{ProjectID: projectID, OrgID: "org1", Namespace: namespace, DeploymentMode: domain.ModeK8s, DBType: domain.PostgreSQL,
		NoDatabase: true, Status: string(domain.StatusActive), RollbackLog: `[{"name":"` + removeDatabaseCleanup(namespace) + `","ok":false,"error":"x"}]`}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
}

// A removal that cannot finish must leave the project's lease free nearly all
// the time, so a DELETE or an add is not told busy for as long as it is stuck.
func TestRetryFailedRollbacks_BacksOffAStuckRemovalAndFreesTheLease(t *testing.T) {
	svc, store, _, cluster := stuckRemovalService(t)
	cluster.stuckNamespace = "org1-stuck"
	addFailedRemovalRow(t, store, "stuck")

	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 {
		t.Fatalf("finished %v", finished)
	}
	first := cluster.attempts.Load()
	if first == 0 {
		t.Fatal("no attempt made")
	}
	release, claimed, err := svc.claimer().Claim(context.Background(), "stuck", OperationDeletion)
	if err != nil || !claimed {
		t.Fatalf("the lease is still held between attempts: claimed=%v err=%v", claimed, err)
	}
	release()

	svc.RetryFailedRollbacks(context.Background())
	if got := cluster.attempts.Load(); got != first {
		t.Fatalf("tried again straight away: %d looks, was %d", got, first)
	}

	passRetryWindow(svc)
	svc.RetryFailedRollbacks(context.Background())
	if got := cluster.attempts.Load(); got == first {
		t.Fatal("never tried again once the backoff passed")
	}
}

func TestRollbackRetries_TheBackoffGrowsAndIsCapped(t *testing.T) {
	retries := &rollbackRetrySchedule{}
	now := time.Now()
	retries.now = func() time.Time { return now }
	retries.failed("p")
	first := retries.next["p"].delay
	retries.failed("p")
	if retries.next["p"].delay != 2*first {
		t.Fatalf("delay %s after %s", retries.next["p"].delay, first)
	}
	for range 20 {
		retries.failed("p")
	}
	if retries.next["p"].delay != rollbackRetryMaxDelay {
		t.Fatalf("delay %s, want the cap %s", retries.next["p"].delay, rollbackRetryMaxDelay)
	}
	retries.succeeded("p")
	if !retries.due("p") {
		t.Fatal("a success did not clear the backoff")
	}
}

// One stuck project does not hold up the others in a pass.
func TestRetryFailedRollbacks_OneStuckProjectDoesNotDelayTheOthers(t *testing.T) {
	svc, store, mock, cluster := stuckRemovalService(t)
	cluster.stuckNamespace = "org1-stuck"
	svc.rollbackRetries.attemptLimit = 2 * time.Second
	addFailedRemovalRow(t, store, "stuck")
	addFailedRemovalRow(t, store, "fine")
	mock.Namespaces["org1-fine"] = true

	started := time.Now()
	finishedFine := make(chan struct{})
	go func() {
		for {
			row, _ := store.FindByProjectID("fine")
			if row != nil && !hasFailedCleanup(row) {
				close(finishedFine)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	done := make(chan []string, 1)
	go func() { done <- svc.RetryFailedRollbacks(context.Background()) }()
	select {
	case <-finishedFine:
	case <-time.After(1500 * time.Millisecond):
		t.Fatalf("the other project waited on the stuck one for %s", time.Since(started))
	}
	<-done
}

func hasFailedCleanup(row *domain.DatabaseInstance) bool {
	for _, result := range parseRollbackLog(row.RollbackLog) {
		if !result.OK {
			return true
		}
	}
	return false
}

// While the old cluster still stands a new add is refused and the record kept:
// a retry on another engine would otherwise overwrite the engine and the log.
func TestAddDatabase_RefusedWhileTheFailedAddsClusterStands(t *testing.T) {
	svc, store, mock, _, projectID := failedAddWithLeftovers(t)
	before, _ := store.FindByProjectID(projectID)

	_, err := svc.AddDatabase(context.Background(), projectID, addPostgres())

	if !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("add: %v, want it refused as busy", err)
	}
	after, _ := store.FindByProjectID(projectID)
	if after.RollbackLog != before.RollbackLog || after.DBType != before.DBType || after.Status != string(domain.StatusActive) {
		t.Fatalf("the refused add changed the row: %+v", after)
	}
	mock.DeleteCRDError = nil
	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 1 {
		t.Fatalf("finished %v", finished)
	}
}

// Once the row says it has a database nothing may roll that database back: the
// flag has no way back, so the project would read ACTIVE with nothing behind it.
func TestAddDatabase_APanicAfterItIsRegisteredDoesNotRemoveTheDatabase(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	created := createWithoutDatabase(t, svc)
	svc.quotaSync = func(context.Context, string) error { panic("announce panic") }

	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add: %v", err)
	}

	row, _ := store.FindByProjectID(created.ProjectID)
	if row.Status != string(domain.StatusActive) || row.NoDatabase || row.Host == "" || len(mock.CRDs) == 0 {
		t.Fatalf("status=%s noDatabase=%v host=%q crds=%d, want it running with its database", row.Status, row.NoDatabase, row.Host, len(mock.CRDs))
	}
}

// Other survivors of the mutation run.
func TestRetryFailedRollbacks_LeavesADockerRowNamingANamespaceDelete(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.Namespaces["org1-p"] = true
	inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "org1", Namespace: "org1-p", DeploymentMode: domain.ModeDocker,
		Status: "FAILED", RollbackLog: `[{"name":"delete namespace org1-p","ok":false,"error":"x"}]`}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 0 || !mock.Namespaces["org1-p"] {
		t.Fatalf("finished %v, namespace there=%v", finished, mock.Namespaces["org1-p"])
	}
}

type contextRecordingDeleter struct {
	*fakeObjectDeleter
	listErr error
	bounded bool
}

func (d *contextRecordingDeleter) ListKeys(ctx context.Context, bucket, prefix, token string, pageSize int32) ([]string, string, error) {
	d.listErr = ctx.Err()
	_, d.bounded = ctx.Deadline()
	return d.fakeObjectDeleter.ListKeys(ctx, bucket, prefix, token, pageSize)
}

func TestFailedAddPurge_RunsOnALiveBoundedContext(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	deleter := &contextRecordingDeleter{fakeObjectDeleter: newFakeObjectDeleter()}
	svc.SetBackupPurger(NewBackupPurger(StaticBackupStorage(testBackupCreds()), "backups/",
		func(context.Context, *domain.S3Credentials) (ObjectDeleter, error) { return deleter, nil }))
	enabled := true
	inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "org1", DeploymentMode: domain.ModeK8s, DBType: domain.PostgreSQL, BackupEnabled: &enabled}
	finished, cancel := context.WithCancel(context.Background())
	cancel()

	svc.purgeFailedAddBackups(finished, inst)

	if deleter.listErr != nil || !deleter.bounded {
		t.Fatalf("purge ran with err=%v bounded=%v", deleter.listErr, deleter.bounded)
	}
}

func TestRecoverInterruptedAdd_RemovesOnALiveContext(t *testing.T) {
	svc, store, cluster := liveClusterService(t)
	inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "org1", Namespace: "org1-p", DeploymentMode: domain.ModeK8s, DBType: domain.PostgreSQL,
		NoDatabase: true, Status: string(domain.StatusProvisioning)}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	cluster.CRDs["org1-p/p-postgres"] = nil
	finished, cancel := context.WithCancel(context.Background())
	cancel()

	if err := svc.recoverInterruptedAdd(finished, "p"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(cluster.CRDs) != 0 {
		t.Fatalf("%d resources left", len(cluster.CRDs))
	}
}

// A hang-up mid-way through a create without a database must not fail it.
type hangUpAtNamespace struct {
	*k8s.MockClient
	hangUp context.CancelFunc
}

func (c *hangUpAtNamespace) CreateProjectNamespace(ctx context.Context, name, org string) error {
	c.hangUp()
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.MockClient.CreateProjectNamespace(ctx, name, org)
}

func TestProvisionWithoutDatabase_SurvivesAHangUp(t *testing.T) {
	_, store, mock := setupProvisioningTest(t)
	request, hangUp := context.WithCancel(context.Background())
	defer hangUp()
	cluster := &hangUpAtNamespace{MockClient: mock, hangUp: hangUp}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(cluster, "")), cluster)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())

	resp, err := svc.Provision(request, domain.ProvisioningRequest{ProjectName: "apps only", OrgID: "org1", OwnerID: testUser1, NoDatabase: true})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if resp.Status != string(domain.StatusActive) {
		t.Fatalf("status %s (%s), want ACTIVE", resp.Status, resp.FailureReason)
	}
}

// A panic in one cleanup is that cleanup's failure, not the end of the rollback.
type panicAtNamespaceDelete struct{ *failingExecCluster }

func (c *panicAtNamespaceDelete) DeleteNamespace(context.Context, string) error {
	panic("cleanup panic")
}

func TestBuild_APanicInACleanupStillFailsTheProject(t *testing.T) {
	_, store, mock := setupProvisioningTest(t)
	cluster := &panicAtNamespaceDelete{&failingExecCluster{MockClient: mock}}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(cluster, "")), cluster)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())
	withBackupTarget(t, svc)

	resp, err := svc.Provision(context.Background(), newProjectRequest())
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	row, _ := store.FindByProjectID(resp.ProjectID)
	if row.Status != "FAILED" {
		t.Fatalf("status %s, want FAILED", row.Status)
	}
	requireFailedCleanupRecorded(t, row, deleteNamespaceCleanup(row.Namespace))
	if strings.Contains(row.RollbackLog, "cleanup panic") {
		t.Errorf("the log exposes the panic: %s", row.RollbackLog)
	}
}
