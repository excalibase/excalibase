package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// Release on an empty semaphore blocked forever while the comment promised a
// panic. Either is a programming error; only one of them is diagnosable.
func TestSemaphore_ReleaseWithoutAcquirePanics(t *testing.T) {
	released := make(chan any, 1)
	go func() {
		defer func() { released <- recover() }()
		NewSemaphore(1).Release()
	}()
	select {
	case r := <-released:
		if r == nil {
			t.Error("Release without Acquire must panic, not return silently")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Release on an empty semaphore blocked")
	}
}

// An invoker that panics took the whole process down, and with it every
// other tenant's sweep. One task's panic fails that task.
func TestWorker_InvokerPanicFailsTheTaskAndNotTheProcess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectOneDueRow(mock)
	mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 1))

	w := NewWorker(WorkerConfig{
		DB: db, ProjectID: "proj_a", Logger: quietLogger(),
		Functions: knownFunctions{modules: map[string]bool{"proj_a/jobs": true}},
		Global:    NewSemaphore(1),
		Invoker: funcInvoker(func(context.Context, string, string, string, json.RawMessage) error {
			panic(errors.New("runtime client exploded"))
		}),
	})
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
	// The semaphores were released, so a second tick can still dispatch.
	if err := w.acquire(context.Background()); err != nil {
		t.Fatalf("semaphores were not released by the panicking dispatch: %v", err)
	}
	w.release()
}
