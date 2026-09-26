package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func TestDeployApp_ASucceededRolloutMakesTheAppActive(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	var watched string
	kube.AppRolloutFunc = func(_ context.Context, _, _, deployID string, _ time.Duration) error {
		watched = deployID
		return nil
	}

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if watched != deploy.ID {
		t.Errorf("the rollout watch must look for this deploy's workload: got %q want %q", watched, deploy.ID)
	}
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusRunning {
		t.Fatalf("app status: got %q want %q", got, apphost.StatusRunning)
	}
}

func TestDeployApp_ASucceededRolloutOfZeroReplicasStopsTheApp(t *testing.T) {
	app := sampleDeployApp()
	app.Replicas = 0
	svc, deploys, _ := newDeployTestService(t, app)

	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusStopped {
		t.Fatalf("app status: got %q want %q", got, apphost.StatusStopped)
	}
}

func TestDeployApp_AFailedRolloutFailsTheApp(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.AppRolloutErr[rolloutKey(app, testDeployNamespace)] = fmt.Errorf("%w: storefront CrashLoopBackOff: back-off", k8s.ErrAppRollout)

	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusFailed {
		t.Fatalf("app status: got %q want %q", got, apphost.StatusFailed)
	}
}

// A failed redeploy leaves the previous version serving: the deploy failed,
// the app did not.
func TestDeployApp_AFailedRedeployKeepsAStillServingAppActive(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.AppRolloutErr[rolloutKey(app, testDeployNamespace)] = fmt.Errorf("%w: storefront CrashLoopBackOff: back-off", k8s.ErrAppRollout)
	kube.AppAvailable[rolloutKey(app, testDeployNamespace)] = 1

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	waitForDeployStatus(t, deploys, deploy.ID, apphost.DeployStatusFailed)
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusRunning {
		t.Fatalf("app status: got %q want %q", got, apphost.StatusRunning)
	}
}

func TestDeployApp_AFailedRedeployBeforeApplyKeepsAStillServingAppActive(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.render.Route.Domain = ""
	kube.AppAvailable[rolloutKey(app, testDeployNamespace)] = 2

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	waitForDeployStatus(t, deploys, deploy.ID, apphost.DeployStatusFailed)
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusRunning {
		t.Fatalf("app status: got %q want %q", got, apphost.StatusRunning)
	}
}

// Whether anything still serves could not be read, so no status is guessed.
func TestDeployApp_AFailureWhoseServingIsUnknownLeavesTheApp(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.ApplyAppWorkloadErr = errors.New("apply refused")
	kube.AppAvailableErr = errors.New("api server down")

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	waitForDeployStatus(t, deploys, deploy.ID, apphost.DeployStatusFailed)
	if got, recorded := deploys.appStatusRecorded(app.ID); recorded {
		t.Fatalf("app status must be left alone, got %q", got)
	}
}

func TestDeployApp_ANamespaceLookupErrorLeavesTheApp(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	instances := fakestore.NewInstances()
	instances.Err = errors.New("db down")
	svc.instances = instances

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	waitForDeployStatus(t, deploys, deploy.ID, apphost.DeployStatusFailed)
	if got, recorded := deploys.appStatusRecorded(app.ID); recorded {
		t.Fatalf("app status must be left alone, got %q", got)
	}
}

func TestDeployApp_AFailedApplyFailsTheApp(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	kube.ApplyAppWorkloadErr = errors.New("apply refused")

	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusFailed {
		t.Fatalf("app status: got %q want %q", got, apphost.StatusFailed)
	}
}

// The deploy handed back to the caller is not written by the watch that runs
// after it returns, so encoding it never races the rollout's outcome.
func TestDeployApp_TheWatchDoesNotWriteTheReturnedDeploy(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusRolling {
		t.Errorf("returned deploy: got %q want rolling", deploy.Status)
	}
	waitForDeployStatus(t, deploys, deploy.ID, apphost.DeployStatusSucceeded)
}

// A deploy whose watch died with the process is picked up again and ends.
func TestResumeRollouts_FinishesADeployLeftRolling(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	orphan := seedUnfinishedDeploy(t, deploys, app, apphost.DeployStatusRolling)
	var watched string
	kube.AppRolloutFunc = func(_ context.Context, ns, name, deployID string, _ time.Duration) error {
		if ns != testDeployNamespace || name != k8s.AppObjectName(app.Name) {
			t.Errorf("watched %s/%s", ns, name)
		}
		watched = deployID
		return nil
	}

	if err := svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	if watched != orphan.ID {
		t.Fatalf("resumed watch looked for %q, want %q", watched, orphan.ID)
	}
	waitForDeployStatus(t, deploys, orphan.ID, apphost.DeployStatusSucceeded)
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusRunning {
		t.Fatalf("app status: got %q want %q", got, apphost.StatusRunning)
	}
}

func TestResumeRollouts_FailsADeployWhoseWorkloadNeverRan(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	orphan := seedUnfinishedDeploy(t, deploys, app, apphost.DeployStatusPending)
	kube.AppRolloutErr[rolloutKey(app, testDeployNamespace)] = fmt.Errorf("%w: never ran this deploy's workload", k8s.ErrAppRollout)

	if err := svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	failed := waitForDeployStatus(t, deploys, orphan.ID, apphost.DeployStatusFailed)
	if failed.FailureReason == "" {
		t.Error("a failed deploy must say why")
	}
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusFailed {
		t.Fatalf("app status: got %q want %q", got, apphost.StatusFailed)
	}
}

func TestResumeRollouts_LeavesADeployThisProcessIsWatching(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.async = func(f func()) { go f() }
	release := make(chan struct{})
	var calls int32
	kube.AppRolloutFunc = func(context.Context, string, string, string, time.Duration) error {
		atomic.AddInt32(&calls, 1)
		<-release
		return nil
	}
	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}

	if err := svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	close(release)
	waitForDeployStatus(t, deploys, deploy.ID, apphost.DeployStatusSucceeded)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("rollout watches: got %d want 1", got)
	}
}

// A newer deploy another replica created replaces the older watch held here.
func TestResumeRollouts_ANewerDeployReplacesAnOlderWatch(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.async = func(f func()) { go f() }
	older := make(chan struct{})
	olderStopped := make(chan struct{})
	var calls int32
	kube.AppRolloutFunc = func(ctx context.Context, _, _, _ string, _ time.Duration) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(older)
			<-ctx.Done()
			close(olderStopped)
			return ctx.Err()
		}
		return nil
	}
	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	<-older
	newer := seedUnfinishedDeploy(t, deploys, app, apphost.DeployStatusRolling)

	if err := svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	waitForDeployStatus(t, deploys, newer.ID, apphost.DeployStatusSucceeded)
	select {
	case <-olderStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the older watch must be stopped")
	}
	if got := deploys.getByID(first.ID); got.Status != apphost.DeployStatusSuperseded {
		t.Errorf("older deploy: got %q want superseded", got.Status)
	}
}

func TestResumeRollouts_ReportsAStoreError(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	deploys.listErr = errors.New("db down")
	if err := svc.ResumeRollouts(context.Background()); err == nil {
		t.Fatal("a failed listing must be reported")
	}
}

// A lookup that fails says nothing about the deploy, so it is left for the
// next sweep instead of being failed.
func TestResumeRollouts_LeavesADeployWhoseLookupFailed(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	orphan := seedUnfinishedDeploy(t, deploys, app, apphost.DeployStatusRolling)
	instances := fakestore.NewInstances()
	instances.Err = errors.New("db down")
	svc.instances = instances

	if err := svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	svc.apps.(*fakeAppStoreForDeploy).getErr = errors.New("db down")
	if err := svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	if got := deploys.getByID(orphan.ID).Status; got != apphost.DeployStatusRolling {
		t.Fatalf("deploy: got %q, want it left rolling", got)
	}
	if len(kube.Calls) != 0 {
		t.Errorf("nothing may be watched without a namespace: %v", kube.Calls)
	}
}

func TestResumeRollouts_FailsADeployWhoseProjectHasNoNamespace(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	orphan := seedUnfinishedDeploy(t, deploys, app, apphost.DeployStatusPending)
	svc.instances = fakestore.NewInstances()

	if err := svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	waitForDeployStatus(t, deploys, orphan.ID, apphost.DeployStatusFailed)
}

func TestResumeRollouts_SkipsADeployOfADeletedApp(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	gone := *app
	gone.ID = "app_gone"
	orphan := seedUnfinishedDeploy(t, deploys, &gone, apphost.DeployStatusRolling)

	if err := svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	if got := deploys.getByID(orphan.ID).Status; got != apphost.DeployStatusRolling {
		t.Fatalf("deploy: got %q", got)
	}
	if len(kube.Calls) != 0 {
		t.Errorf("nothing may be watched for a deleted app: %v", kube.Calls)
	}
}

type fakeLeader struct {
	leader bool
	err    error
}

func (f fakeLeader) IsLeader(context.Context) (bool, error) { return f.leader, f.err }

func TestStartRolloutSweeper_ResumesAtOnceWhenLeading(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	orphan := seedUnfinishedDeploy(t, deploys, app, apphost.DeployStatusRolling)

	stop := svc.StartRolloutSweeper(context.Background(), fakeLeader{leader: true}, time.Hour)
	defer stop()
	waitForDeployStatus(t, deploys, orphan.ID, apphost.DeployStatusSucceeded)
}

func TestStartRolloutSweeper_LeavesDeploysToTheLeader(t *testing.T) {
	for name, leader := range map[string]fakeLeader{
		"not leading":      {leader: false},
		"leadership error": {err: errors.New("db down")},
	} {
		t.Run(name, func(t *testing.T) {
			app := sampleDeployApp()
			svc, deploys, _ := newDeployTestService(t, app)
			orphan := seedUnfinishedDeploy(t, deploys, app, apphost.DeployStatusRolling)

			stop := svc.StartRolloutSweeper(context.Background(), leader, 10*time.Millisecond)
			time.Sleep(50 * time.Millisecond)
			stop()
			if got := deploys.getByID(orphan.ID).Status; got != apphost.DeployStatusRolling {
				t.Fatalf("deploy: got %q, want it left to the leader", got)
			}
		})
	}
}

func TestStartRolloutSweeper_KeepsGoingAfterAStoreError(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	deploys.listErr = errors.New("db down")

	stop := svc.StartRolloutSweeper(context.Background(), fakeLeader{leader: true}, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	deploys.mu.Lock()
	deploys.listErr = nil
	deploys.mu.Unlock()
	orphan := seedUnfinishedDeploy(t, deploys, app, apphost.DeployStatusRolling)
	defer stop()
	waitForDeployStatus(t, deploys, orphan.ID, apphost.DeployStatusSucceeded)
}

func seedUnfinishedDeploy(t *testing.T, deploys *fakeDeployStore, app *apphost.App, status string) *apphost.Deploy {
	t.Helper()
	deploy := &apphost.Deploy{
		ID: fmt.Sprintf("dep-orphan-%d", time.Now().UnixNano()), AppID: app.ID, ProjectID: app.ProjectID,
		Image: app.Image, Config: apphost.ConfigFromApp(app), Spec: apphost.DeploySpec{AppName: app.Name},
		Status: apphost.DeployStatusPending, CreatedBy: "dev-1", CreatedAt: time.Now().UTC(),
	}
	if err := deploys.Create(deploy); err != nil {
		t.Fatalf("seed deploy: %v", err)
	}
	if status != apphost.DeployStatusPending {
		if err := deploys.UpdateStatus(deploy.ID, status, "", nil); err != nil {
			t.Fatalf("seed deploy status: %v", err)
		}
	}
	return deploy
}
