package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fixedSlots answers one plan's project limit for every organisation.
type fixedSlots struct {
	limit int
	tier  domain.TierType
	err   error
	asked []string
}

func (f *fixedSlots) OrgProjectLimit(_ context.Context, orgID string) (int, domain.TierType, error) {
	f.asked = append(f.asked, orgID)
	return f.limit, f.tier, f.err
}

func copyRequest() domain.RestoreRequest {
	return domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "proj-copy0001"}
}

// EXC-639: a restore into a new project takes its organisation's slot when
// it is submitted, so nothing created while it runs can take it away.
func TestStartingACopyTakesTheOrganisationsSlot(t *testing.T) {
	jobs := newFakeJobs()
	slots := &fixedSlots{limit: 3, tier: domain.Standard}
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs, Slots: slots})

	if _, err := orch.Start(context.Background(), sourceInstance(), copyRequest()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := storage.ProjectSlot{OrgID: "org", MaxProjects: 3}
	if len(jobs.slots) != 1 || jobs.slots[0] != want {
		t.Fatalf("slots taken = %v, want %v", jobs.slots, want)
	}
}

func TestStartingACopyWithNoSlotLeftIsTheProjectLimit(t *testing.T) {
	jobs := newFakeJobs()
	jobs.full = true
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs, Slots: &fixedSlots{limit: 1, tier: domain.Free}})

	_, err := orch.Start(context.Background(), sourceInstance(), copyRequest())
	var limit *OrgProjectLimitError
	if !errors.As(err, &limit) || limit.Limit != 1 || limit.Tier != domain.Free {
		t.Fatalf("Start = %v, want the Free plan's project limit", err)
	}
}

func TestStartingACopyRefusesWithoutAProjectLimit(t *testing.T) {
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: newFakeJobs()})
	if _, err := orch.Start(context.Background(), sourceInstance(), copyRequest()); !errors.Is(err, ErrOrgCapacityNotConfigured) {
		t.Fatalf("Start = %v, want ErrOrgCapacityNotConfigured", err)
	}
	slots := &fixedSlots{err: errors.New("org store down")}
	orch = NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: newFakeJobs(), Slots: slots})
	if _, err := orch.Start(context.Background(), sourceInstance(), copyRequest()); err == nil {
		t.Fatal("a limit that cannot be read must refuse the copy")
	}
}

func TestReplacingAProjectInPlaceTakesNoSlot(t *testing.T) {
	jobs := newFakeJobs()
	slots := &fixedSlots{limit: 1, tier: domain.Free}
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs, Slots: slots})
	req := domain.RestoreRequest{Mode: domain.RestoreModeInPlace, ConfirmReplace: true, TargetProjectID: "src"}

	if _, err := orch.Start(context.Background(), sourceInstance(), req); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(slots.asked) != 0 || jobs.slots[0] != (storage.ProjectSlot{}) {
		t.Fatalf("an in-place restore asked for %v and took %v", slots.asked, jobs.slots)
	}
}

func TestOrgProjectLimitIsTheCurrentPlans(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	setOrgTier(svc, "org9", domain.Free)
	limit, tier, err := svc.OrgProjectLimit(context.Background(), "org9")
	if err != nil || limit != 1 || tier != domain.Free {
		t.Fatalf("OrgProjectLimit = %d %s %v, want the Free plan's 1", limit, tier, err)
	}
	svc.SetSelfHostedMode(true)
	if limit, _, err := svc.OrgProjectLimit(context.Background(), "org9"); err != nil || limit != 0 {
		t.Fatalf("a self-hosted install is unmetered, got %d %v", limit, err)
	}
	if _, _, err := svc.OrgProjectLimit(context.Background(), "org-unknown"); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("an unknown organisation = %v, want ErrOrgTierUnresolved", err)
	}
}
