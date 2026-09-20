package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// brokenStore fails every create and every count, standing in for a platform
// database that is down.
type brokenStore struct {
	*fakestore.Instances
	err error
}

func (s brokenStore) CreateWithinOrgLimit(*domain.DatabaseInstance, int) error { return s.err }
func (s brokenStore) CountOrgProjects(string) (int, error)                     { return 0, s.err }

func newBrokenStore(err error) brokenStore {
	return brokenStore{Instances: fakestore.NewInstances(), err: err}
}

// The end-to-end failure this ticket comes from: a project deleted a moment
// ago is still DELETING while its teardown runs, and the org must be able to
// create its replacement immediately.
func TestProvision_DeletingProjectDoesNotHoldTheSlot(t *testing.T) {
	for _, status := range storage.NonSlotStatuses() {
		t.Run(status, func(t *testing.T) {
			svc, store, _ := setupProvisioningTest(t)
			if err := store.Create(&domain.DatabaseInstance{
				ProjectID: "proj-oldone001", OrgID: "org1", Tier: domain.Free, Status: status,
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			if _, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
				ProjectName: "replacement", OrgID: "org1",
				DBType: domain.PostgreSQL, Tier: domain.Free,
			}); err != nil {
				t.Fatalf("a project under teardown must not block a new one: %v", err)
			}
		})
	}
}

// The refusal a client sees names the limit and the tier and nothing else —
// no organisation id, no internal wording.
func TestProvision_AtTheLimitReturnsTheFixedRefusal(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-inuse0001", OrgID: "org-secret", Tier: domain.Free, Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "second", OrgID: "org-secret",
		DBType: domain.PostgreSQL, Tier: domain.Free,
	})
	var limitErr *OrgProjectLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("Provision = %v, want *OrgProjectLimitError", err)
	}
	if !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("the refusal must identify itself as the project limit: %v", err)
	}
	if limitErr.Limit != 1 || limitErr.Tier != domain.Free {
		t.Fatalf("refusal carries limit %d tier %s", limitErr.Limit, limitErr.Tier)
	}
	if strings.Contains(err.Error(), "org-secret") {
		t.Fatalf("the message must not echo the organisation id: %q", err)
	}
}

// A store that cannot answer must not be read as "there is room".
func TestProvision_StoreFailureRefusesAndCreatesNothing(t *testing.T) {
	store := newBrokenStore(errors.New("platform database is down"))
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)

	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "unlucky", OrgID: "org1",
		DBType: domain.PostgreSQL, Tier: domain.Free,
	})
	if !errors.Is(err, ErrProjectStoreUnavailable) {
		t.Fatalf("Provision = %v, want ErrProjectStoreUnavailable", err)
	}
	if errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatal("a store failure must not be reported as a reached limit")
	}
	if len(store.Items) != 0 {
		t.Fatalf("nothing may be created: %v", store.Items)
	}
}

func TestProvision_UnlimitedTierIsUnaffected(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	for _, id := range []string{"proj-ent00001", "proj-ent00002", "proj-ent00003"} {
		if err := store.Create(&domain.DatabaseInstance{
			ProjectID: id, OrgID: "org1", Tier: domain.Enterprise, Status: "ACTIVE",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if _, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "another", OrgID: "org1",
		DBType: domain.PostgreSQL, Tier: domain.Enterprise,
	}); err != nil {
		t.Fatalf("an unlimited tier must admit the project: %v", err)
	}
}

// Self-hosted installs are not metered.
func TestProvision_SelfHostedModeIsUnlimited(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	svc.SetSelfHostedMode(true)
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-selfhost1", OrgID: "org1", Tier: domain.Free, Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "second", OrgID: "org1",
		DBType: domain.PostgreSQL, Tier: domain.Free,
	}); err != nil {
		t.Fatalf("self-hosted mode must stay unlimited: %v", err)
	}
}

func TestEnsureOrgProjectCapacity(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-cap000001", OrgID: "org1", Tier: domain.Free, Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-cap000002", OrgID: "org2", Tier: domain.Free, Status: string(domain.StatusDeleting),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := context.Background()

	if err := svc.EnsureOrgProjectCapacity(ctx, "org2", domain.Free); err != nil {
		t.Fatalf("a deleting project must leave the slot free: %v", err)
	}
	if err := svc.EnsureOrgProjectCapacity(ctx, "org1", domain.Enterprise); err != nil {
		t.Fatalf("an unlimited tier must always have room: %v", err)
	}
	err := svc.EnsureOrgProjectCapacity(ctx, "org1", domain.Free)
	if !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("EnsureOrgProjectCapacity = %v, want the limit refusal", err)
	}
}

func TestEnsureOrgProjectCapacity_StoreFailureRefuses(t *testing.T) {
	svc := NewProvisioningService(newBrokenStore(errors.New("platform database is down")),
		provisioner.NewFactory(), nil)

	err := svc.EnsureOrgProjectCapacity(context.Background(), "org1", domain.Free)
	if !errors.Is(err, ErrProjectStoreUnavailable) {
		t.Fatalf("EnsureOrgProjectCapacity = %v, want ErrProjectStoreUnavailable", err)
	}
}

// unlimitedCapacity is the capacity checker for restore tests whose subject
// is not the limit.
type unlimitedCapacity struct{}

func (unlimitedCapacity) EnsureOrgProjectCapacity(context.Context, string, domain.TierType) error {
	return nil
}

// A restore creates a project, so it is metered like a provision — and the
// refusal lands before the restore has created a namespace, a cluster or a
// row.
func TestRestore_RefusedAtTheLimitBeforeAnySideEffect(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	// FREE allows one project, and the source project is it.
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-source001", OrgID: "org1", Tier: domain.Free,
		Namespace: "org1-proj-source001", Status: "ACTIVE", DBType: domain.PostgreSQL,
	}); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	svc := NewBackupService(store, mock, dir, StaticBackupStorage(r2Storage()))
	svc.SetProjectRegistrar(&fakeRegistrar{store: store})
	armRestore(svc, mock, store)
	svc.SetOrgProjectCapacity(NewProvisioningService(store, provisioner.NewFactory(), mock))

	_, err = svc.RestoreFromBackup(context.Background(), "proj-source001", domain.RestoreRequest{
		NewProjectName: "recovered", TargetProjectID: "proj-target001",
	})
	var limitErr *OrgProjectLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("RestoreFromBackup = %v, want *OrgProjectLimitError", err)
	}
	if len(mock.Namespaces) != 0 {
		t.Fatalf("no namespace may be created for a refused restore: %v", mock.Namespaces)
	}
	if len(mock.CRDs) != 0 {
		t.Fatalf("no cluster may be created for a refused restore: %v", mock.CRDs)
	}
	if inst, _ := store.FindByProjectID("proj-target001"); inst != nil {
		t.Fatal("a refused restore must register no project")
	}
}

// There is no unmetered restore path: a platform that cannot answer the limit
// refuses the restore rather than handing out a free project.
func TestRestore_WithoutACapacityCheckerIsRefused(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-source001", OrgID: "org1", Tier: domain.Free,
		Namespace: "org1-proj-source001", Status: "ACTIVE", DBType: domain.PostgreSQL,
	}); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	mock := k8s.NewMockClient()
	svc := NewBackupService(store, mock, dir, StaticBackupStorage(r2Storage()))

	_, err = svc.RestoreFromBackup(context.Background(), "proj-source001", domain.RestoreRequest{
		NewProjectName: "recovered", TargetProjectID: "proj-target001",
	})
	if !errors.Is(err, ErrOrgCapacityNotConfigured) {
		t.Fatalf("RestoreFromBackup = %v, want ErrOrgCapacityNotConfigured", err)
	}
}

// The registration path itself takes the slot, so a restore that got past the
// early check — because another project was created while it ran — still
// cannot overshoot the limit.
func TestRegisterProject_TakesTheOrgSlot(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-held00001", OrgID: "org1", Tier: domain.Free, Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := NewProvisioningService(store, provisioner.NewFactory(), nil)

	err = svc.RegisterProject(context.Background(), &domain.DatabaseInstance{
		ProjectID: "proj-restored1", OrgID: "org1", Tier: domain.Free,
	}, RegistrationOptions{})
	if !errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("RegisterProject = %v, want the org project limit refusal", err)
	}
	if inst, _ := store.FindByProjectID("proj-restored1"); inst != nil {
		t.Fatal("a refused registration must leave no row")
	}
}

// A tier the platform does not know is not a licence to create: the check
// fails rather than reading an unresolvable limit as unlimited.
func TestEnsureOrgProjectCapacity_UnknownTierFails(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)

	err := svc.EnsureOrgProjectCapacity(context.Background(), "org1", domain.TierType("PLATINUM"))
	if err == nil {
		t.Fatal("an unknown tier must fail the check")
	}
	if errors.Is(err, storage.ErrOrgProjectLimitReached) {
		t.Fatalf("an unknown tier is not a reached limit: %v", err)
	}
}
