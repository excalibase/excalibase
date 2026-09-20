package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// funcInvoker adapts a plain function to the Invoker seam.
type funcInvoker func(ctx context.Context, projectID, moduleName, exportName string, args json.RawMessage) error

func (f funcInvoker) Invoke(ctx context.Context, projectID, moduleName, exportName string, args json.RawMessage) error {
	return f(ctx, projectID, moduleName, exportName, args)
}

// expectOneDueRow programs the claim transaction to hand the worker a
// single pending task.
func expectOneDueRow(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec("status = 'running'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("FROM excalibase.excalibase_scheduled_functions").
		WillReturnRows(sqlmock.NewRows(
			[]string{"id", "project_id", "module_name", "export_name", "args", "attempts"}).
			AddRow("task1", "proj_a", "jobs", "send", []byte(`{}`), 0))
	mock.ExpectExec("UPDATE excalibase.excalibase_scheduled_functions").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// A project the platform must not serve has no runtime to dispatch into.
// Retrying such a task would burn its attempts on a project that is being
// deleted or restored, so the row is closed as skipped instead.
func TestWorker_NotServableProjectIsRecordedAsSkipped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectOneDueRow(mock)
	mock.ExpectExec("SET status = 'skipped'").
		WillReturnResult(sqlmock.NewResult(0, 1))

	w := NewWorker(WorkerConfig{
		DB:        db,
		ProjectID: "proj_a",
		Functions: knownFunctions{modules: map[string]bool{"proj_a/jobs": true}},
		Invoker: funcInvoker(func(context.Context, string, string, string, json.RawMessage) error {
			return fmt.Errorf("resolve runtime: %w", ErrNotServable)
		}),
	})
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Any other runtime failure keeps the existing retry semantics: the row
// goes back to pending with a bumped attempt count and a backoff.
func TestWorker_RuntimeErrorReschedulesWithBackoff(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectOneDueRow(mock)
	mock.ExpectExec("SET status = 'pending'").
		WillReturnResult(sqlmock.NewResult(0, 1))

	w := NewWorker(WorkerConfig{
		DB:        db,
		ProjectID: "proj_a",
		Functions: knownFunctions{modules: map[string]bool{"proj_a/jobs": true}},
		Invoker: funcInvoker(func(context.Context, string, string, string, json.RawMessage) error {
			return errors.New("runtime said 500")
		}),
	})
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Failing to record the skip must not take the worker out: the row stays
// claimable and the failure is logged.
func TestWorker_SkipRecordFailureIsSurvivable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectOneDueRow(mock)
	mock.ExpectExec("SET status = 'skipped'").WillReturnError(errors.New("connection lost"))

	w := NewWorker(WorkerConfig{
		DB:        db,
		ProjectID: "proj_a",
		Functions: knownFunctions{modules: map[string]bool{"proj_a/jobs": true}},
		Logger:    log.New(io.Discard, "", 0),
		Invoker: funcInvoker(func(context.Context, string, string, string, json.RawMessage) error {
			return fmt.Errorf("gone: %w", ErrNotServable)
		}),
	})
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
}
