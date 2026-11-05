package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// endRunElsewhere ends the project's running run as recovery would have,
// and sets the project to status.
func endRunElsewhere(t *testing.T, w *upgradeWorld, outcome string, converted bool, status domain.ProvisioningStage) {
	t.Helper()
	w.runs.mu.Lock()
	for _, run := range w.runs.runs {
		if run.Status == domain.MajorUpgradeRunning {
			run.Status, run.Converted, run.Step, run.Owner = outcome, converted, "ended-elsewhere", "recovery"
		}
	}
	w.runs.mu.Unlock()
	row := projectRow(t, w)
	row.Status = string(status)
	if err := w.instances.Update(row); err != nil {
		t.Fatal(err)
	}
	w.record("ended-elsewhere")
}

func callsAfter(calls []string, marker string) []string {
	for i, call := range calls {
		if call == marker {
			return calls[i+1:]
		}
	}
	return nil
}

// Recovery took over and completed the run: the driver that outlived it
// must not finish it a second time.
func TestMajorUpgrade_ADriverDoesNotFinishARunRecoveryCompleted(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.midConvert = func() { endRunElsewhere(t, w, domain.MajorUpgradeCompleted, true, domain.StatusActive) }
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
		t.Fatal(err)
	}
	if after := callsAfter(w.sequence(), "ended-elsewhere"); len(after) != 0 {
		t.Fatalf("the driver went on after recovery completed the run: %v", after)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeCompleted || latest.Step != "ended-elsewhere" {
		t.Fatalf("run = %+v", latest)
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) {
		t.Fatalf("project = %s", inst.Status)
	}
}

// Recovery ended the run without converting it: the driver must leave the
// project as recovery left it, never UPGRADING with no run to recover.
func TestMajorUpgrade_ADriverLeavesARunRecoveryEndedAlone(t *testing.T) {
	for _, outcome := range []string{domain.MajorUpgradeFailed, domain.MajorUpgradeRolledBack} {
		t.Run(outcome, func(t *testing.T) {
			w, svc := newUpgradeWorld(t, upgradeProject())
			w.midConvert = func() { endRunElsewhere(t, w, outcome, false, domain.StatusActive) }
			if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
				t.Fatal(err)
			}
			if after := callsAfter(w.sequence(), "ended-elsewhere"); len(after) != 0 {
				t.Fatalf("the driver went on after recovery ended the run: %v", after)
			}
			latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
			if latest.Status != outcome || latest.Step != "ended-elsewhere" {
				t.Fatalf("run = %+v", latest)
			}
			if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) {
				t.Fatalf("project = %s, want it left as recovery set it", inst.Status)
			}
		})
	}
}

// A run left open whose record cannot be written is not reported as
// waiting: the project is only set UPGRADING while its run is RUNNING.
func TestMajorUpgrade_LeaveOpenNeverSetsUpgradingWithoutARunningRun(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.outcome = UpgradeOutcome{Unsettled: true, Public: "still converting"}
	w.midConvert = func() { endRunElsewhere(t, w, domain.MajorUpgradeFailed, false, domain.StageFailed) }
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
		t.Fatal(err)
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StageFailed) {
		t.Fatalf("project = %s, want it left as recovery set it", inst.Status)
	}
}

// The waiting step and the project's status are one write: when it fails,
// neither changes.
func TestMajorUpgrade_LeaveOpenWritesTheRunAndTheProjectTogether(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.outcome = UpgradeOutcome{Unsettled: true, Public: "still converting"}
	w.runs.waitingErr = errors.New("platform db blip")
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
		t.Fatal(err)
	}
	inst := projectRow(t, w)
	if inst.Status != string(domain.StatusUpgrading) || inst.CurrentStep != domain.MajorUpgradeStepConverting {
		t.Fatalf("project = %s at %s", inst.Status, inst.CurrentStep)
	}
}

// Recovery ended the run while the driver was still taking the backup: the
// driver must not go on to convert, nor touch the project's status.
func TestMajorUpgrade_ADriverWhoseRunRecoveryEndedDuringTheBackupChangesNothing(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.midBackup = func() { endRunElsewhere(t, w, domain.MajorUpgradeRolledBack, false, domain.StatusActive) }
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
		t.Fatal(err)
	}
	if after := callsAfter(w.sequence(), "ended-elsewhere"); len(after) != 0 {
		t.Fatalf("the driver went on after recovery ended the run: %v", after)
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) {
		t.Fatalf("project = %s, want it left as recovery set it", inst.Status)
	}
}

// A driver that outlived its run never writes the project's status.
func TestMajorUpgrade_StatusWritesNeedTheRunStillRunning(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	run := &domain.MajorUpgrade{ID: "mu-x", ProjectID: "proj-up", FromVersion: "16", ToVersion: "17"}
	if err := w.runs.StartMajorUpgrade(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	endRunElsewhere(t, w, domain.MajorUpgradeRolledBack, false, domain.StatusActive)
	svc.progressStep(context.Background(), run, domain.MajorUpgradeStepConverting)
	svc.end(context.Background(), run, domain.MajorUpgradeFailed, "late", domain.StageFailed, "late")
	if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) || inst.FailureReason != "" {
		t.Fatalf("project = %s (%q), want it left as recovery set it", inst.Status, inst.FailureReason)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeRolledBack {
		t.Fatalf("run = %+v", latest)
	}
}

// Recovery claimed the run and recorded the conversion while the driver
// converted: the driver's mark is refused and it stops, touching nothing.
func TestMajorUpgrade_OnlyTheActorThatRecordedTheConversionFinishesIt(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.midConvert = func() {
		w.runs.setRun("mu-test", func(run *domain.MajorUpgrade) {
			at := time.Now()
			run.Owner, run.Converted, run.ConvertedAt = "recovery", true, &at
		})
		w.record("converted-elsewhere")
	}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
		t.Fatal(err)
	}
	if after := callsAfter(w.sequence(), "converted-elsewhere"); len(after) != 0 {
		t.Fatalf("the driver finished a conversion another actor recorded: %v", after)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeRunning || latest.Step == domain.MajorUpgradeStepWaiting {
		t.Fatalf("run = %+v", latest)
	}
}

// staleRun makes the project's run look abandoned to recovery.
func staleRun(w *upgradeWorld, id string) {
	w.runs.setRun(id, func(run *domain.MajorUpgrade) { run.HeartbeatAt = time.Now().Add(-time.Hour) })
}

func timesCalled(calls []string, name string) int {
	n := 0
	for _, call := range calls {
		if call == name {
			n++
		}
	}
	return n
}

// (b) The driver records the conversion after recovery listed the run but
// before it claimed it: the claim fails, and nothing is put back.
func TestMajorUpgrade_RecoveryActsOnlyOnTheRunItClaimed(t *testing.T) {
	project := upgradeProject()
	project.Status = string(domain.StatusUpgrading)
	w, svc := newUpgradeWorld(t, project)
	run := &domain.MajorUpgrade{ID: "live", ProjectID: "proj-up", FromVersion: "16", ToVersion: "17", Owner: "driver",
		StartedAt: time.Now().Add(-time.Hour)}
	if err := w.runs.StartMajorUpgrade(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	w.runs.beforeClaim = func() {
		if err := w.runs.MarkMajorUpgradeConverted(context.Background(), "live", "proj-up", "17", "driver", time.Now()); err != nil {
			t.Error(err)
		}
	}
	svc.RecoverInterrupted(context.Background())
	if calls := strings.Join(w.sequence(), ","); strings.Contains(calls, "recover:") || strings.Contains(calls, "wait-serving") {
		t.Fatalf("recovery acted on a run it did not claim: %s", calls)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeRunning || latest.Owner != "driver" || !latest.Converted {
		t.Fatalf("run = %+v", latest)
	}
}

// (b) A claim that lands on a run converted since the list finishes it
// forward: the listed copy said unconverted, but nothing is put back.
func TestMajorUpgrade_RecoveryFinishesARunConvertedBeforeItsClaim(t *testing.T) {
	project := upgradeProject()
	project.Status = string(domain.StatusUpgrading)
	w, svc := newUpgradeWorld(t, project)
	run := &domain.MajorUpgrade{ID: "live", ProjectID: "proj-up", FromVersion: "16", ToVersion: "17", Owner: "driver",
		StartedAt: time.Now().Add(-time.Hour)}
	if err := w.runs.StartMajorUpgrade(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	w.runs.beforeClaim = func() {
		if err := w.runs.MarkMajorUpgradeConverted(context.Background(), "live", "proj-up", "17", "driver", time.Now().Add(-time.Hour)); err != nil {
			t.Error(err)
		}
		staleRun(w, "live")
	}
	svc.RecoverInterrupted(context.Background())
	calls := strings.Join(w.sequence(), ",")
	if strings.Contains(calls, "recover:") || !strings.Contains(calls, "wait-serving") {
		t.Fatalf("calls = %s", calls)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeCompleted {
		t.Fatalf("run = %+v", latest)
	}
	if inst := projectRow(t, w); inst.PostgresVersion != "17" || inst.Status != string(domain.StatusActive) {
		t.Fatalf("project = %s on %s", inst.Status, inst.PostgresVersion)
	}
}

// (c) Recovery takes over a converted run while its driver finishes it:
// only one of them runs the finishing steps.
func TestMajorUpgrade_OnlyOneActorFinishesAConvertedRun(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.midServing = func() {
		staleRun(w, "mu-test")
		svc.RecoverInterrupted(context.Background())
	}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
		t.Fatal(err)
	}
	calls := w.sequence()
	for _, step := range []string{"reapply-credentials", "announce", "after-steps", "cleanup"} {
		if n := timesCalled(calls, step); n != 1 {
			t.Fatalf("%s ran %d times: %v", step, n, calls)
		}
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeCompleted {
		t.Fatalf("run = %+v", latest)
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) {
		t.Fatalf("project = %s", inst.Status)
	}
}

// A converted run never ends as failed or rolled back: the data is on the
// new major whatever the driver thinks.
func TestMajorUpgrade_AConvertedRunIsNeverEndedAsFailedOrRolledBack(t *testing.T) {
	for _, outcome := range []string{domain.MajorUpgradeFailed, domain.MajorUpgradeRolledBack} {
		t.Run(outcome, func(t *testing.T) {
			project := upgradeProject()
			project.Status = string(domain.StatusUpgrading)
			w, svc := newUpgradeWorld(t, project)
			run := &domain.MajorUpgrade{ID: "conv", ProjectID: "proj-up", FromVersion: "16", ToVersion: "17", Owner: "me"}
			if err := w.runs.StartMajorUpgrade(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if err := w.runs.MarkMajorUpgradeConverted(context.Background(), "conv", "proj-up", "17", "me", time.Now()); err != nil {
				t.Fatal(err)
			}
			svc.end(context.Background(), run, outcome, "late", domain.StageFailed, "late")
			latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
			if latest.Status != domain.MajorUpgradeRunning || !latest.Converted {
				t.Fatalf("run = %+v", latest)
			}
			if inst := projectRow(t, w); inst.Status != string(domain.StatusUpgrading) {
				t.Fatalf("project = %s", inst.Status)
			}
		})
	}
}

// A driver whose run another actor claimed writes neither the run nor the
// project, though the run is still RUNNING.
func TestMajorUpgrade_AClaimedRunRefusesItsFormerDriver(t *testing.T) {
	project := upgradeProject()
	project.Status = string(domain.StatusUpgrading)
	w, svc := newUpgradeWorld(t, project)
	run := &domain.MajorUpgrade{ID: "mu-x", ProjectID: "proj-up", FromVersion: "16", ToVersion: "17", Owner: "driver"}
	if err := w.runs.StartMajorUpgrade(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	w.runs.setRun("mu-x", func(stored *domain.MajorUpgrade) { stored.Owner = "recovery" })
	if err := svc.progressStep(context.Background(), run, domain.MajorUpgradeStepFinishing); !errors.Is(err, errRunTakenOver) {
		t.Fatalf("step = %v, want errRunTakenOver", err)
	}
	svc.end(context.Background(), run, domain.MajorUpgradeRolledBack, "late", domain.StatusActive, "")
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeRunning || latest.Step == domain.MajorUpgradeStepFinishing {
		t.Fatalf("run = %+v", latest)
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusUpgrading) {
		t.Fatalf("project = %s", inst.Status)
	}
}

// A failed ownership read in conclude is tried once more before the
// outcome is dropped.
func TestMajorUpgrade_ConcludeRetriesTheOwnershipReadOnce(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.outcome = UpgradeOutcome{RolledBack: true}
	w.midConvert = func() { w.runs.latestErrs = []error{errors.New("platform db blip")} }
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
		t.Fatal(err)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeRolledBack {
		t.Fatalf("run = %+v", latest)
	}
}

// A heartbeat refused because the run is no longer this actor's cancels the
// actor's work.
func TestMajorUpgrade_ALostHeartbeatCancelsTheActor(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	svc.heartbeatEvery = time.Millisecond
	run := &domain.MajorUpgrade{ID: "mu-x", ProjectID: "proj-up", FromVersion: "16", ToVersion: "17", Owner: "driver"}
	if err := w.runs.StartMajorUpgrade(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	w.runs.setRun("mu-x", func(stored *domain.MajorUpgrade) { stored.Owner = "recovery" })
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := svc.keepAlive(*run, svc.cfg.Now(), cancel)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the actor was not cancelled")
	}
	if !errors.Is(context.Cause(ctx), errRunTakenOver) {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
}
