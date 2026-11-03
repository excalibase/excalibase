package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// The edge gives up on a request after a minute; a teardown waits out the
// namespace for longer. The caller leaving must not strand the project in
// DELETING with its namespace already gone.

// hangUpPurger is the caller hanging up mid-teardown: it cancels the request
// and fails the way a context-aware step does once it is cancelled.
type hangUpPurger struct{ cancel context.CancelFunc }

func (h *hangUpPurger) PurgeProjectObjects(ctx context.Context, _ string) (int, error) {
	h.cancel()
	return 0, ctx.Err()
}

func TestDeprovision_CallerHangingUpMidTeardownStillDeletesTheRow(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	svc.SetObjectPurger(&hangUpPurger{cancel: cancel})

	if err := svc.DeprovisionWithOptions(ctx, testDeletingProj, DeprovisionOptions{}); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Fatalf("row left behind as %s at %s", inst.Status, inst.DeletionStep)
	}
}

func TestDeprovisionInBackground_AnswersOnceDeletingIsRecordedThenDeletes(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	runs := &[]func(){}
	svc.SetBackgroundRunner(func(run func()) { *runs = append(*runs, run) })
	ctx, cancel := context.WithCancel(context.Background())

	if err := svc.DeprovisionInBackground(ctx, testDeletingProj, DeprovisionOptions{}); err != nil {
		t.Fatalf("DeprovisionInBackground: %v", err)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst == nil || inst.Status != string(domain.StatusDeleting) || len(*runs) != 1 {
		t.Fatalf("answer: row %+v with %d runs, want DELETING and the teardown started", inst, len(*runs))
	}
	cancel()
	(*runs)[0]()
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Fatalf("row left behind as %s at %s", inst.Status, inst.DeletionStep)
	}
}

// What would refuse the delete is still the caller's answer.
func TestDeprovisionInBackground_RefusalIsAnsweredAtOnce(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	runs := &[]func(){}
	svc.SetBackgroundRunner(func(run func()) { *runs = append(*runs, run) })
	inst, _ := store.FindByProjectID(testDeletingProj)
	on := true
	inst.DeletionProtection = &on
	_ = store.Update(inst)

	if err := svc.DeprovisionInBackground(context.Background(), testDeletingProj, DeprovisionOptions{}); err == nil || len(*runs) != 0 {
		t.Fatalf("protected project: err %v with %d runs, want a refusal and nothing started", err, len(*runs))
	}
}

// The lease is held from the answer to the end of the run.
func TestDeprovisionInBackground_HoldsTheProjectUntilTheRunEnds(t *testing.T) {
	svc, _, _ := setupDeletionTest(t)
	runs := &[]func(){}
	svc.SetBackgroundRunner(func(run func()) { *runs = append(*runs, run) })

	if err := svc.DeprovisionInBackground(context.Background(), testDeletingProj, DeprovisionOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, _ := svc.claimer().Claim(context.Background(), testDeletingProj, OperationPause); claimed {
		t.Fatal("the project was free while its teardown was still to run")
	}
	(*runs)[0]()
	release, claimed, _ := svc.claimer().Claim(context.Background(), testDeletingProj, OperationPause)
	if !claimed {
		t.Fatal("the lease outlived the teardown")
	}
	release()
}

// hangUpPauser cancels the request while the database is being stopped or
// started, failing the way a context-aware wait does.
type hangUpPauser struct {
	fakePauser
	cancel context.CancelFunc
}

func (h *hangUpPauser) Pause(ctx context.Context, _, _ string) error {
	h.cancel()
	return ctx.Err()
}

func (h *hangUpPauser) Resume(ctx context.Context, _, _ string) error {
	h.cancel()
	return ctx.Err()
}

func hangUpPauseService(t *testing.T, status string) (*PauseService, context.Context, func() string) {
	t.Helper()
	svc, store, _, _ := setupPauseTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	svc.pausers = map[domain.DeploymentMode]provisioner.Pauser{domain.ModeDocker: &hangUpPauser{cancel: cancel}}
	activeProject(t, store, status)
	return svc, ctx, func() string { return statusOf(store).Status }
}

func TestPause_CallerHangingUpStillPauses(t *testing.T) {
	svc, ctx, status := hangUpPauseService(t, "ACTIVE")
	if err := svc.Pause(ctx, "p1", domain.PauseReasonManual); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got := status(); got != string(domain.StatusPaused) {
		t.Fatalf("status %s, want PAUSED", got)
	}
}

func TestResume_CallerHangingUpStillResumes(t *testing.T) {
	svc, ctx, status := hangUpPauseService(t, "PAUSED")
	if err := svc.Resume(ctx, "p1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := status(); got != "ACTIVE" {
		t.Fatalf("status %s, want ACTIVE", got)
	}
}
