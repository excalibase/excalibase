package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
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

// A MySQL project (ADR 0041) has no Postgres cron tables. A function without
// schedules deploys with no sync; one with schedules is refused with 409.
func TestBeginCronSync_MySQLProject(t *testing.T) {
	h := &FunctionHandler{
		projectDBFn: func(_ context.Context, _ string) (*sql.DB, error) {
			return nil, fmt.Errorf("open: %w", domain.ErrPostgresOnly)
		},
	}
	for _, none := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("[]")} {
		tx, commit, err := h.beginCronSync(context.Background(), "proj", "fn", none)
		if err != nil || tx != nil || commit() != nil {
			t.Errorf("no schedules %q: tx=%v err=%v", none, tx, err)
		}
	}
	_, _, err := h.beginCronSync(context.Background(), "proj", "fn", json.RawMessage(`[{"name":"n","schedule":"* * * * *"}]`))
	if !errors.Is(err, domain.ErrPostgresOnly) {
		t.Fatalf("err = %v", err)
	}
	if msg, code := cronSyncFailure(err); code != http.StatusConflict || msg != "scheduled functions are not available for MySQL projects yet" {
		t.Errorf("answered %d %q", code, msg)
	}
}
