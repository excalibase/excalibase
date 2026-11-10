package service

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// failingExecCluster fails role creation with an ordinary error, the caller
// still connected.
type failingExecCluster struct{ *k8s.MockClient }

func (c *failingExecCluster) ExecInPod(ctx context.Context, namespace, pod, container string, cmd []string) (string, error) {
	if strings.Contains(strings.Join(cmd, " "), "CREATE ROLE") {
		return "", errors.New("exec: boom")
	}
	return c.MockClient.ExecInPod(ctx, namespace, pod, container, cmd)
}

func (c *failingExecCluster) ExecInPodStdin(ctx context.Context, namespace, pod, container string, cmd []string, stdin string) (string, error) {
	if strings.Contains(strings.Join(cmd, " ")+stdin, "CREATE ROLE") {
		return "", errors.New("exec: boom")
	}
	return c.MockClient.ExecInPodStdin(ctx, namespace, pod, container, cmd, stdin)
}

func failingExecService(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	_, store, mock := setupProvisioningTest(t)
	cluster := &failingExecCluster{MockClient: mock}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(cluster, "")), cluster)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())
	withBackupTarget(t, svc)
	return svc, store, mock
}

// failedCreateWithLeftoverNamespace is a create whose role creation failed and
// whose namespace delete failed once, an API blip since passed.
func failedCreateWithLeftoverNamespace(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient, *domain.DatabaseInstance) {
	t.Helper()
	svc, store, mock := failingExecService(t)
	mock.DeleteNamespaceError = errors.New("etcdserver: request timed out")
	resp, err := svc.Provision(context.Background(), newProjectRequest())
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	row, _ := store.FindByProjectID(resp.ProjectID)
	if row.Status != "FAILED" || !mock.Namespaces[row.Namespace] {
		t.Fatalf("status=%s namespace there=%v, want FAILED with its namespace left", row.Status, mock.Namespaces[row.Namespace])
	}
	return svc, store, mock, row
}

func TestRollbackFailure_IsLogged(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	_, _, _, row := failedCreateWithLeftoverNamespace(t)

	line := logged.String()
	if !strings.Contains(line, "delete namespace "+row.Namespace) || !strings.Contains(line, "etcdserver: request timed out") {
		t.Fatalf("the log does not name the failed cleanup and why:\n%s", line)
	}
}

func TestRetryFailedRollbacks_RemovesTheLeftoverNamespaceOnce(t *testing.T) {
	svc, store, mock, row := failedCreateWithLeftoverNamespace(t)
	mock.DeleteNamespaceError = nil

	retried := svc.RetryFailedRollbacks(context.Background())

	if len(retried) != 1 || retried[0] != row.ProjectID {
		t.Fatalf("retried %v, want %s", retried, row.ProjectID)
	}
	if mock.Namespaces[row.Namespace] {
		t.Fatal("namespace still there")
	}
	after, _ := store.FindByProjectID(row.ProjectID)
	if after.Status != "FAILED" {
		t.Errorf("status %s, want FAILED: the failure stays the record", after.Status)
	}
	requireCleanRollback(t, after)
	if again := svc.RetryFailedRollbacks(context.Background()); len(again) != 0 {
		t.Errorf("a clean row was retried again: %v", again)
	}
}

func TestRetryFailedRollbacks_KeepsTryingWhileTheDeleteFails(t *testing.T) {
	svc, store, mock, row := failedCreateWithLeftoverNamespace(t)

	if retried := svc.RetryFailedRollbacks(context.Background()); len(retried) != 0 {
		t.Fatalf("reported %v as cleaned while the delete still fails", retried)
	}
	after, _ := store.FindByProjectID(row.ProjectID)
	if after.RollbackLog != row.RollbackLog {
		t.Fatalf("rollback log rewritten by a failed retry: %s", after.RollbackLog)
	}

	mock.DeleteNamespaceError = nil
	passRetryWindow(svc)
	if retried := svc.RetryFailedRollbacks(context.Background()); len(retried) != 1 {
		t.Fatalf("retried %v once the delete works", retried)
	}
}

// FAILED alone proves nothing: a major upgrade can leave a live project FAILED
// with the customer's data in its namespace. Only a rollback that recorded this
// very delete failing licenses a retry.
func TestRetryFailedRollbacks_LeavesAFailedProjectWhoseRollbackDidNotAskForIt(t *testing.T) {
	cases := map[string]string{
		"no rollback":             "",
		"unreadable log":          "not json",
		"delete that worked":      `[{"name":"delete namespace org1-live","ok":true}]`,
		"another namespace":       `[{"name":"delete namespace org1-other","ok":false,"error":"x"}]`,
		"another cleanup":         `[{"name":"delete vault x","ok":false,"error":"x"}]`,
		"removal, not the delete": `[{"name":"remove database from namespace org1-live","ok":false,"error":"x"}]`,
	}
	for name, rollbackLog := range cases {
		t.Run(name, func(t *testing.T) {
			svc, store, mock := setupProvisioningTest(t)
			mock.Namespaces["org1-live"] = true
			inst := &domain.DatabaseInstance{ProjectID: "live", OrgID: "org1", Namespace: "org1-live", DeploymentMode: domain.ModeK8s,
				DBType: domain.PostgreSQL, Status: "FAILED", RollbackLog: rollbackLog}
			if err := store.Create(inst); err != nil {
				t.Fatal(err)
			}
			if retried := svc.RetryFailedRollbacks(context.Background()); len(retried) != 0 {
				t.Fatalf("retried %v", retried)
			}
			if !mock.Namespaces["org1-live"] {
				t.Fatal("the namespace was deleted")
			}
		})
	}
}

// A status other than FAILED is not the sweep's: a delete in progress owns it.
func TestRetryFailedRollbacks_LeavesAProjectNoLongerFailed(t *testing.T) {
	for _, status := range []string{string(domain.StatusActive), string(domain.StatusPendingDeletion), "DELETING"} {
		svc, store, mock := setupProvisioningTest(t)
		mock.Namespaces["org1-live"] = true
		inst := &domain.DatabaseInstance{ProjectID: "live", OrgID: "org1", Namespace: "org1-live", DeploymentMode: domain.ModeK8s,
			DBType: domain.PostgreSQL, Status: status, RollbackLog: `[{"name":"delete namespace org1-live","ok":false,"error":"x"}]`}
		if err := store.Create(inst); err != nil {
			t.Fatal(err)
		}
		if retried := svc.RetryFailedRollbacks(context.Background()); len(retried) != 0 || !mock.Namespaces["org1-live"] {
			t.Fatalf("%s: retried %v, namespace there=%v", status, retried, mock.Namespaces["org1-live"])
		}
	}
}

// The lease excludes a delete or a pause that is running now.
func TestRetryFailedRollbacks_SkipsAProjectUnderAnotherOperation(t *testing.T) {
	svc, _, mock, row := failedCreateWithLeftoverNamespace(t)
	mock.DeleteNamespaceError = nil
	release, claimed, err := svc.claimer().Claim(context.Background(), row.ProjectID, OperationDeletion)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()

	if retried := svc.RetryFailedRollbacks(context.Background()); len(retried) != 0 {
		t.Fatalf("retried %v under another operation's lease", retried)
	}
	if !mock.Namespaces[row.Namespace] {
		t.Fatal("namespace removed under another operation's lease")
	}
}

// An add whose removal failed leaves the database in a namespace that stays:
// the project reads ACTIVE without a database while the cluster runs on.
func TestRetryFailedRollbacks_FinishesRemovingAFailedAdd(t *testing.T) {
	svc, store, mock := failingExecService(t)
	created := createWithoutDatabase(t, svc)
	mock.DeleteCRDError = errors.New("etcdserver: request timed out")
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add: %v", err)
	}
	row, _ := store.FindByProjectID(created.ProjectID)
	if row.Status != string(domain.StatusActive) || !row.NoDatabase || len(mock.CRDs) == 0 {
		t.Fatalf("status=%s noDatabase=%v crds=%d, want ACTIVE, no database, resources left", row.Status, row.NoDatabase, len(mock.CRDs))
	}
	mock.DeleteCRDError = nil

	if retried := svc.RetryFailedRollbacks(context.Background()); len(retried) != 1 {
		t.Fatalf("retried %v", retried)
	}

	if len(mock.CRDs) != 0 {
		t.Errorf("%d resources left", len(mock.CRDs))
	}
	if !mock.Namespaces[row.Namespace] {
		t.Error("the project's namespace went; its apps live there")
	}
	after, _ := store.FindByProjectID(created.ProjectID)
	requireCleanRollback(t, after)
}

// A later add cleared the failure: its database must not be swept.
func TestRetryFailedRollbacks_LeavesADatabaseAddedSince(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.CRDs["org1-p/p-postgres"] = nil
	mock.Namespaces["org1-p"] = true
	inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "org1", Namespace: "org1-p", DeploymentMode: domain.ModeK8s, DBType: domain.PostgreSQL,
		Status: string(domain.StatusActive), NoDatabase: false, RollbackLog: `[{"name":"remove database from namespace org1-p","ok":false,"error":"x"}]`}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	if retried := svc.RetryFailedRollbacks(context.Background()); len(retried) != 0 || len(mock.CRDs) != 1 {
		t.Fatalf("retried %v, %d resources left", retried, len(mock.CRDs))
	}
}

// A namespace-only create records the delete it could not do, so the sweep
// finds that one too.
func TestRetryFailedRollbacks_CoversAProjectCreatedWithoutADatabase(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	mock.Namespaces["org1-p"] = true
	mock.DeleteNamespaceError = errors.New("etcdserver: request timed out")
	inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "org1", Namespace: "org1-p", DeploymentMode: domain.ModeK8s,
		NoDatabase: true, Status: string(domain.StatusProvisioning)}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	svc.failProjectWithoutDatabase(context.Background(), inst, "record project", errors.New("boom"), true)
	mock.DeleteNamespaceError = nil

	if retried := svc.RetryFailedRollbacks(context.Background()); len(retried) != 1 || mock.Namespaces["org1-p"] {
		t.Fatalf("retried %v, namespace there=%v", retried, mock.Namespaces["org1-p"])
	}
}

// A single host has no cluster to ask.
func TestRetryFailedRollbacks_DoesNothingWithoutACluster(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	svc.k8sClient = nil
	inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "org1", Namespace: "org1-p", DeploymentMode: domain.ModeDocker,
		Status: "FAILED", RollbackLog: `[{"name":"delete namespace org1-p","ok":false,"error":"x"}]`}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	if retried := svc.RetryFailedRollbacks(context.Background()); len(retried) != 0 {
		t.Fatalf("retried %v", retried)
	}
}

// Deleting the FAILED project removes the namespace whatever the sweep did.
func TestDeletingAFailedProjectRemovesItsLeftoverNamespace(t *testing.T) {
	svc, store, mock, row := failedCreateWithLeftoverNamespace(t)
	mock.DeleteNamespaceError = nil

	if _, err := svc.ScheduleDeletion(context.Background(), row.ProjectID, DeprovisionOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if mock.Namespaces[row.Namespace] {
		t.Error("namespace still there")
	}
	if got, err := store.FindByProjectID(row.ProjectID); err == nil && got != nil {
		t.Errorf("row still there: %s", got.Status)
	}
}

func namespaceThere(mock *k8s.MockClient, name string) bool {
	there, _ := mock.NamespaceExists(context.Background(), name)
	return there
}

type retrySweepLeader struct{ leads bool }

func (l retrySweepLeader) IsLeader(context.Context) (bool, error) { return l.leads, nil }

func TestRollbackRetrySweeper_RunsAtOnceAsLeaderOnly(t *testing.T) {
	svc, _, mock, row := failedCreateWithLeftoverNamespace(t)
	mock.DeleteNamespaceError = nil

	stop := svc.StartRollbackRetrySweeper(context.Background(), retrySweepLeader{leads: false}, time.Hour)
	time.Sleep(50 * time.Millisecond)
	stop()
	if !namespaceThere(mock, row.Namespace) {
		t.Fatal("a replica that does not lead swept")
	}

	stop = svc.StartRollbackRetrySweeper(context.Background(), retrySweepLeader{leads: true}, time.Hour)
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	for namespaceThere(mock, row.Namespace) {
		if time.Now().After(deadline) {
			t.Fatal("the leader did not sweep")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
