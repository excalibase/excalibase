package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-683: a start refused while the host firewall is down says nothing about
// the database. After a power cut provisioning can claim a run before the
// firewall's first healthy check, so these end open, with nothing removed, and
// the next recovery finishes them.

func quickStartPoller(t *testing.T) {
	t.Helper()
	before := restoreStartPoller
	restoreStartPoller = provisioner.NewPoller(time.Millisecond, 5*time.Millisecond)
	t.Cleanup(func() { restoreStartPoller = before })
}

func TestRecoverOfALoadedConversionWaitsForTheFirewall(t *testing.T) {
	for name, down := range map[string]func(*upgradeEngine){
		"down when checked":    func(e *upgradeEngine) { e.firewallDown = true },
		"down after the check": func(e *upgradeEngine) { e.refuseStart = true },
	} {
		t.Run(name, func(t *testing.T) {
			quickStartPoller(t)
			w := newDockerUpgradeWorld(t)
			fresh := w.swappedAndRecorded("postgres:17", false)
			down(w.engine)
			outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress)
			if !outcome.Unsettled || outcome.Converted || outcome.RolledBack {
				t.Fatalf("outcome = %+v", outcome)
			}
			if w.engine.find(fresh.id) == nil {
				t.Fatal("the converted database was removed while the firewall was down")
			}
			w.engine.firewallDown, w.engine.refuseStart = false, false
			if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.Converted {
				t.Fatalf("once the firewall is healthy: %+v", outcome)
			}
		})
	}
}

// Control: with the new container already running no start is asked for.
func TestRecoverOfARunningConversionNeedsNoStart(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.swappedAndRecorded("postgres:17", true)
	w.engine.refuseStart = true
	if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.Converted {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestRecoverBeforeTheSwapPutsBackOnceTheFirewallIsHealthy(t *testing.T) {
	quickStartPoller(t)
	w := newDockerUpgradeWorld(t)
	old := w.engine.find("old-id")
	old.running, old.fenced = false, true
	w.engine.refuseStart = true
	outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Unsettled || outcome.Public == notPutBackPublic {
		t.Fatalf("outcome = %+v", outcome)
	}
	w.engine.refuseStart = false
	if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
		t.Fatalf("once the firewall is healthy: %+v", outcome)
	}
	if old.fenced || !old.running {
		t.Fatalf("old = %+v", old)
	}
}

// The new container is not recorded: recovery would remove it, but not while it could not start the old one.
func TestRecoverRemovesNothingWhileTheFirewallIsDown(t *testing.T) {
	quickStartPoller(t)
	w := newDockerUpgradeWorld(t)
	old := w.engine.find("old-id")
	old.name, old.running, old.fenced = provisioner.PreUpgradeContainerName("dk-1", "mu-1"), false, true
	fresh := &engineContainer{id: "new-id-9", name: provisioner.DatabaseContainerName("dk-1"), image: "postgres:17", fenced: true}
	w.engine.byID[fresh.id] = fresh
	w.engine.firewallDown = true
	outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Unsettled {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.engine.find(fresh.id) == nil || w.called("rename") {
		t.Fatalf("recovery changed the host with the firewall down: %v", w.engine.calls)
	}
}

func TestConvertChangesNothingWhileTheFirewallIsDown(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.engine.firewallDown = true
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.RolledBack || !errors.Is(outcome.Cause, provisioner.ErrHostFirewallDown) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if old := w.engine.find("old-id"); !old.running || old.fenced {
		t.Fatalf("the old database was touched: %+v", old)
	}
}

// The firewall goes down after the check, with the old container stopped and renamed.
func TestConvertRefusedMidSwapStaysOpenForRecovery(t *testing.T) {
	quickStartPoller(t)
	w := newDockerUpgradeWorld(t)
	w.engine.refuseStart = true
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Unsettled || outcome.Public == notPutBackPublic {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.engine.find(provisioner.PreUpgradeContainerName("dk-1", "mu-1")) == nil {
		t.Fatalf("the old database is gone: %v", w.engine.calls)
	}
	w.engine.refuseStart = false
	if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
		t.Fatalf("once the firewall is healthy: %+v", outcome)
	}
	if old := w.engine.find(provisioner.DatabaseContainerName("dk-1")); old == nil || old.id != "old-id" || !old.running || old.fenced {
		t.Fatalf("the old database is not back: %+v", old)
	}
}

// A converted run keeps the old container until the new one serves: Cleanup removes the only way back.
func TestFinishingWaitsForTheFirewallBeforeRemovingTheOldDatabase(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.servingErr = provisioner.ErrHostFirewallDown
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if slices.Contains(w.calls, "cleanup") {
		t.Fatalf("the old database was removed with the new one fenced: %v", w.calls)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeRunning || !latest.Converted {
		t.Fatalf("record = %+v", latest)
	}
}

func quickFirewallPollers(t *testing.T) {
	t.Helper()
	first, rollback := restoreFirewallPoller, restoreRollbackFirewallPoller
	restoreFirewallPoller = provisioner.NewPoller(time.Millisecond, 5*time.Millisecond)
	restoreRollbackFirewallPoller = provisioner.NewPoller(time.Millisecond, 5*time.Millisecond)
	t.Cleanup(func() { restoreFirewallPoller, restoreRollbackFirewallPoller = first, rollback })
}

func TestInPlaceRestoreRemovesNothingWhileTheFirewallIsDown(t *testing.T) {
	quickFirewallPollers(t)
	f := newInPlaceFixture(t)
	ctx := context.Background()
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: time.Now().Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	f.dc.firewallDown = true
	if _, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: time.Now().Add(-time.Hour)}}); !errors.Is(err, provisioner.ErrHostFirewallDown) {
		t.Fatalf("target with the firewall down: %v", err)
	}
	err = f.adapter.ReplaceDatabase(ctx, f.inst, target)
	if !errors.Is(err, ErrDatabaseNotReplaced) {
		t.Fatalf("replace = %v", err)
	}
	if len(f.dc.removed) != 0 {
		t.Fatalf("removed %v with the firewall down", f.dc.removed)
	}
}

// Nothing was replaced, so there is nothing to roll back and the running database stays in service.
func TestInPlaceRestoreNotStartedLeavesTheProjectActive(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.replaceErrs = []error{ErrDatabaseNotReplaced}
	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	var public *InPlaceRestoreError
	if !errors.As(err, &public) || !strings.Contains(public.Public, "nothing was changed") {
		t.Fatalf("got %v", err)
	}
	if len(w.targets) != 1 {
		t.Fatalf("rolled back something never replaced: %v", w.targets)
	}
	row, _ := w.instances.FindByProjectID("proj-a")
	if row.Status != string(domain.StatusActive) {
		t.Errorf("status %s", row.Status)
	}
}

func TestHostMySQLInPlaceRestoreRemovesNothingWhileTheFirewallIsDown(t *testing.T) {
	quickFirewallPollers(t)
	f := newHostBackupFixture(t)
	ctx := context.Background()
	if _, err := f.adapter.TriggerManual(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: config.TierConfig{Instances: 1, Memory: "1Gi", CPU: "1"}}})
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{BackupID: "20261011-100000"})
	if err != nil {
		t.Fatal(err)
	}
	f.engine.firewallDown = true
	if _, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{BackupID: "20261011-100000"}); !errors.Is(err, provisioner.ErrHostFirewallDown) {
		t.Fatalf("target with the firewall down: %v", err)
	}
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, target); !errors.Is(err, ErrDatabaseNotReplaced) {
		t.Fatalf("replace = %v", err)
	}
	if len(f.engine.removedVolumes) != 0 || !f.engine.containers["excalibase-p1-mysql"] {
		t.Fatalf("the database was touched: removed %v", f.engine.removedVolumes)
	}
}

// EXC-683: on Docker the old database is the only way back until the new one has
// taken over, whatever kept it from doing so. Kubernetes replicas may still copy, and warn.
func TestDockerWaitServingNamesEveryFailure(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	fresh := w.swappedAndRecorded("postgres:17", true)
	w.engine.notReady["postgres:17"] = -1
	err := w.upgrader.WaitServing(context.Background(), w.inst)
	if !errors.Is(err, ErrMajorUpgradeNotServing) || !fresh.fenced {
		t.Fatalf("slow server: %v, fenced %v", err, fresh.fenced)
	}
}

// A run that stays open says what is true, and how to leave: the backup taken first, by name.
func TestAnOpenRunNamesTheBackupAndTheWayOut(t *testing.T) {
	for name, cause := range map[string]error{
		"firewall":    provisioner.ErrHostFirewallDown,
		"not serving": fmt.Errorf("%w: never", ErrMajorUpgradeNotServing),
	} {
		t.Run(name, func(t *testing.T) {
			w, svc := newUpgradeWorld(t, upgradeProject())
			w.servingErr = cause
			if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
				t.Fatal(err)
			}
			latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
			row := projectRow(t, w)
			for _, said := range []string{latest.Error, row.FailureReason} {
				if !strings.Contains(said, latest.SafetyBackupID) || latest.SafetyBackupID == "" ||
					!strings.Contains(said, "new project") || !strings.Contains(said, "checked again") {
					t.Fatalf("says %q (backup %q)", said, latest.SafetyBackupID)
				}
			}
		})
	}
}

func TestANewServerNotServingKeepsTheOldDatabaseAndFinishesOnce(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.servingErr = fmt.Errorf("%w: postgres did not start within 2m0s", ErrMajorUpgradeNotServing)
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
		t.Fatal(err)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if slices.Contains(w.calls, "cleanup") || latest.Status != domain.MajorUpgradeRunning || !latest.Converted {
		t.Fatalf("run %+v, calls %v", latest, w.calls)
	}
	w.servingErr = nil
	now := time.Now().Add(majorUpgradeStaleAfter + time.Minute)
	w.runs.clock = func() time.Time { return now }
	svc.RecoverInterrupted(context.Background())
	latest, _ = w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	cleanups := 0
	for _, call := range w.calls {
		if call == "cleanup" {
			cleanups++
		}
	}
	if latest.Status != domain.MajorUpgradeCompleted || cleanups != 1 {
		t.Fatalf("status %s, cleanups %d", latest.Status, cleanups)
	}
}

// An old server slower than the wait is still the old database: recovery looks again, the fence and dump kept for it.
func TestASlowPutBackStaysOpenAndFinishes(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	old := w.engine.find("old-id")
	old.running, old.fenced = false, true
	w.engine.notReady["postgres:16"] = -1
	outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Unsettled || outcome.Public == notPutBackPublic || !old.fenced {
		t.Fatalf("outcome = %+v, old = %+v", outcome, old)
	}
	w.engine.notReady["postgres:16"] = 0
	if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack || old.fenced {
		t.Fatalf("once it listens: %+v, old = %+v", outcome, old)
	}
}

// The start waits out a firewall that went down after the last check, past the short wait for a held port.
func TestABringUpWaitsLongForTheFirewall(t *testing.T) {
	quickStartPoller(t)
	before := restoreFirewallPoller
	restoreFirewallPoller = provisioner.NewPoller(time.Millisecond, time.Minute)
	t.Cleanup(func() { restoreFirewallPoller = before })
	f := newInPlaceFixture(t)
	ctx := context.Background()
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: time.Now().Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	f.adapter.SetDockerClient(&flipClient{fakeDockerClientForAdapter: f.dc, onRemove: func() {
		f.dc.setFirewallDown(true)
		time.AfterFunc(100*time.Millisecond, func() { f.dc.setFirewallDown(false) })
	}})
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, target); err != nil {
		t.Fatalf("replace = %v", err)
	}
}

// flipClient loses the firewall after the last check, as the old database is removed.
type flipClient struct {
	*fakeDockerClientForAdapter
	onRemove func()
}

func (c *flipClient) RemoveContainer(ctx context.Context, id string) error {
	err := c.fakeDockerClientForAdapter.RemoveContainer(ctx, id)
	c.onRemove()
	return err
}

// A refused replace leaves no seeded container for the proxy's promise to start later.
func TestARestoreRefusedAfterTheRemovalLeavesNoOrphan(t *testing.T) {
	quickStartPoller(t)
	quickFirewallPollers(t)
	f := newInPlaceFixture(t)
	ctx := context.Background()
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: time.Now().Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	f.adapter.SetDockerClient(&flipClient{fakeDockerClientForAdapter: f.dc, onRemove: func() { f.dc.firewallDown = true }})
	err = f.adapter.ReplaceDatabase(ctx, f.inst, target)
	if !errors.Is(err, provisioner.ErrHostFirewallDown) || errors.Is(err, ErrDatabaseNotReplaced) {
		t.Fatalf("replace = %v", err)
	}
	if !slices.Contains(f.dc.removed, f.dc.createdContainerID()) {
		t.Fatalf("removed %v, seeded %q", f.dc.removed, f.dc.createdContainerID())
	}
}

func TestHostMySQLRestoreRefusedAfterTheRemovalLeavesNoOrphan(t *testing.T) {
	quickFirewallPollers(t)
	f := newHostBackupFixture(t)
	ctx := context.Background()
	if _, err := f.adapter.TriggerManual(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: config.TierConfig{Instances: 1, Memory: "1Gi", CPU: "1"}}})
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{BackupID: "20261011-100000"})
	if err != nil {
		t.Fatal(err)
	}
	f.engine.refuseStarts = true
	err = f.adapter.ReplaceDatabase(ctx, f.inst, target)
	if !errors.Is(err, provisioner.ErrHostFirewallDown) || errors.Is(err, ErrDatabaseNotReplaced) {
		t.Fatalf("replace = %v", err)
	}
	if _, left := f.engine.containers["excalibase-p1-mysql"]; left {
		t.Fatalf("an empty database is left behind: %v", f.engine.containers)
	}
}

// The firewall went down with the old database gone: the rollback waits for it and the project ends up as it was.
func TestARollbackWaitsForTheFirewall(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.replaceErrs = []error{fmt.Errorf("start restored container: %w", provisioner.ErrHostFirewallDown)}
	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	var public *InPlaceRestoreError
	if !errors.As(err, &public) || !strings.Contains(public.Public, "put back as it was") {
		t.Fatalf("got %v", err)
	}
	if !slices.Contains(w.calls, "wait-firewall") {
		t.Fatalf("calls %v", w.calls)
	}
	if row, _ := w.instances.FindByProjectID("proj-a"); row.Status != string(domain.StatusActive) {
		t.Fatalf("status %s", row.Status)
	}
}

func TestARollbackTheFirewallOutlastsSaysSo(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.replaceErrs = []error{fmt.Errorf("start restored container: %w", provisioner.ErrHostFirewallDown)}
	w.firewallWait = provisioner.ErrHostFirewallDown
	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	var public *InPlaceRestoreError
	if !errors.As(err, &public) || !strings.Contains(public.Public, "host firewall") || !strings.Contains(public.Public, "safety-1") {
		t.Fatalf("got %v", err)
	}
	if len(w.targets) != 1 {
		t.Fatalf("rolled back with the firewall down: %v", w.targets)
	}
	if row, _ := w.instances.FindByProjectID("proj-a"); row.CurrentStep != domain.RestoreStepRestoreStopped {
		t.Fatalf("step %s", row.CurrentStep)
	}
}

// A retry during the outage has no backup of its own: the one the first failure named stays the way back.
func TestARestoreRetriedDuringTheOutageKeepsTheBackupName(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.replaceErrs = []error{fmt.Errorf("start restored container: %w", provisioner.ErrHostFirewallDown)}
	w.firewallWait = provisioner.ErrHostFirewallDown
	_ = restorer.Restore(context.Background(), w, "proj-a", restoreAt())

	w.replaceErrs = []error{fmt.Errorf("%w: %w", ErrDatabaseNotReplaced, provisioner.ErrHostFirewallDown)}
	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	var public *InPlaceRestoreError
	if !errors.As(err, &public) || !strings.Contains(public.Public, "safety-1") {
		t.Fatalf("got %v", err)
	}
	row, _ := w.instances.FindByProjectID("proj-a")
	if !strings.Contains(row.FailureReason, "safety-1") || row.CurrentStep != domain.RestoreStepRestoreStopped {
		t.Fatalf("row: %s %q", row.CurrentStep, row.FailureReason)
	}
}

// With nothing named before, a retry says what it always said.
func TestARestoreOfAStoppedProjectWithNoBackupNamedSaysRestoreAgain(t *testing.T) {
	project := inPlaceProject()
	project.Status = string(domain.StatusRestoring)
	project.CurrentStep = domain.RestoreStepRestoreStopped
	w, restorer := newInPlaceWorld(t, project)
	w.replaceErrs = []error{errors.New("recovered database never became ready")}
	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	var public *InPlaceRestoreError
	if !errors.As(err, &public) || !strings.Contains(public.Public, "restore again from Backups") {
		t.Fatalf("got %v", err)
	}
}

// rollbackContexts records the context a rollback's wait and second replace get.
type rollbackContexts struct {
	*inPlaceWorld
	cancel                 func()
	replaces               int
	waitErr, secondReplace error
}

func (c *rollbackContexts) WaitHostFirewall(ctx context.Context) error {
	c.waitErr = ctx.Err()
	return nil
}

func (c *rollbackContexts) ReplaceDatabase(ctx context.Context, _ *domain.DatabaseInstance, _ map[string]interface{}) error {
	c.replaces++
	if c.replaces == 1 {
		c.cancel()
		return fmt.Errorf("start: %w", provisioner.ErrHostFirewallDown)
	}
	c.secondReplace = ctx.Err()
	return nil
}

// Whoever asked for the restore may be gone: the project is put back all the same.
func TestARollbackOutlivesTheRequest(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cluster := &rollbackContexts{inPlaceWorld: w, cancel: cancel}
	_ = restorer.Restore(ctx, cluster, "proj-a", restoreAt())
	if cluster.replaces != 2 || cluster.waitErr != nil || cluster.secondReplace != nil {
		t.Fatalf("replaces %d, wait saw %v, put back saw %v", cluster.replaces, cluster.waitErr, cluster.secondReplace)
	}
	if row, _ := w.instances.FindByProjectID("proj-a"); row.Status != string(domain.StatusActive) {
		t.Fatalf("status %s", row.Status)
	}
}

// Other rollbacks do not wait on a firewall they have no reason to doubt.
func TestAnOrdinaryRollbackDoesNotAskForTheFirewall(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.replaceErrs = []error{errors.New("recovered database never became ready")}
	_ = restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	if slices.Contains(w.calls, "wait-firewall") {
		t.Fatalf("calls %v", w.calls)
	}
}

func TestInPlaceRestoreNotStartedRestartsReplication(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.replaceErrs = []error{ErrDatabaseNotReplaced}
	_ = restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	if !slices.Contains(w.calls, "restart-replication") {
		t.Fatalf("the watcher stopped for the restore stays stopped: %v", w.calls)
	}
}

// A project an earlier restore left stopped has nothing running to return to.
func TestInPlaceRestoreNotStartedOfAStoppedProjectStaysStopped(t *testing.T) {
	project := inPlaceProject()
	project.Status = string(domain.StatusRestoring)
	project.CurrentStep = domain.RestoreStepRestoreStopped
	w, restorer := newInPlaceWorld(t, project)
	w.replaceErrs = []error{ErrDatabaseNotReplaced}
	_ = restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	if row, _ := w.instances.FindByProjectID("proj-a"); row.Status != string(domain.StatusRestoring) || row.CurrentStep != domain.RestoreStepRestoreStopped {
		t.Fatalf("status %s step %s", row.Status, row.CurrentStep)
	}
}

// As on Postgres: the start after the removal waits out a firewall lost right after the last check.
func TestHostMySQLRestoreWaitsLongForTheFirewall(t *testing.T) {
	before := restoreFirewallPoller
	restoreFirewallPoller = provisioner.NewPoller(time.Millisecond, time.Minute)
	t.Cleanup(func() { restoreFirewallPoller = before })
	f := newHostBackupFixture(t)
	ctx := context.Background()
	if _, err := f.adapter.TriggerManual(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: config.TierConfig{Instances: 1, Memory: "1Gi", CPU: "1"}}})
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{BackupID: "20261011-100000"})
	if err != nil {
		t.Fatal(err)
	}
	f.engine.afterRemoveVolume = func() {
		f.engine.firewallDown = true
		f.engine.refuseStarts = true
		time.AfterFunc(200*time.Millisecond, func() {
			f.engine.mu.Lock()
			defer f.engine.mu.Unlock()
			f.engine.firewallDown, f.engine.refuseStarts = false, false
		})
	}
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, target); err != nil {
		t.Fatalf("replace = %v", err)
	}
}
