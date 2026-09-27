package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func (f *lifecycleFixture) resumed() bool {
	return slices.Contains(f.kube.Calls, "ResumeAppWorkload:"+f.key())
}

func (f *lifecycleFixture) assertRefused(t *testing.T, want error) {
	t.Helper()
	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if f.resumed() {
		t.Fatal("a refused resume must not scale the app up")
	}
	if f.status() != apphost.StatusStopped {
		t.Fatalf("status = %q, want PAUSED back", f.status())
	}
}

func TestResumeApp_RefusesMoreCopiesThanThePlanNowAllows(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.PausedReplicas = 3
	f.svc.SetPlanTiers(fixedPlan{tier: domain.Free})

	f.assertRefused(t, ErrAppOverPlan)
}

func TestResumeApp_RefusesWhenNoNodeHasRoom(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.Capacity = oneNodeCluster(1000, 1000*mi, 980, 100*mi)

	f.assertRefused(t, ErrAppCapacity)
}

// A downgrade does not block a resume: the pods come back at the new plan's
// size, and that size is what must fit. 100m free holds a FREE pod, not a STANDARD one.
func TestResumeApp_AfterADowngradeRunsAtTheNewPlanSize(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.app.Tier = domain.Standard
	f.svc.SetPlanTiers(fixedPlan{tier: domain.Free})
	f.kube.Capacity = oneNodeCluster(1000, 1000*mi, 900, 100*mi)

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if got := f.kube.ResumedTier[f.key()]; got != domain.Free {
		t.Fatalf("resumed at %q, want the new plan's size", got)
	}
}

func TestResumeApp_AdmitsAtThePlanSizeNotTheOldOne(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetPlanTiers(fixedPlan{tier: domain.Enterprise})
	f.kube.Capacity = oneNodeCluster(1000, 8000*mi, 500, 100*mi)

	f.assertRefused(t, ErrAppCapacity)
}

func TestResumeApp_AnUnchangedPlanResumesAtTheSameSize(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if got := f.kube.ResumedTier[f.key()]; got != f.app.Tier {
		t.Fatalf("resumed at %q, want %q", got, f.app.Tier)
	}
}

func TestResumeApp_AnUnreadablePlanRefuses(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetPlanTiers(fixedPlan{err: ErrOrgTierUnresolved})
	f.assertRefused(t, ErrOrgTierUnresolved)

	g := newLifecycleFixture(t, apphost.StatusStopped)
	g.svc.SetPlanTiers(nil)
	g.assertRefused(t, ErrOrgTierUnresolved)
}

func TestResumeApp_AnUnreadableClusterRefuses(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.CapacityError = errors.New("connection refused")

	f.assertRefused(t, ErrAppCapacity)
}

func TestResumeApp_AnUnreadableReplicaCountRefuses(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.PausedReplicasErr = errors.New("connection refused")

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID); err == nil {
		t.Fatal("want the error")
	}
	if f.resumed() {
		t.Fatal("an unknown size must not be admitted")
	}
}

func TestResumeApp_NotPausedIsFoundBeforeAdmission(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.PausedReplicasErr = k8s.ErrAppNotPaused

	f.assertRefused(t, k8s.ErrAppNotPaused)
}

func TestResumeApp_AdmittedWhenItFits(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.PausedReplicas = 3
	f.svc.SetPlanTiers(fixedPlan{tier: domain.Standard})

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if !f.resumed() || f.status() != apphost.StatusRunning || f.kube.ResumedTier[f.key()] != domain.Standard {
		t.Fatalf("status = %q, calls = %v", f.status(), f.kube.Calls)
	}
	if !slices.Contains(f.kube.Calls, "PausedAppReplicas:"+f.key()) || !slices.Contains(f.kube.Calls, "GetClusterCapacity") {
		t.Fatalf("resume was not admitted against the cluster: %v", f.kube.Calls)
	}
}
