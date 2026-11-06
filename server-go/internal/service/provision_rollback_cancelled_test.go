package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// liveCluster behaves as the real client does under a finished context: calls
// fail and nothing is removed. onRoleSQL runs the first time role SQL is
// executed, the moment the build is creating the project's roles.
type liveCluster struct {
	*k8s.MockClient
	onRoleSQL func()
	hang      bool
	once      sync.Once
}

func (c *liveCluster) gate(ctx context.Context, command string) error {
	if strings.Contains(command, "CREATE ROLE") {
		c.once.Do(func() {
			if c.onRoleSQL != nil {
				c.onRoleSQL()
			}
		})
		if c.hang {
			<-ctx.Done()
		}
	}
	return ctx.Err()
}

func (c *liveCluster) ExecInPod(ctx context.Context, namespace, pod, container string, cmd []string) (string, error) {
	if err := c.gate(ctx, strings.Join(cmd, " ")); err != nil {
		return "", err
	}
	return c.MockClient.ExecInPod(ctx, namespace, pod, container, cmd)
}

func (c *liveCluster) ExecInPodStdin(ctx context.Context, namespace, pod, container string, cmd []string, stdin string) (string, error) {
	if err := c.gate(ctx, strings.Join(cmd, " ")+stdin); err != nil {
		return "", err
	}
	return c.MockClient.ExecInPodStdin(ctx, namespace, pod, container, cmd, stdin)
}

func (c *liveCluster) DeleteNamespace(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.MockClient.DeleteNamespace(ctx, name)
}

func (c *liveCluster) DeleteCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.MockClient.DeleteCRD(ctx, gvr, namespace, name)
}

func liveClusterService(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *liveCluster) {
	t.Helper()
	_, store, mock := setupProvisioningTest(t)
	cluster := &liveCluster{MockClient: mock}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(cluster, "")), cluster)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())
	withBackupTarget(t, svc)
	return svc, store, cluster
}

func newProjectRequest() domain.ProvisioningRequest {
	return domain.ProvisioningRequest{ProjectName: "x", OrgID: "org1", OwnerID: testUser1, DBType: domain.PostgreSQL, PostgresVersion: "17"}
}

// within fails the test when run has not returned in time, instead of hanging
// the package on a build nothing puts a deadline on.
func within(t *testing.T, limit time.Duration, what string, run func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		run()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s still running after %s", what, limit)
	}
}

func requireCleanRollback(t *testing.T, row *domain.DatabaseInstance) {
	t.Helper()
	var results []provisioner.CleanupResult
	if err := json.Unmarshal([]byte(row.RollbackLog), &results); err != nil {
		t.Fatalf("rollback log %q: %v", row.RollbackLog, err)
	}
	if len(results) == 0 {
		t.Fatal("no cleanup ran")
	}
	for _, result := range results {
		if !result.OK {
			t.Errorf("cleanup %q failed: %s", result.Name, result.Error)
		}
	}
}

// A caller who drops a synchronous add during role creation does not own the
// build: it finishes, and the project has its database and its credential.
func TestAddDatabase_ACallerWhoHangsUpDuringRoleCreationDoesNotCancelTheBuild(t *testing.T) {
	svc, store, cluster := liveClusterService(t)
	created := createWithoutDatabase(t, svc)
	request, hangUp := context.WithCancel(context.Background())
	defer hangUp()
	cluster.onRoleSQL = hangUp

	if _, err := svc.AddDatabase(request, created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add database: %v", err)
	}

	row, _ := store.FindByProjectID(created.ProjectID)
	if row.Status != string(domain.StatusActive) || row.NoDatabase || row.FailureReason != "" {
		t.Fatalf("status=%s noDatabase=%v reason=%q, want ACTIVE with its database", row.Status, row.NoDatabase, row.FailureReason)
	}
	if _, err := svc.vault.Get(vaultCredentialPath(created.ProjectID, roleAdmin)); err != nil {
		t.Fatalf("admin credential: %v", err)
	}
}

// The admission itself is not tied to the caller either: a request already
// gone when the add starts still gets a lease and a build.
func TestAddDatabase_AdmissionIsNotTiedToTheCaller(t *testing.T) {
	svc, store, _ := liveClusterService(t)
	created := createWithoutDatabase(t, svc)
	seen := &contextRecordingClaimer{ProjectOperationClaimer: NewInProcessOperationClaimer()}
	svc.SetOperationClaimer(seen)
	request, hangUp := context.WithCancel(context.Background())
	hangUp()

	if _, err := svc.AddDatabase(request, created.ProjectID, addPostgres()); err != nil {
		t.Fatalf("add database: %v", err)
	}

	if seen.err != nil {
		t.Fatalf("the lease was claimed on a finished context: %v", seen.err)
	}
	row, _ := store.FindByProjectID(created.ProjectID)
	if row.NoDatabase {
		t.Fatal("no database after the add")
	}
}

type contextRecordingClaimer struct {
	ProjectOperationClaimer
	err error
}

func (c *contextRecordingClaimer) Claim(ctx context.Context, projectID string, op ProjectOperation) (func(), bool, error) {
	c.err = ctx.Err()
	return c.ProjectOperationClaimer.Claim(ctx, projectID, op)
}

// A commit that never returns (a paid tier with both standbys dead before
// CREATE ROLE) ends at the build's deadline, with the reason named and the
// rollback done, on every path a database is built by.
func TestBuild_ADeadlineAtRoleCreationFailsTheBuildAndRollsBack(t *testing.T) {
	inline := func(f func()) { f() }
	creates := map[string]func(*ProvisioningService) string{
		"create": func(svc *ProvisioningService) string {
			resp, err := svc.Provision(context.Background(), newProjectRequest())
			if err != nil {
				t.Fatalf("provision: %v", err)
			}
			return resp.ProjectID
		},
		"create in background": func(svc *ProvisioningService) string {
			svc.SetBackgroundRunner(inline)
			resp, err := svc.ProvisionInBackground(context.Background(), newProjectRequest())
			if err != nil {
				t.Fatalf("provision: %v", err)
			}
			return resp.ProjectID
		},
	}
	for name, create := range creates {
		t.Run(name, func(t *testing.T) {
			svc, store, cluster := liveClusterService(t)
			svc.buildTimeout = 50 * time.Millisecond
			cluster.hang = true
			var projectID string
			within(t, 5*time.Second, "build", func() { projectID = create(svc) })

			row, _ := store.FindByProjectID(projectID)
			if row.Status != "FAILED" || row.FailureStage != domain.StageRoleCreation {
				t.Fatalf("status=%s stage=%s reason=%q, want FAILED at ROLE_CREATION", row.Status, row.FailureStage, row.FailureReason)
			}
			if !strings.Contains(row.FailureReason, "did not finish within") {
				t.Errorf("reason %q does not name the deadline", row.FailureReason)
			}
			requireCleanRollback(t, row)
			if cluster.Namespaces[row.Namespace] {
				t.Errorf("namespace %s survived the rollback", row.Namespace)
			}
		})
	}

	adds := map[string]func(*ProvisioningService, string){
		"add": func(svc *ProvisioningService, projectID string) {
			if _, err := svc.AddDatabase(context.Background(), projectID, addPostgres()); err != nil {
				t.Fatalf("add: %v", err)
			}
		},
		"add in background": func(svc *ProvisioningService, projectID string) {
			svc.SetBackgroundRunner(inline)
			if _, err := svc.AddDatabaseInBackground(context.Background(), projectID, addPostgres()); err != nil {
				t.Fatalf("add: %v", err)
			}
		},
	}
	for name, add := range adds {
		t.Run(name, func(t *testing.T) {
			svc, store, cluster := liveClusterService(t)
			created := createWithoutDatabase(t, svc)
			svc.buildTimeout = 50 * time.Millisecond
			cluster.hang = true
			within(t, 5*time.Second, "add", func() { add(svc, created.ProjectID) })

			row, _ := store.FindByProjectID(created.ProjectID)
			if row.Status != string(domain.StatusActive) || !row.NoDatabase {
				t.Fatalf("status=%s noDatabase=%v, want ACTIVE without a database", row.Status, row.NoDatabase)
			}
			if !strings.Contains(row.FailureReason, "did not finish within") {
				t.Errorf("reason %q does not name the deadline", row.FailureReason)
			}
			requireCleanRollback(t, row)
			if len(cluster.CRDs) != 0 {
				t.Errorf("%d resources of the failed add survived the rollback", len(cluster.CRDs))
			}
			if !cluster.Namespaces[row.Namespace] {
				t.Error("the rollback removed the project's namespace; its apps live there")
			}
		})
	}
}

// A delete claiming the project mid-build still stops it: the build's context
// is cancelled from inside, so role creation fails on a finished context and
// the rollback must not run on that context.
func TestBuild_ADeleteClaimingDuringRoleCreationStopsTheBuildAndRollsBack(t *testing.T) {
	claim := func(t *testing.T, svc *ProvisioningService, projectID string) {
		t.Helper()
		if _, err := forceClaim(svc, projectID); err != nil {
			t.Errorf("claim for deletion: %v", err)
		}
	}
	t.Run("create", func(t *testing.T) {
		svc, store, cluster := liveClusterService(t)
		cluster.onRoleSQL = func() {
			all, _ := store.FindAll()
			claim(t, svc, all[0].ProjectID)
		}
		resp, err := svc.Provision(context.Background(), newProjectRequest())
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		if resp.Status != "FAILED" {
			t.Fatalf("response %s, want the build stopped", resp.Status)
		}
		row, _ := store.FindByProjectID(resp.ProjectID)
		if row.Status != string(domain.StatusDeleting) {
			t.Errorf("row %s, want it still the delete's", row.Status)
		}
		if cluster.Namespaces[row.Namespace] {
			t.Errorf("namespace %s survived the rollback", row.Namespace)
		}
	})
	t.Run("add", func(t *testing.T) {
		svc, _, cluster := liveClusterService(t)
		created := createWithoutDatabase(t, svc)
		cluster.onRoleSQL = func() { claim(t, svc, created.ProjectID) }
		resp, err := svc.AddDatabase(context.Background(), created.ProjectID, addPostgres())
		if err != nil {
			t.Fatalf("add: %v", err)
		}
		if resp.FailureReason == "" {
			t.Fatalf("response %+v, want the build stopped", resp)
		}
		if len(cluster.CRDs) != 0 {
			t.Errorf("%d resources of the add survived the rollback", len(cluster.CRDs))
		}
	})
}

// The deadline ends a hung build well before the row counts as abandoned, so a
// delete cannot take a project whose build is still running; and it leaves room
// for the longest pod wait, 11 minutes on a paid tier.
func TestBuildDeadline_SitsBetweenThePodWaitAndTheStaleBuildRule(t *testing.T) {
	if defaultBuildTimeout <= 11*time.Minute || defaultBuildTimeout >= storage.StaleBuildAfter {
		t.Fatalf("build timeout %s must be above 11m and below %s", defaultBuildTimeout, storage.StaleBuildAfter)
	}
}

// A restore whose job was taken has its context cancelled precisely so it
// stops and compensates. The compensations must still reach the cluster.
func TestFailRestore_CompensatesOnACancelledContext(t *testing.T) {
	_, _, cluster := liveClusterService(t)
	if err := cluster.CreateProjectNamespace(context.Background(), "org1-copy", "org1"); err != nil {
		t.Fatal(err)
	}
	pc := provisioner.NewProvisionContext(nil, nil)
	pc.RegisterCleanup("delete restore namespace", func(ctx context.Context) error {
		return cluster.DeleteNamespace(ctx, "org1-copy")
	})
	finished, cancel := context.WithCancel(context.Background())
	cancel()

	if err := failRestore(finished, pc, emptyInstanceStore(t), &domain.DatabaseInstance{ProjectID: "copy"}, errors.New("job taken")); !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("failRestore: %v", err)
	}
	if cluster.Namespaces["org1-copy"] {
		t.Fatal("the restore's namespace survived its compensations")
	}
}

// A create without a database that gives up mid-way removes its namespace on a
// bounded context, whatever became of the request's.
func TestFailProjectWithoutDatabase_RemovesItsNamespaceOnABoundedContext(t *testing.T) {
	svc, _, _ := liveClusterService(t)
	seen := &deadlineRecordingCluster{MockClient: k8s.NewMockClient()}
	svc.k8sClient = seen
	finished, cancel := context.WithCancel(context.Background())
	cancel()
	inst := &domain.DatabaseInstance{ProjectID: "p", Namespace: "org1-p", Status: string(domain.StatusProvisioning)}

	svc.failProjectWithoutDatabase(finished, inst, "create namespace", errors.New("boom"), true)

	if seen.err != nil || !seen.bounded {
		t.Fatalf("namespace removal ran with err=%v bounded=%v, want a live context with a deadline", seen.err, seen.bounded)
	}
}

type deadlineRecordingCluster struct {
	*k8s.MockClient
	err     error
	bounded bool
}

func (c *deadlineRecordingCluster) DeleteNamespace(ctx context.Context, name string) error {
	c.err = ctx.Err()
	_, c.bounded = ctx.Deadline()
	return c.MockClient.DeleteNamespace(ctx, name)
}
