//go:build integration

package schema

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestIntegration_ExecuteReadOnlyQuery pins EXC-544: read-only SQL reads, and
// nothing it is handed can write, whether directly, through a data-modifying
// WITH, or by ending the read-only transaction and starting another.
func TestIntegration_ExecuteReadOnlyQuery(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := appDB.ExecContext(ctx, "INSERT INTO users (email, full_name) VALUES ('ro@test.com', 'RO')"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	introspector := NewIntrospector()

	result := introspector.ExecuteReadOnlyQuery(ctx, appDB, "SELECT email FROM users")
	if result.Error != "" || len(result.Rows) != 1 || result.Rows[0][0] != "ro@test.com" {
		t.Fatalf("read: %+v", result)
	}

	for _, statement := range []string{
		"INSERT INTO users (email) VALUES ('w1@test.com')",
		"UPDATE users SET full_name = 'x'",
		"WITH gone AS (DELETE FROM users RETURNING id) SELECT count(*) FROM gone",
		"COMMIT; DELETE FROM users",
		"SELECT 1; DELETE FROM users",
		"CREATE TABLE sneaky (id int)",
	} {
		result := introspector.ExecuteReadOnlyQuery(ctx, appDB, statement)
		if result.Error == "" {
			t.Errorf("%q ran in read-only mode", statement)
		}
	}

	// Postgres names the outer SELECT when a data-modifying WITH is refused;
	// the answer says what was refused instead.
	cte := introspector.ExecuteReadOnlyQuery(ctx, appDB, "WITH gone AS (DELETE FROM users RETURNING id) SELECT count(*) FROM gone")
	if !strings.Contains(cte.Error, "read-only SQL cannot change data or schema") || strings.Contains(cte.Error, "cannot execute SELECT") {
		t.Errorf("CTE refusal = %q", cte.Error)
	}

	// Postgres may accept SET TRANSACTION READ WRITE here, but it is the only
	// statement the transaction runs before the rollback, so it writes nothing.
	introspector.ExecuteReadOnlyQuery(ctx, appDB, "SET TRANSACTION READ WRITE")

	var users int
	if err := appDB.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&users); err != nil {
		t.Fatalf("count: %v", err)
	}
	var full string
	_ = appDB.QueryRowContext(ctx, "SELECT coalesce(full_name, '') FROM users WHERE email = 'ro@test.com'").Scan(&full)
	if users != 1 || full != "RO" {
		t.Fatalf("read-only SQL changed the data: users=%d full_name=%q", users, full)
	}
	var sneaky bool
	_ = appDB.QueryRowContext(ctx, "SELECT to_regclass('public.sneaky') IS NOT NULL").Scan(&sneaky)
	if sneaky {
		t.Fatalf("read-only SQL created a table")
	}
}

// TestIntegration_ExecuteReadOnlyQuery_LeavesNoSessionBehind pins that the
// statement's connection is thrown away: a session-level advisory lock taken
// in read-only SQL does not outlive the call on a pooled connection.
func TestIntegration_ExecuteReadOnlyQuery_LeavesNoSessionBehind(t *testing.T) {
	superDB, appDB, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	appDB.SetMaxOpenConns(1)
	result := NewIntrospector().ExecuteReadOnlyQuery(ctx, appDB, "SELECT pg_advisory_lock(4242)")
	if result.Error != "" {
		t.Fatalf("lock: %s", result.Error)
	}
	if open := appDB.Stats().OpenConnections; open != 0 {
		t.Fatalf("the read-only call's connection went back to the pool: %d open", open)
	}
	// The server ends the closed session's backend, and its locks, shortly after.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var free bool
		if err := superDB.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(4242)").Scan(&free); err != nil {
			t.Fatalf("try lock: %v", err)
		}
		if free {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the advisory lock outlived the read-only call")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestIntegration_ExecuteReadOnlyQuery_RefusesSideEffectFunctions pins the
// functions that act outside the transaction: signalling other sessions,
// writing over another connection, server files.
func TestIntegration_ExecuteReadOnlyQuery_RefusesSideEffectFunctions(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()
	for _, statement := range []string{
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid <> pg_backend_pid()",
		`SELECT "pg_cancel_backend"(1)`,
		"SELECT pg_catalog.pg_reload_conf()",
		"SELECT dblink_exec('dbname=testdb', 'DELETE FROM users')",
		"SELECT lo_export(1, '/tmp/x')",
	} {
		result := NewIntrospector().ExecuteReadOnlyQuery(context.Background(), appDB, statement)
		if !strings.Contains(result.Error, "read-only SQL may not call") {
			t.Errorf("%q: error %q", statement, result.Error)
		}
	}
}

func TestIntegration_ExecuteReadOnlyQuery_IsBounded(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()
	started := time.Now()
	result := NewIntrospector().WithStatementTimeout(time.Second).
		ExecuteReadOnlyQuery(context.Background(), appDB, "SELECT pg_sleep(30)")
	if result.Error == "" || time.Since(started) > 10*time.Second {
		t.Fatalf("not cut short: %+v after %s", result, time.Since(started))
	}
	if !strings.Contains(strings.ToLower(result.Error), "cancel") && !strings.Contains(result.Error, "timeout") {
		t.Errorf("error %q does not say it was cut short", result.Error)
	}
}
