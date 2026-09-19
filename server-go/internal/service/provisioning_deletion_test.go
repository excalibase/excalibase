package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	testDeletingProj = "deleting-proj"
	testDeletingNS   = "org1-deleting-proj"
	testWantErrNil   = "Deprovision must report failure, got nil"
	testRowGoneFmt   = "row must survive a failed teardown, got %v"
)

// fastPoller returns a poller whose clock advances one interval per look, so
// a wait that never clears gives up after maxLooks checks without sleeping.
func fastPoller(maxLooks int) provisioner.Poller {
	fired := make(chan time.Time)
	close(fired)
	looks := 0
	start := time.Unix(0, 0)
	return provisioner.Poller{
		Interval: time.Second,
		Timeout:  time.Duration(maxLooks) * time.Second,
		Now: func() time.Time {
			looks++
			return start.Add(time.Duration(looks) * time.Second)
		},
		After: func(time.Duration) <-chan time.Time { return fired },
	}
}

// setupDeletionTest wires a project that owns a namespace, a cluster CRD, a
// running pod and a PVC — the state a real ACTIVE project holds.
func setupDeletionTest(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	mock.SetupPostgreSQLMock(testDeletingProj, testDeletingNS, 1)
	mock.Namespaces[testDeletingNS] = true
	mock.PVCs[testDeletingNS] = []string{testDeletingProj + "-postgres-1"}
	mock.CRDs[testDeletingNS+"/"+testDeletingProj+"-postgres"] = nil

	pgProv := provisioner.NewPostgreSQLProvisioner(mock, "")
	pgProv.SetDeletionPoller(fastPoller(3))
	svc := NewProvisioningService(store, provisioner.NewFactory(pgProv), mock)

	store.Create(&domain.DatabaseInstance{
		ProjectID: testDeletingProj,
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Namespace: testDeletingNS,
		Status:    "ACTIVE",
	})
	return svc, store, mock
}

func requireDeletingRow(t *testing.T, store *storage.FileSystemStore, step string) *domain.DatabaseInstance {
	t.Helper()
	inst, _ := store.FindByProjectID(testDeletingProj)
	if inst == nil {
		t.Fatalf(testRowGoneFmt, inst)
	}
	if inst.Status != string(domain.StatusDeleting) {
		t.Errorf("status: got %q, want %q", inst.Status, domain.StatusDeleting)
	}
	if inst.DeletionStep != step {
		t.Errorf("deletion step: got %q, want %q", inst.DeletionStep, step)
	}
	if inst.DeletionError == "" {
		t.Error("deletion error must be recorded on the row")
	}
	return inst
}

// The finding: RBAC denies the namespace delete, yet the API reported success
// and the row disappeared, stranding live resources with no record.
func TestDeprovisionNamespaceDeleteDeniedKeepsRowAndFails(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	mock.DeleteNamespaceError = errors.New(`namespaces "org1-deleting-proj" is forbidden`)

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteResources)
}

// The finding's second half: the delete request is accepted but a finalizer
// keeps the namespace alive forever. Submitting the request is not proof.
func TestDeprovisionFinalizerBlockedNamespaceFails(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	mock.StuckNamespaces[testDeletingNS] = true

	err := svc.Deprovision(context.Background(), testDeletingProj)
	if err == nil {
		t.Fatal(testWantErrNil)
	}
	if !errors.Is(err, provisioner.ErrWaitTimeout) {
		t.Errorf("want a wait timeout, got %v", err)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteResources)
}

// A Terminating pod still holds its CPU request, so capacity admission would
// over-commit if teardown returned before the pod was actually gone.
func TestDeprovisionWaitsForTerminatingPod(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	mock.StuckNamespaces[testDeletingNS] = true

	err := svc.Deprovision(context.Background(), testDeletingProj)
	if err == nil {
		t.Fatal(testWantErrNil)
	}
	if !strings.Contains(err.Error(), testDeletingProj+"-postgres-1") {
		t.Errorf("error must name the resource still present, got %v", err)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteResources)
}

// An unreachable vault leaves live credentials behind; reporting success
// there is exactly what the review flagged.
func TestDeprovisionVaultUnavailableFails(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	svc.SetVault(&errVault{err: errors.New("vault outage")})

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteVault)
}

// A sealed vault cannot remove anything, so it is a failure, not a skip.
func TestDeprovisionSealedVaultFails(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	svc.SetVault(&sealedVault{})

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteVault)
}

// Vault paths surviving the prefix delete must not pass as done.
func TestDeprovisionFailsWhenVaultPrefixNotEmpty(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	svc.SetVault(&stubbornVault{leftover: vaultProjectPrefix(testDeletingProj) + "credentials/admin"})

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteVault)
}

// Retrying the same DELETE resumes: the step that failed is retried and the
// row is removed once everything is observed gone.
func TestDeprovisionRetryAfterPartialFailureCompletes(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	vault := newFakeVault()
	prefix := vaultProjectPrefix(testDeletingProj) + "credentials/"
	vault.Put(prefix+"admin", map[string]string{"password": "secret"})
	failing := &errVault{err: errors.New("vault outage")}
	svc.SetVault(failing)

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	if mock.Namespaces[testDeletingNS] {
		t.Error("namespace should already be gone after the first attempt")
	}

	svc.SetVault(vault)
	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("retry must complete: %v", err)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("row must be removed once every step is observed complete")
	}
	if _, leaked := vault.data[prefix+"admin"]; leaked {
		t.Error("vault credentials must be gone")
	}
}

// Resources another attempt already removed count as success, so the retry
// is idempotent rather than tripping over its own progress.
func TestDeprovisionAlreadyGoneResourcesSucceed(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	pgProv := provisioner.NewPostgreSQLProvisioner(mock, "")
	pgProv.SetDeletionPoller(fastPoller(3))
	svc := NewProvisioningService(store, provisioner.NewFactory(pgProv), mock)

	store.Create(&domain.DatabaseInstance{
		ProjectID: testDeletingProj,
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Namespace: testDeletingNS,
		Status:    string(domain.StatusDeleting),
	})

	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("already-gone resources must count as success: %v", err)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("row should be removed")
	}
}

// The database Cluster must be observed gone before the namespace is
// deleted: a namespace removed under a live Cluster wedges the operator.
func TestDeprovisionDeletesClusterBeforeNamespace(t *testing.T) {
	svc, _, mock := setupDeletionTest(t)

	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}
	order := strings.Join(mock.Calls, " ")
	cluster := strings.Index(order, "DeleteCRD:"+testDeletingNS)
	namespace := strings.Index(order, "DeleteNamespace:"+testDeletingNS)
	if cluster < 0 || namespace < 0 || cluster > namespace {
		t.Errorf("cluster must be deleted before the namespace, calls: %v", mock.Calls)
	}
}

// stubbornVault accepts the prefix delete but keeps reporting a path under
// it — the "delete submitted, nothing removed" shape.
type stubbornVault struct{ leftover string }

func (s *stubbornVault) Get(string) (map[string]string, error) { return nil, nil }
func (s *stubbornVault) Put(string, map[string]string) error   { return nil }
func (s *stubbornVault) Delete(string) error                   { return nil }
func (s *stubbornVault) DeletePrefix(string) (int, error)      { return 0, nil }
func (s *stubbornVault) List(string) ([]string, error)         { return []string{s.leftover}, nil }
func (s *stubbornVault) Sealed() bool                          { return false }
func (s *stubbornVault) GetPublicKey() (string, error)         { return "", nil }

// failingStore lets a test fail the row writes the deletion state machine
// depends on, without touching the rest of the store's behaviour.
type failingStore struct {
	storage.InstanceStore
	updateErr error
	// failFrom is the 1-based update call from which updateErr starts being
	// returned, so a test can let markDeleting through and fail the write
	// that records the failure.
	failFrom int
	updates  int
}

func (s *failingStore) Update(inst *domain.DatabaseInstance) error {
	s.updates++
	if s.updateErr != nil && s.updates >= s.failFrom {
		return s.updateErr
	}
	return s.InstanceStore.Update(inst)
}

// If the row cannot even be pinned to DELETING, nothing is torn down: the
// record that tracks the teardown has to exist before the teardown starts.
func TestDeprovisionStopsWhenDeletingStateCannotBePersisted(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	svc.store = &failingStore{InstanceStore: store, updateErr: errors.New("db down"), failFrom: 1}

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	if !mock.Namespaces[testDeletingNS] {
		t.Error("nothing must be torn down before the row says DELETING")
	}
}

// The teardown failure still reaches the caller when the row that should
// record it cannot be written: losing the note must not become a success.
func TestDeprovisionReportsFailureEvenWhenItCannotBeRecorded(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	mock.DeleteNamespaceError = errors.New("forbidden")
	// Update 1 is markDeleting; update 2 records the failing step.
	svc.store = &failingStore{InstanceStore: store, updateErr: errors.New("db down"), failFrom: 2}

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst == nil {
		t.Error("row must survive")
	}
}

// Without a provisioner for the project's engine nothing can confirm its
// resources are gone, so the teardown refuses rather than guessing.
func TestDeprovisionFailsWithoutAProvisionerForTheEngine(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	svc := NewProvisioningService(store, provisioner.NewFactory(), k8s.NewMockClient())
	store.Create(&domain.DatabaseInstance{
		ProjectID: testDeletingProj, OrgID: "org1", DBType: domain.MySQL, Status: "ACTIVE",
	})

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteResources)
}

// A vault that accepts the delete but cannot be listed afterwards has not
// proven anything was removed.
func TestDeprovisionFailsWhenVaultCannotBeListed(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	svc.SetVault(&unlistableVault{})

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteVault)
}

// Credentials of a project under teardown open a database on its way out.
func TestGetCredentialsRefusedWhileDeleting(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	inst, _ := store.FindByProjectID(testDeletingProj)
	inst.Status = string(domain.StatusDeleting)
	if err := store.Update(inst); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetCredentials(testDeletingProj); !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("err = %v, want ErrProjectDeleting", err)
	}
}

// unlistableVault deletes the prefix but cannot report what is left.
type unlistableVault struct{ fakeVault }

func (u *unlistableVault) DeletePrefix(string) (int, error) { return 0, nil }
func (u *unlistableVault) List(string) ([]string, error) {
	return nil, errors.New("vault list unavailable")
}
func (u *unlistableVault) Sealed() bool { return false }
