package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// Two new FREE apps ask 2 x 50m/128Mi together; the room for one is not room for both.
func TestAdmitNewApps_AllOrNothing(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	svc.SetPlanTiers(fixedPlan{tier: domain.Free})
	ctx := context.Background()

	kube.Capacity = oneNodeCluster(1000, 1000*mi, 920, 100*mi)
	if err := svc.AdmitNewApps(ctx, app.ProjectID, []int{1, 1}); !errors.Is(err, ErrAppCapacity) {
		t.Fatalf("room for one app must refuse two: %v", err)
	}
	if err := svc.AdmitNewApps(ctx, app.ProjectID, []int{1}); err != nil {
		t.Fatalf("one app fits: %v", err)
	}
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 800, 100*mi)
	if err := svc.AdmitNewApps(ctx, app.ProjectID, []int{1, 1}); err != nil {
		t.Fatalf("both fit: %v", err)
	}
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 1000, 1000*mi)
	if err := svc.AdmitNewApps(ctx, app.ProjectID, []int{0, 0}); err != nil {
		t.Fatalf("stopped apps need no room: %v", err)
	}
}

func TestAdmitNewApps_CountsRolloutsAlreadyAdmitted(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.SetPlanTiers(fixedPlan{tier: domain.Free})
	// 150m free: both new apps (100m) fit, but not beside a rollout charged 2 x 50m.
	kube.Capacity = oneNodeCluster(1000, 1000*mi, 850, 100*mi)
	if err := svc.AdmitNewApps(context.Background(), app.ProjectID, []int{1, 1}); err != nil {
		t.Fatalf("without the other rollout both fit: %v", err)
	}
	rolling := &apphost.Deploy{ID: "d-other", AppID: "other", ProjectID: "p2", Status: apphost.DeployStatusRolling,
		Spec: apphost.DeploySpec{Replicas: 1, Resources: apphost.DeployResources{CPURequest: "50m", MemoryRequest: "128Mi"}}}
	if err := deploys.Create(rolling); err != nil {
		t.Fatal(err)
	}
	if err := svc.AdmitNewApps(context.Background(), app.ProjectID, []int{1, 1}); !errors.Is(err, ErrAppCapacity) {
		t.Fatalf("another app's rollout holds 100m of the room: %v", err)
	}
}

func TestAdmitNewApps_RefusesOverThePlanAndWithoutASandboxNode(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	svc.SetPlanTiers(fixedPlan{tier: domain.Free})
	if err := svc.AdmitNewApps(context.Background(), app.ProjectID, []int{3}); !errors.Is(err, ErrAppOverPlan) {
		t.Fatalf("FREE runs one copy: %v", err)
	}
	kube.Placement = k8s.RuntimePlacement{NodeSelector: map[string]string{"excalibase.io/gvisor": "true"}}
	if err := svc.AdmitNewApps(context.Background(), app.ProjectID, []int{1}); !errors.Is(err, ErrAppNoSandboxNode) {
		t.Fatalf("no node runs the sandbox: %v", err)
	}
	svc.SetPlanTiers(fixedPlan{err: ErrOrgTierUnresolved})
	if err := svc.AdmitNewApps(context.Background(), app.ProjectID, []int{1}); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("an unreadable plan refuses: %v", err)
	}
}
