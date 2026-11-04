package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// templateAppStore lets the template create apps in the store the deploy service reads.
type templateAppStore struct{ *fakeAppStoreForDeploy }

func (s templateAppStore) Create(app *apphost.App, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *app
	s.apps[app.ProjectID+"/"+app.ID] = &copied
	return nil
}

func newTemplateDeployer(store templateAppStore, deploys *fakeDeployStore, kube *k8s.MockClient, instances *fakestore.Instances) *AppDeployService {
	svc := NewAppDeployService(store, deploys, kube, instances, nil, testDeployRender)
	svc.SetPlanTiers(fixedPlan{tier: domain.Free})
	svc.SetNamespaceQuotaTiers(quotaTestTiers)
	svc.async = func(f func()) { f() }
	return svc
}

// The process dies after the answer, before any first deploy ran: each app
// already has a pending deploy, and the next process's sweep ends it failed.
func TestDeployTemplate_ARestartBeforeTheFirstDeploysEndsThemFailed(t *testing.T) {
	rig := newTemplateRig(t)
	store := templateAppStore{&fakeAppStoreForDeploy{apps: map[string]*apphost.App{}}}
	deploys := newFakeDeployStore()
	kube := k8s.NewMockClient()
	kube.Capacity = roomyCluster
	instances := fakestore.NewInstances()
	instances.Items[tplProject] = rig.project
	rig.svc.deps.Apps = store
	rig.svc.deps.Projects = instances
	rig.svc.deps.Deployer = newTemplateDeployer(store, deploys, kube, instances)
	rig.svc.async = func(func()) {} // the background step never runs

	result, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if countCalls(kube, "CreateAppDisk") != 0 || countCalls(kube, "ApplyAppWorkload") != 0 {
		t.Fatalf("the answer prepared a disk or applied a workload: %v", kube.Calls)
	}
	restarted := newTemplateDeployer(store, deploys, kube, instances)
	for _, app := range result.Apps {
		if app.DeployID == "" || app.DeployStatus != apphost.DeployStatusPending {
			t.Fatalf("app %+v has no pending deploy", app)
		}
		kube.AppRolloutErr[rolloutKey(&apphost.App{Name: app.Name}, rig.project.Namespace)] =
			fmt.Errorf("%w: never ran this deploy's workload", k8s.ErrAppRollout)
	}
	if err := restarted.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	for _, app := range result.Apps {
		waitForDeployStatus(t, deploys, app.DeployID, apphost.DeployStatusFailed)
	}
}
