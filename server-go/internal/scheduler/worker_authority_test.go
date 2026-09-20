package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// knownFunctions answers for a fixed set of (project, module) pairs.
type knownFunctions struct {
	modules map[string]bool
	err     error
}

func (k knownFunctions) HasFunction(projectID, moduleName string) (bool, error) {
	if k.err != nil {
		return false, k.err
	}
	return k.modules[projectID+"/"+moduleName], nil
}

// dueRow programs the claim transaction with one row of arbitrary content —
// tenant-written content, which is exactly what these tests are about.
func dueRow(mock sqlmock.Sqlmock, projectID, module, export string, args any, attempts int) {
	mock.ExpectBegin()
	mock.ExpectQuery("FROM excalibase.excalibase_scheduled_functions").
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "project_id", "module_name", "export_name", "args", "attempts"}).
			AddRow("task1", projectID, module, export, args, attempts))
	mock.ExpectExec("UPDATE excalibase.excalibase_scheduled_functions").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// sweptWorker is a worker for proj_a whose registry knows proj_a/jobs.
func sweptWorker(db interface{ Close() error }, inv Invoker) WorkerConfig {
	return WorkerConfig{
		ProjectID: "proj_a",
		Invoker:   inv,
		Functions: knownFunctions{modules: map[string]bool{"proj_a/jobs": true}},
		Logger:    log.New(io.Discard, "", 0),
	}
}

// countingInvoker records dispatches.
type countingInvoker struct{ calls []string }

func (c *countingInvoker) Invoke(_ context.Context, projectID, moduleName, exportName string, _ json.RawMessage) error {
	c.calls = append(c.calls, projectID+"/"+moduleName+"."+exportName)
	return nil
}

// The tenant owns its database, so it can write any project id into its own
// queue. The project identity must come from the sweep, never from the row:
// a row naming another project is closed, and that project's runtime is
// never touched.
func TestWorker_RowNamingAnotherProjectIsNeverDispatched(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dueRow(mock, "proj_victim", "jobs", "send", []byte(`{}`), 0)
	mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 1))

	inv := &countingInvoker{}
	cfg := sweptWorker(db, inv)
	cfg.DB = db
	if err := NewWorker(cfg).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(inv.calls) != 0 {
		t.Fatalf("a row naming another project was dispatched: %v", inv.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The module name selects which function runs, so it is checked against the
// platform's own registry for the swept project.
func TestWorker_UnknownFunctionIsClosedAsFailed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dueRow(mock, "proj_a", "not-deployed", "send", []byte(`{}`), 0)
	mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 1))

	inv := &countingInvoker{}
	cfg := sweptWorker(db, inv)
	cfg.DB = db
	if err := NewWorker(cfg).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(inv.calls) != 0 {
		t.Fatalf("an undeployed module was dispatched: %v", inv.calls)
	}
}

// A registry that cannot answer fails the row closed rather than guessing.
func TestWorker_UnreadableRegistryFailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dueRow(mock, "proj_a", "jobs", "send", []byte(`{}`), 0)
	mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 1))

	inv := &countingInvoker{}
	cfg := sweptWorker(db, inv)
	cfg.DB = db
	cfg.Functions = knownFunctions{err: errors.New("store unavailable")}
	if err := NewWorker(cfg).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(inv.calls) != 0 {
		t.Fatalf("dispatched without being able to check the registry: %v", inv.calls)
	}
}

func TestWorker_RejectsHostileRowContent(t *testing.T) {
	big := make([]byte, 64)
	for i := range big {
		big[i] = 'x'
	}
	cases := map[string]struct {
		module, export string
		args           any
	}{
		"path in module":  {"../../etc/passwd", "send", []byte(`{}`)},
		"empty module":    {"", "send", []byte(`{}`)},
		"space in export": {"jobs", "send handler", []byte(`{}`)},
		"null args":       {"jobs", "send", nil},
		"oversized args":  {"jobs", "send", append([]byte(`{"a":"`), append(big, []byte(`"}`)...)...)},
		"non-json args":   {"jobs", "send", []byte(`not json`)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			dueRow(mock, "proj_a", c.module, c.export, c.args, 0)
			mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 1))

			inv := &countingInvoker{}
			cfg := sweptWorker(db, inv)
			cfg.DB = db
			cfg.MaxArgsBytes = 32
			if err := NewWorker(cfg).Tick(context.Background()); err != nil {
				t.Fatalf("Tick: %v", err)
			}
			if len(inv.calls) != 0 {
				t.Fatalf("hostile row content was dispatched: %v", inv.calls)
			}
		})
	}
}

// A tenant can write any attempt count. The platform's retry budget is the
// platform's: an attempt count at or past the limit ends the row, and a
// negative one does not buy extra retries.
func TestWorker_AttemptsFromTheRowCannotRaiseThePlatformBudget(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dueRow(mock, "proj_a", "jobs", "send", []byte(`{}`), 1000)
	mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 1))

	cfg := sweptWorker(db, funcInvoker(func(context.Context, string, string, string, json.RawMessage) error {
		return errors.New("runtime said 500")
	}))
	cfg.DB = db
	cfg.MaxAttempts = 3
	if err := NewWorker(cfg).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The good path still works: the swept project's own row, naming a deployed
// module, is dispatched with the sweep's project id.
func TestWorker_DispatchesTheSweptProjectsOwnRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dueRow(mock, "proj_a", "jobs", "send", []byte(`{"to":"ada"}`), 0)
	mock.ExpectExec("SET status = 'completed'").WillReturnResult(sqlmock.NewResult(0, 1))

	inv := &countingInvoker{}
	cfg := sweptWorker(db, inv)
	cfg.DB = db
	if err := NewWorker(cfg).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(inv.calls) != 1 || inv.calls[0] != "proj_a/jobs.send" {
		t.Fatalf("dispatches: got %v, want [proj_a/jobs.send]", inv.calls)
	}
}

// The reason recorded on a refused row is the platform's own fixed text,
// never tenant-supplied content echoed back into the row.
func TestWorker_RefusalReasonIsPlatformText(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dueRow(mock, "proj_victim", "jobs", "send", []byte(`{}`), 0)
	mock.ExpectExec("SET status = 'failed'").
		WithArgs("task1", rejectForeignProject).
		WillReturnResult(sqlmock.NewResult(0, 1))

	cfg := sweptWorker(db, &countingInvoker{})
	cfg.DB = db
	if err := NewWorker(cfg).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
	if strings.Contains(rejectForeignProject, "proj_victim") {
		t.Error("the refusal reason echoes tenant content")
	}
}

func TestSemaphore_BoundsAndReleases(t *testing.T) {
	s := NewSemaphore(1)
	ctx := context.Background()
	if err := s.Acquire(ctx); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	full, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Acquire(full); err == nil {
		t.Error("a full semaphore must not hand out a slot to a cancelled caller")
	}
	s.Release()
	if err := s.Acquire(ctx); err != nil {
		t.Errorf("acquire after release: %v", err)
	}
}

// A limiter of zero would stop the scheduler rather than bound it.
func TestSemaphore_ZeroBecomesOne(t *testing.T) {
	s := NewSemaphore(0)
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	s.Release()
}

func TestValidModuleName(t *testing.T) {
	good := []string{"jobs", "admin/cleanup", "a.b", "jobs-2", "_private"}
	bad := []string{"", "../escape", "/abs", "jobs/", "jobs//x", "a b", "jobs;drop", strings.Repeat("x", 129)}
	for _, name := range good {
		if !validModuleName(name) {
			t.Errorf("module %q: refused, want accepted", name)
		}
	}
	for _, name := range bad {
		if validModuleName(name) {
			t.Errorf("module %q: accepted, want refused", name)
		}
	}
}

func TestClampAttempts(t *testing.T) {
	cases := map[int]int{-5: 0, 0: 0, 2: 2, 99: 5}
	for in, want := range cases {
		if got := clampAttempts(in, 5); got != want {
			t.Errorf("clampAttempts(%d): got %d, want %d", in, got, want)
		}
	}
}

// Failing to record a refusal must not take the worker out.
func TestWorker_RefusalRecordFailureIsSurvivable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dueRow(mock, "proj_victim", "jobs", "send", []byte(`{}`), 0)
	mock.ExpectExec("SET status = 'failed'").WillReturnError(errors.New("connection lost"))

	cfg := sweptWorker(db, &countingInvoker{})
	cfg.DB = db
	if err := NewWorker(cfg).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
}

// When the platform-wide limiter cannot hand out a slot the batch stops
// rather than dispatching around the limit.
func TestWorker_StopsWhenThePlatformLimiterIsUnavailable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dueRow(mock, "proj_a", "jobs", "send", []byte(`{}`), 0)

	global := NewSemaphore(1)
	if err := global.Acquire(context.Background()); err != nil {
		t.Fatalf("fill the limiter: %v", err)
	}
	inv := &countingInvoker{}
	cfg := sweptWorker(db, inv)
	cfg.DB = db
	cfg.Global = global

	// The claim runs immediately; the wait for a slot is what runs out.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := NewWorker(cfg).Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(inv.calls) != 0 {
		t.Errorf("dispatched past the platform limiter: %v", inv.calls)
	}
}
