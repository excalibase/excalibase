//go:build integration

package apphost_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func sampleDeploy(projectID, appID, createdBy string) *apphost.Deploy {
	value := "production"
	return &apphost.Deploy{
		ID:        appID + "-deploy-" + createdBy,
		AppID:     appID,
		ProjectID: projectID,
		Image:     "ghcr.io/acme/storefront:1.4.2",
		Spec: apphost.DeploySpec{
			Image: "ghcr.io/acme/storefront:1.4.2",
			Env:   []apphost.EnvSummary{{Name: "MODE", Kind: apphost.KindLiteral}},
			Port:  8080, Replicas: 1,
			Resources: apphost.DeployResources{
				CPURequest: "250m", CPULimit: "1", MemoryRequest: "512Mi", MemoryLimit: "1Gi",
			},
		},
		Config: apphost.DeployConfig{
			Image: "ghcr.io/acme/storefront:1.4.2",
			Env:   []apphost.EnvVar{{Name: "MODE", Kind: apphost.KindLiteral, Value: &value}},
			Port:  8080, Replicas: 1, Tier: domain.Standard,
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

func TestPGDeployStore_ConfigAndRedeployOfRoundTrip(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)

	app := sampleApp("proj_deploy_config", "app_deploy_config", "storefront")
	if err := appStore.Create(app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	source := sampleDeploy(app.ProjectID, app.ID, "dev-1")
	if err := deployStore.Create(source); err != nil {
		t.Fatalf("create source deploy: %v", err)
	}

	redeploy := sampleDeploy(app.ProjectID, app.ID, "dev-2")
	redeploy.RedeployOf = source.ID
	if err := deployStore.Create(redeploy); err != nil {
		t.Fatalf("create redeploy: %v", err)
	}

	got, err := deployStore.Get(app.ProjectID, app.ID, redeploy.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected the redeploy row")
	}
	if got.RedeployOf != source.ID {
		t.Errorf("redeployOf: got %q want %q", got.RedeployOf, source.ID)
	}
	if len(got.Config.Env) != 1 || got.Config.Env[0].Value == nil || *got.Config.Env[0].Value != "production" {
		t.Errorf("config env did not round-trip: got %+v", got.Config.Env)
	}
	if got.Config.Tier != domain.Standard {
		t.Errorf("config tier: got %q want %q", got.Config.Tier, domain.Standard)
	}

	sourceGot, err := deployStore.Get(app.ProjectID, app.ID, source.ID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if sourceGot == nil || sourceGot.RedeployOf != "" {
		t.Fatalf("a plain deploy must have no redeployOf: got %+v", sourceGot)
	}
}

func TestPGDeployStore_Get_CrossAppAndCrossProjectAreNotFound(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)

	appA := sampleApp("proj_deploy_get_a", "app_deploy_get_a", "storefront")
	if err := appStore.Create(appA); err != nil {
		t.Fatalf("create app a: %v", err)
	}
	appB := sampleApp("proj_deploy_get_b", "app_deploy_get_b", "storefront")
	if err := appStore.Create(appB); err != nil {
		t.Fatalf("create app b: %v", err)
	}

	deploy := sampleDeploy(appA.ProjectID, appA.ID, "dev-1")
	if err := deployStore.Create(deploy); err != nil {
		t.Fatalf("create deploy: %v", err)
	}

	if got, err := deployStore.Get(appB.ProjectID, appA.ID, deploy.ID); err != nil || got != nil {
		t.Errorf("cross-project get: got %+v, %v", got, err)
	}
	if got, err := deployStore.Get(appA.ProjectID, appB.ID, deploy.ID); err != nil || got != nil {
		t.Errorf("cross-app get: got %+v, %v", got, err)
	}
	if got, err := deployStore.Get(appA.ProjectID, appA.ID, "does-not-exist"); err != nil || got != nil {
		t.Errorf("unknown id get: got %+v, %v", got, err)
	}
}

func TestPGDeployStore_Get_InvalidIDsError(t *testing.T) {
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	if _, err := deployStore.Get("bad id", "app1", "dep1"); err == nil {
		t.Fatal("expected an error for an invalid project id")
	}
	if _, err := deployStore.Get("proj1", "bad id", "dep1"); err == nil {
		t.Fatal("expected an error for an invalid app id")
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
	if _, err := deployStore.Get("proj1", "app1", "dep1"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Get on a closed db: got %v", err)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
