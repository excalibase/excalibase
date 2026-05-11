//go:build integration

package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// setupSchedulerPG spins up Postgres in a container, creates the scheduler
// tables, and returns a *sql.DB plus a teardown closure. Mirrors the pattern
// in internal/edgefn/migrator_test.go so the test infra stays consistent.
func setupSchedulerPG(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("sched_test"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	connStr, _ := c.ConnectionString(ctx, "sslmode=disable")
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := EnsureTables(ctx, db); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	teardown := func() {
		_ = db.Close()
		_ = c.Terminate(ctx)
	}
	return db, teardown
}

// stubInvoker records every invocation and returns a programmable result.
// The scheduler worker calls Invoke(...) for each due task; the stub lets
// tests assert which (projectID, fnId, args) tuples were dispatched.
type stubInvoker struct {
	mu    sync.Mutex
	calls []invokeCall
	// fail, when non-nil, is consumed once per Invoke call. Subsequent
	// calls fall back to a successful response.
	fail []error
}

type invokeCall struct {
	ProjectID  string
	ModuleName string
	ExportName string
	Args       map[string]any
}

func (s *stubInvoker) Invoke(_ context.Context, projectID, moduleName, exportName string, args json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var parsed map[string]any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &parsed)
	}
	s.calls = append(s.calls, invokeCall{
		ProjectID:  projectID,
		ModuleName: moduleName,
		ExportName: exportName,
		Args:       parsed,
	})
	if len(s.fail) > 0 {
		err := s.fail[0]
		s.fail = s.fail[1:]
		return err
	}
	return nil
}

func (s *stubInvoker) Calls() []invokeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]invokeCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// TestWorker_PicksUpAndCompletes seeds a pending task, runs one poll tick,
// and asserts the task is marked completed after a successful invocation.
func TestWorker_PicksUpAndCompletes(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()
	inv := &stubInvoker{}

	// Seed: one pending task whose scheduled_for is in the past.
	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status, attempts)
		VALUES
		  ('task001', 'proj_a', 'jobs', 'send', '{"to":"ada@example.com"}', now() - interval '1 second', 'pending', 0)
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := NewWorker(WorkerConfig{DB: db, Invoker: inv, PollInterval: 100 * time.Millisecond})
	if err := w.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	calls := inv.Calls()
	if len(calls) != 1 {
		t.Fatalf("invocations: got %d, want 1", len(calls))
	}
	if calls[0].ProjectID != "proj_a" || calls[0].ModuleName != "jobs" || calls[0].ExportName != "send" {
		t.Errorf("invocation: got %+v", calls[0])
	}
	if calls[0].Args["to"] != "ada@example.com" {
		t.Errorf("invocation args.to: got %v", calls[0].Args["to"])
	}
	var status string
	var attempts int
	if err := db.QueryRowContext(ctx,
		`SELECT status, attempts FROM excalibase_scheduled_functions WHERE id = $1`,
		"task001",
	).Scan(&status, &attempts); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "completed" {
		t.Errorf("status: got %q, want %q", status, "completed")
	}
	if attempts != 1 {
		t.Errorf("attempts: got %d, want 1", attempts)
	}
}

// TestWorker_DoesNotPickFutureTasks confirms scheduled_for > now() is skipped.
func TestWorker_DoesNotPickFutureTasks(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()
	inv := &stubInvoker{}

	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES
		  ('future001', 'proj_a', 'jobs', 'send', '{}', now() + interval '1 hour', 'pending')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := NewWorker(WorkerConfig{DB: db, Invoker: inv, PollInterval: 100 * time.Millisecond})
	if err := w.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := len(inv.Calls()); got != 0 {
		t.Fatalf("invocations: got %d, want 0 (future task picked up)", got)
	}
}

// TestWorker_RetriesOnFailureUpToMaxAttempts — a failing invoke
// increments `attempts`, sets `last_error`, and reschedules into the
// future (exponential backoff). After MaxAttempts failures the row is
// finalised as `failed` and no longer retried.
func TestWorker_RetriesOnFailureUpToMaxAttempts(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()
	inv := &stubInvoker{fail: []error{
		errors.New("transient 1"),
		errors.New("transient 2"),
		errors.New("transient 3"),
		errors.New("transient 4"),
		errors.New("transient 5"),
	}}

	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES
		  ('retry001', 'proj_a', 'jobs', 'flaky', '{}', now() - interval '1 second', 'pending')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := NewWorker(WorkerConfig{DB: db, Invoker: inv, PollInterval: 100 * time.Millisecond})
	// Five ticks — backoff each time. We rewind scheduled_for between ticks
	// so the worker treats it as due again (avoids waiting for real backoff).
	for i := 0; i < 5; i++ {
		if err := w.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE excalibase_scheduled_functions SET scheduled_for = now() - interval '1 second' WHERE id = $1 AND status = 'pending'`,
			"retry001",
		); err != nil {
			t.Fatalf("rewind %d: %v", i, err)
		}
	}
	var status string
	var attempts int
	var lastErr sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT status, attempts, last_error FROM excalibase_scheduled_functions WHERE id = $1`,
		"retry001",
	).Scan(&status, &attempts, &lastErr); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "failed" {
		t.Errorf("status: got %q, want %q (after %d attempts)", status, "failed", attempts)
	}
	if attempts != 5 {
		t.Errorf("attempts: got %d, want 5", attempts)
	}
	if !lastErr.Valid || lastErr.String == "" {
		t.Errorf("last_error: empty after failed retries")
	}
}

// TestWorker_SkipsLockedRows — concurrent tickers must not double-fire the
// same task. We start two workers simultaneously against one seeded task and
// assert exactly one Invoke happens.
func TestWorker_SkipsLockedRows(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()
	inv := &stubInvoker{}

	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES
		  ('once001', 'proj_a', 'jobs', 'send', '{}', now() - interval '1 second', 'pending')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	w1 := NewWorker(WorkerConfig{DB: db, Invoker: inv, PollInterval: 100 * time.Millisecond})
	w2 := NewWorker(WorkerConfig{DB: db, Invoker: inv, PollInterval: 100 * time.Millisecond})
	var wg sync.WaitGroup
	wg.Add(2)
	var err1, err2 error
	go func() { defer wg.Done(); err1 = w1.Tick(ctx) }()
	go func() { defer wg.Done(); err2 = w2.Tick(ctx) }()
	wg.Wait()
	if err1 != nil {
		t.Fatalf("worker 1 tick: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("worker 2 tick: %v", err2)
	}
	if got := len(inv.Calls()); got != 1 {
		t.Fatalf("invocations: got %d, want 1 (FOR UPDATE SKIP LOCKED protection broken)", got)
	}
}

// TestCancel_BlocksDispatchOfPending — Cancel on a pending row sets status to
// cancelled; a subsequent tick must NOT invoke it.
func TestCancel_BlocksDispatchOfPending(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()
	inv := &stubInvoker{}

	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES
		  ('cancel001', 'proj_a', 'jobs', 'send', '{}', now() - interval '1 second', 'pending')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Cancel(ctx, db, "cancel001"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	w := NewWorker(WorkerConfig{DB: db, Invoker: inv, PollInterval: 100 * time.Millisecond})
	if err := w.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := len(inv.Calls()); got != 0 {
		t.Fatalf("invocations: got %d, want 0 (cancel ignored)", got)
	}
	var status string
	_ = db.QueryRowContext(ctx,
		`SELECT status FROM excalibase_scheduled_functions WHERE id = $1`,
		"cancel001",
	).Scan(&status)
	if status != "cancelled" {
		t.Errorf("status: got %q, want %q", status, "cancelled")
	}
}

// TestCancel_NoopOnCompleted — cancelling a row already in a terminal state
// is a no-op (idempotent).
func TestCancel_NoopOnCompleted(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES
		  ('done001', 'proj_a', 'jobs', 'send', '{}', now(), 'completed')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Cancel(ctx, db, "done001"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var status string
	_ = db.QueryRowContext(ctx,
		`SELECT status FROM excalibase_scheduled_functions WHERE id = $1`,
		"done001",
	).Scan(&status)
	if status != "completed" {
		t.Errorf("status: got %q, want %q", status, "completed")
	}
}

// TestCronRunner_Run_StopsOnContextCancel mirrors the worker test —
// CronRunner.Run must return cleanly when the context is cancelled so the
// server can shut down without leaking goroutines.
func TestCronRunner_Run_StopsOnContextCancel(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	cr := NewCronRunner(CronRunnerConfig{DB: db})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = cr.Run(ctx)
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("CronRunner.Run did not return after ctx cancel")
	}
}

// TestRun_StopsOnContextCancel — Run blocks until the context is cancelled.
// The worker must drain in-flight ticks and return without leaking goroutines.
func TestRun_StopsOnContextCancel(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	inv := &stubInvoker{}
	w := NewWorker(WorkerConfig{DB: db, Invoker: inv, PollInterval: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr atomic.Value
	go func() {
		if err := w.Run(ctx); err != nil {
			runErr.Store(err)
		}
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
	if v := runErr.Load(); v != nil {
		t.Errorf("Run returned err: %v", v)
	}
}
