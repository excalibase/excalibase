package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// restoreStartBound is the longest one uninterruptible step of a put back
// takes: starting the old container.
const restoreStartBound = 30 * time.Second

// An actor gives up early enough that a step it is in the middle of, and the
// lateness of one heartbeat tick, still end before recovery may claim.
func TestMajorUpgrade_AnActorGivesUpWithRoomForAStepInFlight(t *testing.T) {
	w, svc, run, clock := heartbeatWorld(t)
	w.runs.setTouchErr(errors.New("platform db down"))
	stamped := clock.read()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := svc.keepAlive(run, stamped, cancel)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the actor kept going on failing heartbeats")
	}
	latest := clock.read().Sub(stamped) + majorUpgradeHeartbeat + restoreStartBound
	if latest >= majorUpgradeStaleAfter {
		t.Fatalf("gave up at %s: a late tick and a step in flight end at %s, not before the claim at %s",
			clock.read().Sub(stamped), latest, majorUpgradeStaleAfter)
	}
}

// An ownership check the database cannot answer does not count as the run
// still being this actor's once the fence was met while it waited.
func TestMajorUpgrade_AFenceMetDuringAnUnansweredOwnershipCheckStopsTheActor(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	progress := &recordingProgress{holdErr: errors.New("dial tcp: connection refused")}
	progress.onHold = func() { cancel(fmt.Errorf("%w: no heartbeat landed", errRunTakenOver)) }
	if lost := lostRun(ctx, errors.New("the load failed"), progress); !errors.Is(lost, errRunTakenOver) {
		t.Fatalf("lostRun = %v, want the run lost", lost)
	}
}

// The driver gives up while it puts the old container back: the call in
// flight finishes, and no further step runs.
func TestDockerPutBackStopsBeforeItsNextStepOnceTheDriverGivesUp(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.engine.load = provisioner.UpgradeFailed
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	putBackRename := "rename:" + provisioner.DatabaseContainerName("dk-1")
	w.engine.after = map[string]func(){putBackRename: func() {
		cancel(fmt.Errorf("%w: no heartbeat landed", errRunTakenOver))
		w.progress.holdErr = errors.New("dial tcp: connection refused")
		w.engine.calls = append(w.engine.calls, "gave-up")
	}}
	outcome := w.upgrader.Convert(ctx, w.inst, w.run(), w.progress)
	if outcome.RolledBack || !errors.Is(outcome.Cause, errRunTakenOver) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if old := w.engine.find(provisioner.DatabaseContainerName("dk-1")); old == nil || old.id != "old-id" {
		t.Fatalf("the rename in flight did not finish: %+v", old)
	}
	if after := callsAfter(w.engine.calls, "gave-up"); len(after) != 0 {
		t.Fatalf("the put back went on after the driver gave up: %v", after)
	}
}

// actorCall is one engine call on the race's shared clock.
type actorCall struct {
	actor, call string
	at          time.Time
}

// simulatedHeartbeat runs the actor's heartbeat ticks on the test's clock,
// with the real per-tick check.
type simulatedHeartbeat struct {
	svc      *MajorUpgradeService
	run      domain.MajorUpgrade
	lastBeat time.Time
	next     time.Time
	lost     func(error)
	gaveUp   bool
}

func (h *simulatedHeartbeat) advanceTo(now time.Time) {
	for !h.gaveUp && !now.Before(h.next) {
		h.gaveUp = h.svc.fenced(h.run, &h.lastBeat, h.lost)
		h.next = h.next.Add(majorUpgradeHeartbeat)
	}
}

// The platform database goes down while the driver swaps containers and the
// old server is slow to come back: the driver gives up during the put back,
// recovery claims the run once it is stale and puts the old container back
// itself. No driver call lands at or after the claim.
func TestDockerADriverThatGivesUpMidPutBackNeverOverlapsRecovery(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	start := w.now
	clock := func() time.Time { return w.now }
	runs := newMemMajorUpgrades(w.store)
	runs.clock = clock
	svc := NewMajorUpgradeService(MajorUpgradeConfig{Runs: runs, Instances: w.store, Now: clock})
	run := w.run()
	run.Owner, run.StartedAt = "driver", start
	if err := runs.StartMajorUpgrade(context.Background(), &run); err != nil {
		t.Fatal(err)
	}

	actor := "driver"
	var calls []actorCall
	w.engine.onCall = func(call string) { calls = append(calls, actorCall{actor: actor, call: call, at: w.now}) }
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	beats := &simulatedHeartbeat{svc: svc, run: run, lastBeat: start, next: start.Add(majorUpgradeHeartbeat), lost: cancel}
	w.upgrader.sleep = func(sleepCtx context.Context, d time.Duration) error {
		w.now = w.now.Add(d)
		beats.advanceTo(w.now)
		return sleepCtx.Err()
	}
	// The database goes down as the new container starts, 70s into the
	// run; the load then fails and the old server stays deaf for good.
	w.engine.load = provisioner.UpgradeFailed
	w.engine.after = map[string]func(){"start:postgres:17": func() {
		runs.setTouchErr(errors.New("platform db down"))
		w.now = start.Add(70 * time.Second)
		beats.advanceTo(w.now)
		w.engine.notReady["postgres:16"] = -1
	}}
	driven := w.upgrader.Convert(ctx, w.inst, run, svc.progressOf(&run))
	if !beats.gaveUp || !errors.Is(driven.Cause, errRunTakenOver) || driven.RolledBack {
		t.Fatalf("driver: gave up %v, outcome %+v", beats.gaveUp, driven)
	}

	// The database is back; recovery claims the stale run.
	runs.setTouchErr(nil)
	w.engine.notReady["postgres:16"] = 0
	w.now = start.Add(majorUpgradeStaleAfter + time.Second)
	claimAt := w.now
	claimed, err := runs.ClaimMajorUpgrade(context.Background(), run.ID, "recovery", majorUpgradeStaleAfter)
	if err != nil || claimed == nil {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	actor = "recovery"
	inst, err := w.store.FindByProjectID("dk-1")
	if err != nil {
		t.Fatal(err)
	}
	recovered := w.upgrader.Recover(context.Background(), inst, *claimed, svc.progressOf(claimed))
	if !recovered.RolledBack {
		t.Fatalf("recovery: outcome %+v", recovered)
	}
	w.assertPutBack(t)
	for _, call := range calls {
		if call.actor == "driver" && !call.at.Before(claimAt) {
			t.Fatalf("driver call %s at +%s overlaps recovery's claim at +%s: %s",
				call.call, call.at.Sub(start), claimAt.Sub(start), describeCalls(calls, start))
		}
	}
}

func describeCalls(calls []actorCall, start time.Time) string {
	lines := make([]string, 0, len(calls))
	for _, call := range calls {
		lines = append(lines, fmt.Sprintf("%s +%s %s", call.actor, call.at.Sub(start), call.call))
	}
	return strings.Join(lines, "\n")
}

// The fence stops the k8s put back's wait, and the outcome says the run was
// lost rather than that the old major did not come back.
func TestK8sPutBackReportsALostRunWhenTheFenceStopsItsWait(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	w.react = func(w *k8sUpgradeWorld) {
		w.upgrading()
		switch {
		case w.imageIs("17"):
			w.job(true)
		case w.imageIs("16"):
			cancel(fmt.Errorf("%w: no heartbeat landed", errRunTakenOver))
		}
	}
	outcome := w.upgrader.Convert(ctx, w.inst, w.run(), w.progress)
	if outcome.RolledBack || !errors.Is(outcome.Cause, errRunTakenOver) {
		t.Fatalf("outcome = %+v", outcome)
	}
}
