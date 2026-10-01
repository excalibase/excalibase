package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-473: DELETE of a project holding data pauses it first, backup included,
// minutes before it is PENDING_DELETION. Started in the background, the
// answer comes once the pause is recorded, and the project carries on to
// PENDING_DELETION or names why it was not stopped.

type backgroundGrace struct {
	*graceHarness
	pause  *PauseService
	backup *fakeBackupTrigger
	runs   *[]func()
}

func newBackgroundGrace(t *testing.T) *backgroundGrace {
	t.Helper()
	h := newGraceHarness(t)
	backup := &fakeBackupTrigger{}
	pause := NewPauseService(PauseServiceConfig{
		Instances: h.store,
		Pausers:   map[domain.DeploymentMode]provisioner.Pauser{domain.ModeK8s: &fakePauser{}},
		Backups:   backup,
		Claimer:   h.svc.claimer(),
	})
	h.svc.SetDeletionPauser(pause)
	return &backgroundGrace{graceHarness: h, pause: pause, backup: backup, runs: backgroundPauses(pause)}
}

func TestScheduleDeletionInBackground_AnswersOnceTheStopIsRecordedThenSchedulesIt(t *testing.T) {
	h := newBackgroundGrace(t)
	ctx, cancel := context.WithCancel(context.Background())

	accepted, err := h.svc.ScheduleDeletionInBackground(ctx, graceProject, DeprovisionOptions{})
	if err != nil {
		t.Fatalf("ScheduleDeletionInBackground: %v", err)
	}
	if accepted == nil || accepted.Status != string(domain.StatusPausing) || len(*h.runs) != 1 {
		t.Fatalf("answer %+v with %d runs, want PAUSING and the stop started", accepted, len(*h.runs))
	}
	cancel() // the caller leaving does not stop the deletion
	(*h.runs)[0]()
	row := h.row(t)
	if row.Status != string(domain.StatusPendingDeletion) || row.DeletionDueAt == nil ||
		!row.DeletionDueAt.Equal(h.now.Add(DeletionGracePeriod)) || row.PauseReason != PauseReasonDeletion {
		t.Fatalf("after the run: %+v, want PENDING_DELETION due in 7 days", row)
	}
}

// A stop that does not complete schedules nothing, and says why on the project.
func TestScheduleDeletionInBackground_AFailedStopSchedulesNothing(t *testing.T) {
	h := newBackgroundGrace(t)
	h.backup.err = errors.New("R2 unreachable")

	if _, err := h.svc.ScheduleDeletionInBackground(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatal(err)
	}
	(*h.runs)[0]()
	row := h.row(t)
	if row.Status != "ACTIVE" || row.FailureReason == "" || row.DeletionDueAt != nil {
		t.Fatalf("after the run: %+v, want ACTIVE, not scheduled, the failure named", row)
	}
}

// What needs no stop is answered at once: a refusal, or a project that is
// already stopped, which is scheduled in the answer.
func TestScheduleDeletionInBackground_AnswersAtOnceWhenNothingNeedsStopping(t *testing.T) {
	h := newBackgroundGrace(t)
	row := h.row(t)
	on := true
	row.DeletionProtection = &on
	_ = h.store.Update(row)
	if _, err := h.svc.ScheduleDeletionInBackground(context.Background(), graceProject, DeprovisionOptions{}); !errors.Is(err, ErrDeletionProtected) {
		t.Fatalf("a protected project: %v, want ErrDeletionProtected", err)
	}

	row = h.row(t)
	off := false
	row.DeletionProtection = &off
	row.Status = string(domain.StatusPaused)
	_ = h.store.Update(row)
	scheduled, err := h.svc.ScheduleDeletionInBackground(context.Background(), graceProject, DeprovisionOptions{})
	if err != nil || scheduled == nil || scheduled.Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("a paused project: %+v, %v; want it scheduled in the answer", scheduled, err)
	}
	if len(*h.runs) != 0 {
		t.Fatalf("%d runs started", len(*h.runs))
	}
}
