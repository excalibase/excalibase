package handler

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// TestBeginCronSync_NoProjectDBFnReturnsNoop — when the handler hasn't
// been wired with a project-DB resolver (the autoMigrate=false /
// test-without-DB paths), beginCronSync must skip the sync and return
// a no-op commit callback.
func TestBeginCronSync_NoProjectDBFnReturnsNoop(t *testing.T) {
	h := &FunctionHandler{} // no projectDBFn
	tx, commit, err := h.beginCronSync(context.Background(), "proj", "fn", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx != nil {
		t.Errorf("expected nil tx, got %v", tx)
	}
	if commit == nil {
		t.Fatal("commit callback must not be nil")
	}
	if err := commit(); err != nil {
		t.Errorf("no-op commit returned error: %v", err)
	}
}

// TestBeginCronSync_DBOpenError — when the project-DB resolver itself
// fails, beginCronSync surfaces the error wrapped with context.
func TestBeginCronSync_DBOpenError(t *testing.T) {
	h := &FunctionHandler{
		projectDBFn: func(_ context.Context, _ string) (*sql.DB, error) {
			return nil, errors.New("vault unreachable")
		},
	}
	tx, _, err := h.beginCronSync(context.Background(), "proj", "fn", nil)
	if err == nil {
		t.Fatal("expected error when projectDBFn returns error")
	}
	if tx != nil {
		t.Errorf("expected nil tx on error, got %v", tx)
	}
}

// TestRollbackCronSync_NilTxIsSafe — the rollback helper accepts a nil
// transaction (the no-DB path's return value) without panicking.
func TestRollbackCronSync_NilTxIsSafe(t *testing.T) {
	if err := rollbackCronSync(nil); err != nil {
		t.Errorf("rollbackCronSync(nil) returned: %v", err)
	}
}
