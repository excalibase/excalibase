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

// A customer table in public that shares a scheduler table's name stays
// where it is, and the platform's own table is created beside it.
func TestEnsureSchedulerTables_LeavesAPublicTableOfTheSameNameAlone(t *testing.T) {
	db, teardown := startPG(t)
	defer teardown()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE public.excalibase_cron_jobs (planted int)`); err != nil {
		t.Fatalf("seed public table: %v", err)
	}
	if err := EnsureSchedulerTables(ctx, db); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	var schemas []string
	rows, err := db.QueryContext(ctx, `SELECT table_schema FROM information_schema.tables
		WHERE table_name = $1 ORDER BY table_schema`, cronJobsTable)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			t.Fatal(err)
		}
		schemas = append(schemas, schema)
	}
	if len(schemas) != 2 || schemas[0] != ReservedSchema || schemas[1] != "public" {
		t.Fatalf("cron table copies = %v, want the platform's in excalibase and the customer's left in public", schemas)
	}
}
