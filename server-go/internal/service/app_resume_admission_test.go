package service

import (
	"context"
	"errors"
	"slices"
	"strings"
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
	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")
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

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
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

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
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

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err == nil {
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

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if !f.resumed() || f.status() != apphost.StatusRunning || f.kube.ResumedTier[f.key()] != domain.Standard {
		t.Fatalf("status = %q, calls = %v", f.status(), f.kube.Calls)
	}
	if !slices.Contains(f.kube.Calls, "PausedAppReplicas:"+f.key()) || !slices.Contains(f.kube.Calls, "GetClusterCapacity") {
		t.Fatalf("resume was not admitted against the cluster: %v", f.kube.Calls)
	}
}

func TestResumeApp_OverThePlanSaysHowManyItAllows(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.PausedReplicas = 3
	f.svc.SetPlanTiers(fixedPlan{tier: domain.Free})

	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")
	if !errors.Is(err, ErrAppOverPlan) {
		t.Fatalf("err = %v, want ErrAppOverPlan", err)
	}
	for _, want := range []string{"allows at most 1", "paused with 3", "scale it down to 1", "redeploy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not say %q", err.Error(), want)
		}
	}
}

func (f *lifecycleFixture) seedDeploy(t *testing.T, tier domain.TierType) *apphost.Deploy {
	t.Helper()
	deploy := &apphost.Deploy{
		ID: "d-seed", AppID: f.app.ID, ProjectID: f.app.ProjectID, Image: f.app.Image,
		Spec: apphost.DeploySpec{Image: f.app.Image, Port: 8080, Replicas: 1, URL: "https://storefront.example", AppName: f.app.Name,
			Resources: apphost.DeployResources{CPURequest: "250m", CPULimit: "1", MemoryRequest: "512Mi", MemoryLimit: "1Gi"}},
		Config: apphost.DeployConfig{Image: f.app.Image, Port: 8080, Replicas: 1, Tier: tier},
		Status: apphost.DeployStatusPending, CreatedBy: "dev-0",
	}
	if err := f.deploys.Create(deploy); err != nil {
		t.Fatal(err)
	}
	f.deploys.finishSeeded(deploy.ID)
	return deploy
}

func TestResumeApp_AResizedResumeIsRecorded(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.app.Tier = domain.Standard
	seeded := f.seedDeploy(t, domain.Standard)
	f.kube.PausedReplicas = 1

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev-7"); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	latest, _ := f.deploys.GetLatest(f.app.ProjectID, f.app.ID)
	if latest.ID == seeded.ID || latest.Kind != apphost.DeployKindResize || latest.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("latest history entry = %+v, want a finished resize", latest)
	}
	if latest.Config.Tier != domain.Free || latest.Spec.Resources.CPURequest != "50m" || latest.Spec.Resources.MemoryLimit == "1Gi" {
		t.Fatalf("resize records %s %+v, want the FREE size", latest.Config.Tier, latest.Spec.Resources)
	}
	if latest.Spec.Replicas != 1 || latest.Config.Replicas != 1 || latest.Spec.URL != seeded.Spec.URL || latest.CreatedBy != "dev-7" || latest.FinishedAt == nil {
		t.Fatalf("resize entry = %+v", latest)
	}
	if got := f.deploys.appTierOf(f.app.ID); got != domain.Free {
		t.Fatalf("app row tier = %q, want FREE", got)
	}
}

func TestResumeApp_AnUnchangedPlanWritesNoResizeEntry(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	seeded := f.seedDeploy(t, domain.Free)

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if latest, _ := f.deploys.GetLatest(f.app.ProjectID, f.app.ID); latest.ID != seeded.ID {
		t.Fatalf("an unchanged plan wrote %+v", latest)
	}
	if got := f.deploys.appTierOf(f.app.ID); got != "" {
		t.Fatalf("app row tier rewritten to %q", got)
	}
}

func TestResumeApp_ANewAppWithNoHistoryIsRecordedFromItsRecord(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.app.Tier = domain.Standard

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	latest, _ := f.deploys.GetLatest(f.app.ProjectID, f.app.ID)
	if latest == nil || latest.Kind != apphost.DeployKindResize || latest.Image != f.app.Image || latest.Spec.AppName != f.app.Name {
		t.Fatalf("latest = %+v", latest)
	}
}

func TestResumeApp_AResizeThatCannotBeRecordedIsNotApplied(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.app.Tier = domain.Standard
	f.deploys.recordErr = errors.New("connection refused")

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err == nil {
		t.Fatal("want the error")
	}
	if f.resumed() {
		t.Fatal("a size the history does not show must not be applied")
	}
}

func TestResumeApp_UnreadableHistoryRefuses(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.deploys.listErr = errors.New("connection refused")

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err == nil || f.resumed() {
		t.Fatalf("err = %v, resumed = %v", err, f.resumed())
	}
}
