package service

import (
	"context"
	"errors"
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

func TestInPlaceRestoreRemovesNothingWhileTheFirewallIsDown(t *testing.T) {
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
