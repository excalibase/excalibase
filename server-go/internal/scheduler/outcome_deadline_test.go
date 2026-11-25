package scheduler

import (
	"context"
	"encoding/json"
	"errors"
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
