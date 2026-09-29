package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

var quotaTestTiers = fixedTierConfigs{
	domain.Free:       {Instances: 1, MaxApps: 2},
	domain.Standard:   {Instances: 3, MaxApps: 5},
	domain.Enterprise: {Instances: 5, MaxApps: 20},
}

// A deploy sizes the project's quota from its plan first, so the quota never
// refuses what the plan allows (EXC-524).
func TestDeployApp_SizesTheNamespaceQuotaFromThePlan(t *testing.T) {
	app := sampleDeployApp()
	app.Tier = domain.Enterprise
	svc, _, kube := newDeployTestService(t, app)
	svc.SetPlanTiers(fixedPlan{tier: domain.Enterprise})
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	appTier, _ := config.GetAppTierConfig(domain.Enterprise)
	want := k8s.NamespaceQuotaForPlan(k8s.PlanQuotaInputs{DBInstances: 5, Apps: 20, AppMaxReplicas: appTier.MaxReplicas})
	if got := kube.NamespaceQuotas[testDeployNamespace]; got != want {
		t.Fatalf("quota = %+v, want %+v", got, want)
	}
}

// After a downgrade the apps the project still holds keep their room.
func TestDeployApp_QuotaCoversAppsAboveTheLimit(t *testing.T) {
	app := sampleDeployApp()
	app.Tier = domain.Free
	svc, _, kube := newDeployTestService(t, app)
	store := svc.apps.(*fakeAppStoreForDeploy)
	for _, name := range []string{"two", "three"} {
		extra := *app
		extra.ID, extra.Name = "app-"+name, name
		store.apps[extra.ID] = &extra
	}
	svc.SetPlanTiers(fixedPlan{tier: domain.Free})
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if got := kube.NamespaceQuotas[testDeployNamespace]; got.Services != k8s.DefaultNamespaceQuota.Services+3 {
		t.Fatalf("quota = %+v, want room for the 3 apps held", got)
	}
}

// No fallback: a quota that cannot be sized or applied fails the deploy before anything runs.
func TestDeployApp_FailsWhenTheQuotaCannotBeSized(t *testing.T) {
	for name, setup := range map[string]func(*AppDeployService, *k8s.MockClient){
		"no tier source":    func(s *AppDeployService, _ *k8s.MockClient) { s.SetNamespaceQuotaTiers(nil) },
		"unreadable tier":   func(s *AppDeployService, _ *k8s.MockClient) { s.SetNamespaceQuotaTiers(fixedTierConfigs{}) },
		"quota not applied": func(_ *AppDeployService, k *k8s.MockClient) { k.NamespaceQuotaErr = errors.New("api down") },
	} {
		t.Run(name, func(t *testing.T) {
			app := sampleDeployApp()
			svc, deploys, kube := newDeployTestService(t, app)
			setup(svc, kube)
			returned, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
			if err != nil {
				t.Fatalf("DeployApp: %v", err)
			}
			if deploys.getByID(returned.ID).Status != apphost.DeployStatusFailed || len(kube.AppWorkloads) != 0 {
				t.Fatalf("the deploy must fail before any workload is applied")
			}
		})
	}
}

// The quota follows the plan without any app deploy (EXC-524): a database-only
// or brand-new project is sized as soon as its plan is known or changes.
func TestSyncProjectQuota_FollowsThePlanWithoutADeploy(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	store := svc.apps.(*fakeAppStoreForDeploy)
	delete(store.apps, app.ProjectID+"/"+app.ID)
	plan := &switchablePlan{tier: domain.Free}
	svc.SetPlanTiers(plan)
	if err := svc.SyncProjectQuota(context.Background(), app.ProjectID); err != nil {
		t.Fatalf("sync on FREE: %v", err)
	}
	free := kube.NamespaceQuotas[testDeployNamespace]
	plan.tier = domain.Enterprise
	svc.SyncAllProjectQuotas(context.Background())
	enterprise := kube.NamespaceQuotas[testDeployNamespace]
	appTier, _ := config.GetAppTierConfig(domain.Enterprise)
	want := k8s.NamespaceQuotaForPlan(k8s.PlanQuotaInputs{DBInstances: 5, Apps: 20, AppMaxReplicas: appTier.MaxReplicas})
	if enterprise != want || enterprise.PVCs <= free.PVCs {
		t.Fatalf("FREE %+v then ENTERPRISE %+v, want %+v", free, enterprise, want)
	}
}

func TestSyncProjectQuota_SkipsWhatHasNoNamespace(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	svc.instances.(*fakestore.Instances).Items[app.ProjectID].Namespace = ""
	if err := svc.SyncProjectQuota(context.Background(), app.ProjectID); err != nil {
		t.Fatalf("a project with no namespace has no quota to size: %v", err)
	}
	if err := svc.SyncProjectQuota(context.Background(), "proj-missing"); err == nil {
		t.Fatal("an unknown project must be reported")
	}
	if len(kube.NamespaceQuotas) != 0 {
		t.Fatalf("nothing may be sized: %v", kube.NamespaceQuotas)
	}
}

func TestSyncProjectQuota_AnUnreadablePlanIsAnError(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	svc.SetPlanTiers(fixedPlan{err: ErrOrgTierUnresolved})
	if err := svc.SyncProjectQuota(context.Background(), app.ProjectID); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("err = %v", err)
	}
}

type switchablePlan struct{ tier domain.TierType }

func (p *switchablePlan) ProjectPlanTier(context.Context, string) (domain.TierType, error) {
	return p.tier, nil
}
