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
	f.kube.PausedSize = k8s.PausedApp{Replicas: 3, CPURequest: "50m", MemoryRequest: "128Mi"}
	f.svc.SetPlanTiers(fixedPlan{tier: domain.Free})

	f.assertRefused(t, ErrAppOverPlan)
}

func TestResumeApp_RefusesWhenNoNodeHasRoom(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.Capacity = oneNodeCluster(1000, 1000*mi, 980, 100*mi)

	f.assertRefused(t, ErrAppCapacity)
}

// The pods come back at the size they were deployed with, not at the plan's size now.
func TestResumeApp_CountsTheWorkloadsOwnSize(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.PausedSize = k8s.PausedApp{Replicas: 1, CPURequest: "1", MemoryRequest: "128Mi"}
	f.kube.Capacity = oneNodeCluster(1000, 1000*mi, 500, 100*mi)

	f.assertRefused(t, ErrAppCapacity)
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

func TestResumeApp_AnUnreadableWorkloadSizeRefuses(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.PausedSizeErr = errors.New("connection refused")

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID); err == nil {
		t.Fatal("want the error")
	}
	if f.resumed() {
		t.Fatal("an unknown size must not be admitted")
	}
}

func TestResumeApp_NotPausedIsFoundBeforeAdmission(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.PausedSizeErr = k8s.ErrAppNotPaused

	f.assertRefused(t, k8s.ErrAppNotPaused)
}

func TestResumeApp_AdmittedWhenItFits(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.PausedSize = k8s.PausedApp{Replicas: 3, CPURequest: "250m", MemoryRequest: "512Mi"}
	f.svc.SetPlanTiers(fixedPlan{tier: domain.Standard})

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if !f.resumed() || f.status() != apphost.StatusRunning {
		t.Fatalf("status = %q, calls = %v", f.status(), f.kube.Calls)
	}
	if !slices.Contains(f.kube.Calls, "PausedAppSize:"+f.key()) || !slices.Contains(f.kube.Calls, "GetClusterCapacity") {
		t.Fatalf("resume was not admitted against the cluster: %v", f.kube.Calls)
	}
}
