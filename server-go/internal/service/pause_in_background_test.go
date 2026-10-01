package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-473: a pause takes a backup first and a resume waits for the database,
// minutes either way, longer than a browser waits for one request. Started in
// the background, the operation is admitted and recorded (PAUSING, RESUMING)
// before the answer, keeps the project's lease until it ends, and leaves its
// outcome on the project: PAUSED or ACTIVE, or the failure named.

func backgroundPauses(svc *PauseService) *[]func() {
	runs := &[]func(){}
	svc.SetBackgroundRunner(func(run func()) { *runs = append(*runs, run) })
	return runs
}

func activeProject(t *testing.T, store *storage.FileSystemStore, status string) {
	t.Helper()
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: status,
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
	}); err != nil {
		t.Fatal(err)
	}
}

func statusOf(store *storage.FileSystemStore) *domain.DatabaseInstance {
	inst, _ := store.FindByProjectID("p1")
	return inst
}

func TestStartPause_AnswersOncePausingIsRecordedThenPauses(t *testing.T) {
	svc, store, pauser, _ := setupPauseTest(t)
	activeProject(t, store, "ACTIVE")
	runs := backgroundPauses(svc)
	var outcome error = errors.New("not called")

	started, err := svc.StartPause(context.Background(), "p1", domain.PauseReasonManual, func(err error) { outcome = err })
	if err != nil || !started {
		t.Fatalf("StartPause = %v, %v; want started", started, err)
	}
	if got := statusOf(store); got.Status != string(domain.StatusPausing) || pauser.pauseCalls != 0 {
		t.Fatalf("before the run: %s with %d stops, want PAUSING and nothing stopped yet", got.Status, pauser.pauseCalls)
	}
	(*runs)[0]()
	if got := statusOf(store); got.Status != string(domain.StatusPaused) || outcome != nil {
		t.Fatalf("after the run: %s, outcome %v; want PAUSED", got.Status, outcome)
	}
}

// The lease is held from the answer to the end of the run.
func TestStartPause_HoldsTheProjectUntilTheRunEnds(t *testing.T) {
	svc, store, _, _ := setupPauseTest(t)
	activeProject(t, store, "ACTIVE")
	runs := backgroundPauses(svc)
	if _, err := svc.StartPause(context.Background(), "p1", domain.PauseReasonManual, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartPause(context.Background(), "p1", domain.PauseReasonManual, nil); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("a second pause during the run: %v, want ErrProjectOperationRunning", err)
	}
	(*runs)[0]()
	if _, err := svc.StartResume(context.Background(), "p1", nil); err != nil {
		t.Fatalf("a resume after the run: %v", err)
	}
}

// A backup that does not complete leaves the project running, the reason on it.
func TestStartPause_AFailedBackupIsRecordedOnTheProject(t *testing.T) {
	svc, store, pauser, bk := setupPauseTest(t)
	activeProject(t, store, "ACTIVE")
	runs := backgroundPauses(svc)
	bk.err = errors.New("R2 unreachable")
	var outcome error

	if _, err := svc.StartPause(context.Background(), "p1", domain.PauseReasonManual, func(err error) { outcome = err }); err != nil {
		t.Fatal(err)
	}
	(*runs)[0]()
	got := statusOf(store)
	if got.Status != "ACTIVE" || got.FailureReason == "" || pauser.pauseCalls != 0 || !errors.Is(outcome, ErrPauseBackupNotCompleted) {
		t.Fatalf("after the run: %+v, outcome %v; want ACTIVE with the failure named", got, outcome)
	}
}

// What is decided before anything runs is answered at once.
func TestStartPause_RefusesOrSkipsWithoutStarting(t *testing.T) {
	svc, store, _, _ := setupPauseTest(t)
	runs := backgroundPauses(svc)
	if _, err := svc.StartPause(context.Background(), "p1", domain.PauseReasonManual, nil); err == nil {
		t.Fatal("an unknown project was accepted")
	}
	activeProject(t, store, string(domain.StatusPaused))
	started, err := svc.StartPause(context.Background(), "p1", domain.PauseReasonManual, nil)
	if err != nil || started {
		t.Fatalf("an already paused project: %v, %v; want nothing to do", started, err)
	}
	if len(*runs) != 0 {
		t.Fatalf("%d runs started", len(*runs))
	}
}

func TestStartResume_AnswersOnceResumingIsRecordedThenResumes(t *testing.T) {
	svc, store, pauser, _ := setupPauseTest(t)
	activeProject(t, store, string(domain.StatusPaused))
	runs := backgroundPauses(svc)
	ctx, cancel := context.WithCancel(context.Background())

	started, err := svc.StartResume(ctx, "p1", nil)
	if err != nil || !started {
		t.Fatalf("StartResume = %v, %v; want started", started, err)
	}
	cancel() // the caller leaving does not stop the resume
	if got := statusOf(store); got.Status != string(domain.StatusResuming) || pauser.resumeCalls != 0 {
		t.Fatalf("before the run: %s, want RESUMING", got.Status)
	}
	(*runs)[0]()
	if got := statusOf(store); got.Status != "ACTIVE" {
		t.Fatalf("after the run: %s, want ACTIVE", got.Status)
	}
}

func TestStartResume_AFailedStartIsRecordedOnTheProject(t *testing.T) {
	svc, store, pauser, _ := setupPauseTest(t)
	activeProject(t, store, string(domain.StatusPaused))
	runs := backgroundPauses(svc)
	pauser.resumeErr = errors.New("pods never ready")

	if _, err := svc.StartResume(context.Background(), "p1", nil); err != nil {
		t.Fatal(err)
	}
	(*runs)[0]()
	if got := statusOf(store); got.Status != string(domain.StatusResuming) || got.FailureReason == "" {
		t.Fatalf("after the run: %+v, want RESUMING with the failure named", got)
	}
}
