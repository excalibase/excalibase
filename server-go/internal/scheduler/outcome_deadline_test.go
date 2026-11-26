package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const (
	sweepDeadline = 40 * time.Millisecond
	pastDeadline  = 80 * time.Millisecond
	settleWithin  = 3 * time.Second
	completedSQL  = "SET status = 'completed'"
)

// bodyInvoker runs fn for every dispatch.
type bodyInvoker func(ctx context.Context) error

func (f bodyInvoker) Invoke(ctx context.Context, _, _, _ string, _ json.RawMessage) error {
	return f(ctx)
}

// settled waits for the expectations to be met: the outcome write may land
// after the tick has already returned.
func settled(t *testing.T, p *projectMock) error {
	t.Helper()
	deadline := time.Now().Add(settleWithin)
	for {
		err := p.mock.ExpectationsWereMet()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func oneDueTask(t *testing.T, inv Invoker) (*Fanout, *projectMock) {
	t.Helper()
	p := newProjectMock(t)
	p.expectSchedulerTables(true)
	expectOneDueRow(p.mock)
	f := fanoutOver(map[string]*projectMock{"proj_a": p}, inv, alwaysLeader{})
	f.projectTimeout = sweepDeadline
	return f, p
}

// A function that finishes after the project's sweep deadline still ran, so
// its outcome must be recorded. Left 'running', the row is reclaimed after
// the claim lease and the function runs a second time.
func TestFanout_FunctionFinishingPastTheDeadlineIsStillRecorded(t *testing.T) {
	f, p := oneDueTask(t, bodyInvoker(func(context.Context) error {
		time.Sleep(pastDeadline)
		return nil
	}))
	p.mock.ExpectExec(completedSQL).WillReturnResult(sqlmock.NewResult(0, 1))

	_ = f.TaskTick(context.Background())

	if err := settled(t, p); err != nil {
		t.Fatalf("the finished function was not recorded: %v", err)
	}
}

// The project deadline must not cancel a running function: its own
// invocation timeout bounds it, and cancelling would run it twice.
func TestFanout_SweepDeadlineDoesNotCancelARunningFunction(t *testing.T) {
	f, p := oneDueTask(t, bodyInvoker(func(ctx context.Context) error {
		select {
		case <-time.After(pastDeadline):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	p.mock.ExpectExec(completedSQL).WillReturnResult(sqlmock.NewResult(0, 1))

	_ = f.TaskTick(context.Background())

	if err := settled(t, p); err != nil {
		t.Fatalf("the sweep deadline cancelled a running function: %v", err)
	}
}

// A failure that arrives after the deadline is recorded too, or the row
// stays 'running' and is retried without the attempt being counted properly.
func TestFanout_FailureAfterTheDeadlineIsStillRecorded(t *testing.T) {
	f, p := oneDueTask(t, bodyInvoker(func(context.Context) error {
		time.Sleep(pastDeadline)
		return errors.New("boom")
	}))
	p.mock.ExpectExec("SET status = 'pending'").WillReturnResult(sqlmock.NewResult(0, 1))

	_ = f.TaskTick(context.Background())

	if err := settled(t, p); err != nil {
		t.Fatalf("the failed function was not recorded: %v", err)
	}
}

// Detaching the function from the deadline must keep the property the
// deadline exists for: a project that holds the sweep open is abandoned.
func TestFanout_RunningFunctionDoesNotHoldTheSweepPastTheDeadline(t *testing.T) {
	release := make(chan struct{})
	f, p := oneDueTask(t, bodyInvoker(func(context.Context) error {
		select {
		case <-release:
		case <-time.After(settleWithin):
		}
		return nil
	}))
	p.mock.ExpectExec(completedSQL).WillReturnResult(sqlmock.NewResult(0, 1))

	start := time.Now()
	err := f.TaskTick(context.Background())
	elapsed := time.Since(start)
	close(release)

	if err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("the sweep waited %v on a running function", elapsed)
	}
	if !f.backedOff("proj_a") {
		t.Error("a project that held the sweep past its deadline must be backed off")
	}
	if err := settled(t, p); err != nil {
		t.Fatalf("the function released later was not recorded: %v", err)
	}
}

// A function the sweep stopped waiting on still holds its project's slot, so
// the next sweep may not hand the project a fresh set of slots: that would
// run more of one tenant's functions at once than its limit allows.
func TestFanout_ProjectConcurrencyHoldsAcrossTicks(t *testing.T) {
	var now atomic.Int64
	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	inv := bodyInvoker(func(context.Context) error {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		select {
		case <-release:
		case <-time.After(settleWithin):
		}
		inFlight.Add(-1)
		return nil
	})
	f, p := oneDueTask(t, inv)
	expectOneDueRow(p.mock)
	p.mock.ExpectExec(completedSQL).WillReturnResult(sqlmock.NewResult(0, 1))
	f.limits.ProjectConcurrency = 1
	start := time.Now()
	f.now = func() time.Time { return start.Add(time.Duration(now.Load())) }

	_ = f.TaskTick(context.Background())
	now.Store(int64(time.Hour)) // past the backoff the first deadline caused
	_ = f.TaskTick(context.Background())
	close(release)
	// Only the first row ever starts; the second finds the project's slot taken.
	if err := settled(t, p); err != nil {
		t.Fatalf("expected exactly one completion: %v", err)
	}

	if got := peak.Load(); got != 1 {
		t.Fatalf("%d invocations of one project in flight at once, limit is 1", got)
	}
}

func oneRunOfAnotherProject(t *testing.T) (*Fanout, *projectMock) {
	t.Helper()
	p := newProjectMock(t)
	p.expectSchedulerTables(true)
	expectOneDueRow(p.mock)
	p.mock.ExpectExec(completedSQL).WillReturnResult(sqlmock.NewResult(0, 1))
	f := fanoutOver(map[string]*projectMock{"proj_a": p}, &recordingInvoker{}, alwaysLeader{})
	return f, p
}

// The slots are kept per project, so a project that is deleted must not
// leave one behind for ever, and must not lose one that is still held.
func TestFanout_ProjectSlotsAreDroppedWhenTheProjectIsGone(t *testing.T) {
	f, _ := oneRunOfAnotherProject(t)
	if err := f.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if f.projectSlotCount() != 1 {
		t.Fatalf("slots after a sweep: got %d, want 1", f.projectSlotCount())
	}
	held := f.projectSlots("proj_a")
	if err := held.Acquire(context.Background()); err != nil {
		t.Fatalf("hold a slot: %v", err)
	}
	f.projects = func(context.Context) ([]string, error) { return nil, nil }

	_ = f.TaskTick(context.Background())
	if f.projectSlotCount() != 1 {
		t.Fatal("a project's slots were dropped while a function still held one")
	}
	held.Release()
	_ = f.TaskTick(context.Background())
	if f.projectSlotCount() != 0 {
		t.Fatalf("slots of a deleted project: got %d, want 0", f.projectSlotCount())
	}
}

// The panic path records through the same detached context: a function that
// panics after the sweep deadline must still be closed, not left running.
func TestFanout_PanicAfterTheDeadlineIsStillRecorded(t *testing.T) {
	f, p := oneDueTask(t, bodyInvoker(func(context.Context) error {
		time.Sleep(pastDeadline)
		panic("boom")
	}))
	p.mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 1))

	_ = f.TaskTick(context.Background())

	if err := settled(t, p); err != nil {
		t.Fatalf("the panicked function was not recorded: %v", err)
	}
}

// A Fanout built without limits gets the platform's default per-project
// limit, not a single slot.
func TestNewFanout_ProjectSlotsDefaultToTheProjectConcurrency(t *testing.T) {
	f := NewFanout(FanoutConfig{})
	sem := f.projectSlots("proj_a")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	for i := 0; i < DefaultProjectConcurrency; i++ {
		if err := sem.Acquire(ctx); err != nil {
			t.Fatalf("slot %d of %d: %v", i+1, DefaultProjectConcurrency, err)
		}
	}
	if got := sem.InUse(); got != DefaultProjectConcurrency {
		t.Fatalf("slots: got %d, want %d", got, DefaultProjectConcurrency)
	}
}
