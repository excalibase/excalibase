package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const mi = 1 << 20

func deployOutcome(t *testing.T, svc *AppDeployService, deploys *fakeDeployStore, app *apphost.App) *apphost.Deploy {
	t.Helper()
	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	return deploys.getByID(deploy.ID)
}

// A FREE app asks 50m and 128Mi; with a 10m/32Mi sandbox overhead one pod needs 60m and 160Mi.
func TestAppAdmission_RefusesWhatTheClusterCannotHold(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.Placement = k8s.RuntimePlacement{OverheadCPUMilli: 10, OverheadMemBytes: 32 * mi}
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 950, 100*mi)

	got := deployOutcome(t, svc, deploys, app)
	if got.Status != apphost.DeployStatusFailed || !strings.Contains(got.FailureReason, "no room") {
		t.Fatalf("deploy = %s %q", got.Status, got.FailureReason)
	}
	if len(kube.AppWorkloads) != 0 {
		t.Fatal("nothing may be applied without room for it")
	}
}

func TestAppAdmission_AdmitsWhatFits(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.Placement = k8s.RuntimePlacement{OverheadCPUMilli: 10, OverheadMemBytes: 32 * mi}
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 940, 840*mi)

	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("deploy = %s %q", got.Status, got.FailureReason)
	}
}

// The terminating pod's 60m is already in Requested and stays there until it
// exits; the check must not also treat it as the pod being replaced.
func TestAppAdmission_ATerminatingPodIsNotRoomForTheNewOne(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 960, 128*mi)
	kube.LivePods = map[string]k8s.AppPods{}

	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusFailed {
		t.Fatalf("over-admitted against a pod that is still terminating: %s", got.Status)
	}
}

// A redeploy of a running app starts one surge pod beside the live one.
func TestAppAdmission_ARedeployNeedsRoomForItsSurgePod(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.LivePods = map[string]k8s.AppPods{testDeployNamespace + "/" + app.ID: {Count: 1, CPUMilli: 50, MemBytes: 128 * mi, MaxCPUMilli: 50, MaxMemBytes: 128 * mi}}
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 960, 128*mi)
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusFailed {
		t.Fatalf("a surge pod with no room was admitted: %s", got.Status)
	}
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 900, 128*mi)
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("deploy = %s %q", got.Status, got.FailureReason)
	}
}

func TestAppAdmission_HeadroomIsHeldBack(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.SetCapacityHeadroom(10)
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 870, 128*mi)
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusFailed {
		t.Fatalf("the headroom was spent: %s", got.Status)
	}
}

func TestAppAdmission_ZeroReplicasNeedsNoRoom(t *testing.T) {
	app := sampleDeployApp()
	app.Replicas = 0
	svc, deploys, kube := newDeployTestService(t, app)
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 1000, 0)
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("deploy = %s %q", got.Status, got.FailureReason)
	}
}

func TestAppAdmission_FailsClosedWhenTheClusterCannotSay(t *testing.T) {
	for name, set := range map[string]func(*k8s.MockClient){
		"capacity error":   func(k *k8s.MockClient) { k.CapacityError = errors.New("api down") },
		"no allocatable":   func(k *k8s.MockClient) { k.Capacity = k8s.ClusterCapacity{} },
		"live pods error":  func(k *k8s.MockClient) { k.LivePodsErr = errors.New("api down") },
		"overhead unknown": func(k *k8s.MockClient) { k.PlacementErr = errors.New("no runtime class") },
		"no node fits":     func(k *k8s.MockClient) { k.Capacity.Nodes = nil },
	} {
		app := sampleDeployApp()
		svc, deploys, kube := newDeployTestService(t, app)
		set(kube)
		if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusFailed {
			t.Errorf("%s: deploy = %s; an unknown capacity must refuse, not admit", name, got.Status)
		}
	}
}

func TestDeployApp_SizesByTheOrganisationsCurrentPlan(t *testing.T) {
	app := sampleDeployApp()
	app.Tier = domain.Enterprise
	svc, deploys, kube := newDeployTestService(t, app)
	svc.SetPlanTiers(fixedPlan{tier: domain.Free})

	got := deployOutcome(t, svc, deploys, app)
	if got.Status != apphost.DeployStatusSucceeded || got.Spec.Resources.CPURequest != "50m" || got.Config.Tier != domain.Free {
		t.Fatalf("deploy = %s %+v tier %s", got.Status, got.Spec.Resources, got.Config.Tier)
	}
	workload := kube.AppWorkloads[testDeployNamespace+"/"+k8s.AppObjectName(app.Name)]
	if cpu := workload.Deployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String(); cpu != "50m" {
		t.Fatalf("rendered cpu request = %s", cpu)
	}
}

func TestDeployApp_RefusesMoreCopiesThanThePlanAllows(t *testing.T) {
	app := sampleDeployApp()
	app.Tier, app.Replicas = domain.Standard, 3
	svc, _, _ := newDeployTestService(t, app)
	svc.SetPlanTiers(fixedPlan{tier: domain.Free})
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); !errors.Is(err, ErrAppOverPlan) {
		t.Fatalf("err = %v, want ErrAppOverPlan", err)
	}
}

func TestDeployApp_AnUnreadablePlanRefuses(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	svc.SetPlanTiers(fixedPlan{err: ErrOrgTierUnresolved})
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("err = %v", err)
	}
	svc.SetPlanTiers(nil)
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("no plan source: err = %v", err)
	}
}

func TestOrgPlanTiers(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o1"}
	instances.Items["p2"] = &domain.DatabaseInstance{ProjectID: "p2", OrgID: "o-missing"}
	instances.Items["p3"] = &domain.DatabaseInstance{ProjectID: "p3", OrgID: "o3"}
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("o1", domain.Standard)
	orgs.AddOrg("o3", domain.TierType("BOGUS"))
	plans := NewOrgPlanTiers(instances, orgs)

	if tier, err := plans.ProjectPlanTier(context.Background(), "p1"); err != nil || tier != domain.Standard {
		t.Fatalf("p1 = %s %v", tier, err)
	}
	for _, project := range []string{"p2", "p3", "p-none"} {
		if _, err := plans.ProjectPlanTier(context.Background(), project); !errors.Is(err, ErrOrgTierUnresolved) {
			t.Errorf("%s: err = %v", project, err)
		}
	}
}

// A plan upgrade replaces 50m pods with 250m ones; the difference must be found.
func TestAppAdmission_AnUpgradeIsChargedTheLargerPods(t *testing.T) {
	app := sampleDeployApp()
	app.Tier = domain.Standard
	svc, deploys, kube := newDeployTestService(t, app)
	kube.LivePods = map[string]k8s.AppPods{testDeployNamespace + "/" + app.ID: {Count: 1, CPUMilli: 50, MemBytes: 128 * mi, MaxCPUMilli: 50, MaxMemBytes: 128 * mi}}
	kube.Capacity = oneNodeCluster(1000, 4000*mi, 600, 128*mi)
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusFailed {
		t.Fatalf("two 250m pods beside the old 50m one need 450m more than there is room for: %s", got.Status)
	}
}

func TestAppAdmission_APodMustFitOnOneNode(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.Capacity = k8s.ClusterCapacity{
		AllocatableCPUMilli: 2000, AllocatableMemBytes: 2000 * mi, RequestedCPUMilli: 1960, RequestedMemBytes: 0,
		Nodes: []k8s.NodeCapacity{
			{Name: "a", AllocatableCPUMilli: 1000, AllocatableMemBytes: 1000 * mi, RequestedCPUMilli: 980},
			{Name: "b", AllocatableCPUMilli: 1000, AllocatableMemBytes: 1000 * mi, RequestedCPUMilli: 980},
		},
	}
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusFailed {
		t.Fatalf("40m free, but split 20m and 20m across nodes, cannot hold a 50m pod: %s", got.Status)
	}
}

func TestAppAdmission_OnlyNodesTheSandboxRunsOnCount(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.Placement = k8s.RuntimePlacement{NodeSelector: map[string]string{"excalibase.io/gvisor": "true"}}
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusFailed {
		t.Fatalf("no node offers the sandbox, yet the app was admitted: %s", got.Status)
	}
	kube.Capacity.Nodes[0].Labels = map[string]string{"excalibase.io/gvisor": "true"}
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("deploy = %s %q", got.Status, got.FailureReason)
	}
}

// Another app's rollout still in flight holds its room even before its pods exist.
func TestAppAdmission_AnotherRolloutInFlightIsCharged(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.Capacity = oneNodeCluster(1000, 4000*mi, 870, 0)
	other := &apphost.Deploy{ID: "other", AppID: "app-other", ProjectID: "p2", Status: apphost.DeployStatusRolling,
		Spec: apphost.DeploySpec{Replicas: 1, Resources: apphost.DeployResources{CPURequest: "50m", MemoryRequest: "128Mi"}}}
	if err := deploys.Create(other); err != nil {
		t.Fatal(err)
	}
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusFailed {
		t.Fatalf("130m free less the other rollout's 100m cannot hold a 50m pod: %s", got.Status)
	}
	if err := deploys.Finish("other", apphost.DeployStatusSucceeded, "", time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if got := deployOutcome(t, svc, deploys, app); got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("once the other rollout finished its pods count as requested, not reserved: %s %q", got.Status, got.FailureReason)
	}
}
