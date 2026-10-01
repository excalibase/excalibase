//go:build integration

package apphost_test

import (
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func createdApp(t *testing.T, projectID, appID string) (*apphost.PostgresAppStore, *apphost.App) {
	t.Helper()
	s := newPGAppStore(t)
	app := sampleApp(projectID, appID, "life")
	if err := s.Create(app, 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	return s, app
}

func TestPGAppStore_TransitionMovesOnlyFromAnAllowedStatus(t *testing.T) {
	s, app := createdApp(t, "proj_life_move", "app_life_move")

	_, err := s.Transition(app.ProjectID, app.ID, []string{apphost.StatusRunning}, apphost.StatusPausing)
	if !errors.Is(err, apphost.ErrAppStatusConflict) {
		t.Fatalf("from CREATED: err = %v, want ErrAppStatusConflict", err)
	}

	moved, err := s.Transition(app.ProjectID, app.ID, []string{apphost.StatusCreated}, apphost.StatusDeleting)
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if moved.Status != apphost.StatusDeleting || moved.Version != app.Version {
		t.Errorf("moved = status %q version %d; the status moves, the version does not", moved.Status, moved.Version)
	}
	got, err := s.Get(app.ProjectID, app.ID)
	if err != nil || got.Status != apphost.StatusDeleting {
		t.Fatalf("stored status = %+v, %v", got, err)
	}

	if _, err := s.Transition("proj_other", app.ID, []string{apphost.StatusDeleting}, apphost.StatusRunning); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("another project: err = %v, want ErrAppNotFound", err)
	}
	if _, err := s.Transition(app.ProjectID, app.ID, []string{apphost.StatusDeleting}, "BOGUS"); err == nil {
		t.Fatal("an unknown status must be refused")
	}
}

func TestPGAppStore_TransitionSupersedesUnfinishedDeploys(t *testing.T) {
	s, app := createdApp(t, "proj_life_sup", "app_life_sup")
	deploys := apphost.NewPostgresDeployStore(sharedDB)
	deploy := sampleDeploy(app.ProjectID, app.ID, "dev")
	if err := deploys.Create(deploy); err != nil {
		t.Fatalf("create deploy: %v", err)
	}

	if _, err := s.Transition(app.ProjectID, app.ID, []string{apphost.StatusCreated}, apphost.StatusPausing); err != nil {
		t.Fatalf("transition: %v", err)
	}
	got, err := deploys.Get(app.ProjectID, app.ID, deploy.ID)
	if err != nil || got.Status != apphost.DeployStatusSuperseded {
		t.Fatalf("deploy = %+v, %v; want superseded", got, err)
	}
	if err := deploys.Finish(deploy.ID, apphost.DeployStatusSucceeded, "", time.Now(), apphost.StatusRunning); err != nil {
		t.Fatalf("finish: %v", err)
	}
	after, _ := s.Get(app.ProjectID, app.ID)
	if after.Status != apphost.StatusPausing {
		t.Errorf("a superseded deploy spoke for the app: status %q", after.Status)
	}
}

func TestPGDeployStore_CreateRefusesABusyApp(t *testing.T) {
	deploys := apphost.NewPostgresDeployStore(sharedDB)
	for _, busy := range []string{apphost.StatusPausing, apphost.StatusResuming, apphost.StatusDeleting} {
		s, app := createdApp(t, "proj_busy_"+busy, "app_busy_"+busy)
		if _, err := s.Transition(app.ProjectID, app.ID, []string{apphost.StatusCreated}, busy); err != nil {
			t.Fatalf("transition: %v", err)
		}
		if err := deploys.Create(sampleDeploy(app.ProjectID, app.ID, "dev")); !errors.Is(err, apphost.ErrAppBusy) {
			t.Errorf("%s: err = %v, want ErrAppBusy", busy, err)
		}
	}
}

func TestPGDeployStore_CreateAllowsAPausedApp(t *testing.T) {
	s, app := createdApp(t, "proj_paused_deploy", "app_paused_deploy")
	if _, err := s.Transition(app.ProjectID, app.ID, []string{apphost.StatusCreated}, apphost.StatusStopped); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := apphost.NewPostgresDeployStore(sharedDB).Create(sampleDeploy(app.ProjectID, app.ID, "dev")); err != nil {
		t.Fatalf("a paused app is started by a deploy: %v", err)
	}
}

func TestPGAppStore_UpdateRefusesADeletingApp(t *testing.T) {
	s, app := createdApp(t, "proj_life_upd", "app_life_upd")
	if _, err := s.Transition(app.ProjectID, app.ID, []string{apphost.StatusCreated}, apphost.StatusDeleting); err != nil {
		t.Fatalf("transition: %v", err)
	}
	app.Replicas = 0
	if err := s.Update(app, app.Version); !errors.Is(err, apphost.ErrAppBusy) {
		t.Fatalf("err = %v, want ErrAppBusy", err)
	}
}

func TestPGAppStore_DeleteOnlyATornDownApp(t *testing.T) {
	s, app := createdApp(t, "proj_life_del", "app_life_del")
	deploys := apphost.NewPostgresDeployStore(sharedDB)
	if err := deploys.Create(sampleDeploy(app.ProjectID, app.ID, "dev")); err != nil {
		t.Fatalf("create deploy: %v", err)
	}
	if err := s.Delete(app.ProjectID, app.ID); !errors.Is(err, apphost.ErrAppStatusConflict) {
		t.Fatalf("delete before teardown: err = %v, want ErrAppStatusConflict", err)
	}
	if _, err := s.Transition(app.ProjectID, app.ID, []string{apphost.StatusCreated}, apphost.StatusDeleting); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := s.Delete(app.ProjectID, app.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	history, err := deploys.ListByApp(app.ProjectID, app.ID, 0)
	if err != nil || len(history) != 0 {
		t.Fatalf("history left behind: %d, %v", len(history), err)
	}
	if err := s.Delete(app.ProjectID, app.ID); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("second delete: err = %v, want ErrAppNotFound", err)
	}
}

func TestPGAppStore_PurgeProjectRemovesItsAppsAndHistoryOnly(t *testing.T) {
	s, app := createdApp(t, "proj_purge_gone", "app_purge_gone")
	deploys := apphost.NewPostgresDeployStore(sharedDB)
	if err := deploys.Create(sampleDeploy(app.ProjectID, app.ID, "dev")); err != nil {
		t.Fatal(err)
	}
	_, other := createdApp(t, "proj_purge_kept", "app_purge_kept")

	removed, err := s.PurgeProjectApps(app.ProjectID)
	if err != nil || removed != 1 {
		t.Fatalf("PurgeProjectApps = %d, %v", removed, err)
	}
	if got, _ := s.Get(app.ProjectID, app.ID); got != nil {
		t.Error("the app row is still there")
	}
	var history int
	if err := sharedDB.QueryRow(`SELECT count(*) FROM app_deploys WHERE project_id = $1`, app.ProjectID).Scan(&history); err != nil || history != 0 {
		t.Errorf("deploy history left: %d, %v", history, err)
	}
	if got, _ := s.Get(other.ProjectID, other.ID); got == nil {
		t.Error("another project's app went too")
	}
	if removed, err := s.PurgeProjectApps(app.ProjectID); err != nil || removed != 0 {
		t.Fatalf("a repeated purge = %d, %v", removed, err)
	}
	if _, err := s.PurgeProjectApps("bad id!"); err == nil {
		t.Fatal("an invalid project id must be refused")
	}
}

// EXC-523: why the last lifecycle operation did not complete is on the app,
// written only by the store; an edit neither erases nor forges it.
func TestPGAppStore_LifecycleFailureIsRecordedAndKeptByEdits(t *testing.T) {
	s, app := createdApp(t, "proj_life_fail", "app_life_fail")
	failure := &apphost.LifecycleFailure{Operation: "pause", Reason: "the app's workload is still running",
		At: time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)}

	if err := s.RecordLifecycleFailure(app.ProjectID, app.ID, failure); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, _ := s.Get(app.ProjectID, app.ID)
	if got.LifecycleFailure == nil || *got.LifecycleFailure != *failure {
		t.Fatalf("stored failure = %+v, want %+v", got.LifecycleFailure, failure)
	}
	listed, _ := s.List(app.ProjectID)
	if len(listed) != 1 || listed[0].LifecycleFailure == nil {
		t.Fatalf("listed = %+v, want the failure on the app", listed)
	}

	got.LifecycleFailure = nil
	got.Image = "nginx:1.27"
	if err := s.Update(got, got.Version); err != nil {
		t.Fatalf("update: %v", err)
	}
	if kept, _ := s.Get(app.ProjectID, app.ID); kept.LifecycleFailure == nil {
		t.Fatal("an edit erased the recorded failure")
	}
	kept, _ := s.Get(app.ProjectID, app.ID)
	kept.LifecycleFailure = &apphost.LifecycleFailure{Operation: "forged"}
	if err := s.Update(kept, kept.Version); err != nil {
		t.Fatalf("update: %v", err)
	}
	if after, _ := s.Get(app.ProjectID, app.ID); after.LifecycleFailure.Operation != "pause" {
		t.Fatalf("an edit replaced the failure with %+v", after.LifecycleFailure)
	}

	if err := s.RecordLifecycleFailure(app.ProjectID, app.ID, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared, _ := s.Get(app.ProjectID, app.ID); cleared.LifecycleFailure != nil {
		t.Fatalf("cleared failure = %+v, want none", cleared.LifecycleFailure)
	}
	if err := s.RecordLifecycleFailure("proj_other", app.ID, failure); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("another project: err = %v, want ErrAppNotFound", err)
	}
}

func TestPGAppStore_ACreatedAppCarriesNoLifecycleFailure(t *testing.T) {
	s := newPGAppStore(t)
	app := sampleApp("proj_life_new", "app_life_new", "fresh")
	app.LifecycleFailure = &apphost.LifecycleFailure{Operation: "forged"}
	if err := s.Create(app, 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, _ := s.Get(app.ProjectID, app.ID); got.LifecycleFailure != nil {
		t.Fatalf("created app carries %+v", got.LifecycleFailure)
	}
}
