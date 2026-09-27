package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const graceProject = "grace-db"

// stoppingPauser stands in for PauseService: it records the call and leaves
// the project PAUSED, as a real pause does.
type stoppingPauser struct {
	store  storage.InstanceStore
	calls  []string
	reason string
	err    error
}

func (p *stoppingPauser) Pause(_ context.Context, projectID, reason string) error {
	p.calls = append(p.calls, projectID)
	p.reason = reason
	if p.err != nil {
		return p.err
	}
	inst, err := p.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return errors.New("not found")
	}
	inst.Status = string(domain.StatusPaused)
	return p.store.Update(inst)
}

type graceHarness struct {
	svc      *ProvisioningService
	store    storage.InstanceStore
	pauser   *stoppingPauser
	retained *fakestore.RetainedBackups
	deleter  *fakeObjectDeleter
	now      time.Time
}

func newGraceHarness(t *testing.T) *graceHarness {
	t.Helper()
	svc, store, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock(graceProject, "org1-"+graceProject, 1)
	h := &graceHarness{
		svc:      svc,
		store:    store,
		pauser:   &stoppingPauser{store: store},
		retained: fakestore.NewRetainedBackups(),
		deleter:  newFakeObjectDeleter(graceProject + "/cloud/base/b1/data.tar.gz"),
		now:      time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC),
	}
	svc.SetBackupPurger(newTestPurger(h.deleter))
	svc.SetDeletionPauser(h.pauser)
	svc.SetRetainedBackupStore(h.retained)
	svc.SetDeletionClock(func() time.Time { return h.now })
	saveActiveProject(t, svc, graceProject)
	return h
}

func (h *graceHarness) row(t *testing.T) *domain.DatabaseInstance {
	t.Helper()
	inst, _ := h.store.FindByProjectID(graceProject)
	return inst
}

func TestScheduleDeletionStopsTheProjectAndKeepsItForTheGracePeriod(t *testing.T) {
	h := newGraceHarness(t)

	scheduled, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{})
	if err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	if scheduled == nil {
		t.Fatal("an ACTIVE project must be scheduled, not deleted")
	}
	if len(h.pauser.calls) != 1 || h.pauser.reason != PauseReasonDeletion {
		t.Fatalf("pause calls = %v reason %q, want one with %q", h.pauser.calls, h.pauser.reason, PauseReasonDeletion)
	}
	row := h.row(t)
	if row == nil || row.Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("row = %+v, want PENDING_DELETION", row)
	}
	if row.DeletionDueAt == nil || !row.DeletionDueAt.Equal(h.now.Add(DeletionGracePeriod)) {
		t.Fatalf("due = %v, want %v", row.DeletionDueAt, h.now.Add(DeletionGracePeriod))
	}
	if DeletionGracePeriod != 7*24*time.Hour {
		t.Fatalf("grace period = %v, the owner decided 7 days", DeletionGracePeriod)
	}
	if !domain.IsNotServable(row.Status) {
		t.Fatal("a project scheduled for deletion must not be served")
	}
}

func TestScheduleDeletionRefusesAProtectedProjectWithoutStoppingIt(t *testing.T) {
	h := newGraceHarness(t)
	row := h.row(t)
	on := true
	row.DeletionProtection = &on
	_ = h.store.Update(row)

	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); !errors.Is(err, ErrDeletionProtected) {
		t.Fatalf("got %v, want ErrDeletionProtected", err)
	}
	if len(h.pauser.calls) != 0 || h.row(t).Status != "ACTIVE" {
		t.Fatal("a protected project must be left running")
	}
}

func TestScheduleDeletionOfAFailedProjectDeletesItNow(t *testing.T) {
	h := newGraceHarness(t)
	row := h.row(t)
	row.Status = string(domain.StageFailed)
	_ = h.store.Update(row)

	scheduled, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{})
	if err != nil || scheduled != nil {
		t.Fatalf("got %v, %v: a FAILED project holds no data and is deleted at once", scheduled, err)
	}
	if h.row(t) != nil || len(h.pauser.calls) != 0 {
		t.Fatal("the FAILED project must be gone without a pause")
	}
}

func TestScheduleDeletionTwiceKeepsTheFirstDueDate(t *testing.T) {
	h := newGraceHarness(t)
	ctx := context.Background()
	if _, err := h.svc.ScheduleDeletion(ctx, graceProject, DeprovisionOptions{}); err != nil {
		t.Fatal(err)
	}
	first := *h.row(t).DeletionDueAt
	h.now = h.now.Add(48 * time.Hour)
	again, err := h.svc.ScheduleDeletion(ctx, graceProject, DeprovisionOptions{})
	if err != nil || again == nil || !again.DeletionDueAt.Equal(first) {
		t.Fatalf("second schedule: %v %v, want the first due date kept", again, err)
	}
	if len(h.pauser.calls) != 1 {
		t.Fatal("an already scheduled project is not paused again")
	}
}

func TestScheduleDeletionFailsWhenThePauseFails(t *testing.T) {
	h := newGraceHarness(t)
	h.pauser.err = errors.New("backup did not complete")

	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err == nil {
		t.Fatal("a project that could not be stopped must not be scheduled")
	}
	if h.row(t).Status == string(domain.StatusPendingDeletion) {
		t.Fatal("status must not claim a stopped project")
	}
}

func TestScheduleDeletionWithoutAPauserIsRefused(t *testing.T) {
	h := newGraceHarness(t)
	h.svc.SetDeletionPauser(nil)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); !errors.Is(err, ErrDeletionGraceUnavailable) {
		t.Fatalf("got %v, want ErrDeletionGraceUnavailable", err)
	}
	if h.row(t).Status != "ACTIVE" {
		t.Fatal("the project must be untouched")
	}
}

func TestCancelDeletionLeavesTheProjectPausedAndProtected(t *testing.T) {
	h := newGraceHarness(t)
	ctx := context.Background()
	if _, err := h.svc.ScheduleDeletion(ctx, graceProject, DeprovisionOptions{DeleteBackups: DeleteBackupsOption(true)}); err != nil {
		t.Fatal(err)
	}

	if err := h.svc.CancelDeletion(ctx, graceProject); err != nil {
		t.Fatalf("CancelDeletion: %v", err)
	}
	row := h.row(t)
	if row.Status != string(domain.StatusPaused) || row.DeletionDueAt != nil || row.DeletionScheduledAt != nil {
		t.Fatalf("row = %+v, want PAUSED with no schedule", row)
	}
	if row.DeletionProtection == nil || !*row.DeletionProtection || row.DeletionDeleteBackups {
		t.Fatal("a cancelled deletion turns protection back on and forgets the backup purge")
	}
	h.now = h.now.Add(DeletionGracePeriod + time.Hour)
	h.svc.RunDueDeletions(ctx)
	if h.row(t) == nil {
		t.Fatal("a cancelled project must survive the sweep")
	}
}

func TestCancelDeletionOfAProjectNotScheduledIsRefused(t *testing.T) {
	h := newGraceHarness(t)
	if err := h.svc.CancelDeletion(context.Background(), graceProject); !errors.Is(err, ErrNotScheduledForDeletion) {
		t.Fatalf("got %v, want ErrNotScheduledForDeletion", err)
	}
	if err := h.svc.CancelDeletion(context.Background(), "nope"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("unknown project: got %v, want ErrProjectNotFound", err)
	}
}

func TestSetDeletionProtectionIsRefusedWhileScheduled(t *testing.T) {
	h := newGraceHarness(t)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetDeletionProtection(graceProject, true); !errors.Is(err, ErrScheduledForDeletion) {
		t.Fatalf("got %v, want ErrScheduledForDeletion", err)
	}
}

func TestRunDueDeletionsHardDeletesOnlyAfterTheGracePeriod(t *testing.T) {
	h := newGraceHarness(t)
	ctx := context.Background()
	if _, err := h.svc.ScheduleDeletion(ctx, graceProject, DeprovisionOptions{}); err != nil {
		t.Fatal(err)
	}

	h.now = h.now.Add(DeletionGracePeriod - time.Minute)
	if report := h.svc.RunDueDeletions(ctx); len(report.Deleted) != 0 || h.row(t) == nil {
		t.Fatal("nothing is deleted before the grace period ends")
	}

	h.now = h.now.Add(2 * time.Minute)
	report := h.svc.RunDueDeletions(ctx)
	if len(report.Deleted) != 1 || h.row(t) != nil {
		t.Fatalf("report %+v: the project must be gone once due", report)
	}
	if len(h.deleter.remaining()) != 1 {
		t.Fatal("kept backups are not purged by the hard delete")
	}
	kept, ok := h.retained.Items[graceProject]
	if !ok {
		t.Fatal("the kept backups must be recorded with a purge date")
	}
	if !kept.PurgeAfter.Equal(h.now.Add(RetainedBackupPeriod)) || RetainedBackupPeriod != 14*24*time.Hour {
		t.Fatalf("purge after = %v, want deletion + 14 days", kept.PurgeAfter)
	}
}

func TestRunDueDeletionsHonoursTheRecordedBackupPurge(t *testing.T) {
	h := newGraceHarness(t)
	ctx := context.Background()
	if _, err := h.svc.ScheduleDeletion(ctx, graceProject, DeprovisionOptions{DeleteBackups: DeleteBackupsOption(true)}); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(DeletionGracePeriod + time.Minute)
	h.svc.RunDueDeletions(ctx)

	if h.row(t) != nil || len(h.deleter.remaining()) != 0 {
		t.Fatal("confirmDeleteBackups recorded at schedule time must purge at the hard delete")
	}
	if _, ok := h.retained.Items[graceProject]; ok {
		t.Fatal("purged backups leave nothing to retain")
	}
}

func TestImmediateDeletionAlsoRecordsKeptBackups(t *testing.T) {
	h := newGraceHarness(t)
	if err := h.svc.Deprovision(context.Background(), graceProject); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.retained.Items[graceProject]; !ok {
		t.Fatal("every deletion that keeps backups records when they are purged")
	}
}

func TestPurgeDueRetainedBackups(t *testing.T) {
	h := newGraceHarness(t)
	ctx := context.Background()
	if err := h.svc.Deprovision(ctx, graceProject); err != nil {
		t.Fatal(err)
	}

	h.now = h.now.Add(RetainedBackupPeriod - time.Minute)
	if report := h.svc.PurgeDueRetainedBackups(ctx); len(report.Purged) != 0 || len(h.deleter.remaining()) != 1 {
		t.Fatal("backups stay until their purge date")
	}

	h.now = h.now.Add(2 * time.Minute)
	h.deleter.deleteErr = errors.New("r2 unavailable")
	if report := h.svc.PurgeDueRetainedBackups(ctx); len(report.Failed) != 1 {
		t.Fatalf("report %+v: a failed purge is reported", report)
	}
	if _, ok := h.retained.Items[graceProject]; !ok {
		t.Fatal("a failed purge keeps its record for the next sweep")
	}

	h.deleter.deleteErr = nil
	if report := h.svc.PurgeDueRetainedBackups(ctx); len(report.Purged) != 1 {
		t.Fatalf("report %+v: the due purge must run", report)
	}
	if len(h.deleter.remaining()) != 0 {
		t.Fatal("the backups must be gone")
	}
	if _, ok := h.retained.Items[graceProject]; ok {
		t.Fatal("the record goes once its backups are purged")
	}
}

// A resuming project holds data: it is never deleted at once, and its
// schedule waits until it settles.
func TestScheduleDeletionOfAResumingProjectIsRefusedNotDeleted(t *testing.T) {
	h := newGraceHarness(t)
	row := h.row(t)
	row.Status = string(domain.StatusResuming)
	_ = h.store.Update(row)
	h.pauser.err = nil
	h.pauser.store = nil
	h.svc.SetDeletionPauser(noopPauser{})

	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); !errors.Is(err, storage.ErrProjectStatusChanged) {
		t.Fatalf("got %v, want ErrProjectStatusChanged", err)
	}
	if h.row(t) == nil || h.row(t).Status != string(domain.StatusResuming) {
		t.Fatal("a resuming project must be left alone")
	}
}

// noopPauser mirrors PauseService on a project it may not pause: nothing happens.
type noopPauser struct{}

func (noopPauser) Pause(context.Context, string, string) error { return nil }

// The sweep's list is read before each teardown takes the lease. A project
// cancelled (and even unprotected) in between is not due and must survive.
func TestSweepSkipsAProjectCancelledAfterItWasListed(t *testing.T) {
	h := newGraceHarness(t)
	ctx := context.Background()
	if _, err := h.svc.ScheduleDeletion(ctx, graceProject, DeprovisionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.CancelDeletion(ctx, graceProject); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetDeletionProtection(graceProject, false); err != nil {
		t.Fatal(err)
	}
	due := h.now.Add(DeletionGracePeriod + time.Hour)
	err := h.svc.DeprovisionWithOptions(ctx, graceProject, DeprovisionOptions{dueAt: &due})
	if !errors.Is(err, ErrDeletionNotDue) || h.row(t) == nil {
		t.Fatalf("got %v: a cancelled project must not be hard-deleted by a stale sweep", err)
	}
}

// A scheduled teardown that stopped at the backup purge is retried by the
// sweep; no owner is left to retry it.
func TestSweepRetriesAScheduledDeletionStuckAtTheBackupPurge(t *testing.T) {
	h := newGraceHarness(t)
	ctx := context.Background()
	if _, err := h.svc.ScheduleDeletion(ctx, graceProject, DeprovisionOptions{DeleteBackups: DeleteBackupsOption(true)}); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(DeletionGracePeriod + time.Minute)
	h.deleter.deleteErr = errors.New("r2 unavailable")
	h.svc.RunDueDeletions(ctx)
	if row := h.row(t); row == nil || row.Status != string(domain.StatusBackupsPendingDelete) {
		t.Fatalf("row = %+v, want BACKUPS_PENDING_DELETE", row)
	}
	h.deleter.deleteErr = nil
	if report := h.svc.RunDueDeletions(ctx); len(report.Deleted) != 1 || h.row(t) != nil {
		t.Fatalf("report %+v: the sweep must finish the teardown", report)
	}
}

type unsupportedPauser struct{}

func (unsupportedPauser) Pause(context.Context, string, string) error { return ErrPauseUnsupported }

func TestScheduleDeletionWithAnUnsupportedModeIsUnavailable(t *testing.T) {
	h := newGraceHarness(t)
	h.svc.SetDeletionPauser(unsupportedPauser{})
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); !errors.Is(err, ErrDeletionGraceUnavailable) {
		t.Fatalf("got %v, want ErrDeletionGraceUnavailable", err)
	}
}
