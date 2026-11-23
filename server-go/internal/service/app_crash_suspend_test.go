package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func suspendedApp(t *testing.T, f *lifecycleFixture) {
	t.Helper()
	suspension, err := f.svc.SuspendForCrashLoop(context.Background(), f.app.ProjectID, f.app.ID, 11, time.Hour)
	if err != nil {
		t.Fatalf("SuspendForCrashLoop: %v", err)
	}
	if suspension.Kind != apphost.SuspensionCrashLoop || suspension.Restarts != 11 {
		t.Fatalf("suspension = %+v", suspension)
	}
}

// EXC-839: the stop scales the app to zero, records why and when on the app,
// and marks the workload, so Studio can say what happened.
func TestSuspendForCrashLoop_StopsTheAppAndRecordsWhy(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	suspendedApp(t, f)

	if f.status() != apphost.StatusStopped {
		t.Errorf("status = %s, want the app stopped", f.status())
	}
	if !f.kube.AppPaused[f.key()] {
		t.Error("the workload was not scaled to zero")
	}
	stored, _ := f.apps.Get(f.app.ProjectID, f.app.ID)
	if stored.Suspension == nil || stored.Suspension.Kind != "crash-loop" || stored.Suspension.Restarts != 11 ||
		stored.Suspension.WindowMinutes != 60 || stored.Suspension.At.IsZero() {
		t.Fatalf("suspension row = %+v", stored.Suspension)
	}
	if stored.Suspension.Reason != "kept crashing (11 restarts in the last hour)" {
		t.Errorf("reason = %q", stored.Suspension.Reason)
	}
	if f.kube.AppSuspended[f.key()] != "crash-loop" {
		t.Errorf("deployment annotation = %q, want crash-loop", f.kube.AppSuspended[f.key()])
	}
}

// Start again is the ordinary resume, and it clears the stop.
func TestResumeApp_StartAgainClearsTheCrashLoopStop(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	suspendedApp(t, f)

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	stored, _ := f.apps.Get(f.app.ProjectID, f.app.ID)
	if stored.Suspension != nil {
		t.Errorf("the stop survived Start again: %+v", stored.Suspension)
	}
	if f.status() != apphost.StatusRunning {
		t.Errorf("status = %s", f.status())
	}
}

// A new deploy starts the app, so it ends the stop too.
func TestDeployApp_ClearsTheCrashLoopStop(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	suspendedApp(t, f)

	if _, err := f.svc.DeployApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatal(err)
	}
	if stored, _ := f.apps.Get(f.app.ProjectID, f.app.ID); stored.Suspension != nil {
		t.Errorf("a deploy left the stop in place: %+v", stored.Suspension)
	}
}

// A stop that could not be completed is an error and records nothing.
func TestSuspendForCrashLoop_APauseThatFailsRecordsNothing(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.kube.AppPauseErr = context.DeadlineExceeded
	if _, err := f.svc.SuspendForCrashLoop(context.Background(), f.app.ProjectID, f.app.ID, 11, time.Hour); err == nil {
		t.Fatal("want the pause failure")
	}
	if stored, _ := f.apps.Get(f.app.ProjectID, f.app.ID); stored.Suspension != nil {
		t.Errorf("suspension recorded for an app that was not stopped: %+v", stored.Suspension)
	}
}
