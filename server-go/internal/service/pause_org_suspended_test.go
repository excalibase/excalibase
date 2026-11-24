package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
)

func pausedProjectInOrg(t *testing.T, suspended ...string) (*PauseService, *fakePauser) {
	t.Helper()
	svc, store, pauser, _ := setupPauseTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: string(domain.StatusPaused),
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
		PauseReason: domain.PauseReasonIdle7Days,
	})
	set := map[string]bool{}
	for _, id := range suspended {
		set[id] = true
	}
	svc.SetOrgGuard(orgguard.New(store, func(context.Context) (map[string]bool, error) { return set, nil }))
	return svc, pauser
}

func TestPauseService_Resume_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	svc, pauser := pausedProjectInOrg(t, "o")

	if err := svc.Resume(context.Background(), "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("Resume err = %v, want ErrOrgSuspended", err)
	}
	if _, err := svc.StartResume(context.Background(), "p1", nil); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("StartResume err = %v, want ErrOrgSuspended", err)
	}
	if pauser.resumeCalls != 0 {
		t.Errorf("the database was started %d times, want 0", pauser.resumeCalls)
	}
}

func TestPauseService_Resume_APlatformAdminMayResumeASuspendedOrg(t *testing.T) {
	svc, pauser := pausedProjectInOrg(t, "o")

	if err := svc.Resume(orgguard.WithPlatformOverride(context.Background()), "p1"); err != nil {
		t.Fatalf("Resume as platform admin: %v", err)
	}
	if pauser.resumeCalls != 1 {
		t.Errorf("resume calls = %d, want 1", pauser.resumeCalls)
	}
}

func TestPauseService_Resume_AnOrgWithoutASuspensionIsUnaffected(t *testing.T) {
	svc, pauser := pausedProjectInOrg(t, "another-org")

	if err := svc.Resume(context.Background(), "p1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if pauser.resumeCalls != 1 {
		t.Errorf("resume calls = %d, want 1", pauser.resumeCalls)
	}
}
