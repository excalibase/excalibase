package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// recordingStopper stands in for the app service: it records each stop and
// how many database pauses had run when it was called.
type recordingStopper struct {
	pauser          *stoppingPauser
	stops           []string
	pausesAtStop    []int
	restarts        []string
	stopErr         error
	restartErr      error
	stopErrAfterOne bool
}

func (r *recordingStopper) StopProjectWorkloads(_ context.Context, projectID string) error {
	r.stops = append(r.stops, projectID)
	r.pausesAtStop = append(r.pausesAtStop, len(r.pauser.calls))
	if r.stopErrAfterOne && len(r.stops) == 1 {
		return nil
	}
	return r.stopErr
}

func (r *recordingStopper) RestartFunctionRuntime(_ context.Context, projectID string) error {
	r.restarts = append(r.restarts, projectID)
	return r.restartErr
}

func newStoppingGrace(t *testing.T) (*graceHarness, *recordingStopper) {
	t.Helper()
	h := newGraceHarness(t)
	stopper := &recordingStopper{pauser: h.pauser}
	h.svc.SetProjectWorkloadStopper(stopper)
	return h, stopper
}

func TestScheduleDeletionStopsTheAppsBeforeTheDatabaseBackup(t *testing.T) {
	h, stopper := newStoppingGrace(t)

	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	if len(stopper.stops) == 0 || stopper.pausesAtStop[0] != 0 {
		t.Fatalf("stops %v at pauses %v, want the apps stopped before the database pause", stopper.stops, stopper.pausesAtStop)
	}
	if h.row(t).Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("status = %s, want PENDING_DELETION", h.row(t).Status)
	}
}

// An app deployed or resumed while the database was being backed up is
// stopped too: the stop runs again once the database is down.
func TestScheduleDeletionStopsTheAppsAgainWhenItMarksTheProject(t *testing.T) {
	h, stopper := newStoppingGrace(t)

	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	if len(stopper.stops) != 2 || stopper.pausesAtStop[1] != 1 {
		t.Fatalf("stops %v at pauses %v, want a second stop after the database pause", stopper.stops, stopper.pausesAtStop)
	}
}

func TestScheduleDeletionIsRefusedWhenTheAppsCannotBeStopped(t *testing.T) {
	h, stopper := newStoppingGrace(t)
	stopper.stopErr = errors.New("pods still running")

	_, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{})
	if !errors.Is(err, ErrDeletionStopFailed) {
		t.Fatalf("got %v, want ErrDeletionStopFailed", err)
	}
	if len(h.pauser.calls) != 0 || h.row(t).Status != "ACTIVE" {
		t.Fatalf("pauses %v status %s: nothing else may be stopped", h.pauser.calls, h.row(t).Status)
	}
}

// The database is already down when the second stop fails: the project stays
// PAUSED and unscheduled, and the DELETE is retried.
func TestScheduleDeletionIsNotMarkedWhenTheSecondStopFails(t *testing.T) {
	h, stopper := newStoppingGrace(t)
	stopper.stopErr = errors.New("pods still running")
	stopper.stopErrAfterOne = true

	_, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{})
	if !errors.Is(err, ErrDeletionStopFailed) {
		t.Fatalf("got %v, want ErrDeletionStopFailed", err)
	}
	if h.row(t).Status != string(domain.StatusPaused) {
		t.Fatalf("status = %s, want PAUSED and unscheduled", h.row(t).Status)
	}
}

func TestScheduleDeletionOfAProtectedProjectLeavesItsAppsRunning(t *testing.T) {
	h, stopper := newStoppingGrace(t)
	row := h.row(t)
	on := true
	row.DeletionProtection = &on
	_ = h.store.Update(row)

	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); !errors.Is(err, ErrDeletionProtected) {
		t.Fatalf("got %v, want ErrDeletionProtected", err)
	}
	if len(stopper.stops) != 0 {
		t.Fatalf("stops = %v, a refused deletion stops nothing", stopper.stops)
	}
}

func TestScheduleDeletionOfAProjectWithoutADatabaseStopsItsApps(t *testing.T) {
	h, stopper := newStoppingGrace(t)
	row := h.row(t)
	row.NoDatabase = true
	_ = h.store.Update(row)

	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	if len(stopper.stops) == 0 || h.row(t).Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("stops %v status %s, want the apps stopped and the project scheduled", stopper.stops, h.row(t).Status)
	}
}

func TestScheduleDeletionInBackgroundStopsTheAppsBeforeItAnswers(t *testing.T) {
	h := newBackgroundGrace(t)
	stopper := &recordingStopper{pauser: h.pauser}
	h.svc.SetProjectWorkloadStopper(stopper)

	if _, err := h.svc.ScheduleDeletionInBackground(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletionInBackground: %v", err)
	}
	if len(stopper.stops) != 1 {
		t.Fatalf("stops = %v, want the apps stopped in the answer", stopper.stops)
	}
	(*h.runs)[0]()
	if len(stopper.stops) != 2 || h.row(t).Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("stops %v status %s, want a second stop and PENDING_DELETION", stopper.stops, h.row(t).Status)
	}
}

func TestCancelDeletionRestartsTheFunctionRuntime(t *testing.T) {
	h, stopper := newStoppingGrace(t)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}

	if err := h.svc.CancelDeletion(context.Background(), graceProject); err != nil {
		t.Fatalf("CancelDeletion: %v", err)
	}
	if len(stopper.restarts) != 1 || stopper.restarts[0] != graceProject {
		t.Fatalf("restarts = %v, want the function runtime back", stopper.restarts)
	}
}

func TestCancelDeletionIsRefusedWhenTheFunctionRuntimeCannotRestart(t *testing.T) {
	h, stopper := newStoppingGrace(t)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	stopper.restartErr = errors.New("api server down")

	if err := h.svc.CancelDeletion(context.Background(), graceProject); err == nil {
		t.Fatal("a cancel whose function runtime did not restart must fail, so it can be retried")
	}
	if h.row(t).Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("status = %s, want still PENDING_DELETION for the retry", h.row(t).Status)
	}
}
