//go:build integration

package apphost_test

import (
	"database/sql"
	"errors"
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

// A finished deploy and the app's status land together, and the status is
// observed state: it moves no version a developer's edit is checked against.
func TestPGDeployStore_FinishSetsTheAppStatus(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	app := sampleApp("proj_deploy_finish", "app_deploy_finish", "storefront")
	if err := appStore.Create(app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	deploy := sampleDeploy(app.ProjectID, app.ID, "dev-1")
	if err := deployStore.Create(deploy); err != nil {
		t.Fatalf("create deploy: %v", err)
	}

	if err := deployStore.Finish(deploy.ID, apphost.DeployStatusSucceeded, "", time.Now(), apphost.StatusRunning); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got, err := appStore.Get(app.ProjectID, app.ID)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Status != apphost.StatusRunning {
		t.Errorf("app status: got %q want %q", got.Status, apphost.StatusRunning)
	}
	if got.Version != app.Version {
		t.Errorf("an observed status must not move the version: got %d want %d", got.Version, app.Version)
	}
	var column string
	if err := sharedDB.QueryRow(`SELECT status FROM apps WHERE project_id = $1 AND id = $2`, app.ProjectID, app.ID).
		Scan(&column); err != nil || column != apphost.StatusRunning {
		t.Errorf("status column: got %q (%v) want %q", column, err, apphost.StatusRunning)
	}
	stored, _ := deployStore.Get(app.ProjectID, app.ID, deploy.ID)
	if stored.Status != apphost.DeployStatusSucceeded || stored.FinishedAt == nil {
		t.Errorf("deploy: got %+v", stored)
	}
}

// A superseded deploy finishing late must not speak for the app.
func TestPGDeployStore_FinishOfASupersededDeployLeavesTheApp(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	app := sampleApp("proj_deploy_late", "app_deploy_late", "storefront")
	if err := appStore.Create(app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	first := sampleDeploy(app.ProjectID, app.ID, "dev-1")
	second := sampleDeploy(app.ProjectID, app.ID, "dev-2")
	for _, deploy := range []*apphost.Deploy{first, second} {
		if err := deployStore.Create(deploy); err != nil {
			t.Fatalf("create deploy: %v", err)
		}
	}

	if err := deployStore.Finish(first.ID, apphost.DeployStatusFailed, "boom", time.Now(), apphost.StatusFailed); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got, _ := appStore.Get(app.ProjectID, app.ID)
	if got.Status != apphost.StatusCreated {
		t.Errorf("app status: got %q, want it untouched", got.Status)
	}
	stored, _ := deployStore.Get(app.ProjectID, app.ID, first.ID)
	if stored.Status != apphost.DeployStatusSuperseded {
		t.Errorf("first deploy: got %q want superseded", stored.Status)
	}
}

// A failure whose effect on serving could not be observed leaves the app as it was.
func TestPGDeployStore_FinishWithNoAppStatusLeavesTheApp(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	app := sampleApp("proj_deploy_unobserved", "app_deploy_unobserved", "storefront")
	if err := appStore.Create(app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	deploy := sampleDeploy(app.ProjectID, app.ID, "dev-1")
	if err := deployStore.Create(deploy); err != nil {
		t.Fatalf("create deploy: %v", err)
	}
	if err := deployStore.Finish(deploy.ID, apphost.DeployStatusFailed, "boom", time.Now(), ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got, _ := appStore.Get(app.ProjectID, app.ID)
	if got.Status != apphost.StatusCreated {
		t.Errorf("app status: got %q, want it untouched", got.Status)
	}
	stored, _ := deployStore.Get(app.ProjectID, app.ID, deploy.ID)
	if stored.Status != apphost.DeployStatusFailed {
		t.Errorf("deploy: got %q want failed", stored.Status)
	}
}

func TestPGDeployStore_FinishRefusesAnUnknownAppStatus(t *testing.T) {
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	if err := deployStore.Finish("dep1", apphost.DeployStatusSucceeded, "", time.Now(), "RUNNING"); err == nil {
		t.Fatal("an app status outside the vocabulary must be refused")
	}
}

func TestPGDeployStore_ListUnfinished(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	app := sampleApp("proj_deploy_unfinished", "app_deploy_unfinished", "storefront")
	if err := appStore.Create(app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	done := sampleDeploy(app.ProjectID, app.ID, "dev-1")
	if err := deployStore.Create(done); err != nil {
		t.Fatalf("create deploy: %v", err)
	}
	if err := deployStore.Finish(done.ID, apphost.DeployStatusSucceeded, "", time.Now(), apphost.StatusRunning); err != nil {
		t.Fatalf("finish: %v", err)
	}
	rolling := sampleDeploy(app.ProjectID, app.ID, "dev-2")
	if err := deployStore.Create(rolling); err != nil {
		t.Fatalf("create deploy: %v", err)
	}
	if err := deployStore.UpdateStatus(rolling.ID, apphost.DeployStatusRolling, "", nil); err != nil {
		t.Fatalf("roll: %v", err)
	}

	unfinished, err := deployStore.ListUnfinished()
	if err != nil {
		t.Fatalf("list unfinished: %v", err)
	}
	var ours []*apphost.Deploy
	for _, deploy := range unfinished {
		if deploy.AppID == app.ID {
			ours = append(ours, deploy)
		}
	}
	if len(ours) != 1 || ours[0].ID != rolling.ID || ours[0].Config.Image == "" {
		t.Fatalf("unfinished deploys of the app: got %+v, want only the rolling one with its config", ours)
	}
}

func TestPGDeployStore_ClosedDBErrorsOnFinishAndListUnfinished(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://x:x@127.0.0.1:1/x?sslmode=disable")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close()
	deployStore := apphost.NewPostgresDeployStore(db)
	if err := deployStore.Finish("dep1", apphost.DeployStatusSucceeded, "", time.Now(), apphost.StatusRunning); err == nil {
		t.Error("Finish on a closed db must fail")
	}
	if _, err := deployStore.ListUnfinished(); err == nil {
		t.Error("ListUnfinished on a closed db must fail")
	}
}

func resizeOf(from *apphost.Deploy, tier domain.TierType) *apphost.Deploy {
	resize := *from
	resize.ID = from.ID + "-resize"
	resize.Kind = apphost.DeployKindResize
	resize.Status = apphost.DeployStatusSucceeded
	resize.Config.Tier = tier
	resize.Spec.Resources = apphost.DeployResources{CPURequest: "50m", CPULimit: "250m", MemoryRequest: "128Mi", MemoryLimit: "256Mi"}
	resize.CreatedAt = time.Now().UTC()
	resize.FinishedAt = &resize.CreatedAt
	return &resize
}

// A resized resume is one history entry and the app's recorded size, in one
// write, and only while the app is resuming.
func TestPGDeployStore_RecordResize(t *testing.T) {
	appStore, app := createdApp(t, "proj_deploy_resize", "app_deploy_resize")
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	first := sampleDeploy(app.ProjectID, app.ID, "dev-1")
	if err := deployStore.Create(first); err != nil {
		t.Fatalf("create deploy: %v", err)
	}
	if err := deployStore.Finish(first.ID, apphost.DeployStatusSucceeded, "", time.Now(), apphost.StatusRunning); err != nil {
		t.Fatalf("finish: %v", err)
	}

	resize := resizeOf(first, domain.Free)
	if err := deployStore.RecordResize(resize); !errors.Is(err, apphost.ErrAppStatusConflict) {
		t.Fatalf("while running: err = %v, want ErrAppStatusConflict", err)
	}
	if _, err := appStore.Transition(app.ProjectID, app.ID, []string{apphost.StatusRunning}, apphost.StatusResuming); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := deployStore.RecordResize(resize); err != nil {
		t.Fatalf("RecordResize: %v", err)
	}

	latest, err := deployStore.GetLatest(app.ProjectID, app.ID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.ID != resize.ID || latest.Revision != 2 || latest.Kind != apphost.DeployKindResize ||
		latest.Status != apphost.DeployStatusSucceeded || latest.FinishedAt == nil || latest.Config.Tier != domain.Free {
		t.Fatalf("latest = %+v", latest)
	}
	if earlier, _ := deployStore.Get(app.ProjectID, app.ID, first.ID); earlier.Kind != apphost.DeployKindDeploy {
		t.Fatalf("a deploy reads back as kind %q", earlier.Kind)
	}
	stored, err := appStore.Get(app.ProjectID, app.ID)
	if err != nil || stored.Tier != domain.Free || stored.Version != app.Version || stored.Status != apphost.StatusResuming {
		t.Fatalf("app = %+v, %v; the tier moves, the version and status do not", stored, err)
	}

	pending := resizeOf(first, domain.Free)
	pending.ID, pending.Status = "not-finished", apphost.DeployStatusPending
	if err := deployStore.RecordResize(pending); err == nil {
		t.Fatal("a resize is recorded finished, never pending")
	}
}
