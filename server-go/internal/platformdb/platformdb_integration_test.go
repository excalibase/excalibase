//go:build integration

package platformdb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func startPG(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("platformdb_test"),
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
	return db, func() {
		_ = db.Close()
		_ = c.Terminate(ctx)
	}
}

func schemaOf(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var schemas []string
	rows, err := db.Query(
		`SELECT table_schema FROM information_schema.tables WHERE table_name = $1 ORDER BY table_schema`,
		table,
	)
	if err != nil {
		t.Fatalf("lookup %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		schemas = append(schemas, s)
	}
	if len(schemas) != 1 {
		t.Fatalf("table %s: expected exactly one copy, found %v", table, schemas)
	}
	return schemas[0]
}

// A fresh tenant must get both tables in the reserved schema and nothing
// at all in public.
func TestEnsureSchedulerTables_FreshTenantUsesReservedSchema(t *testing.T) {
	db, teardown := startPG(t)
	defer teardown()
	ctx := context.Background()

	if err := EnsureSchedulerTables(ctx, db); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, table := range []string{scheduledFunctionsTable, cronJobsTable} {
		if got := schemaOf(t, db, table); got != ReservedSchema {
			t.Errorf("table %s landed in schema %q, want %q", table, got, ReservedSchema)
		}
	}
	// Idempotent: a second run on the same database is a no-op.
	if err := EnsureSchedulerTables(ctx, db); err != nil {
		t.Fatalf("ensure (second run): %v", err)
	}
	for _, table := range []string{scheduledFunctionsTable, cronJobsTable} {
		if got := schemaOf(t, db, table); got != ReservedSchema {
			t.Errorf("after rerun, table %s is in schema %q, want %q", table, got, ReservedSchema)
		}
	}
}

// An existing tenant already has the tables (and rows) in public. They must
// be moved, data intact, leaving nothing behind in user space.
func TestEnsureSchedulerTables_MovesLegacyPublicTables(t *testing.T) {
	db, teardown := startPG(t)
	defer teardown()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE excalibase_scheduled_functions (
			id text PRIMARY KEY,
			project_id text NOT NULL,
			module_name text NOT NULL,
			export_name text NOT NULL,
			args jsonb NOT NULL,
			scheduled_for timestamptz NOT NULL,
			status text NOT NULL DEFAULT 'pending',
			attempts int NOT NULL DEFAULT 0,
			last_error text,
			created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TABLE excalibase_cron_jobs (
			name text NOT NULL,
			project_id text NOT NULL,
			module_name text NOT NULL,
			export_name text NOT NULL,
			args jsonb NOT NULL,
			schedule jsonb NOT NULL,
			last_enqueued_at timestamptz,
			PRIMARY KEY (project_id, name)
		);
		INSERT INTO excalibase_scheduled_functions
			(id, project_id, module_name, export_name, args, scheduled_for)
		VALUES ('task-1', 'proj-1', 'jobs', 'send', '{}'::jsonb, now());
		INSERT INTO excalibase_cron_jobs
			(name, project_id, module_name, export_name, args, schedule)
		VALUES ('nightly', 'proj-1', 'jobs', 'sweep', '{}'::jsonb, '{"kind":"hourly"}'::jsonb);
	`); err != nil {
		t.Fatalf("seed legacy tables: %v", err)
	}

	if err := EnsureSchedulerTables(ctx, db); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	for _, table := range []string{scheduledFunctionsTable, cronJobsTable} {
		if got := schemaOf(t, db, table); got != ReservedSchema {
			t.Errorf("legacy table %s stayed in schema %q, want %q", table, got, ReservedSchema)
		}
	}

	var tasks, crons int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM excalibase.excalibase_scheduled_functions`).Scan(&tasks); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM excalibase.excalibase_cron_jobs`).Scan(&crons); err != nil {
		t.Fatalf("count crons: %v", err)
	}
	if tasks != 1 || crons != 1 {
		t.Errorf("rows lost in the move: tasks=%d crons=%d, want 1 and 1", tasks, crons)
	}

	// The drifted copy of the DDL lacked function_id; the moved table must
	// end up with the reconciled column.
	var hasFunctionID bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_schema = 'excalibase'
			   AND table_name = 'excalibase_cron_jobs'
			   AND column_name = 'function_id')
	`).Scan(&hasFunctionID); err != nil {
		t.Fatalf("check function_id: %v", err)
	}
	if !hasFunctionID {
		t.Error("migrated cron table is missing the function_id column")
	}

	// Re-running after the move must not fail or resurrect a public copy.
	if err := EnsureSchedulerTables(ctx, db); err != nil {
		t.Fatalf("ensure (after move): %v", err)
	}
	for _, table := range []string{scheduledFunctionsTable, cronJobsTable} {
		if got := schemaOf(t, db, table); got != ReservedSchema {
			t.Errorf("after rerun, table %s is in schema %q, want %q", table, got, ReservedSchema)
		}
	}
}
