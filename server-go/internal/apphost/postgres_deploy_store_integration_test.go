//go:build integration

package apphost_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

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

func TestPGDeployStore_GetLatest_NoDeploysIsNil(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)

	app := sampleApp("proj_deploy_empty", "app_deploy_empty", "storefront")
	if err := appStore.Create(app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	latest, err := deployStore.GetLatest(app.ProjectID, app.ID)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest != nil {
		t.Fatalf("expected nil for an app with no deploys, got %+v", latest)
	}
}

func TestPGDeployStore_GetLatest_InvalidIDsError(t *testing.T) {
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	if _, err := deployStore.GetLatest("bad id", "app1"); err == nil {
		t.Fatal("expected an error for an invalid project id")
	}
	if _, err := deployStore.GetLatest("proj1", "bad id"); err == nil {
		t.Fatal("expected an error for an invalid app id")
	}
}

func TestPGDeployStore_UpdateStatus_UnknownIDIsNoop(t *testing.T) {
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	if err := deployStore.UpdateStatus("does-not-exist", apphost.DeployStatusSucceeded, "", timePtr(time.Now())); err != nil {
		t.Fatalf("update status on an unknown id must not error, got %v", err)
	}
}

func TestPGDeployStore_Create_RejectsNonPendingStatus(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)

	app := sampleApp("proj_deploy_badstatus", "app_deploy_badstatus", "storefront")
	if err := appStore.Create(app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	deploy := sampleDeploy(app.ProjectID, app.ID, "dev-1")
	deploy.Status = apphost.DeployStatusRolling
	if err := deployStore.Create(deploy); err == nil {
		t.Fatal("expected an error creating a deploy that is not pending")
	}
}

func TestPGDeployStore_ClosedDBErrors(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://x:x@127.0.0.1:1/x?sslmode=disable")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close()
	deployStore := apphost.NewPostgresDeployStore(db)

	if err := deployStore.Create(sampleDeploy("proj1", "app1", "dev-1")); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Create on a closed db: got %v", err)
	}
	if err := deployStore.UpdateStatus("dep1", apphost.DeployStatusSucceeded, "", nil); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("UpdateStatus on a closed db: got %v", err)
	}
	if _, err := deployStore.ListByApp("proj1", "app1", 0); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("ListByApp on a closed db: got %v", err)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
