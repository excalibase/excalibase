package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-523: a pause, resume or deletion may queue up to three minutes behind
// the app's lease and then wait for its pods, longer than a browser waits for
// one request. Started in the background, it answers with the app as it is,
// and its outcome is on the app: its status, or the failure it recorded.

func captureLifecycleRuns(f *lifecycleFixture) *[]func() {
	runs := &[]func(){}
	f.svc.async = func(run func()) { *runs = append(*runs, run) }
	return runs
}

func (f *lifecycleFixture) stored() *apphost.App {
	app, _ := f.apps.Get(f.app.ProjectID, f.app.ID)
	return app
}

func TestPauseAppInBackground_AnswersWithTheAppThenPausesIt(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	runs := captureLifecycleRuns(f)

	app, err := f.svc.PauseAppInBackground(context.Background(), f.app.ProjectID, f.app.ID)
	if err != nil {
		t.Fatalf("pause in background: %v", err)
	}
	if app.ID != f.app.ID || app.Status != apphost.StatusRunning || len(*runs) != 1 {
		t.Fatalf("answer %+v with %d runs, want the running app and one pause started", app, len(*runs))
	}
	(*runs)[0]()
	if got := f.stored(); got.Status != apphost.StatusStopped || got.LifecycleFailure != nil {
		t.Fatalf("after the pause: %+v, want PAUSED and no failure", got)
	}
}

// The case PR 183 queued for: a lease held past the wait. The app's status
// never moved, so the refusal is what tells the caller the pause did not happen.
func TestPauseAppInBackground_ALeaseHeldPastTheWaitIsRecordedOnTheApp(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	runs := captureLifecycleRuns(f)
	f.svc.lifecycleLeaseWait = 0
	release, err := f.svc.holdApp(context.Background(), f.app.ProjectID, f.app.ID, OperationAppDiskUsage)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.PauseAppInBackground(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("pause in background: %v", err)
	}
	(*runs)[0]()
	release()
	got := f.stored()
	if got.Status != apphost.StatusRunning || got.LifecycleFailure == nil {
		t.Fatalf("after the refused pause: %+v, want still running with the failure recorded", got)
	}
	if got.LifecycleFailure.Operation != string(OperationPause) ||
		!strings.Contains(got.LifecycleFailure.Reason, "another operation") || got.LifecycleFailure.At.IsZero() {
		t.Fatalf("failure %+v, want the pause named, why, and when", got.LifecycleFailure)
	}

	// The next lifecycle operation that succeeds clears it.
	if _, err := f.svc.PauseAppInBackground(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("pause again: %v", err)
	}
	(*runs)[1]()
	if got := f.stored(); got.Status != apphost.StatusStopped || got.LifecycleFailure != nil {
		t.Fatalf("after the retried pause: %+v, want PAUSED and the failure cleared", got)
	}
}

// A cause the platform did not name is recorded in general words; its detail
// stays in the server log.
func TestPauseAppInBackground_AnUnnamedCauseIsRecordedWithoutItsDetail(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	runs := captureLifecycleRuns(f)
	f.kube.AppPauseErr = errors.New("dial tcp 10.43.0.1:443: internal detail")

	if _, err := f.svc.PauseAppInBackground(context.Background(), f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("pause in background: %v", err)
	}
	(*runs)[0]()
	failure := f.stored().LifecycleFailure
	if failure == nil || strings.Contains(failure.Reason, "10.43") || !strings.Contains(failure.Reason, "pause did not complete") {
		t.Fatalf("failure %+v, want the pause named in general words", failure)
	}
}

func TestResumeAppInBackground_AFailedRolloutIsOnTheApp(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	runs := captureLifecycleRuns(f)
	f.kube.AppResumeErr = k8s.ErrAppRollout

	app, err := f.svc.ResumeAppInBackground(context.Background(), f.app.ProjectID, f.app.ID, "dev")
	if err != nil || app.Status != apphost.StatusStopped {
		t.Fatalf("answer %+v, %v; want the paused app", app, err)
	}
	(*runs)[0]()
	got := f.stored()
	if got.Status != apphost.StatusFailed || got.LifecycleFailure == nil || got.LifecycleFailure.Operation != string(OperationResume) {
		t.Fatalf("after the resume: %+v, want FAILED with the resume's failure", got)
	}
}

func TestDeleteAppInBackground_ForgetsTheAppOnceItIsGone(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	runs := captureLifecycleRuns(f)

	if _, err := f.svc.DeleteAppInBackground(context.Background(), f.app.ProjectID, f.app.ID, false); err != nil {
		t.Fatalf("delete in background: %v", err)
	}
	(*runs)[0]()
	if got := f.stored(); got != nil {
		t.Fatalf("after the deletion: %+v, want the app gone", got)
	}
}

// What needs no lease is refused in the answer, and starts nothing.
func TestLifecycleInBackground_RefusesWhatItCanWithoutStarting(t *testing.T) {
	f := diskLifecycleFixture(t)
	runs := captureLifecycleRuns(f)

	if _, err := f.svc.DeleteAppInBackground(context.Background(), f.app.ProjectID, f.app.ID, false); !errors.Is(err, ErrAppDiskDeleteUnconfirmed) {
		t.Fatalf("an unconfirmed disk: got %v, want ErrAppDiskDeleteUnconfirmed", err)
	}
	if _, err := f.svc.PauseAppInBackground(context.Background(), f.app.ProjectID, "app-none"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("an unknown app: got %v, want ErrAppNotFound", err)
	}
	if _, err := f.svc.ResumeAppInBackground(context.Background(), f.app.ProjectID, "app-none", "dev"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("an unknown app: got %v, want ErrAppNotFound", err)
	}
	if len(*runs) != 0 {
		t.Fatalf("refusals started %d runs", len(*runs))
	}
}

// The caller going away does not stop the operation.
func TestPauseAppInBackground_OutlivesTheRequest(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusRunning)
	runs := captureLifecycleRuns(f)
	ctx, cancel := context.WithCancel(context.Background())

	if _, err := f.svc.PauseAppInBackground(ctx, f.app.ProjectID, f.app.ID); err != nil {
		t.Fatalf("pause in background: %v", err)
	}
	cancel()
	(*runs)[0]()
	if got := f.stored(); got.Status != apphost.StatusStopped {
		t.Fatalf("after the caller left: %s, want PAUSED", got.Status)
	}
}
