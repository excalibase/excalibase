package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func TestStopProjectWorkloads_TakesTheRoutesAwayAndStopsEveryRunningApp(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	stopped := sampleDeployAppWithID("app-stopped")
	stopped.Status = apphost.StatusStopped
	created := sampleDeployAppWithID("app-created")
	created.Status = apphost.StatusCreated
	f.apps.apps[stopped.ProjectID+"/"+stopped.ID] = stopped
	f.apps.apps[created.ProjectID+"/"+created.ID] = created

	if err := f.svc.StopProjectWorkloads(context.Background(), f.app.ProjectID); err != nil {
		t.Fatalf("StopProjectWorkloads: %v", err)
	}
	if !slices.Equal(f.kube.WithdrawnWorkloads, []string{testDeployNamespace}) {
		t.Fatalf("withdrawn = %v, want the project's namespace", f.kube.WithdrawnWorkloads)
	}
	if f.status() != apphost.StatusStopped || !f.kube.AppPaused[f.key()] {
		t.Fatalf("running app: status %s paused %t, want STOPPED with no pods", f.status(), f.kube.AppPaused[f.key()])
	}
	if got := f.apps.statusOf(created.ProjectID, created.ID); got != apphost.StatusCreated {
		t.Fatalf("an app never deployed keeps its status, got %s", got)
	}
	withdrawAt := slices.Index(f.kube.Calls, "WithdrawProjectWorkloads:"+testDeployNamespace)
	pauseAt := slices.Index(f.kube.Calls, "PauseAppWorkload:"+f.key())
	if withdrawAt < 0 || pauseAt < withdrawAt {
		t.Fatalf("calls %v: the routes must go before the apps are waited on", f.kube.Calls)
	}
}

func TestStopProjectWorkloads_FailsWhenAnAppKeepsRunning(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.kube.AppPodsGoneErr = k8s.ErrAppPodsRemain

	err := f.svc.StopProjectWorkloads(context.Background(), f.app.ProjectID)
	if !errors.Is(err, k8s.ErrAppPodsRemain) {
		t.Fatalf("got %v, want ErrAppPodsRemain", err)
	}
}

func TestStopProjectWorkloads_FailsWhenTheRoutesCannotBeWithdrawn(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.kube.WithdrawErr = errors.New("api server down")

	if err := f.svc.StopProjectWorkloads(context.Background(), f.app.ProjectID); err == nil {
		t.Fatal("a failed withdrawal must fail the stop")
	}
	if f.status() != apphost.StatusRunning {
		t.Fatalf("status = %s, nothing else may be touched", f.status())
	}
}

func TestStopProjectWorkloads_WithoutANamespaceHasNothingToStop(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.svc.instances.(*fakestore.Instances).Items[f.app.ProjectID].Namespace = ""

	if err := f.svc.StopProjectWorkloads(context.Background(), f.app.ProjectID); err != nil {
		t.Fatalf("StopProjectWorkloads: %v", err)
	}
	if len(f.kube.WithdrawnWorkloads) != 0 {
		t.Fatalf("withdrawn = %v, want nothing", f.kube.WithdrawnWorkloads)
	}
}

func TestRestartFunctionRuntime_BringsTheProjectsRuntimeBack(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)

	if err := f.svc.RestartFunctionRuntime(context.Background(), f.app.ProjectID); err != nil {
		t.Fatalf("RestartFunctionRuntime: %v", err)
	}
	if !slices.Equal(f.kube.RestartedRuntimes, []string{testDeployNamespace}) {
		t.Fatalf("restarted = %v, want the project's namespace", f.kube.RestartedRuntimes)
	}
}

// A project deletion took the app's routes away; resuming it after the
// deletion was cancelled must serve it again at its URL and custom domains.
func TestResumeApp_PutsTheAppsRoutesBack(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	var synced []string
	f.svc.SetDomainSync(func(_ context.Context, namespace string, app *apphost.App) error {
		synced = append(synced, namespace+"/"+app.ID)
		return nil
	})

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev-1"); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if f.kube.RestoredRoutes[f.key()] == nil {
		t.Fatalf("restored routes = %v, want the app's route", f.kube.RestoredRoutes)
	}
	if !slices.Equal(synced, []string{f.key()}) {
		t.Fatalf("domain syncs = %v, want the app's custom domains", synced)
	}
	resumeAt := slices.Index(f.kube.Calls, "ResumeAppWorkload:"+f.key())
	routeAt := slices.Index(f.kube.Calls, "RestoreAppRoute:"+f.key())
	if resumeAt < 0 || routeAt < resumeAt {
		t.Fatalf("calls %v: the route comes back once the pods are ready", f.kube.Calls)
	}
}

func TestResumeApp_StaysResumingWhenTheRouteCannotBeRestored(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.kube.RestoreRouteErr = errors.New("api server down")

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev-1"); err == nil {
		t.Fatal("a resume without its route must fail")
	}
	if f.status() != apphost.StatusResuming {
		t.Fatalf("status = %s, want RESUMING for a retry", f.status())
	}
}
