package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Actions a member of a suspended org must not take (EXC-845): each pins the
// refusal the org guard gives.

// orgGuardSetter is what a service exposes once it refuses suspended orgs.
type orgGuardSetter interface {
	SetOrgGuard(guard *orgguard.Guard)
}

func suspendedGuardOver(t *testing.T, inst *domain.DatabaseInstance, orgs ...string) *orgguard.Guard {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(inst.Clone()); err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, id := range orgs {
		set[id] = true
	}
	return orgguard.New(store, func(context.Context) (map[string]bool, error) { return set, nil })
}

// Cancelling a scheduled deletion restarts the function runtime and serves
// the project's sites again: compute started and content served.
func TestCancelDeletion_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	h, router := newSiteRoutingGrace(t)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	suspendedOwnerGuard(h.svc, "org1")

	if err := h.svc.CancelDeletion(context.Background(), graceProject); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("CancelDeletion err = %v, want ErrOrgSuspended", err)
	}
	if len(router.routed) != 0 {
		t.Fatalf("routed = %v: a suspended org's sites were served again", router.routed)
	}
	if h.row(t).Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("status = %s, want PENDING_DELETION", h.row(t).Status)
	}
}

// Changes to a running database of a suspended org: new logins, more disk,
// restarts. None goes through the guard today.
func TestDatabaseChanges_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	ctx := context.Background()
	actions := map[string]func(*ProvisioningService) error{
		"RotateCredentials": func(s *ProvisioningService) error { _, err := s.RotateCredentials(ctx, testOpsDB); return err },
		"ResizeStorage":     func(s *ProvisioningService) error { return s.ResizeStorage(ctx, testOpsDB, "20Gi") },
		"TuneParameters": func(s *ProvisioningService) error {
			return s.TuneParameters(ctx, testOpsDB, map[string]string{"work_mem": "8MB"})
		},
		"EnableQueryStats": func(s *ProvisioningService) error { return s.EnableQueryStats(ctx, testOpsDB) },
		"ApplyOrgTier":     func(s *ProvisioningService) error { return s.ApplyOrgTier(ctx, testOpsDB, domain.Free) },
		"CreateMongoUser": func(s *ProvisioningService) error {
			_, err := s.CreateMongoUser(ctx, testOpsDB, "extra", MongoRoleReadWrite)
			return err
		},
	}
	for name, act := range actions {
		t.Run(name, func(t *testing.T) {
			svc, _, _ := setupOpsTest(t)
			suspendedOwnerGuard(svc, "org1")
			if err := act(svc); !errors.Is(err, orgguard.ErrOrgSuspended) {
				t.Fatalf("%s err = %v, want ErrOrgSuspended", name, err)
			}
		})
	}
}

// Opening a suspended org's database to the internet.
func TestDBEndpoint_OpeningThePublicPortRefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.instance.OrgID = "org1"
	guarded, ok := any(h.svc).(orgGuardSetter)
	if !ok {
		t.Fatal("DBEndpointService has no org guard: any Admin of a suspended org can open its database's public port")
	}
	guarded.SetOrgGuard(suspendedGuardOver(t, h.instance, "org1"))

	if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("SetPublic err = %v, want ErrOrgSuspended", err)
	}
	if h.serviceExists(t) {
		t.Fatal("a public Service was created for a suspended org's database")
	}
}

// A read address publishes a second public port on the replicas.
func TestReadAddress_EnableRefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t) // opened before the suspension
	h.instance.OrgID = "org1"
	guarded, ok := any(h.svc).(orgGuardSetter)
	if !ok {
		t.Fatal("DBEndpointService has no org guard: a suspended org can still open a read address")
	}
	guarded.SetOrgGuard(suspendedGuardOver(t, h.instance, "org1"))

	if _, err := h.reader.SetReadAddress(context.Background(), endpointProject, true); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("SetReadAddress err = %v, want ErrOrgSuspended", err)
	}
}

// Restore into a new project and copy (EXC-667) build a new database cluster
// without Provision, so its CheckOrg never runs.
func TestRestoreIntoANewProject_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	source := &domain.DatabaseInstance{ProjectID: "src", OrgID: "org1", Status: "ACTIVE"}
	jobs := newFakeJobs()
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs, Slots: &fixedSlots{}})
	orch.SetSteps([]RestoreStep{{Name: "create-target", Run: func(context.Context, *domain.RestoreJob) error { return nil }}})
	guarded, ok := any(orch).(orgGuardSetter)
	if !ok {
		t.Fatal("RestoreOrchestrator has no org guard: a suspended org can restore or copy into a new database cluster")
	}
	guarded.SetOrgGuard(suspendedGuardOver(t, source, "org1"))

	_, err := orch.Start(context.Background(), source, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("Start err = %v, want ErrOrgSuspended", err)
	}
}

// A major upgrade builds and starts the database on the new major; the minor
// upgrade (UpgradeVersion) is refused, the major one is not.
func TestMajorUpgrade_HasAnOrgGuard(t *testing.T) {
	svc := NewMajorUpgradeService(MajorUpgradeConfig{})
	if _, ok := any(svc).(orgGuardSetter); !ok {
		t.Fatal("MajorUpgradeService has no org guard: a suspended org can run a major upgrade while minor upgrades are refused")
	}
}
