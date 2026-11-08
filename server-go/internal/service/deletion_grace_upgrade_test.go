package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A run left open lasts until its cause goes: the 7 days promised stand, and the delete is still the way out.
func leftOpen(t *testing.T, h *graceHarness) {
	t.Helper()
	row := h.row(t)
	row.Status = string(domain.StatusUpgrading)
	row.CurrentStep = domain.MajorUpgradeStepWaiting
	row.FailureReason = "the upgraded database has not started serving yet"
	if err := h.store.Update(row); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteOfAnOpenUpgradeKeepsTheGracePeriod(t *testing.T) {
	h := newGraceHarness(t)
	leftOpen(t, h)

	scheduled, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{})
	if err != nil {
		t.Fatalf("ScheduleDeletion: %v", err)
	}
	if scheduled == nil || scheduled.Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("scheduled = %+v, want the project kept PENDING_DELETION", scheduled)
	}
	if want := h.now.Add(DeletionGracePeriod).UTC(); scheduled.DeletionDueAt == nil || !scheduled.DeletionDueAt.Equal(want) {
		t.Fatalf("due %v, want %v", scheduled.DeletionDueAt, want)
	}
	if len(h.pauser.upgradeStops) != 1 || len(h.pauser.calls) != 0 {
		t.Fatalf("stops without a backup %v, pauses %v: an open run's database cannot take a backup", h.pauser.upgradeStops, h.pauser.calls)
	}
}

func TestDeleteOfAnOpenUpgradeInTheBackgroundKeepsTheGracePeriod(t *testing.T) {
	h := newGraceHarness(t)
	leftOpen(t, h)

	scheduled, err := h.svc.ScheduleDeletionInBackground(context.Background(), graceProject, DeprovisionOptions{})
	if err != nil {
		t.Fatalf("ScheduleDeletionInBackground: %v", err)
	}
	if scheduled == nil || scheduled.Status != string(domain.StatusPendingDeletion) {
		t.Fatalf("scheduled = %+v", scheduled)
	}
}

// The database is not known good: PAUSED would promise one to resume, so the run goes on as it was.
func TestCancellingTheDeletionOfAnOpenUpgradeReturnsToIt(t *testing.T) {
	h := newGraceHarness(t)
	leftOpen(t, h)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.CancelDeletion(context.Background(), graceProject); err != nil {
		t.Fatalf("CancelDeletion: %v", err)
	}
	row := h.row(t)
	if row.Status != string(domain.StatusUpgrading) || row.CurrentStep != domain.MajorUpgradeStepWaiting {
		t.Fatalf("after the cancel: %s %s", row.Status, row.CurrentStep)
	}
	if row.DeletionDueAt != nil {
		t.Fatalf("still due %v", row.DeletionDueAt)
	}
}

// An ordinary project's cancel is untouched, with nothing left over from a step.
func TestCancellingAnOrdinaryDeletionStaysPaused(t *testing.T) {
	h := newGraceHarness(t)
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.CancelDeletion(context.Background(), graceProject); err != nil {
		t.Fatal(err)
	}
	if row := h.row(t); row.Status != string(domain.StatusPaused) {
		t.Fatalf("status %s", row.Status)
	}
}

func TestAnOpenUpgradeThatCannotStopIsNotScheduled(t *testing.T) {
	h := newGraceHarness(t)
	leftOpen(t, h)
	h.pauser.err = errors.New("engine unreachable")
	_, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{})
	if !errors.Is(err, ErrDeletionStopFailed) {
		t.Fatalf("err = %v", err)
	}
	if row := h.row(t); row == nil || row.Status != string(domain.StatusUpgrading) {
		t.Fatalf("row = %+v", row)
	}
}

func TestAProtectedOpenUpgradeIsRefused(t *testing.T) {
	h := newGraceHarness(t)
	leftOpen(t, h)
	row := h.row(t)
	row.DeletionProtection = boolPtr(true)
	if err := h.store.Update(row); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ScheduleDeletion(context.Background(), graceProject, DeprovisionOptions{}); !errors.Is(err, ErrDeletionProtected) {
		t.Fatalf("err = %v", err)
	}
	if len(h.pauser.upgradeStops) != 0 {
		t.Fatalf("stopped a protected project: %v", h.pauser.upgradeStops)
	}
}

// The sweep leaves a scheduled project alone; it holds the lease only between checks.
func TestAScheduledOpenUpgradeIsNotRecovered(t *testing.T) {
	// A run lasts the 7 days with it, so a sweep a minute must not log each look.
	if err := admitRecovery(string(domain.StatusPendingDeletion)); !errors.Is(err, errRecoveryNotOwed) {
		t.Fatalf("admit = %v", err)
	}
}

// The deletion sweep gives up its turn on a held lease, and both sweeps tick from boot: a
// look under the lease every minute could keep a due project from ever being deleted.
func TestRecoveryLooksAtAScheduledProjectWithoutTheLease(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.servingErr = provisioner.ErrHostFirewallDown
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(majorUpgradeStaleAfter + time.Minute)
	w.runs.clock = func() time.Time { return now }
	row := projectRow(t, w)
	row.Status = string(domain.StatusPendingDeletion)
	if err := w.instances.Update(row); err != nil {
		t.Fatal(err)
	}
	w.holds = 0
	svc.RecoverInterrupted(context.Background())
	if w.holds != 0 {
		t.Fatalf("took the lease %d times", w.holds)
	}
	row.Status = string(domain.StatusUpgrading)
	if err := w.instances.Update(row); err != nil {
		t.Fatal(err)
	}
	svc.RecoverInterrupted(context.Background())
	if w.holds != 1 {
		t.Fatalf("control: an open run was not looked at, holds %d", w.holds)
	}
}

func TestStopOpenUpgradeStopsWithoutABackupAndLeavesTheStatus(t *testing.T) {
	svc, store, pauser, backups := setupPauseTest(t)
	inst := &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", Status: string(domain.StatusUpgrading),
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1"}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopOpenUpgrade(context.Background(), inst); err != nil {
		t.Fatalf("StopOpenUpgrade: %v", err)
	}
	if pauser.pauseCalls != 1 || backups.calls != 0 {
		t.Fatalf("stops %d backups %d", pauser.pauseCalls, backups.calls)
	}
	if got, _ := store.FindByProjectID("p1"); got.Status != string(domain.StatusUpgrading) {
		t.Fatalf("status %s", got.Status)
	}
	pauser.pauseErr = errors.New("engine unreachable")
	if err := svc.StopOpenUpgrade(context.Background(), inst); err == nil {
		t.Fatal("a failed stop reported none")
	}
}

var _ DeletionPauser = (*PauseService)(nil)

// Both single-host adapters must offer the wait, or a rollback goes in blind.
var (
	_ HostFirewallWaiter = (*DockerBackupAdapter)(nil)
	_ HostFirewallWaiter = (*HostMySQLBackupAdapter)(nil)
)
