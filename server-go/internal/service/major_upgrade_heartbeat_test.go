package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// steppingClock moves step forward on every read.
type steppingClock struct {
	mu   sync.Mutex
	at   time.Time
	step time.Duration
}

func (c *steppingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(c.step)
	return c.at
}

func (c *steppingClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func heartbeatWorld(t *testing.T) (*upgradeWorld, *MajorUpgradeService, domain.MajorUpgrade, *steppingClock) {
	t.Helper()
	w, svc := newUpgradeWorld(t, upgradeProject())
	clock := &steppingClock{at: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), step: majorUpgradeHeartbeat}
	svc.cfg.Now = clock.now
	svc.heartbeatEvery = time.Millisecond
	run := domain.MajorUpgrade{ID: "mu-hb", ProjectID: "proj-up", FromVersion: "16", ToVersion: "17", Owner: "driver"}
	if err := w.runs.StartMajorUpgrade(context.Background(), &run); err != nil {
		t.Fatal(err)
	}
	return w, svc, run, clock
}

// Heartbeats that keep failing fence the actor itself, before the run could
// look stale to recovery.
func TestMajorUpgrade_AnActorWhoseHeartbeatsFailGivesUpBeforeRecoveryCanClaim(t *testing.T) {
	w, svc, run, clock := heartbeatWorld(t)
	w.runs.setTouchErr(errors.New("platform db down"))
	started := clock.read()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := svc.keepAlive(run, cancel)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the actor kept going on failing heartbeats")
	}
	if !errors.Is(context.Cause(ctx), errRunTakenOver) {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
	if elapsed := clock.read().Sub(started); elapsed >= majorUpgradeStaleAfter {
		t.Fatalf("fenced after %s, not before recovery could claim at %s", elapsed, majorUpgradeStaleAfter)
	}
}

// A short outage does not fence the actor.
func TestMajorUpgrade_AShortHeartbeatOutageKeepsTheRun(t *testing.T) {
	w, svc, run, _ := heartbeatWorld(t)
	w.runs.failTouches(2, errors.New("platform db blip"))
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := svc.keepAlive(run, cancel)
	deadline := time.Now().Add(5 * time.Second)
	for w.runs.touchCount() < 20 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	if ctx.Err() != nil {
		t.Fatalf("a short outage fenced the actor: %v", context.Cause(ctx))
	}
}

// A heartbeat that hangs is cut off, so stop returns.
func TestMajorUpgrade_AHungHeartbeatDoesNotHoldTheActor(t *testing.T) {
	w, svc, run, _ := heartbeatWorld(t)
	svc.heartbeatTimeout = 5 * time.Millisecond
	w.runs.setTouchHang(true)
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := svc.keepAlive(run, cancel)
	for w.runs.touchCount() < 1 {
		time.Sleep(time.Millisecond)
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop waited on a hung heartbeat")
	}
}
