package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func provisionInto(t *testing.T, svc *ProvisioningService, orgID, name string) (*domain.ProvisioningResponse, error) {
	t.Helper()
	return svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: name, OrgID: orgID, DBType: domain.PostgreSQL,
	})
}

func TestProvision_ProjectTakesItsOrganisationsTier(t *testing.T) {
	for _, tier := range []domain.TierType{domain.Free, domain.Standard, domain.Enterprise} {
		t.Run(string(tier), func(t *testing.T) {
			svc, store, _ := setupProvisioningTest(t)
			setOrgTier(svc, "org-plan", tier)

			resp, err := provisionInto(t, svc, "org-plan", "p")
			if err != nil {
				t.Fatalf("Provision: %v", err)
			}
			inst, err := store.FindByProjectID(resp.ProjectID)
			if err != nil {
				t.Fatalf("find: %v", err)
			}
			if inst.Tier != tier {
				t.Fatalf("project tier = %s, want the org's %s", inst.Tier, tier)
			}
		})
	}
}

func TestProvision_FreeOrganisationIsHeldToTheFreeLimit(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	setOrgTier(svc, "org-free", domain.Free)

	if _, err := provisionInto(t, svc, "org-free", "first"); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := provisionInto(t, svc, "org-free", "second")
	var limitErr *OrgProjectLimitError
	if !errors.As(err, &limitErr) || limitErr.Tier != domain.Free || limitErr.Limit != 1 {
		t.Fatalf("second = %v, want the FREE limit of 1", err)
	}
}

func TestProvision_RefusesWhenTheOrganisationsTierCannotBeRead(t *testing.T) {
	cases := map[string]func(*ProvisioningService){
		"no org store":  func(s *ProvisioningService) { s.SetOrgStore(nil) },
		"unknown org":   func(*ProvisioningService) {},
		"empty tier":    func(s *ProvisioningService) { setOrgTier(s, "org-x", "") },
		"unknown tier":  func(s *ProvisioningService) { setOrgTier(s, "org-x", "GOLD") },
		"store failure": func(s *ProvisioningService) { s.orgStore.(*fakestore.Orgs).Err = errors.New("down") },
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			svc, store, _ := setupProvisioningTest(t)
			arrange(svc)

			_, err := provisionInto(t, svc, "org-x", "p")
			if !errors.Is(err, ErrOrgTierUnresolved) {
				t.Fatalf("Provision = %v, want ErrOrgTierUnresolved", err)
			}
			if all, _ := store.FindAll(); len(all) != 0 {
				t.Fatalf("nothing may be created: %v", all)
			}
		})
	}
}

func TestRegisterProject_RestoredProjectTakesTheOrgsCurrentTier(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	setOrgTier(svc, "org1", domain.Standard)

	if err := svc.RegisterProject(context.Background(), &domain.DatabaseInstance{
		ProjectID: "proj-restored1", OrgID: "org1", Tier: domain.Enterprise,
	}, RegistrationOptions{}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	inst, err := store.FindByProjectID("proj-restored1")
	if err != nil || inst == nil {
		t.Fatalf("find: %v", err)
	}
	if inst.Tier != domain.Standard {
		t.Fatalf("restored tier = %s, want the org's current STANDARD", inst.Tier)
	}
}

func TestRegisterProject_UnreadableOrgTierRefuses(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)

	err := svc.RegisterProject(context.Background(), &domain.DatabaseInstance{
		ProjectID: "proj-restored1", OrgID: "org-gone", Tier: domain.Free,
	}, RegistrationOptions{})
	if !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("RegisterProject = %v, want ErrOrgTierUnresolved", err)
	}
	if inst, _ := store.FindByProjectID("proj-restored1"); inst != nil {
		t.Fatal("a refused registration must leave no row")
	}
}

func TestEnsureOrgCanTakeProject_UsesTheOrgsCurrentTier(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-held00001", OrgID: "org1", Tier: domain.Enterprise, Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := svc.EnsureOrgCanTakeProject(context.Background(), "org1")
	var limitErr *OrgProjectLimitError
	if !errors.As(err, &limitErr) || limitErr.Tier != domain.Free {
		t.Fatalf("EnsureOrgCanTakeProject = %v, want the FREE limit", err)
	}

	setOrgTier(svc, "org1", domain.Enterprise)
	if err := svc.EnsureOrgCanTakeProject(context.Background(), "org1"); err != nil {
		t.Fatalf("an ENTERPRISE org has room: %v", err)
	}
	if err := svc.EnsureOrgCanTakeProject(context.Background(), "org-gone"); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("unknown org = %v, want ErrOrgTierUnresolved", err)
	}
}

func TestRestore_UnreadableOrgTierIsRefusedBeforeAnySideEffect(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-source001", OrgID: "org-gone", Tier: domain.Enterprise,
		Namespace: "org-gone-proj-source001", Status: "ACTIVE", DBType: domain.PostgreSQL,
	}); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	mock := k8s.NewMockClient()
	svc := NewBackupService(store, mock, dir, StaticBackupStorage(r2Storage()))
	svc.SetProjectRegistrar(&fakeRegistrar{store: store})
	armRestore(svc, mock, store)
	capacity := NewProvisioningService(store, provisioner.NewFactory(), mock)
	capacity.SetOrgStore(testOrgs())
	svc.SetOrgProjectCapacity(capacity)

	_, err = svc.RestoreFromBackup(context.Background(), "proj-source001", domain.RestoreRequest{
		NewProjectName: "recovered", TargetProjectID: "proj-target001",
	})
	if !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("RestoreFromBackup = %v, want ErrOrgTierUnresolved", err)
	}
	if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 {
		t.Fatalf("a refused restore must create nothing: %v %v", mock.Namespaces, mock.CRDs)
	}
}
