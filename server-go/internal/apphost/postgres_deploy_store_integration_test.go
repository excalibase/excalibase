//go:build integration

package apphost_test

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func sampleDeploy(projectID, appID, createdBy string) *apphost.Deploy {
	return &apphost.Deploy{
		ID:        appID + "-deploy-" + createdBy,
		AppID:     appID,
		ProjectID: projectID,
		Image:     "ghcr.io/acme/storefront:1.4.2",
		Spec: apphost.DeploySpec{
			Image:    "ghcr.io/acme/storefront:1.4.2",
			EnvNames: []string{"MODE"},
			Port:     8080,
			Replicas: 1,
			Resources: apphost.DeployResources{
				CPURequest: "250m", CPULimit: "1", MemoryRequest: "512Mi", MemoryLimit: "1Gi",
			},
		},
		Status:    apphost.DeployStatusPending,
		CreatedBy: createdBy,
		CreatedAt: time.Now().UTC(),
	}
}

func TestPGDeployStore_CreateAssignsRevision(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)

	app := sampleApp("proj_deploy_rt", "app_deploy_rt", "storefront")
	if err := appStore.Create(app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	first := sampleDeploy(app.ProjectID, app.ID, "dev-1")
	if err := deployStore.Create(first); err != nil {
		t.Fatalf("create first deploy: %v", err)
	}
	if first.Revision != 1 {
		t.Errorf("first revision: got %d want 1", first.Revision)
	}
	if err := deployStore.UpdateStatus(first.ID, apphost.DeployStatusSucceeded, "", timePtr(time.Now())); err != nil {
		t.Fatalf("update status: %v", err)
	}

	second := sampleDeploy(app.ProjectID, app.ID, "dev-2")
	if err := deployStore.Create(second); err != nil {
		t.Fatalf("create second deploy: %v", err)
	}
	if second.Revision != 2 {
		t.Errorf("second revision: got %d want 2", second.Revision)
	}

	latest, err := deployStore.GetLatest(app.ProjectID, app.ID)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest == nil || latest.Revision != 2 {
		t.Fatalf("latest: got %+v", latest)
	}

	all, err := deployStore.ListByApp(app.ProjectID, app.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 || all[0].Revision != 2 || all[1].Revision != 1 {
		t.Fatalf("list order: got %+v", all)
	}
}

func TestPGDeployStore_CreateSupersedesEarlierPendingOrRolling(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)

	app := sampleApp("proj_deploy_supersede", "app_deploy_supersede", "storefront")
	if err := appStore.Create(app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	first := sampleDeploy(app.ProjectID, app.ID, "dev-1")
	if err := deployStore.Create(first); err != nil {
		t.Fatalf("create first deploy: %v", err)
	}

	second := sampleDeploy(app.ProjectID, app.ID, "dev-2")
	if err := deployStore.Create(second); err != nil {
		t.Fatalf("create second deploy: %v", err)
	}

	all, err := deployStore.ListByApp(app.ProjectID, app.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 deploys, got %d", len(all))
	}
	if all[0].ID != second.ID || all[0].Status != apphost.DeployStatusPending {
		t.Errorf("second deploy: got %+v", all[0])
	}
	if all[1].ID != first.ID || all[1].Status != apphost.DeployStatusSuperseded {
		t.Errorf("first deploy should be superseded: got %+v", all[1])
	}

	if err := deployStore.UpdateStatus(first.ID, apphost.DeployStatusSucceeded, "", timePtr(time.Now())); err != nil {
		t.Fatalf("update status: %v", err)
	}
	stillSuperseded, err := deployStore.ListByApp(app.ProjectID, app.ID, 0)
	if err != nil {
		t.Fatalf("list after late write: %v", err)
	}
	if stillSuperseded[1].Status != apphost.DeployStatusSuperseded {
		t.Errorf("first deploy after its late write: got %q, want it to stay superseded", stillSuperseded[1].Status)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
