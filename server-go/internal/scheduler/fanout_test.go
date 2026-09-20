package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// recordingInvoker captures every dispatch the fanout makes.
type recordingInvoker struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingInvoker) Invoke(_ context.Context, projectID, moduleName, exportName string, _ json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, projectID+"/"+moduleName+"."+exportName)
	return nil
}

func (r *recordingInvoker) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// projectMock is one project's database with its programmed expectations.
type projectMock struct {
	db   *sql.DB
	mock sqlmock.Sqlmock
}

func newProjectMock(t *testing.T) *projectMock {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &projectMock{db: db, mock: mock}
}

// expectSchedulerTables programs the reserved-schema presence check.
func (p *projectMock) expectSchedulerTables(present bool) {
	p.mock.ExpectQuery("to_regclass").
		WillReturnRows(sqlmock.NewRows([]string{"present"}).AddRow(present))
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func fanoutOver(projects map[string]*projectMock, inv Invoker, leader Leader) *Fanout {
	ids := make([]string, 0, len(projects))
	for id := range projects {
		ids = append(ids, id)
	}
	return NewFanout(FanoutConfig{
		Projects: func(context.Context) ([]string, error) { return ids, nil },
		DB: func(_ context.Context, projectID string) (*sql.DB, error) {
			p, ok := projects[projectID]
			if !ok {
				return nil, errors.New("no such project")
			}
			return p.db, nil
		},
		Invoker:    inv,
		CronLeader: leader,
		Functions:  knownFunctions{modules: map[string]bool{"proj_a/jobs": true}},
		Logger:     quietLogger(),
	})
}

// alwaysLeader / neverLeader stand in for the platform's leader election.
type alwaysLeader struct{}

func (alwaysLeader) IsLeader(context.Context) (bool, error) { return true, nil }

type neverLeader struct{}

func (neverLeader) IsLeader(context.Context) (bool, error) { return false, nil }

// A deferred task lives in the tenant's own database — the runtime writes it
// there — so the sweep must claim it from that database and dispatch it.
func TestFanout_TaskTickDispatchesFromTheProjectDatabase(t *testing.T) {
	p := newProjectMock(t)
	p.expectSchedulerTables(true)
	expectOneDueRow(p.mock)
	p.mock.ExpectExec("SET status = 'completed'").WillReturnResult(sqlmock.NewResult(0, 1))

	inv := &recordingInvoker{}
	f := fanoutOver(map[string]*projectMock{"proj_a": p}, inv, alwaysLeader{})

	if err := f.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if calls := inv.Calls(); len(calls) != 1 || calls[0] != "proj_a/jobs.send" {
		t.Fatalf("dispatches: got %v, want [proj_a/jobs.send]", calls)
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Most projects never deploy a scheduled function. Their databases have no
// scheduler tables and must be left alone rather than queried every tick.
func TestFanout_SkipsProjectsWithoutSchedulerTables(t *testing.T) {
	p := newProjectMock(t)
	p.expectSchedulerTables(false)

	inv := &recordingInvoker{}
	f := fanoutOver(map[string]*projectMock{"proj_a": p}, inv, alwaysLeader{})

	if err := f.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if len(inv.Calls()) != 0 {
		t.Errorf("a project with no scheduler tables was swept: %v", inv.Calls())
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Cron enqueues are decided from last_enqueued_at, so two replicas reading
// before either writes would both insert. Only the leader walks the registry.
func TestFanout_CronTickOnlyRunsOnTheLeader(t *testing.T) {
	p := newProjectMock(t) // no expectations: nothing may touch this database
	f := fanoutOver(map[string]*projectMock{"proj_a": p}, &recordingInvoker{}, neverLeader{})

	if err := f.CronTick(context.Background()); err != nil {
		t.Fatalf("CronTick: %v", err)
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a follower touched a project database: %v", err)
	}
}

func TestFanout_CronTickWalksTheRegistryOnTheLeader(t *testing.T) {
	p := newProjectMock(t)
	p.expectSchedulerTables(true)
	p.mock.ExpectQuery("FROM excalibase.excalibase_cron_jobs").
		WillReturnRows(sqlmock.NewRows([]string{
			"name", "project_id", "module_name", "export_name", "args", "schedule", "last_enqueued_at",
		}).AddRow("nightly", "proj_a", "jobs", "sweep", []byte(`{}`), []byte(`{"kind":"hourly","minuteUTC":0}`), nil))
	p.mock.ExpectBegin()
	p.mock.ExpectExec("INSERT INTO excalibase.excalibase_scheduled_functions").
		WillReturnResult(sqlmock.NewResult(0, 1))
	p.mock.ExpectExec("UPDATE excalibase.excalibase_cron_jobs").
		WillReturnResult(sqlmock.NewResult(0, 1))
	p.mock.ExpectCommit()

	f := fanoutOver(map[string]*projectMock{"proj_a": p}, &recordingInvoker{}, alwaysLeader{})
	if err := f.CronTick(context.Background()); err != nil {
		t.Fatalf("CronTick: %v", err)
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// One unreachable project must not stop the sweep for every other tenant.
func TestFanout_OneUnreachableProjectDoesNotStopTheSweep(t *testing.T) {
	good := newProjectMock(t)
	good.expectSchedulerTables(true)
	expectOneDueRow(good.mock)
	good.mock.ExpectExec("SET status = 'completed'").WillReturnResult(sqlmock.NewResult(0, 1))

	inv := &recordingInvoker{}
	f := NewFanout(FanoutConfig{
		Projects: func(context.Context) ([]string, error) {
			return []string{"proj_broken", "proj_a"}, nil
		},
		DB: func(_ context.Context, projectID string) (*sql.DB, error) {
			if projectID == "proj_a" {
				return good.db, nil
			}
			return nil, errors.New("credentials unavailable")
		},
		Invoker:    inv,
		CronLeader: alwaysLeader{},
		Functions:  knownFunctions{modules: map[string]bool{"proj_a/jobs": true}},
		Logger:     quietLogger(),
	})

	if err := f.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if calls := inv.Calls(); len(calls) != 1 {
		t.Fatalf("dispatches: got %v, want the reachable project's task", calls)
	}
}

// A project list the platform cannot read is an error, not an empty sweep:
// silently sweeping nothing looks exactly like having nothing to do.
func TestFanout_ProjectListFailureIsReported(t *testing.T) {
	f := NewFanout(FanoutConfig{
		Projects:   func(context.Context) ([]string, error) { return nil, errors.New("store is down") },
		DB:         func(context.Context, string) (*sql.DB, error) { return nil, nil },
		Invoker:    &recordingInvoker{},
		CronLeader: alwaysLeader{},
		Logger:     quietLogger(),
	})
	if err := f.TaskTick(context.Background()); err == nil {
		t.Fatal("an unreadable project list must be reported")
	}
}

func TestFanout_RunStopsOnContextCancel(t *testing.T) {
	var ticks atomic.Int32
	f := NewFanout(FanoutConfig{
		Projects: func(context.Context) ([]string, error) {
			ticks.Add(1)
			return nil, nil
		},
		DB:           func(context.Context, string) (*sql.DB, error) { return nil, nil },
		Invoker:      &recordingInvoker{},
		CronLeader:   alwaysLeader{},
		PollInterval: 5 * time.Millisecond,
		CronInterval: 5 * time.Millisecond,
		Logger:       quietLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for ticks.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("the sweep never ran")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// A database that cannot answer the presence check is skipped, not swept.
func TestFanout_UnreadableProjectDatabaseIsSkipped(t *testing.T) {
	p := newProjectMock(t)
	p.mock.ExpectQuery("to_regclass").WillReturnError(errors.New("connection refused"))

	inv := &recordingInvoker{}
	f := fanoutOver(map[string]*projectMock{"proj_a": p}, inv, alwaysLeader{})

	if err := f.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	if len(inv.Calls()) != 0 {
		t.Error("an unreadable project database was swept anyway")
	}
}

// A sweep that fails for one project is logged and the tick still ends
// cleanly — the next tick retries.
func TestFanout_SweepFailureDoesNotFailTheTick(t *testing.T) {
	p := newProjectMock(t)
	p.expectSchedulerTables(true)
	p.mock.ExpectBegin().WillReturnError(errors.New("too many connections"))

	f := fanoutOver(map[string]*projectMock{"proj_a": p}, &recordingInvoker{}, alwaysLeader{})
	if err := f.TaskTick(context.Background()); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
}

// failingLeader cannot tell whether this replica leads.
type failingLeader struct{}

func (failingLeader) IsLeader(context.Context) (bool, error) {
	return false, errors.New("lock unavailable")
}

// Not knowing whether we lead is reported, never treated as "we lead".
func TestFanout_CronTickReportsALeadershipFailure(t *testing.T) {
	p := newProjectMock(t)
	f := fanoutOver(map[string]*projectMock{"proj_a": p}, &recordingInvoker{}, failingLeader{})

	if err := f.CronTick(context.Background()); err == nil {
		t.Fatal("an unreadable leadership claim must be reported")
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a replica that does not know it leads touched a database: %v", err)
	}
}

// The presence check is asked once per project; a project already known to
// carry the tables is swept without re-asking.
func TestFanout_PresenceCheckIsAskedOncePerProject(t *testing.T) {
	p := newProjectMock(t)
	p.expectSchedulerTables(true)
	for i := 0; i < 2; i++ {
		p.mock.ExpectBegin()
		p.mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 0))
		p.mock.ExpectQuery("FROM excalibase.excalibase_scheduled_functions").
			WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "module_name", "export_name", "args", "attempts"}))
		p.mock.ExpectCommit()
	}

	f := fanoutOver(map[string]*projectMock{"proj_a": p}, &recordingInvoker{}, alwaysLeader{})
	for i := 0; i < 2; i++ {
		if err := f.TaskTick(context.Background()); err != nil {
			t.Fatalf("TaskTick %d: %v", i, err)
		}
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A project whose database cannot be reached must not be retried every few
// seconds forever: the sweep backs off and leaves it alone until the backoff
// expires.
func TestFanout_UnreachableProjectIsBackedOff(t *testing.T) {
	attempts := 0
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	f := NewFanout(FanoutConfig{
		Projects: func(context.Context) ([]string, error) { return []string{"proj_down"}, nil },
		DB: func(context.Context, string) (*sql.DB, error) {
			attempts++
			return nil, errors.New("connection refused")
		},
		Invoker:    &recordingInvoker{},
		Functions:  knownFunctions{},
		CronLeader: alwaysLeader{},
		Now:        func() time.Time { return now },
		Logger:     quietLogger(),
	})

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := f.TaskTick(ctx); err != nil {
			t.Fatalf("TaskTick %d: %v", i, err)
		}
	}
	if attempts != 1 {
		t.Fatalf("connection attempts while backed off: got %d, want 1", attempts)
	}

	now = now.Add(10 * time.Minute)
	if err := f.TaskTick(ctx); err != nil {
		t.Fatalf("TaskTick after backoff: %v", err)
	}
	if attempts != 2 {
		t.Errorf("connection attempts after the backoff expired: got %d, want 2", attempts)
	}
}

// A project that answers again clears its backoff, so one bad minute does
// not keep a healthy project out of the sweep.
func TestFanout_RecoveredProjectIsSweptAgain(t *testing.T) {
	p := newProjectMock(t)
	p.expectSchedulerTables(false)
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	fail := true
	f := NewFanout(FanoutConfig{
		Projects: func(context.Context) ([]string, error) { return []string{"proj_a"}, nil },
		DB: func(context.Context, string) (*sql.DB, error) {
			if fail {
				return nil, errors.New("connection refused")
			}
			return p.db, nil
		},
		Invoker:    &recordingInvoker{},
		Functions:  knownFunctions{},
		CronLeader: alwaysLeader{},
		Now:        func() time.Time { return now },
		Logger:     quietLogger(),
	})

	ctx := context.Background()
	if err := f.TaskTick(ctx); err != nil {
		t.Fatalf("TaskTick: %v", err)
	}
	fail = false
	now = now.Add(10 * time.Minute)
	if err := f.TaskTick(ctx); err != nil {
		t.Fatalf("TaskTick after recovery: %v", err)
	}
	now = now.Add(time.Second)
	if err := f.TaskTick(ctx); err != nil {
		t.Fatalf("TaskTick after recovery: %v", err)
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a recovered project was not swept again: %v", err)
	}
}

// Most projects never deploy a scheduled function. Re-asking their database
// every few seconds would keep a connection warm on every tenant for
// nothing, so a negative answer is remembered for a while.
func TestFanout_NegativePresenceCheckIsRemembered(t *testing.T) {
	p := newProjectMock(t)
	p.expectSchedulerTables(false)
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	f := NewFanout(FanoutConfig{
		Projects:   func(context.Context) ([]string, error) { return []string{"proj_a"}, nil },
		DB:         func(context.Context, string) (*sql.DB, error) { return p.db, nil },
		Invoker:    &recordingInvoker{},
		Functions:  knownFunctions{},
		CronLeader: alwaysLeader{},
		Now:        func() time.Time { return now },
		Logger:     quietLogger(),
	})

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := f.TaskTick(ctx); err != nil {
			t.Fatalf("TaskTick %d: %v", i, err)
		}
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}

	// After the window, the project is asked again: it may have deployed its
	// first scheduled function in the meantime.
	p.expectSchedulerTables(true)
	p.mock.ExpectBegin()
	p.mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 0))
	p.mock.ExpectQuery("FROM excalibase.excalibase_scheduled_functions").
		WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "module_name", "export_name", "args", "attempts"}))
	p.mock.ExpectCommit()
	now = now.Add(10 * time.Minute)
	if err := f.TaskTick(ctx); err != nil {
		t.Fatalf("TaskTick after the window: %v", err)
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the project was not re-checked after the window: %v", err)
	}
}
