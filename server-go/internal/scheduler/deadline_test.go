package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// A tenant owns its database and can make any statement there hang — a view
// over the scheduler table calling pg_sleep, a held lock. Without a deadline
// the sweep waits on it forever and every other tenant's tasks stop.
func TestFanout_SlowProjectDoesNotStallTheOthers(t *testing.T) {
	slow := newProjectMock(t)
	slow.mock.ExpectQuery("to_regclass").WillDelayFor(30 * time.Second).
		WillReturnRows(sqlmock.NewRows([]string{"present"}).AddRow(true))

	fast := newProjectMock(t)
	fast.expectSchedulerTables(true)
	expectOneDueRow(fast.mock)
	fast.mock.ExpectExec("SET status = 'completed'").WillReturnResult(sqlmock.NewResult(0, 1))

	inv := &recordingInvoker{}
	f := fanoutOver(map[string]*projectMock{"slow": slow, "proj_a": fast}, inv, alwaysLeader{})
	f.projectTimeout = 50 * time.Millisecond

	start := time.Now()
	if err := f.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("tick took %v; one slow project must not hold the sweep", elapsed)
	}
	if calls := inv.Calls(); len(calls) != 1 || calls[0] != "proj_a/jobs.send" {
		t.Fatalf("dispatches: got %v, want [proj_a/jobs.send]", calls)
	}
}

// Nothing errors when a tenant simply answers slowly, so without counting
// the deadline as a failure the per-project backoff would never fire and the
// sweep would pay the full timeout on every tick.
func TestFanout_TimedOutProjectIsBackedOff(t *testing.T) {
	slow := newProjectMock(t)
	slow.mock.ExpectQuery("to_regclass").WillDelayFor(30 * time.Second).
		WillReturnRows(sqlmock.NewRows([]string{"present"}).AddRow(true))

	f := fanoutOver(map[string]*projectMock{"slow": slow}, &recordingInvoker{}, alwaysLeader{})
	f.projectTimeout = 50 * time.Millisecond

	if err := f.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if !f.backedOff("slow") {
		t.Fatal("a project that timed out must be backed off")
	}
}

// The deadline is an operator bound, so the sweep must take it from Limits
// rather than hard-coding one.
func TestNewFanout_ProjectTimeoutComesFromLimits(t *testing.T) {
	f := NewFanout(FanoutConfig{Limits: Limits{ProjectTimeout: 7 * time.Second}})
	if f.projectTimeout != 7*time.Second {
		t.Errorf("projectTimeout: got %v, want 7s", f.projectTimeout)
	}
	def := NewFanout(FanoutConfig{})
	if def.projectTimeout != DefaultProjectTimeout {
		t.Errorf("default projectTimeout: got %v, want %v", def.projectTimeout, DefaultProjectTimeout)
	}
}
