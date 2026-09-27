//go:build integration

package schema

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIntegration_ExecuteQuery_CapsRows(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	result := NewIntrospector().ExecuteQuery(context.Background(), appDB, "SELECT g FROM generate_series(1, 50000) g")
	if result.Error != "" {
		t.Fatalf("query: %s", result.Error)
	}
	if len(result.Rows) != MaxQueryRows || !result.Truncated {
		t.Fatalf("rows=%d truncated=%v, want %d and true", len(result.Rows), result.Truncated, MaxQueryRows)
	}
}

// One tenant's wide result must not be buffered whole in the control plane.
func TestIntegration_ExecuteQuery_CapsBytes(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	result := NewIntrospector().ExecuteQuery(context.Background(), appDB,
		"SELECT repeat('x', 1000000) FROM generate_series(1, 100)")
	if result.Error != "" {
		t.Fatalf("query: %s", result.Error)
	}
	if !result.Truncated {
		t.Fatal("100 MB of rows came back untruncated")
	}
	held := 0
	for _, row := range result.Rows {
		held += len(row[0].(string))
	}
	if held > MaxQueryBytes {
		t.Fatalf("held %d bytes, cap is %d", held, MaxQueryBytes)
	}
}

func TestIntegration_ExecuteQuery_SmallResultIsNotTruncated(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	result := NewIntrospector().ExecuteQuery(context.Background(), appDB, "SELECT 1")
	if result.Error != "" || result.Truncated || len(result.Rows) != 1 {
		t.Fatalf("got %+v", result)
	}
}

func assertCancelledQuickly(t *testing.T, started time.Time, errText string) {
	t.Helper()
	if errText == "" {
		t.Fatal("a statement past the timeout succeeded")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("statement ran %s past a 1s bound", elapsed)
	}
}

func TestIntegration_ExecuteDDL_IsBoundedInTime(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	started := time.Now()
	result := NewIntrospector().WithStatementTimeout(time.Second).
		ExecuteDDL(context.Background(), appDB, "SELECT pg_sleep(30)")
	assertCancelledQuickly(t, started, result.Error)
}

// The tenant's own SQL can lift the server-side timeout; the bound still holds.
func TestIntegration_ExecuteDDL_BoundSurvivesTheTenantLiftingTheTimeout(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	started := time.Now()
	result := NewIntrospector().WithStatementTimeout(time.Second).
		ExecuteDDL(context.Background(), appDB, "SET statement_timeout = 0; SELECT pg_sleep(30)")
	assertCancelledQuickly(t, started, result.Error)
}

func TestIntegration_ExecuteQuery_BoundSurvivesTheTenantLiftingTheTimeout(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	for _, sql := range []string{
		"SELECT pg_sleep(30)",
		"SET LOCAL statement_timeout = 0; SELECT pg_sleep(30)",
		"UPDATE users SET full_name = full_name FROM (SELECT pg_sleep(30)) AS pause",
	} {
		started := time.Now()
		result := NewIntrospector().WithStatementTimeout(time.Second).ExecuteQuery(context.Background(), appDB, sql)
		assertCancelledQuickly(t, started, result.Error)
		if !strings.Contains(strings.ToLower(result.Error), "cancel") && !strings.Contains(result.Error, "timeout") {
			t.Errorf("%q: error %q does not say it was cut short", sql, result.Error)
		}
	}
}
