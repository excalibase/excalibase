package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

type lifecycleFixture struct {
	svc     *AppDeployService
	apps    *fakeAppStoreForDeploy
	deploys *fakeDeployStore
	kube    *k8s.MockClient
	purger  *fakeSecretPurger
	app     *apphost.App
}

type fakeSecretPurger struct {
	mu       sync.Mutex
	prefixes []string
	err      error
}

func (f *fakeSecretPurger) DeletePrefix(prefix string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.prefixes = append(f.prefixes, prefix)
	return 1, nil
}

func newLifecycleFixture(t *testing.T, status string) *lifecycleFixture {
	t.Helper()
	app := sampleDeployApp()
	app.Status = status
	apps := newFakeAppStoreForDeploy(app)
	deploys := newFakeDeployStore()
	apps.deploys = deploys
	kube := k8s.NewMockClient()
	instances := fakestore.NewInstances()
	instances.Items[app.ProjectID] = &domain.DatabaseInstance{ProjectID: app.ProjectID, Namespace: testDeployNamespace}
	kube.Capacity = roomyCluster
	kube.PausedReplicas = 1
	svc := NewAppDeployService(apps, deploys, kube, instances, nil, testDeployRender)
	svc.SetPlanTiers(fixedPlan{tier: app.Tier})
	svc.SetNamespaceQuotaTiers(quotaTestTiers)
	svc.async = func(f func()) { f() }
	purger := &fakeSecretPurger{}
	svc.SetSecretPurger(purger)
	return &lifecycleFixture{svc: svc, apps: apps, deploys: deploys, kube: kube, purger: purger, app: app}
}

func (f *lifecycleFixture) status() string { return f.apps.statusOf(f.app.ProjectID, f.app.ID) }

func (f *lifecycleFixture) key() string { return testDeployNamespace + "/" + f.app.ID }

func TestPauseApp_StopsTheWorkloadAndWaitsForItsPods(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)

	app, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID)
	if err != nil {
		t.Fatalf("PauseApp: %v", err)
	}
	if app.Status != apphost.StatusStopped || f.status() != apphost.StatusStopped {
		t.Fatalf("status = %q / %q, want %q", app.Status, f.status(), apphost.StatusStopped)
	}
	if !f.kube.AppPaused[f.key()] {
		t.Error("the workload was not scaled down")
	}
	if !slices.Contains(f.kube.Calls, "WaitForAppPodsGone:"+f.key()) {
		t.Error("PAUSED must wait for the pods to be gone")
	}
	if want := []string{apphost.StatusPausing, apphost.StatusStopped}; !slices.Equal(f.apps.transitions, want) {
		t.Errorf("transitions = %v, want %v", f.apps.transitions, want)
	}
}

func TestPauseApp_PodsThatStayKeepItPausing(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.kube.AppPodsGoneErr = fmt.Errorf("%w after 3m: web-1", k8s.ErrAppPodsRemain)

	_, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID)
	if !errors.Is(err, k8s.ErrAppPodsRemain) {
		t.Fatalf("err = %v, want ErrAppPodsRemain", err)
	}
	if f.status() != apphost.StatusPausing {
		t.Fatalf("status = %q; a pause that did not finish must say so and stay retryable", f.status())
	}

	f.kube.AppPodsGoneErr = nil
	if _, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if f.status() != apphost.StatusStopped {
		t.Fatalf("status after retry = %q", f.status())
	}
}

func TestPauseApp_SupersedesARolloutStillBeingWatched(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	deploy := &apphost.Deploy{ID: "d1", AppID: f.app.ID, ProjectID: f.app.ProjectID, Status: apphost.DeployStatusRolling}
	if err := f.deploys.Create(deploy); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("PauseApp: %v", err)
	}
	if got := f.deploys.getByID("d1").Status; got != apphost.DeployStatusSuperseded {
		t.Fatalf("deploy status = %q, want superseded", got)
	}
}

func TestPauseApp_AlreadyPausedIsANoOp(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	app, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID)
	if err != nil || app.Status != apphost.StatusStopped {
		t.Fatalf("PauseApp = %+v, %v", app, err)
	}
	if len(f.kube.Calls) != 0 {
		t.Errorf("nothing to do, but called %v", f.kube.Calls)
	}
}

func TestPauseApp_RefusesWhatIsNotRunning(t *testing.T) {
	for _, status := range []string{apphost.StatusCreated, apphost.StatusResuming, apphost.StatusDeleting} {
		f := newLifecycleFixture(t, status)
		_, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID)
		if !errors.Is(err, apphost.ErrAppStatusConflict) {
			t.Errorf("%s: err = %v, want ErrAppStatusConflict", status, err)
		}
		if f.status() != status {
			t.Errorf("%s: status moved to %q", status, f.status())
		}
	}
}

func TestPauseApp_NoWorkloadPutsTheStatusBack(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusFailed)
	f.kube.AppPauseErr = k8s.ErrAppNotDeployed

	_, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID)
	if !errors.Is(err, k8s.ErrAppNotDeployed) {
		t.Fatalf("err = %v, want ErrAppNotDeployed", err)
	}
	if f.status() != apphost.StatusFailed {
		t.Fatalf("status = %q, want it back at FAILED", f.status())
	}
}

func TestPauseApp_MissingApp(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	if _, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, "nope"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("err = %v, want ErrAppNotFound", err)
	}
}

func TestPauseApp_RefusedWhileAnotherOperationHoldsTheApp(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	release, err := f.svc.holdApp(context.Background(), f.app.ProjectID, f.app.ID, OperationDeletion)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	f.svc.deployLeaseWait = 0
	f.svc.lifecycleLeaseWait = 0

	if _, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("err = %v, want ErrProjectOperationRunning", err)
	}
	if _, err := f.svc.DeployApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("deploy: err = %v, want ErrProjectOperationRunning", err)
	}
}

func TestResumeApp_RestoresTheWorkloadAndRuns(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)

	app, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")
	if err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if app.Status != apphost.StatusRunning || f.status() != apphost.StatusRunning {
		t.Fatalf("status = %q / %q, want %q", app.Status, f.status(), apphost.StatusRunning)
	}
	if !slices.Contains(f.kube.Calls, "ResumeAppWorkload:"+f.key()) {
		t.Errorf("calls = %v", f.kube.Calls)
	}
}

func TestResumeApp_ARolloutThatFailsFailsTheApp(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.AppResumeErr = fmt.Errorf("%w: storefront CrashLoopBackOff", k8s.ErrAppRollout)

	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")
	if !errors.Is(err, k8s.ErrAppRollout) {
		t.Fatalf("err = %v", err)
	}
	if f.status() != apphost.StatusFailed {
		t.Fatalf("status = %q, want FAILED", f.status())
	}
}

func TestResumeApp_AnUnreachableClusterStaysResuming(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.AppResumeErr = errors.New("connection refused")

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err == nil {
		t.Fatal("want the error")
	}
	if f.status() != apphost.StatusResuming {
		t.Fatalf("status = %q, want RESUMING so a retry picks it up", f.status())
	}
	f.kube.AppResumeErr = nil
	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestResumeApp_NotPausedPutsTheStatusBack(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.AppResumeErr = k8s.ErrAppNotPaused

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); !errors.Is(err, k8s.ErrAppNotPaused) {
		t.Fatalf("err = %v", err)
	}
	if f.status() != apphost.StatusStopped {
		t.Fatalf("status = %q, want PAUSED back", f.status())
	}
}

func TestResumeApp_RunningIsANoOpAndBusyIsRefused(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	if app, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil || app.Status != apphost.StatusRunning {
		t.Fatalf("ResumeApp = %+v, %v", app, err)
	}
	for _, status := range []string{apphost.StatusCreated, apphost.StatusPausing, apphost.StatusDeleting} {
		f := newLifecycleFixture(t, status)
		if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); !errors.Is(err, apphost.ErrAppStatusConflict) {
			t.Errorf("%s: err = %v, want ErrAppStatusConflict", status, err)
		}
	}
}

func TestDeleteApp_TearsDownThenForgets(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)

	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if !f.kube.AppDeleted[f.key()] {
		t.Error("the workload was not torn down")
	}
	if want := apphost.AppSecretPrefix(f.app.ProjectID, f.app.ID); !slices.Equal(f.purger.prefixes, []string{want}) {
		t.Errorf("purged %v, want %s", f.purger.prefixes, want)
	}
	if got, _ := f.apps.Get(f.app.ProjectID, f.app.ID); got != nil {
		t.Error("the app row must go last")
	}
}

func TestDeleteApp_AFailedTeardownKeepsTheRow(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.kube.AppDeleteErr = fmt.Errorf("%w after 3m: web-1", k8s.ErrAppPodsRemain)

	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false); !errors.Is(err, k8s.ErrAppPodsRemain) {
		t.Fatalf("err = %v", err)
	}
	if f.status() != apphost.StatusDeleting {
		t.Fatalf("status = %q, want DELETING", f.status())
	}
	if len(f.purger.prefixes) != 0 {
		t.Error("secrets must outlive a workload that may still read them")
	}

	f.kube.AppDeleteErr = nil
	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got, _ := f.apps.Get(f.app.ProjectID, f.app.ID); got != nil {
		t.Error("the retried deletion must finish")
	}
}

func TestDeleteApp_AFailedPurgeKeepsTheRow(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.purger.err = errors.New("vault down")

	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false); err == nil {
		t.Fatal("want the purge failure")
	}
	if got, _ := f.apps.Get(f.app.ProjectID, f.app.ID); got == nil {
		t.Fatal("the row must stay so the deletion can be retried")
	}
}

func TestDeleteApp_NoNamespaceHasNoWorkloadToWaitFor(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusCreated)
	f.svc.instances = fakestore.NewInstances()

	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if len(f.kube.Calls) != 0 {
		t.Errorf("no namespace, no cluster calls; got %v", f.kube.Calls)
	}
}

func TestDeleteApp_MissingApp(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, "nope", false); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteApp_WithoutAPurgerStillDeletes(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.svc.SetSecretPurger(nil)
	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
}

// recordingOriginReleaser stands in for the CORS store an app's origin lives in.
type recordingOriginReleaser struct {
	released []string
	err      error
}

func (r *recordingOriginReleaser) ReleaseAppCorsOrigins(_ context.Context, projectID, appID string) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.released = append(r.released, projectID+"/"+appID)
	return []string{"https://web.apps.example.test"}, nil
}

func TestDeleteApp_ReleasesTheOriginTheAppAdded(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	origins := &recordingOriginReleaser{}
	f.svc.SetCorsOriginReleaser(origins)
	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if want := f.app.ProjectID + "/" + f.app.ID; !slices.Equal(origins.released, []string{want}) {
		t.Fatalf("released %v, want %s", origins.released, want)
	}
}

func TestDeleteApp_AFailedOriginReleaseKeepsTheRow(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.svc.SetCorsOriginReleaser(&recordingOriginReleaser{err: errors.New("platform db down")})
	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false); err == nil {
		t.Fatal("want the release failure")
	}
	if got, _ := f.apps.Get(f.app.ProjectID, f.app.ID); got == nil {
		t.Fatal("the row must stay so the deletion can be retried")
	}
}

func TestDeployApp_RefusedWhileTheAppIsBusy(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.deploys.createErr = fmt.Errorf("%w: it is PAUSING", apphost.ErrAppBusy)
	if _, err := f.svc.DeployApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); !errors.Is(err, apphost.ErrAppBusy) {
		t.Fatalf("err = %v, want ErrAppBusy", err)
	}
}

func TestLifecycle_ACancelledRequestStillFinishes(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.svc.PauseApp(ctx, f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("PauseApp: %v", err)
	}
	if f.status() != apphost.StatusStopped {
		t.Fatalf("status = %q", f.status())
	}
}

func TestPauseAndResume_NoNamespaceMeansNothingDeployed(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.svc.instances = fakestore.NewInstances()
	if _, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID); !errors.Is(err, k8s.ErrAppNotDeployed) {
		t.Fatalf("pause: err = %v", err)
	}
	g := newLifecycleFixture(t, apphost.StatusStopped)
	g.svc.instances = fakestore.NewInstances()
	if _, err := g.svc.ResumeApp(context.Background(), g.app.ProjectID, g.app.ID, "dev"); !errors.Is(err, k8s.ErrAppNotDeployed) {
		t.Fatalf("resume: err = %v", err)
	}
}

func TestSetOperationClaimer_IsTheLeaseEveryOperationTakes(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	shared := NewInProcessOperationClaimer()
	f.svc.SetOperationClaimer(shared)
	release, claimed, err := shared.Claim(context.Background(), appLeaseKey(f.app.ProjectID, f.app.ID), OperationPause)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()
	f.svc.lifecycleLeaseWait = 0
	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("err = %v, want ErrProjectOperationRunning", err)
	}
}

func TestDeployApp_QueuesBehindAnotherDeployBeingApplied(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	release, err := f.svc.holdApp(context.Background(), f.app.ProjectID, f.app.ID, OperationDeploy)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(3 * deployLeaseRetry)
		release()
	}()
	if _, err := f.svc.DeployApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatalf("a deploy must wait its turn, got %v", err)
	}
}

func TestDeployApp_GivesUpWhenTheCallerDoes(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	release, err := f.svc.holdApp(context.Background(), f.app.ProjectID, f.app.ID, OperationPause)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), deployLeaseRetry/2)
	defer cancel()
	if _, err := f.svc.DeployApp(ctx, f.app.ProjectID, f.app.ID, "dev"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
}

func TestDeployApp_ARenamedAppLeavesNothingUnderItsOldName(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	deploy, err := f.svc.DeployApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	want := "PruneAppWorkload:" + testDeployNamespace + "/" + f.app.ID + "!=" + f.app.Name
	if !slices.Contains(f.kube.Calls, want) {
		t.Fatalf("calls = %v, want %s", f.kube.Calls, want)
	}
	if got := f.deploys.getByID(deploy.ID).Status; got != apphost.DeployStatusSucceeded {
		t.Fatalf("status = %s", got)
	}
}

func TestDeployApp_AnOldNameThatWillNotGoFailsTheDeploy(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.kube.AppPruneErr = fmt.Errorf("%w after 3m: web-old-1", k8s.ErrAppPodsRemain)
	f.kube.AppAvailable[testDeployNamespace+"/"+k8s.AppObjectName(f.app.Name)] = 1
	deploy, err := f.svc.DeployApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	got := f.deploys.getByID(deploy.ID)
	if got.Status != apphost.DeployStatusFailed || !strings.Contains(got.FailureReason, "earlier name") {
		t.Fatalf("deploy = %s %q", got.Status, got.FailureReason)
	}
}

func TestAppLease_IsPerProject(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	release, err := f.svc.holdApp(context.Background(), "proj-intruder", f.app.ID, OperationDeploy)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("another project's claim on the same app id held the app: %v", err)
	}
}

// A watch that finishes after a newer deploy took over must not prune what the newer deploy made.
func TestFinishRollout_AStaleWatchPrunesNothing(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	stale := &apphost.Deploy{ID: "old", AppID: f.app.ID, ProjectID: f.app.ProjectID, Status: apphost.DeployStatusRolling}
	if err := f.deploys.Create(stale); err != nil {
		t.Fatal(err)
	}
	if err := f.deploys.Create(&apphost.Deploy{ID: "new", AppID: f.app.ID, ProjectID: f.app.ProjectID, Status: apphost.DeployStatusRolling}); err != nil {
		t.Fatal(err)
	}
	f.svc.finishRollout(context.Background(), stale, testDeployNamespace, "old-name")
	for _, call := range f.kube.Calls {
		if strings.HasPrefix(call, "PruneAppWorkload") {
			t.Fatalf("a superseded deploy pruned: %v", f.kube.Calls)
		}
	}
	if got := f.deploys.getByID("old").Status; got != apphost.DeployStatusSuperseded {
		t.Fatalf("status = %s", got)
	}
}

func TestFinishRollout_WaitsForTheLease(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.svc.deployLeaseWait = 0
	deploy := &apphost.Deploy{ID: "d1", AppID: f.app.ID, ProjectID: f.app.ProjectID, Status: apphost.DeployStatusRolling}
	if err := f.deploys.Create(deploy); err != nil {
		t.Fatal(err)
	}
	release, err := f.svc.holdApp(context.Background(), f.app.ProjectID, f.app.ID, OperationDeletion)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	f.svc.finishRollout(context.Background(), deploy, testDeployNamespace, f.app.Name)
	if got := f.deploys.getByID("d1").Status; got != apphost.DeployStatusRolling {
		t.Fatalf("without the lease the deploy must stay for the sweeper, got %s", got)
	}
}

func TestResumeRollouts_FollowsTheNameTheDeployRecorded(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	var watched string
	f.kube.AppRolloutFunc = func(_ context.Context, _, name, _ string, _ time.Duration) error {
		watched = name
		return nil
	}
	deploy := &apphost.Deploy{ID: "d1", AppID: f.app.ID, ProjectID: f.app.ProjectID, Status: apphost.DeployStatusRolling,
		Spec: apphost.DeploySpec{AppName: "before-rename"}}
	if err := f.deploys.Create(deploy); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if watched != k8s.AppObjectName("before-rename") {
		t.Fatalf("watched %q", watched)
	}
}

func TestResumeRollouts_ADeployWithoutItsNameFails(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	if err := f.deploys.Create(&apphost.Deploy{ID: "d1", AppID: f.app.ID, ProjectID: f.app.ProjectID, Status: apphost.DeployStatusRolling}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.deploys.getByID("d1"); got.Status != apphost.DeployStatusFailed || !strings.Contains(got.FailureReason, "name") {
		t.Fatalf("deploy = %+v", got)
	}
}
